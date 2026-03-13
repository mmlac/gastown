package dispatch

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// DogTarget dispatches work to a Deacon dog worker.
// It wraps the existing dog dispatch flow (resolve/spawn dog, assign work,
// ensure session running) behind the DispatchTarget interface.
type DogTarget struct {
	dogName   string
	townRoot  string
	workDesc  string
	create    bool
	agentOver string // agent override (e.g. "codex", "gemini")

	// resolved during Prepare
	mgr      *dog.Manager
	sessMgr  *dog.SessionManager
	resolved *dog.Dog
	spawned  bool
}

// DogTargetConfig configures a DogTarget.
type DogTargetConfig struct {
	// DogName is the specific dog name, or empty for pool dispatch.
	DogName string

	// TownRoot overrides the town root directory. If empty, discovered from cwd.
	TownRoot string

	// WorkDesc is the work description (bead ID or formula) assigned to the dog.
	WorkDesc string

	// Create allows creating a new dog if none is available.
	Create bool

	// AgentOverride specifies an alternate agent binary (e.g. "codex").
	AgentOverride string
}

// NewDogTarget creates a DogTarget from the given config.
func NewDogTarget(cfg DogTargetConfig) *DogTarget {
	return &DogTarget{
		dogName:   cfg.DogName,
		townRoot:  cfg.TownRoot,
		workDesc:  cfg.WorkDesc,
		create:    cfg.Create,
		agentOver: cfg.AgentOverride,
	}
}

func (d *DogTarget) AgentID() string {
	if d.resolved != nil {
		return fmt.Sprintf("deacon/dogs/%s", d.resolved.Name)
	}
	if d.dogName != "" {
		return fmt.Sprintf("deacon/dogs/%s", d.dogName)
	}
	return "deacon/dogs/<pool>"
}

func (d *DogTarget) TargetType() string { return "dog" }

func (d *DogTarget) WorkDir() string {
	if d.resolved != nil {
		return d.resolved.Path
	}
	if d.townRoot != "" && d.dogName != "" {
		return filepath.Join(d.townRoot, "deacon", "dogs", d.dogName)
	}
	return ""
}

// Prepare resolves the dog plugin, validates availability, and assigns work.
func (d *DogTarget) Prepare(ctx context.Context) error {
	// Resolve town root if not provided.
	if d.townRoot == "" {
		root, err := workspace.FindFromCwd()
		if err != nil {
			return fmt.Errorf("finding town root: %w", err)
		}
		d.townRoot = root
	}

	rigsConfigPath := filepath.Join(d.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return fmt.Errorf("loading rigs config: %w", err)
	}

	d.mgr = dog.NewManager(d.townRoot, rigsConfig)
	t := tmux.NewTmux()
	d.sessMgr = dog.NewSessionManager(t, d.townRoot, d.mgr)

	// Resolve or create the dog.
	if d.dogName != "" {
		d.resolved, err = d.mgr.Get(d.dogName)
		if err != nil {
			if !d.create {
				return fmt.Errorf("dog %s not found (use create option to add)", d.dogName)
			}
			d.resolved, err = d.mgr.Add(d.dogName)
			if err != nil {
				return fmt.Errorf("creating dog %s: %w", d.dogName, err)
			}
			d.spawned = true
		}
	} else {
		// Pool dispatch – find an idle dog.
		d.resolved, err = d.mgr.GetIdleDog()
		if err != nil {
			return fmt.Errorf("finding idle dog: %w", err)
		}
		if d.resolved == nil {
			if !d.create {
				return fmt.Errorf("no idle dogs available")
			}
			// Auto-create logic is left to callers who control pool sizing.
			return fmt.Errorf("no idle dogs available (pool dispatch)")
		}
	}

	// Assign work.
	if err := d.mgr.AssignWork(d.resolved.Name, d.workDesc); err != nil {
		return fmt.Errorf("assigning work to dog: %w", err)
	}

	return nil
}

// StartSession ensures the dog's agent session is running and returns the pane ID.
func (d *DogTarget) StartSession(ctx context.Context, opts StartOpts) (string, error) {
	if d.resolved == nil || d.sessMgr == nil {
		return "", fmt.Errorf("Prepare must be called before StartSession")
	}

	sessOpts := dog.SessionStartOptions{
		WorkDesc:      d.workDesc,
		AgentOverride: d.agentOver,
	}
	pane, err := d.sessMgr.EnsureRunning(d.resolved.Name, sessOpts)
	if err != nil {
		return "", fmt.Errorf("starting dog session: %w", err)
	}
	return pane, nil
}

// Rollback clears the dog's work assignment on failure.
func (d *DogTarget) Rollback(ctx context.Context) error {
	if d.resolved == nil || d.mgr == nil {
		return nil
	}
	// Clear work assignment so the dog returns to idle.
	if err := d.mgr.AssignWork(d.resolved.Name, ""); err != nil {
		return fmt.Errorf("clearing dog work assignment: %w", err)
	}
	return nil
}

// IsSessionRunning checks whether the dog's tmux session is active.
func (d *DogTarget) IsSessionRunning(ctx context.Context) (bool, error) {
	if d.resolved == nil || d.sessMgr == nil {
		return false, nil
	}
	return d.sessMgr.IsRunning(d.resolved.Name)
}
