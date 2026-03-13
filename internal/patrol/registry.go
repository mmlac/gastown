// Package patrol defines the PatrolHandler interface and PatrolRegistry for
// pluggable patrol registration and execution. This replaces the hardcoded
// IsPatrolEnabled() string-matching in the daemon heartbeat loop.
//
// Each patrol implements PatrolHandler and registers itself with a PatrolRegistry.
// The daemon calls RunEnabled() during heartbeat to execute all enabled patrols.
package patrol

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/sandbox"
)

// Handler is implemented by each patrol type.
type Handler interface {
	// Name returns the unique name for this patrol (e.g., "witness", "dolt_remotes").
	Name() string

	// Run executes one cycle of this patrol.
	Run(ctx context.Context, env Env) error

	// DefaultInterval returns the default interval between patrol runs.
	DefaultInterval() time.Duration

	// RequiresRig returns true if this patrol operates per-rig (called once per rig),
	// or false if it operates per-town (called once globally).
	RequiresRig() bool
}

// Env provides context to patrol handlers during execution.
type Env struct {
	// TownRoot is the Gas Town workspace root directory.
	TownRoot string

	// RigName is the name of the current rig. Non-empty only when the patrol's
	// RequiresRig() returns true.
	RigName string

	// Sandbox is the sandbox lifecycle interface for rigs with remote backends.
	// Nil for rigs without a remote backend (e.g., local-only rigs).
	Sandbox sandbox.Lifecycle

	// Logger is the structured logger for patrol output.
	Logger *slog.Logger
}

// Config holds per-patrol configuration that overrides handler defaults.
type Config struct {
	// Enabled controls whether this patrol runs during heartbeat.
	Enabled bool

	// Interval overrides the handler's DefaultInterval if non-zero.
	Interval time.Duration

	// Rigs limits this patrol to specific rigs. If empty, all rigs are patrolled.
	Rigs []string
}

// IntervalOr returns the configured interval if non-zero, otherwise the fallback.
func (c *Config) IntervalOr(fallback time.Duration) time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return fallback
}

// registeredPatrol holds a handler and its runtime configuration.
type registeredPatrol struct {
	handler  Handler
	enabled  bool
	interval time.Duration
	rigs     []string
}

// Registry manages patrol registration and execution.
type Registry struct {
	mu      sync.RWMutex
	patrols map[string]registeredPatrol
}

// NewRegistry creates an empty patrol registry.
func NewRegistry() *Registry {
	return &Registry{
		patrols: make(map[string]registeredPatrol),
	}
}

// Register adds a patrol handler with the given configuration.
// If a handler with the same name is already registered, it is replaced.
func (r *Registry) Register(handler Handler, config *Config) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.patrols[handler.Name()] = registeredPatrol{
		handler:  handler,
		enabled:  config.Enabled,
		interval: config.IntervalOr(handler.DefaultInterval()),
		rigs:     config.Rigs,
	}
}

// RunEnabled executes all enabled patrols sequentially. If a rig filter is set
// on a patrol and env.RigName is non-empty, the patrol only runs if the rig
// is in the filter list. Errors from individual patrols are logged but do not
// stop execution of subsequent patrols.
func (r *Registry) RunEnabled(ctx context.Context, env Env) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, p := range r.patrols {
		if !p.enabled {
			continue
		}

		// Skip rig-scoped patrols when running in town-level context (no rig)
		if p.handler.RequiresRig() && env.RigName == "" {
			continue
		}

		// Skip town-scoped patrols when running in rig-level context
		if !p.handler.RequiresRig() && env.RigName != "" {
			continue
		}

		// Apply rig filter if configured
		if len(p.rigs) > 0 && env.RigName != "" {
			if !rigInList(env.RigName, p.rigs) {
				continue
			}
		}

		logger := env.Logger
		if logger == nil {
			logger = slog.Default()
		}

		if err := p.handler.Run(ctx, env); err != nil {
			logger.Error("patrol failed",
				"patrol", p.handler.Name(),
				"error", err,
			)
		}
	}
}

// Get returns the registered patrol with the given name and whether it was found.
func (r *Registry) Get(name string) (Handler, *Config, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.patrols[name]
	if !ok {
		return nil, nil, false
	}
	return p.handler, &Config{
		Enabled:  p.enabled,
		Interval: p.interval,
		Rigs:     p.rigs,
	}, true
}

// Names returns the names of all registered patrols.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.patrols))
	for name := range r.patrols {
		names = append(names, name)
	}
	return names
}

// EnabledNames returns the names of all enabled patrols.
func (r *Registry) EnabledNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.patrols))
	for name, p := range r.patrols {
		if p.enabled {
			names = append(names, name)
		}
	}
	return names
}

// rigInList checks if a rig name is in the filter list.
func rigInList(rig string, rigs []string) bool {
	for _, r := range rigs {
		if r == rig {
			return true
		}
	}
	return false
}

// DefaultRegistry returns a pre-configured registry with the built-in patrols.
// All patrols are registered but disabled by default — the daemon config
// enables them based on the operator's daemon.json.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(&DoltRemotesPatrol{}, &Config{Enabled: false})
	r.Register(&DoltBackupPatrol{}, &Config{Enabled: false})
	r.Register(&JsonlGitBackupPatrol{}, &Config{Enabled: false})
	r.Register(&WispReaperPatrol{}, &Config{Enabled: false})
	r.Register(&DoctorDogPatrol{}, &Config{Enabled: false})
	r.Register(&CompactorDogPatrol{}, &Config{Enabled: false})
	r.Register(&ScheduledMaintenancePatrol{}, &Config{Enabled: false})
	r.Register(&SandboxReconcilePatrol{}, &Config{Enabled: false})
	return r
}
