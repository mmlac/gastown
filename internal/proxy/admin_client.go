package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// IssueCertResult holds the response from a successful certificate issuance
// via the proxy admin API.
type IssueCertResult struct {
	CN        string `json:"cn"`
	Cert      string `json:"cert"`
	Key       string `json:"key"`
	CA        string `json:"ca"`
	Serial    string `json:"serial"`
	ExpiresAt string `json:"expires_at"`
}

// AdminClient is an HTTP client for the gt-proxy admin API.
// Methods are nil-safe: calling any method on a nil *AdminClient returns nil/nil,
// allowing graceful degradation when the proxy isn't running.
type AdminClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewAdminClient creates an AdminClient targeting the given admin address.
// The address should be host:port (e.g. "127.0.0.1:9877").
func NewAdminClient(addr string) *AdminClient {
	return &AdminClient{
		baseURL:    "http://" + addr,
		httpClient: http.DefaultClient,
	}
}

// IssueCert requests a new polecat client certificate from the proxy CA.
func (c *AdminClient) IssueCert(ctx context.Context, rig, name, ttl string) (*IssueCertResult, error) {
	if c == nil {
		return nil, nil
	}

	body, err := json.Marshal(issueCertRequest{Rig: rig, Name: name, TTL: ttl})
	if err != nil {
		return nil, fmt.Errorf("marshalling issue-cert request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/admin/issue-cert", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating issue-cert request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("issuing cert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		buf.ReadFrom(http.MaxBytesReader(nil, resp.Body, 1<<10))
		return nil, fmt.Errorf("issue-cert failed (status %d): %s", resp.StatusCode, buf.String())
	}

	var result IssueCertResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding issue-cert response: %w", err)
	}
	return &result, nil
}

// DenyCert revokes a certificate by its serial number (lowercase hex, no 0x prefix).
func (c *AdminClient) DenyCert(ctx context.Context, serial string) error {
	if c == nil {
		return nil
	}

	body, err := json.Marshal(denyCertRequest{Serial: serial})
	if err != nil {
		return fmt.Errorf("marshalling deny-cert request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/admin/deny-cert", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating deny-cert request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("denying cert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		var buf bytes.Buffer
		buf.ReadFrom(http.MaxBytesReader(nil, resp.Body, 1<<10))
		return fmt.Errorf("deny-cert failed (status %d): %s", resp.StatusCode, buf.String())
	}
	return nil
}
