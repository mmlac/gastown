package dispatch

import (
	"context"
	"fmt"
)

// SpawnResult holds the result of spawning a polecat for rig dispatch.
type SpawnResult struct {
	RigName     string // Rig name (e.g., "gastown")
	PolecatName string // Polecat name (e.g., "Toast")
	ClonePath   string // Path to polecat's git worktree
	SessionName string // Tmux session name (e.g., "gt-gastown-p-Toast")
	BaseBranch  string // Effective base branch
	Branch      string // Git branch name
}

// SpawnFunc creates a polecat directory and returns spawn metadata.
// Implementations wrap SpawnPolecatForSling or equivalent logic.
type SpawnFunc func(ctx context.Context) (*SpawnResult, error)

// SessionStartFunc starts a tmux session for a spawned polecat.
// Returns the tmux pane ID. Implementations wrap SessionManager.Start
// and pane ID resolution.
type SessionStartFunc func(ctx context.Context, spawn *SpawnResult, opts StartOpts) (paneID string, err error)

// RollbackFunc cleans up a polecat directory on failure.
// Implementations wrap Manager.RemoveWithOptions or nukePolecatDir.
type RollbackFunc func(ctx context.Context, spawn *SpawnResult) error

// SessionCheckFunc checks whether a polecat's session is running.
// Implementations wrap SessionManager.IsRunning.
type SessionCheckFunc func(ctx context.Context, spawn *SpawnResult) (bool, error)

// RigTarget dispatches work to a polecat in a rig.
// It implements DispatchTarget by delegating to injected functions
// that wrap the existing spawn/session/cleanup flows.
type RigTarget struct {
	rigName      string
	spawnFn      SpawnFunc
	startFn      SessionStartFunc
	rollbackFn   RollbackFunc
	sessionCheck SessionCheckFunc

	// populated by Prepare()
	spawn *SpawnResult
	pane  string
}

// NewRigTarget creates a RigTarget with the given dependency functions.
//
// The caller is responsible for constructing the function closures that capture
// the necessary state (tmux, rig, manager, account config, etc.). This avoids
// the dispatch package importing internal/cmd or other heavy packages.
func NewRigTarget(rigName string, spawn SpawnFunc, start SessionStartFunc, rollback RollbackFunc, check SessionCheckFunc) *RigTarget {
	return &RigTarget{
		rigName:      rigName,
		spawnFn:      spawn,
		startFn:      start,
		rollbackFn:   rollback,
		sessionCheck: check,
	}
}

// AgentID returns the fully-qualified agent identifier (e.g., "gastown/polecats/Toast").
// Returns empty string before Prepare() is called.
func (r *RigTarget) AgentID() string {
	if r.spawn == nil {
		return ""
	}
	return fmt.Sprintf("%s/polecats/%s", r.spawn.RigName, r.spawn.PolecatName)
}

// TargetType returns "rig".
func (r *RigTarget) TargetType() string {
	return "rig"
}

// WorkDir returns the polecat's git worktree directory.
// Returns empty string before Prepare() is called.
func (r *RigTarget) WorkDir() string {
	if r.spawn == nil {
		return ""
	}
	return r.spawn.ClonePath
}

// Prepare spawns the polecat directory via the injected SpawnFunc.
// This creates the worktree and polecat state but does not start the tmux session.
func (r *RigTarget) Prepare(ctx context.Context) error {
	if r.spawn != nil {
		return fmt.Errorf("Prepare() already called")
	}
	spawn, err := r.spawnFn(ctx)
	if err != nil {
		return err
	}
	r.spawn = spawn
	return nil
}

// StartSession launches the polecat's tmux session via the injected SessionStartFunc.
// Prepare() must be called first. Returns the tmux pane ID.
func (r *RigTarget) StartSession(ctx context.Context, opts StartOpts) (string, error) {
	if r.spawn == nil {
		return "", fmt.Errorf("Prepare() must be called before StartSession()")
	}
	pane, err := r.startFn(ctx, r.spawn, opts)
	if err != nil {
		return "", err
	}
	r.pane = pane
	return pane, nil
}

// Rollback cleans up the polecat directory on failure via the injected RollbackFunc.
// Safe to call if Prepare() was not called (no-op).
func (r *RigTarget) Rollback(ctx context.Context) error {
	if r.spawn == nil {
		return nil
	}
	return r.rollbackFn(ctx, r.spawn)
}

// BaseBranch returns the effective base branch from the spawn result.
// Returns empty string before Prepare() is called.
func (r *RigTarget) BaseBranch() string {
	if r.spawn == nil {
		return ""
	}
	return r.spawn.BaseBranch
}

// PolecatName returns the polecat name from the spawn result.
// Returns empty string before Prepare() is called.
func (r *RigTarget) PolecatName() string {
	if r.spawn == nil {
		return ""
	}
	return r.spawn.PolecatName
}

// SpawnResult returns the spawn result for backward compatibility with
// callers that need rig-specific spawn metadata. Returns nil before Prepare().
func (r *RigTarget) SpawnResultData() *SpawnResult {
	return r.spawn
}

// IsSessionRunning checks whether the polecat's tmux session is active.
// Returns false if Prepare() has not been called.
func (r *RigTarget) IsSessionRunning(ctx context.Context) (bool, error) {
	if r.spawn == nil {
		return false, nil
	}
	return r.sessionCheck(ctx, r.spawn)
}
