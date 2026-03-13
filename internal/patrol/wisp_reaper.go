package patrol

import (
	"context"
	"time"
)

const defaultWispReaperInterval = 1 * time.Hour

// WispReaperPatrol closes stale wisps (abandoned molecule steps, old patrol
// data) across all Dolt databases to prevent unbounded table growth.
// The actual reap logic lives in the daemon; this handler provides metadata
// (name, interval, rig scope) for the patrol registry.
type WispReaperPatrol struct{}

func (p *WispReaperPatrol) Name() string                       { return "wisp_reaper" }
func (p *WispReaperPatrol) DefaultInterval() time.Duration     { return defaultWispReaperInterval }
func (p *WispReaperPatrol) RequiresRig() bool                  { return false }
func (p *WispReaperPatrol) Run(_ context.Context, _ Env) error { return nil }
