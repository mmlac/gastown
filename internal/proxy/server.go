package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Config holds configuration for the proxy server.
type Config struct {
	ListenAddr      string
	AllowedCommands []string
	// TownRoot is the path to the Gas Town root directory (e.g. ~/gt).
	// Populated from the GT_TOWN env var or ~/gt by default.
	TownRoot string
	// Logger is the structured logger to use. nil uses slog.Default().
	Logger *slog.Logger
}

// Server is an mTLS HTTP proxy server.
type Server struct {
	cfg     Config
	ca      *CA
	allowed map[string]bool
	log     *slog.Logger

	lnMu sync.Mutex
	ln   net.Listener
}

// New creates a new Server with the given config and CA.
// It logs a warning if AllowedCommands is empty, since no commands would be
// permitted — a safe default but almost certainly a misconfiguration.
// Any AllowedCommands entries containing "/" or "\" are rejected and removed.
func New(cfg Config, ca *CA) *Server {
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}

	allowed := make(map[string]bool, len(cfg.AllowedCommands))
	for _, cmd := range cfg.AllowedCommands {
		// Issue 12: AllowedCommands must be plain names, not paths.
		if strings.ContainsAny(cmd, `/\`) {
			l.Error("AllowedCommands entry contains path separator — ignoring", "entry", cmd)
			continue
		}
		allowed[cmd] = true
	}
	if len(allowed) == 0 {
		l.Warn("AllowedCommands is empty — all exec requests will be denied")
	}
	return &Server{cfg: cfg, ca: ca, allowed: allowed, log: l}
}

// Addr returns the address the server is listening on.
// Valid only after Start() has progressed past the listen call (i.e. after
// the first request is handled, or after waitForServer returns in tests).
func (s *Server) Addr() net.Addr {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
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
		Addr:        s.cfg.ListenAddr,
		Handler:     mux,
		TLSConfig:   tlsCfg,
		ReadTimeout: 30 * time.Second,
		// WriteTimeout is generous to accommodate git push/fetch streams.
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	// Generate a server cert from our CA for TLS.
	certPEM, keyPEM, err := s.ca.IssueServer("gt-proxy-server", 365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("issue server cert: %w", err)
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("load server cert: %w", err)
	}
	tlsCfg.Certificates = []tls.Certificate{tlsCert}

	// Issue 11: Use net.Listen + ServeTLS so we can expose the bound address via Addr().
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.lnMu.Lock()
	s.ln = ln
	s.lnMu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("gt-proxy-server: listening", "addr", ln.Addr(), "tls", "mTLS")
		if err := srv.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		// Issue 5: Give shutdown a reasonable deadline to drain in-flight requests.
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// minimalEnv returns a minimal environment for git and gt/bd subprocesses,
// containing only HOME and PATH to avoid leaking server credentials.
// GIT_EXEC_PATH is intentionally omitted: the git binary resolves it
// automatically from its own installation path, so passing HOME and PATH
// is sufficient for git subcommands to locate git-core helpers.
func minimalEnv() []string {
	env := []string{}
	for _, key := range []string{"HOME", "PATH"} {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return env
}
