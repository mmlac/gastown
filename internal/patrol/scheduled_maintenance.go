package patrol

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaintenanceCheckInterval = 5 * time.Minute
	defaultMaintenanceThreshold     = 1000
)

// ScheduledMaintenancePatrol checks if we're in the maintenance window and
// runs `gt maintain --force` when commit counts exceed the threshold.
type ScheduledMaintenancePatrol struct {
	// Window is the time of day to start maintenance (e.g., "03:00").
	Window string

	// Interval controls how often maintenance runs ("daily", "weekly", "monthly", or duration).
	Interval string

	// Threshold is the minimum commit count before maintenance triggers.
	Threshold int

	// DoltPort is the Dolt server port. Defaults to 3307.
	DoltPort int

	// Databases lists specific databases to check. If empty, skips.
	Databases []string

	// lastRun tracks the last successful maintenance run time.
	lastRun time.Time
}

func (p *ScheduledMaintenancePatrol) Name() string                  { return "scheduled_maintenance" }
func (p *ScheduledMaintenancePatrol) DefaultInterval() time.Duration { return defaultMaintenanceCheckInterval }
func (p *ScheduledMaintenancePatrol) RequiresRig() bool              { return false }

func (p *ScheduledMaintenancePatrol) Run(ctx context.Context, env Env) error {
	if p.Window == "" {
		env.Logger.Info("scheduled_maintenance: no window configured, skipping")
		return nil
	}

	now := time.Now()

	if !isInMaintenanceWindow(now, p.Window) {
		return nil
	}

	interval := p.Interval
	if interval == "" {
		interval = "daily"
	}
	if !shouldRunMaintenance(now, p.lastRun, interval) {
		return nil
	}

	env.Logger.Info("scheduled_maintenance: in window, checking commit counts", "window", p.Window)

	threshold := p.Threshold
	if threshold == 0 {
		threshold = defaultMaintenanceThreshold
	}
	port := p.DoltPort
	if port == 0 {
		port = 3307
	}

	databases := p.Databases
	if len(databases) == 0 {
		env.Logger.Info("scheduled_maintenance: no databases configured")
		p.lastRun = now
		return nil
	}

	needsMaintenance := false
	for _, dbName := range databases {
		commitCount, err := compactorCountCommits(dbName, port)
		if err != nil {
			env.Logger.Error("scheduled_maintenance: error counting commits", "db", dbName, "error", err)
			continue
		}
		if commitCount >= threshold {
			env.Logger.Info("scheduled_maintenance: maintenance needed", "db", dbName, "commits", commitCount, "threshold", threshold)
			needsMaintenance = true
			break
		}
	}

	if !needsMaintenance {
		env.Logger.Info("scheduled_maintenance: all databases below threshold")
		p.lastRun = now
		return nil
	}

	env.Logger.Info("scheduled_maintenance: running gt maintain", "threshold", threshold)

	cmd := exec.CommandContext(ctx, "gt", "maintain", "--force",
		"--threshold", strconv.Itoa(threshold))
	cmd.Dir = env.TownRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gt maintain failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	env.Logger.Info("scheduled_maintenance: gt maintain completed successfully")
	p.lastRun = now
	return nil
}

// parseWindowTime parses an HH:MM string and returns the hour and minute.
func parseWindowTime(window string) (hour, minute int, err error) {
	parts := strings.SplitN(window, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid window format %q: expected HH:MM", window)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour in window %q: expected 0-23", window)
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute in window %q: expected 0-59", window)
	}
	return hour, minute, nil
}

// isInMaintenanceWindow checks if the given time falls within a 1-hour window.
func isInMaintenanceWindow(now time.Time, window string) bool {
	hour, minute, err := parseWindowTime(window)
	if err != nil {
		return false
	}

	windowStart := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	windowEnd := windowStart.Add(1 * time.Hour)

	return !now.Before(windowStart) && now.Before(windowEnd)
}

// shouldRunMaintenance checks if enough time has passed since the last run.
func shouldRunMaintenance(now time.Time, lastRun time.Time, interval string) bool {
	if lastRun.IsZero() {
		return true
	}

	var minGap time.Duration
	switch interval {
	case "daily":
		minGap = 20 * time.Hour
	case "weekly":
		minGap = 6 * 24 * time.Hour
	case "monthly":
		minGap = 27 * 24 * time.Hour
	default:
		d, err := time.ParseDuration(interval)
		if err != nil || d <= 0 {
			minGap = 20 * time.Hour
		} else {
			minGap = d - (d / 10)
		}
	}

	return now.Sub(lastRun) >= minGap
}
