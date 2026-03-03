package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminClient_NilSafe(t *testing.T) {
	var c *AdminClient

	t.Run("IssueCert returns nil on nil client", func(t *testing.T) {
		result, err := c.IssueCert(context.Background(), "rig", "name", "720h")
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("DenyCert returns nil on nil client", func(t *testing.T) {
		err := c.DenyCert(context.Background(), "abc123")
		assert.NoError(t, err)
	})

	t.Run("Ping returns nil on nil client", func(t *testing.T) {
		err := c.Ping(context.Background())
		assert.NoError(t, err)
	})
}

func TestAdminClient_IssueCert(t *testing.T) {
	t.Run("successful issue", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/v1/admin/issue-cert", r.URL.Path)

			var req issueCertRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "myrig", req.Rig)
			assert.Equal(t, "ruby", req.Name)
			assert.Equal(t, "720h", req.TTL)

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(IssueCertResult{
				CN:        "gt-myrig-ruby",
				Cert:      "---CERT---",
				Key:       "---KEY---",
				CA:        "---CA---",
				Serial:    "deadbeef",
				ExpiresAt: "2026-04-03T00:00:00Z",
			})
		}))
		defer srv.Close()

		// Strip "http://" prefix since NewAdminClient adds it
		addr := srv.Listener.Addr().String()
		c := NewAdminClient(addr)

		result, err := c.IssueCert(context.Background(), "myrig", "ruby", "720h")
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "gt-myrig-ruby", result.CN)
		assert.Equal(t, "deadbeef", result.Serial)
		assert.Equal(t, "---CERT---", result.Cert)
		assert.Equal(t, "---KEY---", result.Key)
		assert.Equal(t, "---CA---", result.CA)
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad request: rig is required", http.StatusBadRequest)
		}))
		defer srv.Close()

		c := NewAdminClient(srv.Listener.Addr().String())
		result, err := c.IssueCert(context.Background(), "", "ruby", "720h")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "status 400")
	})

	t.Run("unreachable server", func(t *testing.T) {
		c := NewAdminClient("127.0.0.1:1") // port 1 should be unreachable
		result, err := c.IssueCert(context.Background(), "rig", "name", "720h")
		assert.Error(t, err)
		assert.Nil(t, result)
	})
}

func TestAdminClient_DenyCert(t *testing.T) {
	t.Run("successful deny", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/v1/admin/deny-cert", r.URL.Path)

			var req denyCertRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "deadbeef", req.Serial)

			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		c := NewAdminClient(srv.Listener.Addr().String())
		err := c.DenyCert(context.Background(), "deadbeef")
		assert.NoError(t, err)
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad request: serial is required", http.StatusBadRequest)
		}))
		defer srv.Close()

		c := NewAdminClient(srv.Listener.Addr().String())
		err := c.DenyCert(context.Background(), "")
		assert.Error(t, err)
	})

	t.Run("unreachable server", func(t *testing.T) {
		c := NewAdminClient("127.0.0.1:1")
		err := c.DenyCert(context.Background(), "deadbeef")
		assert.Error(t, err)
	})
}

func TestAdminClient_Ping(t *testing.T) {
	t.Run("server reachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Admin endpoint returns 405 for GET, which proves it's alive
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}))
		defer srv.Close()

		c := NewAdminClient(srv.Listener.Addr().String())
		err := c.Ping(context.Background())
		assert.NoError(t, err)
	})
}
