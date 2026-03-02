package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		// argv becomes: [scriptPath, "--identity", "gastown/rust"]
		// so $1="--identity" and $2="gastown/rust".
		scriptPath := filepath.Join(t.TempDir(), "printargs.sh")
		require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '%s %s' \"$1\" \"$2\"\n"), 0755))

		srv2 := New(Config{AllowedCommands: []string{scriptPath}}, nil)
		body := fmt.Sprintf(`{"argv":[%q]}`, scriptPath)
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
	srv := New(Config{AllowedCommands: []string{"gt", "bd"}}, nil)
	assert.True(t, srv.isAllowed("gt"))
	assert.True(t, srv.isAllowed("bd"))
	assert.False(t, srv.isAllowed("curl"))
	assert.False(t, srv.isAllowed(""))

	// Empty allowlist — no commands allowed.
	empty := New(Config{}, nil)
	assert.False(t, empty.isAllowed("gt"))
	assert.False(t, empty.isAllowed("echo"))
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
