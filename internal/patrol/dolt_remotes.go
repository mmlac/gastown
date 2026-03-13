package patrol

import (
	"context"
	"time"
)

const defaultDoltRemotesInterval = 15 * time.Minute

// DoltRemotesPatrol pushes Dolt databases to their configured git remotes.
// The actual push logic lives in the daemon; this handler provides metadata
// (name, interval, rig scope) for the patrol registry.
type DoltRemotesPatrol struct{}

func (p *DoltRemotesPatrol) Name() string                       { return "dolt_remotes" }
func (p *DoltRemotesPatrol) DefaultInterval() time.Duration     { return defaultDoltRemotesInterval }
func (p *DoltRemotesPatrol) RequiresRig() bool                  { return false }
func (p *DoltRemotesPatrol) Run(_ context.Context, _ Env) error { return nil }
