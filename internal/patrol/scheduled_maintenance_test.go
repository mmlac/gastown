package patrol

import (
	"testing"
	"time"
)

func TestScheduledMaintenancePatrol_Interface(t *testing.T) {
	var h Handler = &ScheduledMaintenancePatrol{}
	if h.Name() != "scheduled_maintenance" {
		t.Errorf("Name() = %q, want %q", h.Name(), "scheduled_maintenance")
	}
	if h.DefaultInterval() != 5*time.Minute {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 5*time.Minute)
	}
	if h.RequiresRig() {
		t.Error("RequiresRig() = true, want false")
	}
}

func TestScheduledMaintenancePatrol_RunSkipsNoWindow(t *testing.T) {
	p := &ScheduledMaintenancePatrol{}
	env := testEnv(t)

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil for no window", err)
	}
}

func TestParseWindowTime(t *testing.T) {
	tests := []struct {
		name    string
		window  string
		hour    int
		minute  int
		wantErr bool
	}{
		{"valid", "03:00", 3, 0, false},
		{"valid afternoon", "15:30", 15, 30, false},
		{"midnight", "00:00", 0, 0, false},
		{"end of day", "23:59", 23, 59, false},
		{"invalid hour", "25:00", 0, 0, true},
		{"invalid minute", "03:60", 0, 0, true},
		{"no colon", "0300", 0, 0, true},
		{"empty", "", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, m, err := parseWindowTime(tt.window)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseWindowTime() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if h != tt.hour || m != tt.minute {
					t.Errorf("parseWindowTime() = (%d, %d), want (%d, %d)", h, m, tt.hour, tt.minute)
				}
			}
		})
	}
}

func TestIsInMaintenanceWindow(t *testing.T) {
	loc := time.Local
	tests := []struct {
		name   string
		now    time.Time
		window string
		want   bool
	}{
		{
			"in window",
			time.Date(2026, 3, 13, 3, 30, 0, 0, loc),
			"03:00",
			true,
		},
		{
			"before window",
			time.Date(2026, 3, 13, 2, 59, 0, 0, loc),
			"03:00",
			false,
		},
		{
			"after window",
			time.Date(2026, 3, 13, 4, 01, 0, 0, loc),
			"03:00",
			false,
		},
		{
			"at window start",
			time.Date(2026, 3, 13, 3, 0, 0, 0, loc),
			"03:00",
			true,
		},
		{
			"at window end",
			time.Date(2026, 3, 13, 4, 0, 0, 0, loc),
			"03:00",
			false,
		},
		{
			"invalid window",
			time.Date(2026, 3, 13, 3, 0, 0, 0, loc),
			"invalid",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInMaintenanceWindow(tt.now, tt.window); got != tt.want {
				t.Errorf("isInMaintenanceWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldRunMaintenance(t *testing.T) {
	now := time.Date(2026, 3, 13, 3, 0, 0, 0, time.Local)

	tests := []struct {
		name     string
		lastRun  time.Time
		interval string
		want     bool
	}{
		{"never run", time.Time{}, "daily", true},
		{"ran recently", now.Add(-1 * time.Hour), "daily", false},
		{"ran yesterday", now.Add(-24 * time.Hour), "daily", true},
		{"weekly not yet", now.Add(-3 * 24 * time.Hour), "weekly", false},
		{"weekly ready", now.Add(-7 * 24 * time.Hour), "weekly", true},
		{"custom 48h ready", now.Add(-49 * time.Hour), "48h", true},
		{"custom 48h not ready", now.Add(-40 * time.Hour), "48h", false},
		{"invalid interval defaults to daily", now.Add(-24 * time.Hour), "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRunMaintenance(now, tt.lastRun, tt.interval); got != tt.want {
				t.Errorf("shouldRunMaintenance() = %v, want %v", got, tt.want)
			}
		})
	}
}
