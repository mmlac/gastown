package patrol

import (
	"context"
	"time"

	"github.com/steveyegge/gastown/internal/sandbox"
)

const (
	defaultSandboxReconcileInterval = 30 * time.Minute
)

// SandboxReconcilePatrol calls env.Sandbox.Reconcile() to discover and clean
// up orphaned workspaces and beads for rigs with remote backends.
type SandboxReconcilePatrol struct{}

func (p *SandboxReconcilePatrol) Name() string                  { return "sandbox_reconcile" }
func (p *SandboxReconcilePatrol) DefaultInterval() time.Duration { return defaultSandboxReconcileInterval }
func (p *SandboxReconcilePatrol) RequiresRig() bool              { return true }

func (p *SandboxReconcilePatrol) Run(ctx context.Context, env Env) error {
	if env.Sandbox == nil {
		env.Logger.Info("sandbox_reconcile: no sandbox configured for rig, skipping", "rig", env.RigName)
		return nil
	}

	env.Logger.Info("sandbox_reconcile: reconciling", "rig", env.RigName)

	if err := env.Sandbox.Reconcile(ctx, sandbox.ReconcileOpts{
		Rig: env.RigName,
	}); err != nil {
		return err
	}

	env.Logger.Info("sandbox_reconcile: complete", "rig", env.RigName)
	return nil
}
