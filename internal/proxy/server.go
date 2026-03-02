package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config holds configuration for the proxy server.
type Config struct {
	ListenAddr      string
	CAFile          string
	AllowedCommands []string
}

// Server is an mTLS HTTP proxy server.
type Server struct {
	cfg Config
	ca  *CA
}

// New creates a new Server with the given config and CA.
func New(cfg Config, ca *CA) *Server {
	return &Server{cfg: cfg, ca: ca}
}

// Start begins listening and serving. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	pool := x509.NewCertPool()
	pool.AddCert(s.ca.Cert)

	tlsCfg := &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		MinVersion: tls.VersionTLS13,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/exec", s.handleExec)
	mux.HandleFunc("/v1/git/", s.handleGit)

	srv := &http.Server{
		Addr:      s.cfg.ListenAddr,
		Handler:   mux,
		TLSConfig: tlsCfg,
	}

	// Generate a server cert from our CA for TLS.
	certPEM, keyPEM, err := s.ca.IssuePolecat("gt-proxy-server", 365*24*1e9 /* 365 days */)
	if err != nil {
		return fmt.Errorf("issue server cert: %w", err)
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("load server cert: %w", err)
	}
	tlsCfg.Certificates = []tls.Certificate{tlsCert}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("gt-proxy-server: listening on %s (mTLS)", s.cfg.ListenAddr)
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}

// execRequest is the body for POST /v1/exec.
type execRequest struct {
	Argv []string `json:"argv"`
}

// execResponse is the response for POST /v1/exec.
type execResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract identity from client cert CN (format: gt-<rig>-<name>).
	identity := s.extractIdentity(r)

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Argv) == 0 {
		http.Error(w, "argv is empty", http.StatusBadRequest)
		return
	}

	// Validate argv[0] is in the allowlist.
	cmd0 := req.Argv[0]
	if !s.isAllowed(cmd0) {
		http.Error(w, fmt.Sprintf("command not allowed: %q", cmd0), http.StatusForbidden)
		return
	}

	argv := req.Argv
	// Inject --identity <rig>/<name> if we have a valid identity.
	if identity != "" {
		argv = append(argv, "--identity", identity)
	}

	out, errOut, exitCode := runCommand(argv)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(execResponse{
		Stdout:   out,
		Stderr:   errOut,
		ExitCode: exitCode,
	})
}

// extractIdentity parses the client cert CN "gt-<rig>-<name>" into "<rig>/<name>".
func (s *Server) extractIdentity(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	cn := r.TLS.PeerCertificates[0].Subject.CommonName
	// Expected format: gt-<rig>-<name>
	if !strings.HasPrefix(cn, "gt-") {
		return ""
	}
	rest := cn[3:] // strip "gt-"
	idx := strings.Index(rest, "-")
	if idx < 0 {
		return ""
	}
	rig := rest[:idx]
	name := rest[idx+1:]
	return rig + "/" + name
}

func (s *Server) isAllowed(cmd string) bool {
	for _, a := range s.cfg.AllowedCommands {
		if a == cmd {
			return true
		}
	}
	return false
}

func runCommand(argv []string) (stdout, stderr string, exitCode int) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

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

	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, "no home dir", http.StatusInternalServerError)
		return
	}
	repoPath := filepath.Join(home, "gt", rig, ".repo.git")

	switch {
	case rest == "info/refs":
		s.handleInfoRefs(w, r, repoPath, rig)
	case rest == "git-upload-pack":
		s.handlePack(w, r, repoPath, "git-upload-pack", rig, "")
	case rest == "git-receive-pack":
		cn := s.clientCN(r)
		s.handlePack(w, r, repoPath, "git-receive-pack", rig, cn)
	default:
		http.Error(w, "unknown git endpoint", http.StatusNotFound)
	}
}

func (s *Server) clientCN(r *http.Request) string {
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
	if err := cmd.Run(); err != nil {
		log.Printf("git pack (%s %s): %v", service, rig, err)
	}
}

// authorizeReceivePack checks that the push only touches refs/heads/polecat/<cn-name>-*.
// It reads the pkt-line stream to extract ref names, then rewinds the body.
// For simplicity, we enforce by CN name extracted from the cert.
func (s *Server) authorizeReceivePack(w http.ResponseWriter, r *http.Request, clientCN string) bool {
	// CN format: gt-<rig>-<name>; extract <name>.
	cnName := ""
	if strings.HasPrefix(clientCN, "gt-") {
		rest := clientCN[3:]
		idx := strings.Index(rest, "-")
		if idx >= 0 {
			cnName = rest[idx+1:]
		}
	}
	if cnName == "" {
		http.Error(w, "cannot determine polecat name from cert CN", http.StatusForbidden)
		return false
	}

	// We can't easily inspect the pkt-line stream without consuming it,
	// so we wrap the body in an authorizing reader that buffers and validates.
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

	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return true
}

// validateReceivePackRefs parses the git-receive-pack pkt-line stream and validates
// that all pushed refs are under refs/heads/polecat/<cnName>-*.
func validateReceivePackRefs(body []byte, cnName string) error {
	// The receive-pack request body consists of pkt-lines followed by pack data.
	// Each pkt-line: 4 hex bytes length, then data.
	// We only need to check the ref lines at the start.
	data := string(body)
	offset := 0
	for offset < len(data) {
		if offset+4 > len(data) {
			break
		}
		lenHex := data[offset : offset+4]
		if lenHex == "0000" {
			break // flush packet: end of ref list
		}
		var pktLen int
		_, err := fmt.Sscanf(lenHex, "%x", &pktLen)
		if err != nil || pktLen < 4 {
			break
		}
		end := offset + pktLen
		if end > len(data) {
			break
		}
		line := data[offset+4 : end]
		offset = end

		// Each line: "<old-sha> <new-sha> <refname>\0[capabilities]\n"
		// Strip trailing newline.
		line = strings.TrimRight(line, "\n")
		// Strip NUL and capabilities.
		if idx := strings.IndexByte(line, 0); idx >= 0 {
			line = line[:idx]
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		ref := parts[2]

		allowed := "refs/heads/polecat/" + cnName + "-"
		if ref != "refs/heads/polecat/"+cnName && !strings.HasPrefix(ref, allowed) {
			return fmt.Errorf("push to %q denied: only refs/heads/polecat/%s-* allowed", ref, cnName)
		}
	}
	return nil
}
