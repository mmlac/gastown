package patrol

import (
	"context"
	"time"
)

const defaultJsonlGitBackupInterval = 15 * time.Minute

// JsonlGitBackupPatrol exports issues to JSONL, scrubs ephemeral data,
// and commits/pushes to a git repository.
// The actual export logic lives in the daemon; this handler provides metadata
// (name, interval, rig scope) for the patrol registry.
type JsonlGitBackupPatrol struct{}

func (p *JsonlGitBackupPatrol) Name() string                       { return "jsonl_git_backup" }
func (p *JsonlGitBackupPatrol) DefaultInterval() time.Duration     { return defaultJsonlGitBackupInterval }
func (p *JsonlGitBackupPatrol) RequiresRig() bool                  { return false }
func (p *JsonlGitBackupPatrol) Run(_ context.Context, _ Env) error { return nil }
