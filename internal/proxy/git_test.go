package proxy

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pktLine encodes s as a single git pkt-line (4-hex-byte length prefix).
// The length field includes the 4 bytes of the length prefix itself.
func pktLine(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}

// receivePackBody builds a minimal git-receive-pack pkt-line stream for the given refs.
// Each entry is formatted as "<zero-sha> <ones-sha> <refname>\n".
func receivePackBody(refs ...string) []byte {
	zeroSHA := strings.Repeat("0", 40)
	newSHA := strings.Repeat("a", 40)
	var buf strings.Builder
	for _, ref := range refs {
		line := zeroSHA + " " + newSHA + " " + ref + "\n"
		buf.WriteString(pktLine(line))
	}
	buf.WriteString("0000") // flush packet
	return []byte(buf.String())
}

// errReadCloser is an io.ReadCloser that always returns an error on Read.
type errReadCloser struct{ err error }

func (e errReadCloser) Read(_ []byte) (int, error) { return 0, e.err }
func (e errReadCloser) Close() error               { return nil }

// ---- validateReceivePackRefs ----

func TestValidateReceivePackRefs(t *testing.T) {
	const polecat = "furiosa"

	t.Run("empty body returns nil", func(t *testing.T) {
		assert.NoError(t, validateReceivePackRefs(nil, polecat))
		assert.NoError(t, validateReceivePackRefs([]byte{}, polecat))
	})

	t.Run("flush-only body returns nil", func(t *testing.T) {
		assert.NoError(t, validateReceivePackRefs([]byte("0000"), polecat))
	})

	t.Run("single valid ref returns nil", func(t *testing.T) {
		body := receivePackBody("refs/heads/polecat/furiosa-abc123")
		assert.NoError(t, validateReceivePackRefs(body, polecat))
	})

	t.Run("multiple valid refs return nil", func(t *testing.T) {
		body := receivePackBody(
			"refs/heads/polecat/furiosa-abc123",
			"refs/heads/polecat/furiosa-def456",
		)
		assert.NoError(t, validateReceivePackRefs(body, polecat))
	})

	t.Run("ref to main is denied", func(t *testing.T) {
		body := receivePackBody("refs/heads/main")
		err := validateReceivePackRefs(body, polecat)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "main")
	})

	t.Run("exact polecat ref without dash suffix is denied", func(t *testing.T) {
		// "refs/heads/polecat/furiosa" has no dash suffix → denied.
		body := receivePackBody("refs/heads/polecat/furiosa")
		err := validateReceivePackRefs(body, polecat)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refs/heads/polecat/furiosa")
	})

	t.Run("wrong polecat name is denied", func(t *testing.T) {
		body := receivePackBody("refs/heads/polecat/otherpolecat-abc")
		err := validateReceivePackRefs(body, polecat)
		require.Error(t, err)
	})

	t.Run("mixed valid and invalid returns error on first invalid", func(t *testing.T) {
		body := receivePackBody(
			"refs/heads/polecat/furiosa-ok",
			"refs/heads/main",
		)
		err := validateReceivePackRefs(body, polecat)
		assert.Error(t, err)
	})

	t.Run("malformed pkt-line length stops parsing without panic", func(t *testing.T) {
		// Only 2 bytes where 4 are needed for the length field.
		body := []byte("00")
		var err error
		assert.NotPanics(t, func() {
			err = validateReceivePackRefs(body, polecat)
		})
		require.Error(t, err, "truncated length field must be rejected (fail-closed)")
	})

	t.Run("truncated pkt-line body stops parsing without panic", func(t *testing.T) {
		// Length says 16 bytes but only "hello" (5 bytes payload) is present.
		body := []byte("0010hello")
		var err error
		assert.NotPanics(t, func() {
			err = validateReceivePackRefs(body, polecat)
		})
		require.Error(t, err, "truncated pkt-line body must be rejected (fail-closed)")
	})

	t.Run("pkt-line with NUL-separated capabilities parses ref correctly", func(t *testing.T) {
		zeroSHA := strings.Repeat("0", 40)
		newSHA := strings.Repeat("a", 40)
		// ref line with NUL-separated capability string
		line := zeroSHA + " " + newSHA + " refs/heads/polecat/furiosa-abc\x00side-band-64k\n"
		body := []byte(pktLine(line) + "0000")
		assert.NoError(t, validateReceivePackRefs(body, polecat))
	})

	t.Run("line with fewer than 3 fields is skipped without error", func(t *testing.T) {
		line := "onlyone\n"
		body := []byte(pktLine(line) + "0000")
		assert.NoError(t, validateReceivePackRefs(body, polecat))
	})

	t.Run("pktLen==4 empty payload does not spin", func(t *testing.T) {
		// "0004" means a packet with only the length field (no payload).
		body := []byte("0004" + "0000")
		assert.NotPanics(t, func() {
			_ = validateReceivePackRefs(body, polecat)
		})
	})

	t.Run("binary pack data after flush packet is ignored", func(t *testing.T) {
		// "0000" flush followed by raw binary pack data. Must not panic or error.
		binaryJunk := []byte("0000\x00\x00\x00\x02\xff\xfe\xfd\xfc PACK binary garbage")
		assert.NotPanics(t, func() {
			err := validateReceivePackRefs(binaryJunk, polecat)
			assert.NoError(t, err)
		})
	})
}

// ---- authorizeReceivePack ----

func TestAuthorizeReceivePack(t *testing.T) {
	srv := New(Config{}, nil)

	t.Run("CN with no gt- prefix returns 403", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/git/rig/git-receive-pack",
			bytes.NewReader(receivePackBody()))
		ok := srv.authorizeReceivePack(rec, req, "notgt-rig-name")
		assert.False(t, ok)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("CN = 'gt-' with no rig or name returns 403", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/git/rig/git-receive-pack",
			bytes.NewReader(receivePackBody()))
		ok := srv.authorizeReceivePack(rec, req, "gt-")
		assert.False(t, ok)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("valid CN and valid refs — body is rewound correctly", func(t *testing.T) {
		cn := "gt-gastown-furiosa"
		body := receivePackBody("refs/heads/polecat/furiosa-abc123")

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/git/rig/git-receive-pack",
			bytes.NewReader(body))
		ok := srv.authorizeReceivePack(rec, req, cn)

		require.True(t, ok)
		// Verify body was rewound so git can re-read it.
		rewound, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Equal(t, body, rewound, "body should be rewound to its original content")
	})

	t.Run("valid CN with invalid refs returns 403", func(t *testing.T) {
		cn := "gt-gastown-furiosa"
		body := receivePackBody("refs/heads/main")

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/git/rig/git-receive-pack",
			bytes.NewReader(body))
		ok := srv.authorizeReceivePack(rec, req, cn)

		assert.False(t, ok)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("body read error returns 500", func(t *testing.T) {
		cn := "gt-gastown-furiosa"
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/git/rig/git-receive-pack", nil)
		req.Body = errReadCloser{err: fmt.Errorf("simulated read error")}

		ok := srv.authorizeReceivePack(rec, req, cn)
		assert.False(t, ok)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

// ---- handleGit routing ----

func newGitServer(t *testing.T) (*Server, string) {
	t.Helper()
	townRoot := t.TempDir()
	srv := New(Config{TownRoot: townRoot}, nil)
	// Pre-create the "testrip" repo directory so routing tests that reach
	// handleInfoRefs/handlePack pass the repo existence pre-flight.
	require.NoError(t, os.MkdirAll(filepath.Join(townRoot, "testrip", ".repo.git"), 0700))
	return srv, townRoot
}

func TestHandleGitRouting(t *testing.T) {
	t.Run("no rig segment returns 400", func(t *testing.T) {
		srv, _ := newGitServer(t)
		// /v1/git/ with nothing after → path = "" → only one part
		req := httptest.NewRequest("GET", "/v1/git/", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("known rig with empty operation returns 404", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("GET", "/v1/git/testrip/", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("unknown operation returns 404", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("GET", "/v1/git/testrip/bogus", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("info/refs POST returns 405", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("POST", "/v1/git/testrip/info/refs?service=git-upload-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("info/refs with unsupported service returns 400", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("GET", "/v1/git/testrip/info/refs?service=git-archive", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("git-receive-pack POST with no CN returns 403", func(t *testing.T) {
		srv, _ := newGitServer(t)
		body := receivePackBody("refs/heads/polecat/furiosa-abc")
		req := httptest.NewRequest("POST", "/v1/git/testrip/git-receive-pack",
			bytes.NewReader(body))
		// r.TLS is nil → clientCN returns "" → authorizeReceivePack returns 403
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("git-receive-pack GET returns 405", func(t *testing.T) {
		srv, _ := newGitServer(t)
		// handlePack checks method before auth, so a GET always returns 405.
		req := httptest.NewRequest("GET", "/v1/git/testrip/git-receive-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("git-upload-pack GET returns 405", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("GET", "/v1/git/testrip/git-upload-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("double-slash produces empty rig and returns 400", func(t *testing.T) {
		// /v1/git//info/refs — rig segment is the empty string between the two slashes.
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("GET", "/v1/git//info/refs?service=git-upload-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("path traversal in rig name returns 400", func(t *testing.T) {
		srv, _ := newGitServer(t)
		// URL-encoded ".." traversal attempt.
		req := httptest.NewRequest("GET", "/v1/git/..%2Fetc%2Fpasswd/info/refs?service=git-upload-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("rig with invalid characters returns 400", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("GET", "/v1/git/rig@bad!/info/refs?service=git-upload-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

// TestClientCN exercises all branches of the clientCN helper.
func TestClientCN(t *testing.T) {
	t.Run("nil TLS returns empty string", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		assert.Equal(t, "", clientCN(req))
	})

	t.Run("empty PeerCertificates returns empty string", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		req.TLS = &tls.ConnectionState{}
		assert.Equal(t, "", clientCN(req))
	})

	t.Run("cert with CN returns the CN", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", nil)
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{
				{Subject: pkix.Name{CommonName: "gt-gastown-furiosa"}},
			},
		}
		assert.Equal(t, "gt-gastown-furiosa", clientCN(req))
	})
}

// ---- git integration tests (requires git in PATH) ----

func requireGit(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH; skipping integration test")
	}
	return gitPath
}

// makeBareRepo creates a temporary bare git repo at townRoot/<rig>/.repo.git
// and returns the rig name.
func makeBareRepo(t *testing.T, gitPath, townRoot string) string {
	t.Helper()
	rig := "testrip"
	repoPath := filepath.Join(townRoot, rig, ".repo.git")
	require.NoError(t, os.MkdirAll(repoPath, 0700))
	cmd := exec.Command(gitPath, "init", "--bare", repoPath)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(), // avoid polluting real home
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", out)
	return rig
}

func TestHandleInfoRefsIntegration(t *testing.T) {
	gitPath := requireGit(t)
	srv, townRoot := newGitServer(t)
	rig := makeBareRepo(t, gitPath, townRoot)

	t.Run("git-upload-pack info/refs returns 200 with correct Content-Type", func(t *testing.T) {
		req := httptest.NewRequest("GET",
			"/v1/git/"+rig+"/info/refs?service=git-upload-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t,
			"application/x-git-upload-pack-advertisement",
			rec.Header().Get("Content-Type"))
		// Smart HTTP response must start with the pkt-line service header.
		body := rec.Body.String()
		assert.Contains(t, body, "# service=git-upload-pack",
			"response should contain pkt-line service header")
	})

	t.Run("git-receive-pack info/refs returns 200 with correct Content-Type", func(t *testing.T) {
		req := httptest.NewRequest("GET",
			"/v1/git/"+rig+"/info/refs?service=git-receive-pack", nil)
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t,
			"application/x-git-receive-pack-advertisement",
			rec.Header().Get("Content-Type"))
		body := rec.Body.String()
		assert.Contains(t, body, "# service=git-receive-pack")
	})
}

func TestHandleUploadPackIntegration(t *testing.T) {
	gitPath := requireGit(t)
	srv, townRoot := newGitServer(t)
	rig := makeBareRepo(t, gitPath, townRoot)

	t.Run("POST git-upload-pack returns 200", func(t *testing.T) {
		// Send a flush-only body; git-upload-pack will return its capabilities.
		req := httptest.NewRequest("POST",
			"/v1/git/"+rig+"/git-upload-pack",
			bytes.NewReader([]byte("0000")))
		req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		rec := httptest.NewRecorder()
		srv.handleGit(rec, req)

		// Status 200 is set before git runs; even if git exits non-zero, status is 200.
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/x-git-upload-pack-result",
			rec.Header().Get("Content-Type"))
	})
}

// ---- missing repo pre-flight ----

func TestHandleGitMissingRepo(t *testing.T) {
	t.Run("info/refs returns 404 for missing repo", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("GET",
			"/v1/git/missingrig/info/refs?service=git-upload-pack", nil)
		w := httptest.NewRecorder()
		srv.handleGit(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
		// Must NOT contain a pkt-line service header — that would mean headers
		// were committed before the 404 check.
		assert.NotContains(t, w.Body.String(), "# service=")
	})

	t.Run("git-upload-pack returns 404 for missing repo", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("POST",
			"/v1/git/missingrig/git-upload-pack",
			bytes.NewReader([]byte("0000")))
		w := httptest.NewRecorder()
		srv.handleGit(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("git-receive-pack returns 404 for missing repo", func(t *testing.T) {
		srv, _ := newGitServer(t)
		req := httptest.NewRequest("POST",
			"/v1/git/missingrig/git-receive-pack",
			bytes.NewReader(receivePackBody("refs/heads/polecat/furiosa-abc")))
		w := httptest.NewRecorder()
		srv.handleGit(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
