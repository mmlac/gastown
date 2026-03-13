package patrol

import (
	"context"
	"time"
)

const defaultDoltRemotesInterval = 15 * time.Minute

// DoltRemotesPatrol pushes Dolt databases to their configured git remotes.
type DoltRemotesPatrol struct {
	// RunFn is the implementation injected by the daemon. It contains
	// the actual push logic that depends on daemon state (doltServer,
	// patrolConfig, etc.).
	RunFn func(ctx context.Context, env Env) error
}

func (p *DoltRemotesPatrol) Name() string               { return "dolt_remotes" }
func (p *DoltRemotesPatrol) DefaultInterval() time.Duration { return defaultDoltRemotesInterval }
func (p *DoltRemotesPatrol) RequiresRig() bool            { return false }

func (p *DoltRemotesPatrol) Run(ctx context.Context, env Env) error {
	if p.RunFn != nil {
		return p.RunFn(ctx, env)
	}
	return nil
}
