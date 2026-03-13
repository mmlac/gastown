package patrol

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultWispReaperInterval = 1 * time.Hour
	defaultWispMaxAge         = 24 * time.Hour
	defaultWispDeleteAge      = 3 * 24 * time.Hour
	wispAlertThreshold        = 500
	defaultMailDeleteAge      = 3 * 24 * time.Hour
	defaultStaleIssueAge      = 7 * 24 * time.Hour
)

// WispReaperPatrol closes stale wisps across all databases.
// It dispatches a mol-dog-reaper molecule for agent execution, falling back
// to inline execution if Dog dispatch fails.
type WispReaperPatrol struct {
	// MaxAge is the maximum age before a wisp is reaped. Defaults to 24h.
	MaxAge time.Duration

	// DeleteAge is the age before closed wisps are permanently deleted. Defaults to 3d.
	DeleteAge time.Duration

	// DryRun when true reports only, makes no changes.
	DryRun bool

	// Databases lists specific databases to reap. If empty, auto-discovers.
	Databases []string

	// DoltPort is the Dolt server port. Defaults to 3307.
	DoltPort int
}

func (p *WispReaperPatrol) Name() string                  { return "wisp_reaper" }
func (p *WispReaperPatrol) DefaultInterval() time.Duration { return defaultWispReaperInterval }
func (p *WispReaperPatrol) RequiresRig() bool              { return false }

func (p *WispReaperPatrol) Run(ctx context.Context, env Env) error {
	maxAge := p.MaxAge
	if maxAge == 0 {
		maxAge = defaultWispMaxAge
	}
	deleteAge := p.DeleteAge
	if deleteAge == 0 {
		deleteAge = defaultWispDeleteAge
	}
	port := p.DoltPort
	if port == 0 {
		port = 3307
	}

	vars := map[string]string{
		"max_age":         maxAge.String(),
		"purge_age":       deleteAge.String(),
		"stale_issue_age": defaultStaleIssueAge.String(),
		"mail_delete_age": defaultMailDeleteAge.String(),
		"alert_threshold": fmt.Sprintf("%d", wispAlertThreshold),
		"dolt_port":       fmt.Sprintf("%d", port),
	}

	if p.DryRun {
		vars["dry_run"] = "true"
		env.Logger.Info("wisp_reaper: DRY RUN — reporting only, no changes will be made")
	}
	if len(p.Databases) > 0 {
		vars["databases"] = strings.Join(p.Databases, ",")
	}

	// Try dispatching to a Dog for formula-driven execution.
	if err := dispatchReaperDog(env.TownRoot, vars); err != nil {
		env.Logger.Warn("wisp_reaper: Dog dispatch failed, inline fallback needed", "error", err)
		return fmt.Errorf("wisp_reaper: Dog dispatch failed: %w", err)
	}

	env.Logger.Info("wisp_reaper: dispatched to Dog for formula-driven execution")
	return nil
}

// dispatchReaperDog dispatches the mol-dog-reaper formula to a Dog via gt sling.
func dispatchReaperDog(townRoot string, vars map[string]string) error {
	args := []string{"sling", "mol-dog-reaper", "deacon/dogs"}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	cmd := exec.Command("gt", args...)
	cmd.Dir = townRoot
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gt sling: %w", err)
	}
	return nil
}
