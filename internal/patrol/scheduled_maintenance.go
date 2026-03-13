package patrol

import (
	"context"
	"time"
)

const defaultMaintenanceCheckInterval = 5 * time.Minute

// ScheduledMaintenancePatrol checks periodically whether we're in the
// maintenance window and runs `gt maintain --force` when commit counts
// exceed the configured threshold.
type ScheduledMaintenancePatrol struct {
	// RunFn is the implementation injected by the daemon.
	RunFn func(ctx context.Context, env Env) error
}

func (p *ScheduledMaintenancePatrol) Name() string               { return "scheduled_maintenance" }
func (p *ScheduledMaintenancePatrol) DefaultInterval() time.Duration { return defaultMaintenanceCheckInterval }
func (p *ScheduledMaintenancePatrol) RequiresRig() bool            { return false }

func (p *ScheduledMaintenancePatrol) Run(ctx context.Context, env Env) error {
	if p.RunFn != nil {
		return p.RunFn(ctx, env)
	}
	return nil
}
