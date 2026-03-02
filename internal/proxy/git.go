package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
)

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

	cmd := exec.CommandContext(r.Context(), service, "--stateless-rpc", "--advertise-refs", repoPath)
	cmd.Stdout = w
	cmd.Stderr = log.Writer()
	cmd.Env = minimalEnv()
	if err := cmd.Run(); err != nil {
		log.Printf("git info/refs (%s %s): %v", service, rig, err)
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

	cmd := exec.CommandContext(r.Context(), service, "--stateless-rpc", repoPath)
	cmd.Stdin = r.Body
	cmd.Stdout = w
	cmd.Stderr = log.Writer()
	cmd.Env = minimalEnv()
	if err := cmd.Run(); err != nil {
		log.Printf("git pack (%s %s): %v", service, rig, err)
	}
}

// authorizeReceivePack checks that the push only touches refs/heads/polecat/<cn-name>-*.
// It reads the pkt-line stream to extract ref names, then rewinds the body.
func (s *Server) authorizeReceivePack(w http.ResponseWriter, r *http.Request, clientCN string) bool {
	// CN format: gt-<rig>-<name>; extract <name> using LastIndex.
	cnName := ""
	if strings.HasPrefix(clientCN, "gt-") {
		rest := clientCN[3:]
		idx := strings.LastIndex(rest, "-")
		if idx >= 0 {
			cnName = rest[idx+1:]
		}
	}
	if cnName == "" {
		http.Error(w, "cannot determine polecat name from cert CN", http.StatusForbidden)
		return false
	}

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
	data := string(body)
	offset := 0
	for offset < len(data) {
		// Guard: need at least 4 bytes for the length field.
		if offset+4 > len(data) {
			break
		}
		lenHex := data[offset : offset+4]
		if lenHex == "0000" {
			break // flush packet: end of ref list
		}
		var pktLen int
		_, err := fmt.Sscanf(lenHex, "%x", &pktLen)
		// pktLen < 4 would underflow the payload slice; treat as malformed and stop.
		if err != nil || pktLen < 4 {
			break
		}
		end := offset + pktLen
		// Guard: truncated packet — length field claims more bytes than available.
		if end > len(data) {
			break
		}
		// Payload starts after the 4-byte length prefix; always advances by pktLen
		// (even when pktLen==4, the empty payload line is skipped below).
		line := data[offset+4 : end]
		offset = end

		// Each line: "<old-sha> <new-sha> <refname>\0[capabilities]\n"
		// Strip the trailing newline, then truncate at the first NUL byte so that
		// capability strings (e.g. "\0side-band-64k") do not pollute the ref name.
		line = strings.TrimRight(line, "\n")
		if idx := strings.IndexByte(line, 0); idx >= 0 {
			line = line[:idx]
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		ref := parts[2]

		// Only allow refs/heads/polecat/<cnName>-* (prefix form).
		// Exact-name pushes (without timestamp suffix) are not permitted.
		allowed := "refs/heads/polecat/" + cnName + "-"
		if !strings.HasPrefix(ref, allowed) {
			return fmt.Errorf("push to %q denied: only refs/heads/polecat/%s-* allowed", ref, cnName)
		}
	}
	return nil
}
