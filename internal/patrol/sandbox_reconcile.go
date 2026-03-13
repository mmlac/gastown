package patrol

import (
	"context"
	"time"

	"github.com/steveyegge/gastown/internal/sandbox"
)

const defaultSandboxReconcileInterval = 30 * time.Minute

// SandboxReconcilePatrol periodically reconciles sandbox workspaces for a rig.
// It discovers orphaned workspaces and beads and cleans them up via
// the Sandbox.Reconcile() lifecycle method.
type SandboxReconcilePatrol struct {
	// InstallPrefix is the Daytona workspace naming prefix (e.g., "gt-abc12345").
	InstallPrefix string
}

func (p *SandboxReconcilePatrol) Name() string               { return "sandbox_reconcile" }
func (p *SandboxReconcilePatrol) DefaultInterval() time.Duration { return defaultSandboxReconcileInterval }
func (p *SandboxReconcilePatrol) RequiresRig() bool            { return true }

func (p *SandboxReconcilePatrol) Run(ctx context.Context, env Env) error {
	if env.Sandbox == nil {
		return nil
	}
	return env.Sandbox.Reconcile(ctx, sandbox.ReconcileOpts{
		Rig:           env.RigName,
		InstallPrefix: p.InstallPrefix,
	})
}
