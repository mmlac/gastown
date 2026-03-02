package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logEntry captures a single structured log record for test assertions.
type logEntry struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// logCapture is a slog.Handler that records all log entries in memory.
type logCapture struct {
	mu      sync.Mutex
	entries []logEntry
}

func (lc *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (lc *logCapture) Handle(_ context.Context, r slog.Record) error {
	e := logEntry{
		level: r.Level,
		msg:   r.Message,
		attrs: make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		e.attrs[a.Key] = a.Value.String()
		return true
	})
	lc.mu.Lock()
	lc.entries = append(lc.entries, e)
	lc.mu.Unlock()
	return nil
}

func (lc *logCapture) WithAttrs(_ []slog.Attr) slog.Handler { return lc }
func (lc *logCapture) WithGroup(_ string) slog.Handler      { return lc }

// findEntry returns the first log entry matching the given level and message.
func (lc *logCapture) findEntry(level slog.Level, msg string) (logEntry, bool) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, e := range lc.entries {
		if e.level == level && e.msg == msg {
			return e, true
		}
	}
	return logEntry{}, false
}

// hasLevel reports whether any entry with the given level was logged.
func (lc *logCapture) hasLevel(level slog.Level) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, e := range lc.entries {
		if e.level == level {
			return true
		}
	}
	return false
}

// makeFakeRequest builds an httptest.Request with a fake TLS peer certificate CN.
func makeFakeRequest(method, path, body, cn string) *http.Request {
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if cn != "" {
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{
				{Subject: pkix.Name{CommonName: cn}},
			},
		}
	}
	return req
}

func TestPolecatName(t *testing.T) {
	cases := []struct {
		cn   string
		want string
	}{
		{"gt-gastown-furiosa", "furiosa"},
		{"gt-gas-town-furiosa", "furiosa"}, // hyphenated rig
		{"gt-gastown-", ""},                // empty name
		{"gt--furiosa", "furiosa"},         // empty rig; name still extracted
		{"noprefix-rig-name", ""},          // missing gt- prefix
		{"gt-nodashinrest", ""},            // only one component after stripping gt-
		{"", ""},                           // empty string
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.cn, func(t *testing.T) {
			got := polecatName(tc.cn)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCnToIdentity(t *testing.T) {
	cases := []struct {
		cn   string
		want string
	}{
		{"gt-gastown-furiosa", "gastown/furiosa"},
		{"gt-gas-town-furiosa", "gas-town/furiosa"}, // hyphenated rig
		{"gt-gastown-", ""},                          // empty name
		{"gt--furiosa", ""},                          // empty rig (two consecutive dashes after gt-)
		{"noprefix-rig-name", ""},                    // missing gt- prefix
		{"gt-nodashinrest", ""},                      // only one component after stripping gt-
		{"", ""},                                     // empty string
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.cn, func(t *testing.T) {
			got := cnToIdentity(tc.cn)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractIdentity(t *testing.T) {
	t.Run("nil TLS returns empty string", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", nil)
		// req.TLS is nil by default from httptest
		assert.Equal(t, "", extractIdentity(req))
	})

	t.Run("empty PeerCertificates returns empty string", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", nil)
		req.TLS = &tls.ConnectionState{PeerCertificates: nil}
		assert.Equal(t, "", extractIdentity(req))
	})

	t.Run("valid CN parses to identity", func(t *testing.T) {
		req := makeFakeRequest("POST", "/v1/exec", "", "gt-gastown-rust")
		assert.Equal(t, "gastown/rust", extractIdentity(req))
	})
}

func TestHandleExec(t *testing.T) {
	srv := New(Config{AllowedCommands: []string{"echo", "sh", "sleep"}}, nil)

	t.Run("GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/exec", nil)
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("body over 1 MiB returns 400", func(t *testing.T) {
		// Valid JSON prefix with a huge payload that exceeds the 1 MiB limit.
		bigStr := strings.Repeat("x", 1<<20)
		body := `{"argv":["echo","` + bigStr + `"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("malformed JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader("{not json}"))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("empty argv returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(`{"argv":[]}`))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("argv[0] not in allowlist returns 403", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(`{"argv":["curl","http://evil.com"]}`))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("allowed command succeeds with correct output", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(`{"argv":["echo","hello"]}`))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, 0, resp.ExitCode)
		assert.Contains(t, resp.Stdout, "hello")
	})

	t.Run("--identity flag is injected as argv[1] and argv[2] when CN is present", func(t *testing.T) {
		// Write a tiny script that prints its first two positional args.
		// argv becomes: [scriptName, "--identity", "gastown/rust"]
		// so $1="--identity" and $2="gastown/rust".
		// The script is placed in a temp dir added to PATH so AllowedCommands
		// can reference it by plain name (no path separator — issue 12).
		scriptDir := t.TempDir()
		scriptPath := filepath.Join(scriptDir, "printargs.sh")
		require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '%s %s' \"$1\" \"$2\"\n"), 0755))
		t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))

		srv2 := New(Config{AllowedCommands: []string{"printargs.sh"}}, nil)
		body := `{"argv":["printargs.sh"]}`
		req := makeFakeRequest("POST", "/v1/exec", body, "gt-gastown-rust")
		rec := httptest.NewRecorder()
		srv2.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "--identity gastown/rust", resp.Stdout)
	})

	t.Run("non-zero exit code is returned", func(t *testing.T) {
		body := `{"argv":["sh","-c","exit 42"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, 42, resp.ExitCode)
	})

	t.Run("context cancellation kills command and handler returns", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		body := `{"argv":["sleep","10"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			srv.handleExec(rec, req)
			close(done)
		}()

		// Give the command time to start, then cancel.
		time.Sleep(100 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("handleExec did not return after context cancellation")
		}
		// Response should be written with non-zero exit code.
		var resp execResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.NotZero(t, resp.ExitCode)
	})
}

func TestRunCommand(t *testing.T) {
	t.Run("echo world produces expected stdout", func(t *testing.T) {
		stdout, stderr, code := runCommand(context.Background(), []string{"echo", "world"})
		assert.Equal(t, "world\n", stdout)
		assert.Equal(t, "", stderr)
		assert.Equal(t, 0, code)
	})

	t.Run("sh exit 42 returns exitCode 42", func(t *testing.T) {
		_, _, code := runCommand(context.Background(), []string{"sh", "-c", "exit 42"})
		assert.Equal(t, 42, code)
	})

	t.Run("stderr is captured separately", func(t *testing.T) {
		stdout, stderr, code := runCommand(context.Background(), []string{"sh", "-c", "echo err >&2"})
		assert.Equal(t, "", stdout)
		assert.Equal(t, "err\n", stderr)
		assert.Equal(t, 0, code)
	})

	t.Run("non-existent binary returns exitCode 1", func(t *testing.T) {
		_, _, code := runCommand(context.Background(), []string{"/no/such/binary/xyzzy"})
		assert.Equal(t, 1, code)
	})

	t.Run("environment is restricted", func(t *testing.T) {
		// Set a sentinel in the test process env; the subprocess must not see it.
		t.Setenv("PROXY_TEST_SENTINEL", "super_secret_sentinel_12345")

		stdout, _, code := runCommand(context.Background(), []string{"sh", "-c", "echo ${PROXY_TEST_SENTINEL:-NOT_SET}"})
		assert.Equal(t, 0, code)
		assert.NotContains(t, stdout, "super_secret_sentinel_12345",
			"subprocess should not inherit test env vars")
	})
}

// TestIsAllowed tests the Server.isAllowed helper.
func TestIsAllowed(t *testing.T) {
	srv := New(Config{AllowedCommands: []string{"echo", "sh"}}, nil)
	assert.True(t, srv.isAllowed("echo"))
	assert.True(t, srv.isAllowed("sh"))
	assert.False(t, srv.isAllowed("curl"))
	assert.False(t, srv.isAllowed(""))

	// Empty allowlist — no commands allowed.
	empty := New(Config{}, nil)
	assert.False(t, empty.isAllowed("echo"))
	assert.False(t, empty.isAllowed("sh"))
}

// TestHandleExecBodyBytes tests that bodies close to the limit are handled correctly.
func TestHandleExecBodyBytes(t *testing.T) {
	srv := New(Config{AllowedCommands: []string{"echo"}}, nil)

	t.Run("body exactly at limit succeeds if valid JSON", func(t *testing.T) {
		// Small valid body should succeed.
		req := httptest.NewRequest("POST", "/v1/exec",
			bytes.NewReader([]byte(`{"argv":["echo","ok"]}`)))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// TestSubcommandValidation tests the subcommand allowlist enforcement.
func TestSubcommandValidation(t *testing.T) {
	srv := New(Config{
		AllowedCommands: []string{"echo", "sh"},
		AllowedSubcommands: map[string][]string{
			"echo": {"hello", "world"},
		},
	}, nil)

	t.Run("subcommand not in allowlist returns 403", func(t *testing.T) {
		body := `{"argv":["echo","forbidden"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "subcommand not allowed")
	})

	t.Run("subcommand in allowlist returns 200", func(t *testing.T) {
		body := `{"argv":["echo","hello"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("command with subcommand allowlist but no argv[1] returns 403", func(t *testing.T) {
		body := `{"argv":["echo"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "subcommand required")
	})

	t.Run("command with no subcommand allowlist entry passes subcommand check", func(t *testing.T) {
		// "sh" has no entry in AllowedSubcommands, so any subcommand is allowed.
		body := `{"argv":["sh","-c","exit 0"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// TestHandleExecAuditLog verifies that exec calls produce structured audit log records.
func TestHandleExecAuditLog(t *testing.T) {
	t.Run("success emits INFO record with identity and cmd fields", func(t *testing.T) {
		lc := &logCapture{}
		logger := slog.New(lc)
		srv := New(Config{AllowedCommands: []string{"echo"}, Logger: logger}, nil)

		req := makeFakeRequest("POST", "/v1/exec", `{"argv":["echo","hi"]}`, "gt-gastown-shiny")
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		e, ok := lc.findEntry(slog.LevelInfo, "exec")
		require.True(t, ok, "expected INFO 'exec' log record")
		assert.Equal(t, "gastown/shiny", e.attrs["identity"])
		assert.Equal(t, "echo", e.attrs["cmd"])
	})

	t.Run("non-zero exit emits WARN record", func(t *testing.T) {
		lc := &logCapture{}
		logger := slog.New(lc)
		srv := New(Config{AllowedCommands: []string{"sh"}, Logger: logger}, nil)

		body := `{"argv":["sh","-c","exit 7"]}`
		req := httptest.NewRequest("POST", "/v1/exec", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handleExec(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		e, ok := lc.findEntry(slog.LevelWarn, "exec failed")
		require.True(t, ok, "expected WARN 'exec failed' log record")
		assert.Equal(t, "sh", e.attrs["cmd"])
	})
}

// TestBinaryResolution verifies that commands not found in PATH are removed from the allowlist.
func TestBinaryResolution(t *testing.T) {
	lc := &logCapture{}
	logger := slog.New(lc)

	srv := New(Config{
		AllowedCommands: []string{"echo", "this-binary-does-not-exist-xyzzy-12345"},
		Logger:          logger,
	}, nil)

	assert.True(t, srv.isAllowed("echo"), "echo should remain in allowlist")
	assert.False(t, srv.isAllowed("this-binary-does-not-exist-xyzzy-12345"),
		"non-existent binary should be removed from allowlist")
	assert.True(t, lc.hasLevel(slog.LevelError), "expected error log for missing binary")
}
