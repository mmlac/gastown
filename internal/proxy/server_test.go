package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardLogger returns a slog.Logger that discards all output (keeps test output clean).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNew(t *testing.T) {
	t.Run("empty AllowedCommands logs warning and produces empty map", func(t *testing.T) {
		// New() logs a warning but must not panic.
		assert.NotPanics(t, func() {
			srv := New(Config{Logger: discardLogger()}, nil)
			assert.NotNil(t, srv)
			assert.Empty(t, srv.allowed)
		})
	})

	t.Run("AllowedCommands are stored in allowed map", func(t *testing.T) {
		srv := New(Config{AllowedCommands: []string{"gt", "bd"}, Logger: discardLogger()}, nil)
		assert.True(t, srv.allowed["gt"])
		assert.True(t, srv.allowed["bd"])
		assert.False(t, srv.allowed["curl"])
	})

	t.Run("isAllowed reflects allowed map", func(t *testing.T) {
		srv := New(Config{AllowedCommands: []string{"gt", "bd"}, Logger: discardLogger()}, nil)
		assert.True(t, srv.isAllowed("gt"))
		assert.True(t, srv.isAllowed("bd"))
		assert.False(t, srv.isAllowed("curl"))
		assert.False(t, srv.isAllowed(""))
	})

	t.Run("AllowedCommands with path separators are rejected", func(t *testing.T) {
		srv := New(Config{AllowedCommands: []string{"/usr/bin/gt", "bd", `C:\gt.exe`}, Logger: discardLogger()}, nil)
		assert.False(t, srv.isAllowed("/usr/bin/gt"), "absolute path should be rejected")
		assert.True(t, srv.isAllowed("bd"), "plain name should be accepted")
		assert.False(t, srv.isAllowed(`C:\gt.exe`), "windows path should be rejected")
	})
}

func TestMinimalEnv(t *testing.T) {
	t.Run("contains HOME and PATH when set", func(t *testing.T) {
		// Ensure HOME and PATH are set (they almost always are, but be safe).
		if os.Getenv("HOME") == "" {
			t.Setenv("HOME", "/tmp/testhome")
		}
		if os.Getenv("PATH") == "" {
			t.Setenv("PATH", "/usr/bin:/bin")
		}

		env := minimalEnv()
		hasHome, hasPath := false, false
		for _, e := range env {
			if strings.HasPrefix(e, "HOME=") {
				hasHome = true
			}
			if strings.HasPrefix(e, "PATH=") {
				hasPath = true
			}
		}
		assert.True(t, hasHome, "minimalEnv should include HOME")
		assert.True(t, hasPath, "minimalEnv should include PATH")
	})

	t.Run("does NOT contain arbitrary test env vars", func(t *testing.T) {
		t.Setenv("PROXY_TEST_SENTINEL_ENV", "secret_value_xyz")

		env := minimalEnv()
		for _, e := range env {
			assert.False(t, strings.Contains(e, "PROXY_TEST_SENTINEL_ENV"),
				"minimalEnv should not contain test sentinel env var, got: %s", e)
		}
	})

	t.Run("omits keys that are empty", func(t *testing.T) {
		// Temporarily unset HOME to verify it is omitted when empty.
		orig := os.Getenv("HOME")
		require.NoError(t, os.Unsetenv("HOME"))
		t.Cleanup(func() {
			if orig != "" {
				os.Setenv("HOME", orig) //nolint:errcheck
			}
		})

		env := minimalEnv()
		for _, e := range env {
			assert.False(t, strings.HasPrefix(e, "HOME="),
				"empty HOME should be omitted, got: %s", e)
		}
	})
}

// waitForServer polls addr until a TCP connection succeeds or timeout elapses.
func waitForServer(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %v", addr, timeout)
}

func TestStartIntegration(t *testing.T) {
	dir := t.TempDir()
	ca, err := GenerateCA(dir)
	require.NoError(t, err)

	srv := New(Config{
		ListenAddr:      "127.0.0.1:0",
		AllowedCommands: []string{"echo"},
		TownRoot:        t.TempDir(),
		Logger:          discardLogger(),
	}, ca)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	startErr := make(chan error, 1)
	go func() {
		startErr <- srv.Start(ctx)
	}()

	// Wait until the listener is bound and the address is available via Addr().
	var addr string
	require.Eventually(t, func() bool {
		if a := srv.Addr(); a != nil {
			addr = a.String()
			return true
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "server Addr() should become non-nil after Start()")
	waitForServer(t, addr, 5*time.Second)

	// Build a CA pool and an authorised client cert.
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)

	clientCertPEM, clientKeyPEM, err := ca.IssuePolecat("gt-gastown-testclient", time.Hour)
	require.NoError(t, err)
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	require.NoError(t, err)

	// ServerName must match the CN on the server cert ("gt-proxy-server"),
	// not the IP we're connecting to, because IssueServer issues no IP SANs.
	authorisedClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{clientCert},
				RootCAs:      pool,
				ServerName:   "gt-proxy-server",
			},
		},
	}

	t.Run("authorised client can POST exec and get output", func(t *testing.T) {
		body := strings.NewReader(`{"argv":["echo","hello"]}`)
		resp, err := authorisedClient.Post(
			"https://"+addr+"/v1/exec",
			"application/json",
			body,
		)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var result execResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		assert.Equal(t, 0, result.ExitCode)
		assert.Contains(t, result.Stdout, "hello")
	})

	t.Run("client with no certificate is rejected at TLS handshake", func(t *testing.T) {
		noCertClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:    pool,
					ServerName: "gt-proxy-server",
					// No Certificates — no client cert presented.
				},
			},
		}
		_, err := noCertClient.Post(
			"https://"+addr+"/v1/exec",
			"application/json",
			strings.NewReader(`{"argv":["echo","hi"]}`),
		)
		assert.Error(t, err, "connection without client cert should be rejected")
	})

	t.Run("client cert signed by different CA is rejected", func(t *testing.T) {
		// Generate a completely independent CA.
		dir2 := t.TempDir()
		ca2, err := GenerateCA(dir2)
		require.NoError(t, err)

		wrongCertPEM, wrongKeyPEM, err := ca2.IssuePolecat("gt-gastown-evil", time.Hour)
		require.NoError(t, err)
		wrongClientCert, err := tls.X509KeyPair(wrongCertPEM, wrongKeyPEM)
		require.NoError(t, err)

		wrongClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{wrongClientCert},
					RootCAs:      pool,
					ServerName:   "gt-proxy-server",
				},
			},
		}
		_, err = wrongClient.Post(
			"https://"+addr+"/v1/exec",
			"application/json",
			strings.NewReader(`{"argv":["echo","hi"]}`),
		)
		assert.Error(t, err, "client cert from different CA should be rejected")
	})

	t.Run("cancelling context causes server to shut down", func(t *testing.T) {
		cancel()
		// Allow the server goroutine to finish shutting down.
		select {
		case err := <-startErr:
			// Shutdown returns nil (graceful) or ErrServerClosed — both are fine.
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("server did not shut down within 5s after context cancellation")
		}

		// Subsequent requests must fail (server is gone); use a fresh client to avoid
		// reusing cached connections.
		freshClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{clientCert},
					RootCAs:      pool,
					ServerName:   "gt-proxy-server",
				},
			},
		}
		_, err := freshClient.Post(
			"https://"+addr+"/v1/exec",
			"application/json",
			strings.NewReader(`{"argv":["echo","hi"]}`),
		)
		assert.Error(t, err, "requests after shutdown should fail")
	})
}
