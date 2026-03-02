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
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Config holds configuration for the proxy server.
type Config struct {
	ListenAddr      string
	AllowedCommands []string
	// AllowedSubcommands maps each allowed command ("gt", "bd") to the set of
	// subcommands that polecats may invoke. If a command has an entry here,
	// argv[1] must appear in its list; absent argv[1] → 403.
	// If a command has NO entry, subcommands are unrestricted for that command
	// (safe for single-subcommand tools, but not intended for gt/bd).
	AllowedSubcommands map[string][]string
	// TownRoot is the path to the Gas Town root directory (e.g. ~/gt).
	// Populated from the GT_TOWN env var or ~/gt by default.
	TownRoot string
	// Logger is the structured logger to use. nil uses slog.Default().
	Logger *slog.Logger
}

// Server is an mTLS HTTP proxy server.
type Server struct {
	cfg          Config
	ca           *CA
	allowed      map[string]bool
	allowedSubs  map[string]map[string]bool
	resolvedPaths map[string]string
	log          *slog.Logger

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

	// Resolve binary paths at startup to prevent PATH hijacking after startup.
	resolvedPaths := make(map[string]string, len(allowed))
	for cmd := range allowed {
		path, err := exec.LookPath(cmd)
		if err != nil {
			l.Error("command not found in PATH — removing from allowlist", "cmd", cmd)
			delete(allowed, cmd)
			continue
		}
		resolvedPaths[cmd] = path
	}

	if len(allowed) == 0 {
		l.Warn("AllowedCommands is empty — all exec requests will be denied")
	}

	// Build subcommand allowlists from config.
	allowedSubs := make(map[string]map[string]bool, len(cfg.AllowedSubcommands))
	for cmd, subs := range cfg.AllowedSubcommands {
		m := make(map[string]bool, len(subs))
		for _, sub := range subs {
			m[sub] = true
		}
		allowedSubs[cmd] = m
	}

	return &Server{cfg: cfg, ca: ca, allowed: allowed, allowedSubs: allowedSubs, resolvedPaths: resolvedPaths, log: l}
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

	// Generate a server cert from our CA for TLS, including IP SANs so that clients
	// connecting by IP (e.g. containers reaching the proxy at 172.17.0.1) can verify
	// the cert without an explicit ServerName override.
	ips := serverListenIPs(s.cfg.ListenAddr)
	certPEM, keyPEM, err := s.ca.IssueServer("gt-proxy-server", ips, 365*24*time.Hour)
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

// serverListenIPs returns the IP addresses that should be included as IP SANs in the
// server certificate. It parses the host portion of listenAddr and:
//   - If it is a specific non-loopback IP, returns [that IP, 127.0.0.1].
//   - If it is 0.0.0.0 (unspecified), enumerates all non-loopback IPv4 interface
//     addresses and prepends 127.0.0.1.
//   - Returns [127.0.0.1] at minimum on any parse or enumeration error.
func serverListenIPs(listenAddr string) []net.IP {
	loopback := net.ParseIP("127.0.0.1")

	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return []net.IP{loopback}
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// hostname or empty — just use loopback
		return []net.IP{loopback}
	}

	if ip.IsUnspecified() {
		// 0.0.0.0 — include loopback plus all non-loopback IPv4 interface addresses.
		ips := []net.IP{loopback}
		ifaces, err := net.Interfaces()
		if err != nil {
			return ips
		}
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ifaceIP net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ifaceIP = v.IP
				case *net.IPAddr:
					ifaceIP = v.IP
				}
				if ifaceIP == nil || ifaceIP.IsLoopback() {
					continue
				}
				if ip4 := ifaceIP.To4(); ip4 != nil {
					ips = append(ips, ip4)
				}
			}
		}
		return ips
	}

	if ip.IsLoopback() {
		return []net.IP{loopback}
	}
	// Specific non-loopback IP: include both that IP and loopback for local connections.
	return []net.IP{ip, loopback}
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
