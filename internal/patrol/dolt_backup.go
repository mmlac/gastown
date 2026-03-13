package patrol

import (
	"context"
	"time"
)

const defaultDoltBackupInterval = 15 * time.Minute

// DoltBackupPatrol syncs Dolt databases to local filesystem backup locations.
type DoltBackupPatrol struct {
	// RunFn is the implementation injected by the daemon.
	RunFn func(ctx context.Context, env Env) error
}

func (p *DoltBackupPatrol) Name() string               { return "dolt_backup" }
func (p *DoltBackupPatrol) DefaultInterval() time.Duration { return defaultDoltBackupInterval }
func (p *DoltBackupPatrol) RequiresRig() bool            { return false }

func (p *DoltBackupPatrol) Run(ctx context.Context, env Env) error {
	if p.RunFn != nil {
		return p.RunFn(ctx, env)
	}
	return nil
}
