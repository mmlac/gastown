// Package dispatch provides the DispatchTarget interface and related types
// for dispatching work to different agent targets (rigs, dogs, existing agents).
package dispatch

import "context"

// DispatchTarget represents a destination for slung work.
// Implementations handle the specifics of preparing, starting, and rolling
// back sessions for different target types (rigs, dogs, existing agents).
type DispatchTarget interface {
	// AgentID returns the fully-qualified agent identifier.
	// Examples: "gastown/polecats/Toast", "deacon/dogs/alpha"
	AgentID() string

	// TargetType returns the kind of dispatch target: "rig", "dog", or "agent".
	TargetType() string

	// WorkDir returns the directory where bead files live for this target.
	WorkDir() string

	// Prepare sets up the target before session start.
	// For rigs, this spawns the polecat directory. For dogs, this resolves
	// the plugin worker. For existing agents, this verifies the agent exists.
	Prepare(ctx context.Context) error

	// StartSession launches the agent session with the given options.
	// Returns the tmux pane ID for the started session.
	StartSession(ctx context.Context, opts StartOpts) (paneID string, err error)

	// Rollback cleans up on failure after Prepare has been called.
	// For rigs, this nukes the polecat directory. Other targets may be no-ops.
	Rollback(ctx context.Context) error

	// IsSessionRunning checks whether the target's session is currently active.
	IsSessionRunning(ctx context.Context) (bool, error)
}

// StartOpts configures how a session is started on a dispatch target.
type StartOpts struct {
	// FormulaEnv contains environment variables derived from the formula
	// that should be set in the agent's session environment.
	FormulaEnv map[string]string

	// AgentCommand is the agent binary or command to execute.
	AgentCommand string

	// AgentArgs are additional arguments passed to the agent command.
	AgentArgs []string
}
