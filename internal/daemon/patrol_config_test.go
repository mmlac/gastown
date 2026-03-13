package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPatrolConfig(t *testing.T) {
	// Create a temp dir with test config
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write test config
	configJSON := `{
		"type": "daemon-patrol-config",
		"version": 1,
		"patrols": {
			"refinery": {"enabled": false},
			"witness": {"enabled": true}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Load config and build registry
	config := LoadPatrolConfig(tmpDir)
	if config == nil {
		t.Fatal("expected config to be loaded")
	}

	// Test enabled flags via registry
	r := configurePatrolRegistry(config)
	if r.IsEnabled("refinery") {
		t.Error("expected refinery to be disabled")
	}
	if !r.IsEnabled("witness") {
		t.Error("expected witness to be enabled")
	}
	if !r.IsEnabled("deacon") {
		t.Error("expected deacon to be enabled (default)")
	}
}

func TestPatrolRegistryNilConfig(t *testing.T) {
	// Session lifecycle patrols default to enabled with nil config
	r := configurePatrolRegistry(nil)
	if !r.IsEnabled("refinery") {
		t.Error("expected refinery to be enabled by default")
	}
	if !r.IsEnabled("deacon") {
		t.Error("expected deacon to be enabled by default")
	}

	// Opt-in patrols default to disabled with nil config
	if r.IsEnabled("dolt_remotes") {
		t.Error("expected dolt_remotes to be disabled by default")
	}
}

func TestPatrolRegistryDoltRemotes(t *testing.T) {
	// dolt_remotes defaults to disabled even with nil config (opt-in patrol)
	r := configurePatrolRegistry(nil)
	if r.IsEnabled("dolt_remotes") {
		t.Error("expected dolt_remotes to be disabled with nil config")
	}

	// dolt_remotes defaults to disabled when patrols section exists but DoltRemotes is nil
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{},
	}
	r = configurePatrolRegistry(config)
	if r.IsEnabled("dolt_remotes") {
		t.Error("expected dolt_remotes to be disabled by default")
	}

	// Explicitly enabled
	config.Patrols.DoltRemotes = &DoltRemotesConfig{Enabled: true}
	r = configurePatrolRegistry(config)
	if !r.IsEnabled("dolt_remotes") {
		t.Error("expected dolt_remotes to be enabled when configured")
	}

	// Explicitly disabled
	config.Patrols.DoltRemotes = &DoltRemotesConfig{Enabled: false}
	r = configurePatrolRegistry(config)
	if r.IsEnabled("dolt_remotes") {
		t.Error("expected dolt_remotes to be disabled when explicitly disabled")
	}
}

func TestSaveAndLoadPatrolConfig(t *testing.T) {
	tmpDir := t.TempDir()

	threshold := 500
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			ScheduledMaintenance: &ScheduledMaintenanceConfig{
				Enabled:   true,
				Window:    "03:00",
				Interval:  "daily",
				Threshold: &threshold,
			},
		},
	}

	// Save
	if err := SavePatrolConfig(tmpDir, config); err != nil {
		t.Fatalf("SavePatrolConfig failed: %v", err)
	}

	// Load back
	loaded := LoadPatrolConfig(tmpDir)
	if loaded == nil {
		t.Fatal("expected config to be loaded")
	}

	r := configurePatrolRegistry(loaded)
	if !r.IsEnabled("scheduled_maintenance") {
		t.Error("expected scheduled_maintenance to be enabled")
	}
	sm := loaded.Patrols.ScheduledMaintenance
	if sm.Window != "03:00" {
		t.Errorf("expected window 03:00, got %q", sm.Window)
	}
	if sm.Interval != "daily" {
		t.Errorf("expected interval daily, got %q", sm.Interval)
	}
	if sm.Threshold == nil || *sm.Threshold != 500 {
		t.Errorf("expected threshold 500, got %v", sm.Threshold)
	}
}

func TestDoltRemotesInterval(t *testing.T) {
	// Default interval
	if got := doltRemotesInterval(nil); got != defaultDoltRemotesInterval {
		t.Errorf("expected default interval %v, got %v", defaultDoltRemotesInterval, got)
	}

	// Custom interval
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoltRemotes: &DoltRemotesConfig{
				Enabled:  true,
				Interval: 5 * 60 * 1000000000, // 5 minutes in nanoseconds
			},
		},
	}
	if got := doltRemotesInterval(config); got != 5*60*1000000000 {
		t.Errorf("expected 5m interval, got %v", got)
	}
}
