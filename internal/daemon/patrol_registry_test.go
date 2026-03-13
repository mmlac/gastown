package daemon

import (
	"testing"
	"time"
)

func TestConfigurePatrolRegistry_NilConfig(t *testing.T) {
	r := configurePatrolRegistry(nil)

	// Session lifecycle patrols default to enabled.
	for _, name := range []string{"deacon", "witness", "refinery", "handler"} {
		if !r.IsEnabled(name) {
			t.Errorf("%q should be enabled by default", name)
		}
	}

	// Opt-in patrols default to disabled.
	for _, name := range []string{"dolt_remotes", "dolt_backup", "jsonl_git_backup",
		"wisp_reaper", "doctor_dog", "compactor_dog", "scheduled_maintenance"} {
		if r.IsEnabled(name) {
			t.Errorf("%q should be disabled by default", name)
		}
	}
}

func TestConfigurePatrolRegistry_OverridesLifecycle(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			Refinery: &PatrolConfig{Enabled: false},
			Witness:  &PatrolConfig{Enabled: true, Rigs: []string{"rig-a"}},
		},
	}
	r := configurePatrolRegistry(config)

	if r.IsEnabled("refinery") {
		t.Error("refinery should be disabled when config says so")
	}
	if !r.IsEnabled("witness") {
		t.Error("witness should be enabled")
	}
	// Deacon not mentioned in config → still enabled by default.
	if !r.IsEnabled("deacon") {
		t.Error("deacon should still be enabled by default")
	}
}

func TestConfigurePatrolRegistry_OverridesOptIn(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoltRemotes: &DoltRemotesConfig{
				Enabled:  true,
				Interval: 10 * time.Minute,
			},
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "2h",
			},
		},
	}
	r := configurePatrolRegistry(config)

	if !r.IsEnabled("dolt_remotes") {
		t.Error("dolt_remotes should be enabled")
	}
	if got := r.Interval("dolt_remotes"); got != 10*time.Minute {
		t.Errorf("dolt_remotes interval = %v, want 10m", got)
	}

	if !r.IsEnabled("wisp_reaper") {
		t.Error("wisp_reaper should be enabled")
	}
	if got := r.Interval("wisp_reaper"); got != 2*time.Hour {
		t.Errorf("wisp_reaper interval = %v, want 2h", got)
	}

	// Unconfigured opt-in patrols remain disabled.
	if r.IsEnabled("doctor_dog") {
		t.Error("doctor_dog should remain disabled")
	}
}

func TestParseDurationOr(t *testing.T) {
	tests := []struct {
		input    string
		fallback time.Duration
		want     time.Duration
	}{
		{"", 5 * time.Minute, 5 * time.Minute},
		{"invalid", 5 * time.Minute, 5 * time.Minute},
		{"10m", 5 * time.Minute, 10 * time.Minute},
		{"-1s", 5 * time.Minute, 5 * time.Minute},
		{"0s", 5 * time.Minute, 5 * time.Minute},
		{"1h", 0, 1 * time.Hour},
	}
	for _, tt := range tests {
		got := parseDurationOr(tt.input, tt.fallback)
		if got != tt.want {
			t.Errorf("parseDurationOr(%q, %v) = %v, want %v", tt.input, tt.fallback, got, tt.want)
		}
	}
}
