package patrol

import (
	"context"
	"time"
)

const defaultWispReaperInterval = 1 * time.Hour

// WispReaperPatrol closes stale wisps (abandoned molecule steps, old patrol
// data) across all Dolt databases to prevent unbounded table growth.
type WispReaperPatrol struct {
	// RunFn is the implementation injected by the daemon.
	RunFn func(ctx context.Context, env Env) error
}

func (p *WispReaperPatrol) Name() string               { return "wisp_reaper" }
func (p *WispReaperPatrol) DefaultInterval() time.Duration { return defaultWispReaperInterval }
func (p *WispReaperPatrol) RequiresRig() bool            { return false }

func (p *WispReaperPatrol) Run(ctx context.Context, env Env) error {
	if p.RunFn != nil {
		return p.RunFn(ctx, env)
	}
	return nil
}
