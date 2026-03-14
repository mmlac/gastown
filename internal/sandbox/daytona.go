package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultRemoteCertDir is the directory inside the workspace where mTLS
// certificates are injected for proxy authentication.
// Uses a user-writable path since sandboxes run as non-root (daytona user).
const DefaultRemoteCertDir = "/home/daytona/.gt/certs"

// DefaultProxyAddr is the default proxy server address used when
// RigSettings.RemoteBackend.ProxyAddr is not configured.
const DefaultProxyAddr = "127.0.0.1:9876"

// WorkspaceClient abstracts the Daytona CLI operations needed by DaytonaSandbox.
// The concrete implementation lives in internal/daytona.Client.
type WorkspaceClient interface {
	// WorkspaceName returns the deterministic workspace name for a rig/polecat pair.
	// Format: <installPrefix>-<rig>--<polecat>
	WorkspaceName(rig, polecat string) string

	// Create provisions a new workspace. Idempotent if workspace already exists.
	Create(ctx context.Context, name string, opts WorkspaceCreateOptions) error

	// Start starts a stopped workspace. No-op if already running.
	Start(ctx context.Context, name string) error

	// Stop stops a running workspace.
	Stop(ctx context.Context, name string) error

	// Delete removes a workspace permanently.
	Delete(ctx context.Context, name string) error

	// Exists returns true if the workspace exists (any state).
	Exists(ctx context.Context, name string) (bool, error)

	// InjectCerts writes mTLS certificate files into the workspace filesystem
	// via daytona exec.
	InjectCerts(ctx context.Context, wsName, certDir string, cert, key, ca []byte) error
}

// WorkspaceCreateOptions configures workspace creation.
type WorkspaceCreateOptions struct {
	Image            string
	Snapshot         string
	Dockerfile       string
	Profile          string
	EnvVars          map[string]string
	AutoStopInterval time.Duration
}

// CertIssuer abstracts the proxy admin API for certificate lifecycle.
// The concrete implementation lives in internal/proxy.AdminClient.
type CertIssuer interface {
	// IssueCert requests a new polecat client certificate from the proxy CA.
	// Returns the issued certificate result containing PEM-encoded cert, key,
	// CA, and the certificate serial number.
	IssueCert(ctx context.Context, rig, name, ttl string) (*CertResult, error)

	// DenyCert revokes a certificate by its serial number (lowercase hex, no 0x prefix).
	DenyCert(ctx context.Context, serial string) error
}

// CertResult holds the response from a successful certificate issuance.
type CertResult struct {
	CN        string `json:"cn"`
	Cert      string `json:"cert"`
	Key       string `json:"key"`
	CA        string `json:"ca"`
	Serial    string `json:"serial"`
	ExpiresAt string `json:"expires_at"`
}

// ReconcileFunc is the function signature for the daytona reconciliation
// entrypoint. DaytonaSandbox.Reconcile delegates to this function.
// The concrete implementation lives in internal/daytona.Reconcile.
type ReconcileFunc func(ctx context.Context, opts ReconcileOpts) error

// DaytonaSandbox implements Lifecycle for Daytona remote execution.
// It wraps a WorkspaceClient for workspace CRUD and a CertIssuer for
// mTLS certificate lifecycle management.
type DaytonaSandbox struct {
	client      WorkspaceClient
	certIssuer  CertIssuer
	reconcileMu sync.RWMutex
	reconcileFn ReconcileFunc
}

// NewDaytonaSandbox creates a DaytonaSandbox with the given workspace client
// and certificate issuer.
func NewDaytonaSandbox(client WorkspaceClient, certIssuer CertIssuer, reconcileFn ReconcileFunc) *DaytonaSandbox {
	return &DaytonaSandbox{
		client:      client,
		certIssuer:  certIssuer,
		reconcileFn: reconcileFn,
	}
}

// WorkspaceName returns the deterministic workspace name for a rig/polecat pair.
func (d *DaytonaSandbox) WorkspaceName(rig, polecat string) string {
	return d.client.WorkspaceName(rig, polecat)
}

// PreStart is called before the tmux session is created.
// It ensures the workspace exists and is running, issues an mTLS certificate,
// injects certs into the workspace, and returns inner env vars for the agent
// process inside the container.
func (d *DaytonaSandbox) PreStart(ctx context.Context, opts SandboxOpts) (map[string]string, error) {
	wsName := opts.WorkspaceName

	// 1. Ensure workspace exists and is running.
	//    Reuses existing workspace if available (idempotent create).
	exists, err := d.client.Exists(ctx, wsName)
	if err != nil {
		return nil, fmt.Errorf("checking workspace %s: %w", wsName, err)
	}
	if !exists {
		rb := opts.RigSettings.RemoteBackend
		if rb == nil {
			return nil, fmt.Errorf("remote_backend not configured in rig settings")
		}
		createOpts := WorkspaceCreateOptions{
			Image:      rb.Image,
			Snapshot:   rb.Snapshot,
			Dockerfile: rb.Dockerfile,
			Profile:    rb.Profile,
			EnvVars: map[string]string{
				"GT_RIG":     opts.Rig,
				"GT_POLECAT": opts.Polecat,
				"GT_ROLE":    fmt.Sprintf("%s/polecats/%s", opts.Rig, opts.Polecat),
			},
			AutoStopInterval: rb.AutoStopInterval,
		}
		if err := d.client.Create(ctx, wsName, createOpts); err != nil {
			return nil, fmt.Errorf("creating workspace %s: %w", wsName, err)
		}
	}

	// Start the workspace (no-op if already running).
	if err := d.client.Start(ctx, wsName); err != nil {
		return nil, fmt.Errorf("starting workspace %s: %w", wsName, err)
	}

	// 2. Issue mTLS cert for this polecat's proxy access.
	certResult, err := d.certIssuer.IssueCert(ctx, opts.Rig, opts.Polecat, "720h")
	if err != nil {
		return nil, fmt.Errorf("issuing proxy cert: %w", err)
	}

	// 3. Inject cert into workspace via daytona exec.
	certDir := DefaultRemoteCertDir
	caCert := []byte(certResult.CA)
	if opts.ProxyCA != nil {
		caCert = opts.ProxyCA.CertPEM
	}
	if err := d.client.InjectCerts(ctx, wsName, certDir, []byte(certResult.Cert), []byte(certResult.Key), caCert); err != nil {
		return nil, fmt.Errorf("injecting certs into workspace: %w", err)
	}

	// 4. Return inner env vars for the agent process inside the container.
	proxyAddr := DefaultProxyAddr
	if rb := opts.RigSettings.RemoteBackend; rb != nil && rb.ProxyAddr != "" {
		proxyAddr = rb.ProxyAddr
	}

	innerEnv := map[string]string{
		"GT_RIG":              opts.Rig,
		"GT_POLECAT":          opts.Polecat,
		"GT_ROLE":             fmt.Sprintf("%s/polecats/%s", opts.Rig, opts.Polecat),
		"GT_PROXY_URL":        "https://" + proxyAddr,
		"GT_PROXY_CERT":       certDir + "/client.crt",
		"GT_PROXY_KEY":        certDir + "/client.key",
		"GT_PROXY_CA":         certDir + "/ca.crt",
		"GIT_SSL_CERT":        certDir + "/client.crt",
		"GIT_SSL_KEY":         certDir + "/client.key",
		"GIT_SSL_CAINFO":      certDir + "/ca.crt",
		"GIT_AUTHOR_NAME":     opts.Polecat,
		"GIT_AUTHOR_EMAIL":    opts.Polecat + "@gastown.local",
		"GIT_COMMITTER_NAME":  opts.Polecat,
		"GIT_COMMITTER_EMAIL": opts.Polecat + "@gastown.local",
		"BD_DOLT_AUTO_COMMIT": "off",
		"GT_CERT_SERIAL":     certResult.Serial,
	}
	if opts.Branch != "" {
		innerEnv["GT_REPO_BRANCH"] = opts.Branch
	}

	return innerEnv, nil
}

// certRevocationRetries is the number of DenyCert attempts before giving up.
const certRevocationRetries = 3

// certRevocationBaseDelay is the initial backoff between DenyCert retries.
const certRevocationBaseDelay = 1 * time.Second

// PostStop is called after the tmux session is killed.
// It revokes the polecat's mTLS certificate (critical ordering: revoke before
// any bead state changes to prevent a rogue process from using the cert),
// then optionally stops and/or deletes the workspace.
//
// Cert revocation failure is returned as an error after workspace cleanup
// completes, so callers can detect incomplete revocation.
// Workspace stop/delete errors remain non-fatal (logged as warnings).
func (d *DaytonaSandbox) PostStop(ctx context.Context, opts SandboxOpts) error {
	// 1. Revoke cert BEFORE any bead state changes.
	//    This ordering is critical: revoking first prevents the (now-dead)
	//    polecat's cert from being used by a rogue process.
	var certErr error
	if opts.CertSerial != "" {
		for attempt := 1; attempt <= certRevocationRetries; attempt++ {
			if err := d.certIssuer.DenyCert(ctx, opts.CertSerial); err != nil {
				certErr = err
				if attempt < certRevocationRetries {
					delay := certRevocationBaseDelay * time.Duration(1<<(attempt-1))
					slog.Warn("cert revocation failed, retrying",
						"serial", opts.CertSerial, "attempt", attempt, "err", err)
					select {
					case <-ctx.Done():
						certErr = fmt.Errorf("cert revocation interrupted: %w", ctx.Err())
					case <-time.After(delay):
						continue
					}
				}
				break
			}
			certErr = nil
			break
		}
		if certErr != nil {
			slog.Error("cert revocation failed after retries",
				"serial", opts.CertSerial, "attempts", certRevocationRetries, "err", certErr)
		}
	} else {
		slog.Warn("cert serial not available for revocation, cert will not be revoked",
			"rig", opts.Rig, "polecat", opts.Polecat)
	}

	// RemoteBackend may be nil when PostStop runs in local-only mode or after
	// a config change removed the remote backend. Unlike PreStart (which errors
	// on nil RemoteBackend because workspace creation requires it), PostStop
	// treats nil as a no-op: there is no remote workspace to stop or delete.
	rb := opts.RigSettings.RemoteBackend
	if rb == nil {
		slog.Debug("PostStop: RemoteBackend is nil, skipping workspace cleanup",
			"rig", opts.Rig, "polecat", opts.Polecat)
		return certErr
	}

	// 2. Optionally stop the workspace.
	if rb.AutoStop {
		wsName := opts.WorkspaceName
		if err := d.client.Stop(ctx, wsName); err != nil {
			slog.Warn("workspace stop failed", "workspace", wsName, "err", err)
		}
	}

	// 3. Optionally delete the workspace.
	if rb.AutoDelete {
		wsName := opts.WorkspaceName
		if err := d.client.Delete(ctx, wsName); err != nil {
			slog.Warn("workspace delete failed", "workspace", wsName, "err", err)
		}
	}

	return certErr
}

// SetReconcileFunc sets the reconciliation function. It is safe to call
// concurrently with Reconcile.
func (d *DaytonaSandbox) SetReconcileFunc(fn ReconcileFunc) {
	d.reconcileMu.Lock()
	d.reconcileFn = fn
	d.reconcileMu.Unlock()
}

// Reconcile is called periodically by patrol to discover orphaned workspaces
// and beads, and clean them up. Each orphan gets an independent deadline to
// prevent one slow operation from starving others.
func (d *DaytonaSandbox) Reconcile(ctx context.Context, opts ReconcileOpts) error {
	d.reconcileMu.RLock()
	fn := d.reconcileFn
	d.reconcileMu.RUnlock()
	if fn != nil {
		return fn(ctx, opts)
	}
	return nil
}

// Compile-time interface assertion.
var _ Lifecycle = (*DaytonaSandbox)(nil)
