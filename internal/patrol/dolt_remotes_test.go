package patrol

import (
	"testing"
	"time"
)

func TestDoltRemotesPatrol_Interface(t *testing.T) {
	var h Handler = &DoltRemotesPatrol{}
	if h.Name() != "dolt_remotes" {
		t.Errorf("Name() = %q, want %q", h.Name(), "dolt_remotes")
	}
	if h.DefaultInterval() != 15*time.Minute {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 15*time.Minute)
	}
	if h.RequiresRig() {
		t.Error("RequiresRig() = true, want false")
	}
}

func TestDoltRemotesPatrol_RunSkipsMissingDataDir(t *testing.T) {
	p := &DoltRemotesPatrol{
		DataDir: "/nonexistent/path/that/does/not/exist",
	}
	env := testEnv(t)

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil for missing data dir", err)
	}
}

func TestDoltRemotesPatrol_RunSkipsNoDatabases(t *testing.T) {
	dir := t.TempDir()
	p := &DoltRemotesPatrol{
		DataDir:   dir,
		Databases: []string{}, // Empty but dir exists
	}
	env := testEnv(t)

	// With no databases configured and no dolt server, should return nil
	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
}

func TestPushDatabase_RefusesTestDatabases(t *testing.T) {
	tests := []struct {
		name string
		db   string
	}{
		{"test prefix", "test_mydb"},
		{"beads_t prefix", "beads_t_db"},
		{"beads_pt prefix", "beads_pt_db"},
		{"doctest prefix", "doctest_db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pushDatabase(testCtx(), "/tmp", tt.db, "origin", "main")
			if err == nil {
				t.Error("expected error for test database, got nil")
			}
			if err != nil && !contains(err.Error(), "REFUSED") {
				t.Errorf("expected REFUSED error, got: %v", err)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
