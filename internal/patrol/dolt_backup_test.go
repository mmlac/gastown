package patrol

import (
	"testing"
	"time"
)

func TestDoltBackupPatrol_Interface(t *testing.T) {
	var h Handler = &DoltBackupPatrol{}
	if h.Name() != "dolt_backup" {
		t.Errorf("Name() = %q, want %q", h.Name(), "dolt_backup")
	}
	if h.DefaultInterval() != 15*time.Minute {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 15*time.Minute)
	}
	if h.RequiresRig() {
		t.Error("RequiresRig() = true, want false")
	}
}

func TestDoltBackupPatrol_RunSkipsMissingDataDir(t *testing.T) {
	p := &DoltBackupPatrol{
		DataDir: "/nonexistent/path",
	}
	env := testEnv(t)

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil for missing data dir", err)
	}
}

func TestDoltBackupPatrol_RunSkipsNoDatabases(t *testing.T) {
	dir := t.TempDir()
	p := &DoltBackupPatrol{
		DataDir:   dir,
		Databases: []string{},
	}
	env := testEnv(t)

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
}
