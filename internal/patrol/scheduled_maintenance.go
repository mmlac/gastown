package patrol

import (
	"context"
	"time"
)

const defaultMaintenanceCheckInterval = 5 * time.Minute

// ScheduledMaintenancePatrol checks periodically whether we're in the
// maintenance window and runs `gt maintain --force` when commit counts
// exceed the configured threshold.
// The actual maintenance logic lives in the daemon; this handler provides
// metadata (name, interval, rig scope) for the patrol registry.
type ScheduledMaintenancePatrol struct{}

func (p *ScheduledMaintenancePatrol) Name() string                       { return "scheduled_maintenance" }
func (p *ScheduledMaintenancePatrol) DefaultInterval() time.Duration     { return defaultMaintenanceCheckInterval }
func (p *ScheduledMaintenancePatrol) RequiresRig() bool                  { return false }
func (p *ScheduledMaintenancePatrol) Run(_ context.Context, _ Env) error { return nil }
