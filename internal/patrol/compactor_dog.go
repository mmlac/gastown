package patrol

import (
	"context"
	"time"
)

const defaultCompactorDogInterval = 24 * time.Hour

// CompactorDogPatrol flattens Dolt commit history on production databases
// to reclaim commit graph storage, then runs gc to reclaim chunks.
// The actual compaction logic lives in the daemon; this handler provides
// metadata (name, interval, rig scope) for the patrol registry.
type CompactorDogPatrol struct{}

func (p *CompactorDogPatrol) Name() string                       { return "compactor_dog" }
func (p *CompactorDogPatrol) DefaultInterval() time.Duration     { return defaultCompactorDogInterval }
func (p *CompactorDogPatrol) RequiresRig() bool                  { return false }
func (p *CompactorDogPatrol) Run(_ context.Context, _ Env) error { return nil }
