package patrol

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// testPatrol is a minimal Handler implementation for testing.
type testPatrol struct {
	name            string
	defaultInterval time.Duration
	requiresRig     bool
	runFunc         func(ctx context.Context, env Env) error
	runCount        atomic.Int32
}

func (p *testPatrol) Name() string                  { return p.name }
func (p *testPatrol) DefaultInterval() time.Duration { return p.defaultInterval }
func (p *testPatrol) RequiresRig() bool              { return p.requiresRig }
func (p *testPatrol) Run(ctx context.Context, env Env) error {
	p.runCount.Add(1)
	if p.runFunc != nil {
		return p.runFunc(ctx, env)
	}
	return nil
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if len(r.Names()) != 0 {
		t.Fatalf("expected empty registry, got %d patrols", len(r.Names()))
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	h := &testPatrol{name: "test_patrol", defaultInterval: 5 * time.Minute}

	r.Register(h, &Config{Enabled: true, Interval: 10 * time.Minute})

	got, cfg, ok := r.Get("test_patrol")
	if !ok {
		t.Fatal("expected to find registered patrol")
	}
	if got.Name() != "test_patrol" {
		t.Errorf("got name %q, want %q", got.Name(), "test_patrol")
	}
	if !cfg.Enabled {
		t.Error("expected patrol to be enabled")
	}
	if cfg.Interval != 10*time.Minute {
		t.Errorf("got interval %v, want %v", cfg.Interval, 10*time.Minute)
	}
}

func TestGetNotFound(t *testing.T) {
	r := NewRegistry()
	_, _, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected not found for unregistered patrol")
	}
}

func TestRegisterReplace(t *testing.T) {
	r := NewRegistry()
	h1 := &testPatrol{name: "patrol", defaultInterval: 1 * time.Minute}
	h2 := &testPatrol{name: "patrol", defaultInterval: 2 * time.Minute}

	r.Register(h1, &Config{Enabled: false})
	r.Register(h2, &Config{Enabled: true})

	_, cfg, ok := r.Get("patrol")
	if !ok {
		t.Fatal("expected to find patrol")
	}
	if !cfg.Enabled {
		t.Error("expected replaced patrol to be enabled")
	}
	if cfg.Interval != 2*time.Minute {
		t.Errorf("got interval %v, want %v", cfg.Interval, 2*time.Minute)
	}
}

func TestConfigIntervalOr(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		fallback time.Duration
		want     time.Duration
	}{
		{"zero uses fallback", Config{}, 5 * time.Minute, 5 * time.Minute},
		{"non-zero overrides", Config{Interval: 10 * time.Minute}, 5 * time.Minute, 10 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.IntervalOr(tt.fallback)
			if got != tt.want {
				t.Errorf("IntervalOr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunEnabledSkipsDisabled(t *testing.T) {
	r := NewRegistry()
	enabled := &testPatrol{name: "enabled"}
	disabled := &testPatrol{name: "disabled"}

	r.Register(enabled, &Config{Enabled: true})
	r.Register(disabled, &Config{Enabled: false})

	env := Env{TownRoot: "/tmp/town", Logger: slog.Default()}
	r.RunEnabled(context.Background(), env)

	if enabled.runCount.Load() != 1 {
		t.Errorf("enabled patrol ran %d times, want 1", enabled.runCount.Load())
	}
	if disabled.runCount.Load() != 0 {
		t.Errorf("disabled patrol ran %d times, want 0", disabled.runCount.Load())
	}
}

func TestRunEnabledRigScoping(t *testing.T) {
	r := NewRegistry()
	townPatrol := &testPatrol{name: "town", requiresRig: false}
	rigPatrol := &testPatrol{name: "rig", requiresRig: true}

	r.Register(townPatrol, &Config{Enabled: true})
	r.Register(rigPatrol, &Config{Enabled: true})

	// Town-level context (no rig) — only town patrol runs
	env := Env{TownRoot: "/tmp/town", Logger: slog.Default()}
	r.RunEnabled(context.Background(), env)

	if townPatrol.runCount.Load() != 1 {
		t.Errorf("town patrol ran %d times, want 1", townPatrol.runCount.Load())
	}
	if rigPatrol.runCount.Load() != 0 {
		t.Errorf("rig patrol ran %d times in town context, want 0", rigPatrol.runCount.Load())
	}

	// Rig-level context — only rig patrol runs
	townPatrol.runCount.Store(0)
	rigPatrol.runCount.Store(0)
	env.RigName = "testrig"
	r.RunEnabled(context.Background(), env)

	if townPatrol.runCount.Load() != 0 {
		t.Errorf("town patrol ran %d times in rig context, want 0", townPatrol.runCount.Load())
	}
	if rigPatrol.runCount.Load() != 1 {
		t.Errorf("rig patrol ran %d times, want 1", rigPatrol.runCount.Load())
	}
}

func TestRunEnabledRigFilter(t *testing.T) {
	r := NewRegistry()
	filtered := &testPatrol{name: "filtered", requiresRig: true}

	r.Register(filtered, &Config{
		Enabled: true,
		Rigs:    []string{"rig-a", "rig-b"},
	})

	// Matching rig
	env := Env{TownRoot: "/tmp/town", RigName: "rig-a", Logger: slog.Default()}
	r.RunEnabled(context.Background(), env)
	if filtered.runCount.Load() != 1 {
		t.Errorf("patrol ran %d times for matching rig, want 1", filtered.runCount.Load())
	}

	// Non-matching rig
	filtered.runCount.Store(0)
	env.RigName = "rig-c"
	r.RunEnabled(context.Background(), env)
	if filtered.runCount.Load() != 0 {
		t.Errorf("patrol ran %d times for non-matching rig, want 0", filtered.runCount.Load())
	}
}

func TestRunEnabledLogsErrors(t *testing.T) {
	r := NewRegistry()
	failing := &testPatrol{
		name: "failing",
		runFunc: func(_ context.Context, _ Env) error {
			return errors.New("patrol error")
		},
	}
	succeeding := &testPatrol{name: "succeeding"}

	r.Register(failing, &Config{Enabled: true})
	r.Register(succeeding, &Config{Enabled: true})

	// Should not panic; errors are logged, not propagated
	env := Env{TownRoot: "/tmp/town", Logger: slog.Default()}
	r.RunEnabled(context.Background(), env)

	if failing.runCount.Load() != 1 {
		t.Errorf("failing patrol ran %d times, want 1", failing.runCount.Load())
	}
	// succeeding patrol should still run despite failing patrol's error
	if succeeding.runCount.Load() != 1 {
		t.Errorf("succeeding patrol ran %d times, want 1", succeeding.runCount.Load())
	}
}

func TestRunEnabledNilLogger(t *testing.T) {
	r := NewRegistry()
	p := &testPatrol{name: "test"}
	r.Register(p, &Config{Enabled: true})

	// Nil logger should not panic — falls back to slog.Default()
	env := Env{TownRoot: "/tmp/town"}
	r.RunEnabled(context.Background(), env)

	if p.runCount.Load() != 1 {
		t.Errorf("patrol ran %d times, want 1", p.runCount.Load())
	}
}

func TestNames(t *testing.T) {
	r := NewRegistry()
	r.Register(&testPatrol{name: "beta"}, &Config{Enabled: true})
	r.Register(&testPatrol{name: "alpha"}, &Config{Enabled: false})

	names := r.Names()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("got names %v, want [alpha beta]", names)
	}
}

func TestEnabledNames(t *testing.T) {
	r := NewRegistry()
	r.Register(&testPatrol{name: "on"}, &Config{Enabled: true})
	r.Register(&testPatrol{name: "off"}, &Config{Enabled: false})
	r.Register(&testPatrol{name: "also_on"}, &Config{Enabled: true})

	names := r.EnabledNames()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "also_on" || names[1] != "on" {
		t.Errorf("got enabled names %v, want [also_on on]", names)
	}
}

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()
	if r == nil {
		t.Fatal("DefaultRegistry returned nil")
	}
	// DefaultRegistry currently returns an empty registry; future migrations
	// will populate it with built-in patrols.
}
