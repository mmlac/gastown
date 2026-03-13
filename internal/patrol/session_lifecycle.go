package patrol

import (
	"context"
	"time"
)

// SessionLifecyclePatrol is a stub handler for session lifecycle patrols
// (deacon, witness, refinery, handler). These patrols manage agent session
// lifecycles in the daemon heartbeat — the actual work is performed by
// daemon methods (ensureDeaconRunning, ensureWitnessesRunning, etc.).
//
// The handler exists so these patrols can participate in the PatrolRegistry
// for enablement checking, rig filtering, and interval management.
type SessionLifecyclePatrol struct {
	name     string
	interval time.Duration
}

// NewSessionLifecyclePatrol creates a stub patrol for session lifecycle management.
func NewSessionLifecyclePatrol(name string, interval time.Duration) *SessionLifecyclePatrol {
	return &SessionLifecyclePatrol{name: name, interval: interval}
}

func (p *SessionLifecyclePatrol) Name() string               { return p.name }
func (p *SessionLifecyclePatrol) DefaultInterval() time.Duration { return p.interval }
func (p *SessionLifecyclePatrol) RequiresRig() bool            { return false }

// Run is a no-op — session lifecycle patrols are executed directly by daemon
// heartbeat methods, not through the registry's RunEnabled() path.
func (p *SessionLifecyclePatrol) Run(_ context.Context, _ Env) error {
	return nil
}
