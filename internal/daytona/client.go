// Package daytona wraps the daytona CLI for workspace lifecycle management.
// All daytona interactions go through Client to centralize argument handling,
// workspace naming conventions, and multi-tenancy filtering.
package daytona

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// CommandRunner abstracts command execution for testing.
type CommandRunner interface {
	// Run executes a command and returns its stdout, stderr, exit code, and error.
	// On success or a normal non-zero exit, err is nil and exitCode holds the
	// process exit status. A non-nil err indicates an OS-level failure (e.g.
	// binary not found, signal); in that case exitCode is -1.
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// execRunner is the default CommandRunner that shells out to a real process.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: callers validate args
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return outBuf.String(), errBuf.String(), -1, err
		}
	}
	return outBuf.String(), errBuf.String(), exitCode, nil
}

// Workspace represents a daytona workspace visible to this installation.
type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Rig     string `json:"-"` // parsed from name
	Polecat string `json:"-"` // parsed from name
}

// CreateOptions configures workspace creation.
type CreateOptions struct {
	Image            string            // container image override
	Dockerfile       string            // path to Dockerfile for sandbox snapshot (maps to --dockerfile)
	Snapshot         string            // pre-built snapshot ID (--snapshot flag, mutually exclusive with Image)
	Env              map[string]string // extra environment variables
	Labels           map[string]string // workspace labels (--label KEY=VALUE)
	Volumes          []string          // named volumes in "name:/mount/path" format (maps to --volume)
	NetworkBlockAll  bool              // block all outbound network (--network-block-all)
	NetworkAllowList string            // comma-separated CIDRs to allow (--network-allow-list)
}

// Client wraps the daytona CLI for workspace lifecycle and discovery.
type Client struct {
	installPrefix string // "gt-<installID-short>" — scopes workspaces to this installation
	runner        CommandRunner
	retry         RetryConfig
	listPageSize  int // page size for ListOwned pagination; 0 means use default
}

// NewClient creates a Client that scopes workspaces with the given prefix.
// The installPrefix is typically "gt-<first-12-chars-of-installationID>".
func NewClient(installPrefix string) *Client {
	return &Client{
		installPrefix: installPrefix,
		runner:        &execRunner{},
		retry:         DefaultRetryConfig(),
	}
}

// NewClientWithRunner creates a Client with a custom CommandRunner (for testing).
// Retry is disabled by default; use SetRetry to enable.
func NewClientWithRunner(installPrefix string, runner CommandRunner) *Client {
	return &Client{
		installPrefix: installPrefix,
		runner:        runner,
		retry:         NoRetryConfig(),
	}
}

// SetRetry configures the retry policy for transient CLI failures.
func (c *Client) SetRetry(cfg RetryConfig) {
	c.retry = cfg
}

// WorkspaceName returns the deterministic workspace name for a rig+polecat pair.
// Format: <installPrefix>-<rig>--<polecat>
// The double-hyphen delimiter allows both rig and polecat names to contain
// single hyphens (e.g., rig "my-rig", polecat "bullet-farmer").
func (c *Client) WorkspaceName(rig, polecat string) string {
	return c.installPrefix + "-" + rig + "--" + polecat
}

// ParseWorkspaceName extracts rig and polecat from a workspace name.
// Returns ok=false if the name doesn't match this installation's prefix
// or doesn't contain the "--" rig/polecat delimiter.
func (c *Client) ParseWorkspaceName(name string) (rig, polecat string, ok bool) {
	prefix := c.installPrefix + "-"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	// rest should be "<rig>--<polecat>" — split on LAST double-hyphen delimiter
	// to handle rig names that themselves contain "--".
	idx := strings.LastIndex(rest, "--")
	if idx <= 0 || idx >= len(rest)-2 {
		return "", "", false
	}
	return rest[:idx], rest[idx+2:], true
}

// Create provisions a new daytona workspace from a repo/branch.
// Passes --yes to suppress interactive confirmation prompts.
func (c *Client) Create(ctx context.Context, name, repoURL, branch string, opts CreateOptions) error {
	args := []string{"create", repoURL, "--name", name, "--branch", branch, "--yes"}
	if opts.Snapshot != "" {
		args = append(args, "--snapshot", opts.Snapshot)
	} else if opts.Image != "" {
		args = append(args, "--image", opts.Image)
	}
	if opts.Dockerfile != "" {
		args = append(args, "--dockerfile", opts.Dockerfile)
	}
	for _, vol := range opts.Volumes {
		args = append(args, "--volume", vol)
	}
	for k, v := range opts.Env {
		args = append(args, "--env", k+"="+v)
	}
	for k, v := range opts.Labels {
		args = append(args, "--label", k+"="+v)
	}
	if opts.NetworkBlockAll {
		args = append(args, "--network-block-all")
	}
	if opts.NetworkAllowList != "" {
		args = append(args, "--network-allow-list", opts.NetworkAllowList)
	}
	_, stderr, exitCode, err := c.runWithRetry(ctx, true, func() (string, string, int, error) {
		return c.runner.Run(ctx, "daytona", args...)
	})
	if err != nil {
		return fmt.Errorf("daytona create: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("daytona create failed (exit %d): %s", exitCode, firstLine(stderr))
	}
	return nil
}

// Start ensures a workspace is running.
// Passes --yes to suppress interactive confirmation prompts.
func (c *Client) Start(ctx context.Context, name string) error {
	_, stderr, exitCode, err := c.runWithRetry(ctx, true, func() (string, string, int, error) {
		return c.runner.Run(ctx, "daytona", "start", name, "--yes")
	})
	if err != nil {
		return fmt.Errorf("daytona start: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("daytona start failed (exit %d): %s", exitCode, firstLine(stderr))
	}
	return nil
}

// Stop pauses a workspace (preserves state for re-start).
// Passes --yes to suppress interactive confirmation prompts.
func (c *Client) Stop(ctx context.Context, name string) error {
	_, stderr, exitCode, err := c.runWithRetry(ctx, true, func() (string, string, int, error) {
		return c.runner.Run(ctx, "daytona", "stop", name, "--yes")
	})
	if err != nil {
		return fmt.Errorf("daytona stop: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("daytona stop failed (exit %d): %s", exitCode, firstLine(stderr))
	}
	return nil
}

// Delete permanently removes a workspace.
// Passes --yes to suppress interactive confirmation prompts.
func (c *Client) Delete(ctx context.Context, name string) error {
	_, stderr, exitCode, err := c.runWithRetry(ctx, true, func() (string, string, int, error) {
		return c.runner.Run(ctx, "daytona", "delete", name, "--yes")
	})
	if err != nil {
		return fmt.Errorf("daytona delete: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("daytona delete failed (exit %d): %s", exitCode, firstLine(stderr))
	}
	return nil
}

// Exec runs a command inside a workspace and returns stdout, stderr, and exit code.
// Retries on OS-level errors (e.g., daytona binary I/O failure) but not on non-zero
// exit codes, which belong to the command running inside the workspace.
//
// Environment variables are injected via an inline `env K=V` prefix rather than
// --env flags, because daytona exec does not support --env.
func (c *Client) Exec(ctx context.Context, name string, env map[string]string, cmd ...string) (string, string, int, error) {
	args := []string{"exec", name, "--"}
	if len(env) > 0 {
		// daytona exec does not support --env; use the env command to set
		// variables inline inside the container.
		args = append(args, "env")
		// Sort keys for deterministic command output.
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, k+"="+env[k])
		}
	}
	args = append(args, cmd...)
	stdout, stderr, exitCode, err := c.runWithRetry(ctx, false, func() (string, string, int, error) {
		return c.runner.Run(ctx, "daytona", args...)
	})
	if err != nil {
		return "", "", -1, fmt.Errorf("daytona exec: %w", err)
	}
	return stdout, stderr, exitCode, nil
}

// daytonaListEntry matches the JSON output of `daytona list -o json`.
type daytonaListEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// defaultListPageSize is the number of workspaces fetched per page in ListOwned.
const defaultListPageSize = 100

// ListOwned returns all workspaces belonging to this installation (filtered by installPrefix).
// It paginates through all pages of `daytona list` to ensure no workspaces are missed.
func (c *Client) ListOwned(ctx context.Context) ([]Workspace, error) {
	prefix := c.installPrefix + "-"
	var owned []Workspace

	pageSize := c.listPageSize
	if pageSize <= 0 {
		pageSize = defaultListPageSize
	}

	for page := 1; ; page++ {
		pageStr := fmt.Sprintf("%d", page)
		limitStr := fmt.Sprintf("%d", pageSize)
		stdout, stderr, exitCode, err := c.runWithRetry(ctx, true, func() (string, string, int, error) {
			return c.runner.Run(ctx, "daytona", "list", "-o", "json", "--page", pageStr, "--limit", limitStr)
		})
		if err != nil {
			return nil, fmt.Errorf("daytona list (page %d): %w", page, err)
		}
		if exitCode != 0 {
			return nil, fmt.Errorf("daytona list failed (page %d, exit %d): %s", page, exitCode, firstLine(stderr))
		}

		var entries []daytonaListEntry
		if strings.TrimSpace(stdout) == "" {
			break
		}
		if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
			return nil, fmt.Errorf("daytona list: parse JSON (page %d): %w", page, err)
		}
		if len(entries) == 0 {
			break
		}

		for _, e := range entries {
			if !strings.HasPrefix(e.Name, prefix) {
				continue
			}
			ws := Workspace{
				ID:    e.ID,
				Name:  e.Name,
				State: e.State,
			}
			if rig, polecat, ok := c.ParseWorkspaceName(e.Name); ok {
				ws.Rig = rig
				ws.Polecat = polecat
			}
			owned = append(owned, ws)
		}

		if len(entries) < pageSize {
			break
		}
	}
	return owned, nil
}

// InstallPrefix returns the prefix used for workspace name scoping.
func (c *Client) InstallPrefix() string {
	return c.installPrefix
}

// CertVolumeName returns the deterministic volume name for shared cert storage.
// Format: gt-certs-<installPrefix>
// All workspaces in the same installation share this volume, so certs persist
// across workspace restarts and new workspace creation.
func (c *Client) CertVolumeName() string {
	return "gt-certs-" + c.installPrefix
}

// firstLine returns the first non-empty line from s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}
