package patrol

import (
	"context"
	"time"
)

const defaultDoltBackupInterval = 15 * time.Minute

// DoltBackupPatrol syncs Dolt databases to local filesystem backup locations.
// The actual sync logic lives in the daemon; this handler provides metadata
// (name, interval, rig scope) for the patrol registry.
type DoltBackupPatrol struct{}

func (p *DoltBackupPatrol) Name() string                       { return "dolt_backup" }
func (p *DoltBackupPatrol) DefaultInterval() time.Duration     { return defaultDoltBackupInterval }
func (p *DoltBackupPatrol) RequiresRig() bool                  { return false }
func (p *DoltBackupPatrol) Run(_ context.Context, _ Env) error { return nil }
