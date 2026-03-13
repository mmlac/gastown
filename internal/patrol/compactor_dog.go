package patrol

import (
	"context"
	"time"
)

const defaultCompactorDogInterval = 24 * time.Hour

// CompactorDogPatrol flattens Dolt commit history on production databases
// to reclaim commit graph storage, then runs gc to reclaim chunks.
type CompactorDogPatrol struct {
	// RunFn is the implementation injected by the daemon.
	RunFn func(ctx context.Context, env Env) error
}

func (p *CompactorDogPatrol) Name() string               { return "compactor_dog" }
func (p *CompactorDogPatrol) DefaultInterval() time.Duration { return defaultCompactorDogInterval }
func (p *CompactorDogPatrol) RequiresRig() bool            { return false }

func (p *CompactorDogPatrol) Run(ctx context.Context, env Env) error {
	if p.RunFn != nil {
		return p.RunFn(ctx, env)
	}
	return nil
}
