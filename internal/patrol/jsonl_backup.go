package patrol

import (
	"context"
	"time"
)

const defaultJsonlGitBackupInterval = 15 * time.Minute

// JsonlGitBackupPatrol exports issues to JSONL, scrubs ephemeral data,
// and commits/pushes to a git repository.
type JsonlGitBackupPatrol struct {
	// RunFn is the implementation injected by the daemon.
	RunFn func(ctx context.Context, env Env) error
}

func (p *JsonlGitBackupPatrol) Name() string               { return "jsonl_git_backup" }
func (p *JsonlGitBackupPatrol) DefaultInterval() time.Duration { return defaultJsonlGitBackupInterval }
func (p *JsonlGitBackupPatrol) RequiresRig() bool            { return false }

func (p *JsonlGitBackupPatrol) Run(ctx context.Context, env Env) error {
	if p.RunFn != nil {
		return p.RunFn(ctx, env)
	}
	return nil
}
