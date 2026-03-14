package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/daytona"
)

// DaytonaClientAdapter wraps daytona.Client to implement sandbox.WorkspaceClient.
// daytona.Client has slightly different method signatures (extra repo/branch params
// on Create, no Exists or InjectCerts methods), so this adapter bridges the gap.
type DaytonaClientAdapter struct {
	client *daytona.Client
}

// Compile-time interface assertion.
var _ WorkspaceClient = (*DaytonaClientAdapter)(nil)

// NewDaytonaClientAdapter creates a DaytonaClientAdapter wrapping the given client.
func NewDaytonaClientAdapter(client *daytona.Client) *DaytonaClientAdapter {
	return &DaytonaClientAdapter{client: client}
}

// WorkspaceName returns the deterministic workspace name for a rig/polecat pair.
func (a *DaytonaClientAdapter) WorkspaceName(rig, polecat string) string {
	return a.client.WorkspaceName(rig, polecat)
}

// Create provisions a new workspace. Idempotent if workspace already exists.
func (a *DaytonaClientAdapter) Create(ctx context.Context, name string, opts WorkspaceCreateOptions) error {
	createOpts := daytona.CreateOptions{
		Snapshot:   opts.Snapshot,
		Dockerfile: opts.Dockerfile,
		Env:        opts.EnvVars,
	}
	if opts.Profile != "" {
		createOpts.Class = opts.Profile
	}
	if opts.AutoStopInterval > 0 {
		createOpts.AutoStopInterval = int(opts.AutoStopInterval / time.Minute)
		if createOpts.AutoStopInterval == 0 {
			createOpts.AutoStopInterval = 1
		}
	}
	return a.client.Create(ctx, name, "", "", createOpts)
}

// Start starts a stopped workspace. No-op if already running.
func (a *DaytonaClientAdapter) Start(ctx context.Context, name string) error {
	return a.client.Start(ctx, name)
}

// Stop stops a running workspace.
func (a *DaytonaClientAdapter) Stop(ctx context.Context, name string) error {
	return a.client.Stop(ctx, name)
}

// Delete removes a workspace permanently.
func (a *DaytonaClientAdapter) Delete(ctx context.Context, name string) error {
	return a.client.Delete(ctx, name)
}

// Exists returns true if the workspace exists (any state).
func (a *DaytonaClientAdapter) Exists(ctx context.Context, name string) (bool, error) {
	workspaces, err := a.client.ListOwned(ctx)
	if err != nil {
		return false, fmt.Errorf("listing workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if ws.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// InjectCerts writes mTLS certificate files into the workspace filesystem
// via daytona exec.
func (a *DaytonaClientAdapter) InjectCerts(ctx context.Context, wsName, certDir string, cert, key, ca []byte) error {
	// Create cert directory
	_, _, exitCode, err := a.client.Exec(ctx, wsName, nil, "mkdir", "-p", certDir)
	if err != nil {
		return fmt.Errorf("creating cert dir: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("creating cert dir: exit code %d", exitCode)
	}

	// Write each file via shell heredoc through daytona exec
	files := map[string][]byte{
		certDir + "/client.crt": cert,
		certDir + "/client.key": key,
		certDir + "/ca.crt":     ca,
	}
	for path, content := range files {
		if err := a.InjectFile(ctx, wsName, path, content); err != nil {
			return err
		}
	}
	return nil
}

// InjectFile writes a single file into the workspace filesystem via daytona exec.
// Creates parent directories as needed.
func (a *DaytonaClientAdapter) InjectFile(ctx context.Context, wsName, path string, content []byte) error {
	// Extract directory from path
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		_, _, exitCode, err := a.client.Exec(ctx, wsName, nil, "mkdir", "-p", dir)
		if err != nil {
			return fmt.Errorf("creating dir for %s: %w", path, err)
		}
		if exitCode != 0 {
			return fmt.Errorf("creating dir for %s: exit code %d", path, exitCode)
		}
	}

	_, stderr, exitCode, err := a.client.Exec(ctx, wsName, nil,
		"bash", "-c", fmt.Sprintf("cat > %s << 'CERTEOF'\n%s\nCERTEOF", path, string(content)))
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("writing %s: exit code %d: %s", path, exitCode, stderr)
	}
	return nil
}
