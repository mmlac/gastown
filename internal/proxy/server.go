package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// Config holds configuration for the proxy server.
type Config struct {
	ListenAddr      string
	AllowedCommands []string
	// TownRoot is the path to the Gas Town root directory (e.g. ~/gt).
	// Populated from the GT_TOWN env var or ~/gt by default.
	TownRoot string
}

// Server is an mTLS HTTP proxy server.
type Server struct {
	cfg     Config
	ca      *CA
	allowed map[string]bool
}

// New creates a new Server with the given config and CA.
// It logs a warning if AllowedCommands is empty, since no commands would be
// permitted — a safe default but almost certainly a misconfiguration.
func New(cfg Config, ca *CA) *Server {
	allowed := make(map[string]bool, len(cfg.AllowedCommands))
	for _, cmd := range cfg.AllowedCommands {
		allowed[cmd] = true
	}
	if len(allowed) == 0 {
		log.Printf("gt-proxy-server: WARNING: AllowedCommands is empty — all exec requests will be denied")
	}
	return &Server{cfg: cfg, ca: ca, allowed: allowed}
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

// minimalEnv returns a minimal environment for git and gt/bd subprocesses,
// containing only HOME and PATH to avoid leaking server credentials.
func minimalEnv() []string {
	env := []string{}
	for _, key := range []string{"HOME", "PATH"} {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return env
}
