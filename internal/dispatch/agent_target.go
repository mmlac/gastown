package dispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// ExistingAgentTarget dispatches work to an already-running agent session.
// It wraps the existing resolveTargetAgent flow behind the DispatchTarget
// interface. Since the session already exists, StartSession is a no-op and
// Rollback has nothing to undo.
type ExistingAgentTarget struct {
	// target is the original target spec (e.g. "GasTown/polecats/quartz",
	// "mayor", "deacon/boot").
	target string

	// resolved fields, populated by Prepare.
	agentID string
	pane    string
	workDir string
}

// NewExistingAgentTarget creates an ExistingAgentTarget for the given target spec.
func NewExistingAgentTarget(target string) *ExistingAgentTarget {
	return &ExistingAgentTarget{target: target}
}

func (e *ExistingAgentTarget) AgentID() string {
	if e.agentID != "" {
		return e.agentID
	}
	return e.target
}

func (e *ExistingAgentTarget) TargetType() string { return "agent" }

func (e *ExistingAgentTarget) WorkDir() string { return e.workDir }

// Prepare validates the agent exists and resolves its session pane and work dir.
func (e *ExistingAgentTarget) Prepare(ctx context.Context) error {
	// Resolve the target to a tmux session.
	sessionName, err := resolveRoleToSessionFn(e.target)
	if err != nil {
		return fmt.Errorf("resolving agent session for %q: %w", e.target, err)
	}

	// Convert session name to canonical agent ID.
	e.agentID = sessionToAgentIDLocal(sessionName)

	// Get the pane ID.
	t := tmux.NewTmux()
	e.pane, err = t.GetPaneID(sessionName)
	if err != nil {
		return fmt.Errorf("getting pane for %s: %w", sessionName, err)
	}

	// Get the working directory.
	e.workDir, err = t.GetPaneWorkDir(sessionName)
	if err != nil {
		return fmt.Errorf("getting working dir for %s: %w", sessionName, err)
	}

	return nil
}

// StartSession is a no-op for existing agents — the session is already running.
// Returns the pane ID resolved during Prepare.
func (e *ExistingAgentTarget) StartSession(ctx context.Context, opts StartOpts) (string, error) {
	if e.pane == "" {
		return "", fmt.Errorf("Prepare must be called before StartSession")
	}
	return e.pane, nil
}

// Rollback is a no-op for existing agents — there is nothing to undo.
func (e *ExistingAgentTarget) Rollback(ctx context.Context) error {
	return nil
}

// IsSessionRunning checks whether the agent's tmux session is active.
func (e *ExistingAgentTarget) IsSessionRunning(ctx context.Context) (bool, error) {
	if e.agentID == "" {
		return false, nil
	}
	// Resolve back to a session name for the check.
	sessionName, err := resolveRoleToSessionFn(e.target)
	if err != nil {
		return false, nil // can't resolve → not running
	}
	t := tmux.NewTmux()
	return t.HasSession(sessionName)
}

// resolveRoleToSessionFn is a seam for tests.
// Production value is set in init() to avoid circular imports.
var resolveRoleToSessionFn = defaultResolveRoleToSession

// defaultResolveRoleToSession is a placeholder that returns an error.
// The real implementation is injected from cmd package via SetResolveRoleToSession.
func defaultResolveRoleToSession(role string) (string, error) {
	return "", fmt.Errorf("resolveRoleToSession not initialized (target: %s)", role)
}

// SetResolveRoleToSession allows the cmd package to inject the real
// resolveRoleToSession function, avoiding circular imports.
func SetResolveRoleToSession(fn func(string) (string, error)) {
	resolveRoleToSessionFn = fn
}

// sessionToAgentIDLocal converts a session name to agent ID format.
// Uses session.ParseSessionName for consistent parsing.
func sessionToAgentIDLocal(sessionName string) string {
	identity, err := session.ParseSessionName(sessionName)
	if err != nil {
		// Fallback: strip common prefixes and use as-is.
		return strings.TrimPrefix(sessionName, "hq-")
	}
	return identity.Address()
}
