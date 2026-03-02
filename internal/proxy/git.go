package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// rigNameRe matches valid rig names: alphanumeric, hyphens, and underscores only.
var rigNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// handleGit serves git smart-HTTP protocol for upload-pack and receive-pack.
// Routes:
//
//	GET  /v1/git/<rig>/info/refs?service=git-upload-pack
//	POST /v1/git/<rig>/git-upload-pack
//	GET  /v1/git/<rig>/info/refs?service=git-receive-pack
//	POST /v1/git/<rig>/git-receive-pack
func (s *Server) handleGit(w http.ResponseWriter, r *http.Request) {
	// Path: /v1/git/<rig>/...
	path := strings.TrimPrefix(r.URL.Path, "/v1/git/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		http.Error(w, "invalid git path", http.StatusBadRequest)
		return
	}
	rig := parts[0]
	rest := parts[1]

	// Issue 6: Reject empty rig segment (e.g. /v1/git//info/refs).
	if rig == "" {
		http.Error(w, "missing rig name", http.StatusBadRequest)
		return
	}

	// Issue 1: Validate rig name to prevent path traversal attacks.
	if !rigNameRe.MatchString(rig) {
		http.Error(w, "invalid rig name", http.StatusBadRequest)
		return
	}

	repoPath := filepath.Join(s.cfg.TownRoot, rig, ".repo.git")

	switch {
	case rest == "info/refs":
		s.handleInfoRefs(w, r, repoPath, rig)
	case rest == "git-upload-pack":
		s.handlePack(w, r, repoPath, "git-upload-pack", rig, "")
	case rest == "git-receive-pack":
		cn := clientCN(r)
		s.handlePack(w, r, repoPath, "git-receive-pack", rig, cn)
	default:
		http.Error(w, "unknown git endpoint", http.StatusNotFound)
	}
}

func clientCN(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return r.TLS.PeerCertificates[0].Subject.CommonName
}

func (s *Server) handleInfoRefs(w http.ResponseWriter, r *http.Request, repoPath, rig string) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" && service != "git-receive-pack" {
		http.Error(w, "unsupported service", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")

	// Smart HTTP info/refs pkt-line prefix.
	pktLine := fmt.Sprintf("# service=%s\n", service)
	fmt.Fprintf(w, "%04x%s0000", len(pktLine)+4, pktLine)

	var errBuf strings.Builder
	cmd := exec.CommandContext(r.Context(), service, "--stateless-rpc", "--advertise-refs", repoPath)
	cmd.Stdout = w
	cmd.Stderr = &errBuf
	cmd.Env = minimalEnv()
	if err := cmd.Run(); err != nil {
		s.log.Error("git info/refs failed", "service", service, "rig", rig, "err", err, "stderr", errBuf.String())
	}
}

func (s *Server) handlePack(w http.ResponseWriter, r *http.Request, repoPath, service, rig, clientCN string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// For receive-pack: enforce CN-scoped branch authorization.
	if service == "git-receive-pack" {
		if !s.authorizeReceivePack(w, r, clientCN) {
			return
		}
	}

	w.Header().Set("Content-Type", "application/x-"+service+"-result")
	w.Header().Set("Cache-Control", "no-cache")

	var errBuf strings.Builder
	cmd := exec.CommandContext(r.Context(), service, "--stateless-rpc", repoPath)
	cmd.Stdin = r.Body
	cmd.Stdout = w
	cmd.Stderr = &errBuf
	cmd.Env = minimalEnv()
	if err := cmd.Run(); err != nil {
		s.log.Error("git pack failed", "service", service, "rig", rig, "err", err, "stderr", errBuf.String())
	}
}

// authorizeReceivePack checks that the push only touches refs/heads/polecat/<cn-name>-*.
// It reads the pkt-line stream to extract ref names, then rewinds the body.
func (s *Server) authorizeReceivePack(w http.ResponseWriter, r *http.Request, clientCN string) bool {
	// Issue 8: Use the shared polecatName helper instead of reimplementing CN parsing.
	cnName := polecatName(clientCN)
	if cnName == "" {
		http.Error(w, "cannot determine polecat name from cert CN", http.StatusForbidden)
		return false
	}

	// Issue 3: Bound body reads to prevent a misbehaving client from exhausting memory.
	// 32 MiB is ample for any valid ref advertisement; binary pack data is not read.
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusInternalServerError)
		return false
	}
	r.Body.Close()

	if err := validateReceivePackRefs(body, cnName); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return false
	}

	// Use bytes.NewReader to safely re-wrap binary pack data.
	r.Body = io.NopCloser(bytes.NewReader(body))
	return true
}

// validateReceivePackRefs parses the git-receive-pack pkt-line stream and validates
// that all pushed refs are under refs/heads/polecat/<cnName>-* (prefix form only).
func validateReceivePackRefs(body []byte, cnName string) error {
	// The pkt-line wire format: each record is a 4-hex-digit length (including the
	// length field itself) followed by that many bytes of payload.  "0000" is a
	// flush packet that terminates the ref list.  Any binary pack data that follows
	// the flush packet is never read by this loop.
	//
	// Issue 13: Work in []byte throughout to avoid copying binary pack data into a string.
	allowed := "refs/heads/polecat/" + cnName + "-"
	offset := 0
	for offset < len(body) {
		// Guard: need at least 4 bytes for the length field.
		if offset+4 > len(body) {
			break
		}
		lenHex := body[offset : offset+4]
		if bytes.Equal(lenHex, []byte("0000")) {
			break // flush packet: end of ref list
		}
		var pktLen int
		_, err := fmt.Sscanf(string(lenHex), "%x", &pktLen)
		// pktLen < 4 would underflow the payload slice; treat as malformed and stop.
		if err != nil || pktLen < 4 {
			break
		}
		end := offset + pktLen
		// Guard: truncated packet — length field claims more bytes than available.
		if end > len(body) {
			break
		}
		// Payload starts after the 4-byte length prefix; always advances by pktLen
		// (even when pktLen==4, the empty payload line is skipped below).
		line := body[offset+4 : end]
		offset = end

		// Each line: "<old-sha> <new-sha> <refname>\0[capabilities]\n"
		// Strip the trailing newline, then truncate at the first NUL byte so that
		// capability strings (e.g. "\0side-band-64k") do not pollute the ref name.
		line = bytes.TrimRight(line, "\n")
		if idx := bytes.IndexByte(line, 0); idx >= 0 {
			line = line[:idx]
		}
		parts := bytes.Fields(line)
		if len(parts) < 3 {
			continue
		}
		ref := string(parts[2])

		// Only allow refs/heads/polecat/<cnName>-* (prefix form).
		// Exact-name pushes (without timestamp suffix) are not permitted.
		if !strings.HasPrefix(ref, allowed) {
			return fmt.Errorf("push to %q denied: only refs/heads/polecat/%s-* allowed", ref, cnName)
		}
	}
	return nil
}
