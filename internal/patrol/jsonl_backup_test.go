package patrol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJsonlGitBackupPatrol_Interface(t *testing.T) {
	var h Handler = &JsonlGitBackupPatrol{}
	if h.Name() != "jsonl_git_backup" {
		t.Errorf("Name() = %q, want %q", h.Name(), "jsonl_git_backup")
	}
	if h.DefaultInterval() != 15*time.Minute {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 15*time.Minute)
	}
	if h.RequiresRig() {
		t.Error("RequiresRig() = true, want false")
	}
}

func TestJsonlGitBackupPatrol_RunSkipsMissingRepo(t *testing.T) {
	p := &JsonlGitBackupPatrol{
		GitRepo: "/nonexistent/path",
	}
	env := testEnv(t)

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil for missing repo", err)
	}
}

func TestJsonlGitBackupPatrol_RunSkipsNoDatabases(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	p := &JsonlGitBackupPatrol{
		GitRepo:   dir,
		Databases: []string{},
	}
	env := testEnv(t)

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil for no databases", err)
	}
}

func TestJsonlGitBackupPatrol_SpikeThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold *float64
		want      float64
	}{
		{"nil uses default", nil, defaultSpikeThreshold},
		{"valid override", floatPtr(0.30), 0.30},
		{"too high uses default", floatPtr(1.5), defaultSpikeThreshold},
		{"zero uses default", floatPtr(0), defaultSpikeThreshold},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &JsonlGitBackupPatrol{SpikeThreshold: tt.threshold}
			got := p.spikeThreshold()
			if got != tt.want {
				t.Errorf("spikeThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTestPollution(t *testing.T) {
	tests := []struct {
		name   string
		record map[string]interface{}
		want   bool
	}{
		{"clean record", map[string]interface{}{"id": "gtd-abc", "title": "Real issue"}, false},
		{"test title", map[string]interface{}{"id": "gtd-abc", "title": "Test Issue foo"}, true},
		{"short id", map[string]interface{}{"id": "bd-5", "title": "Something"}, true},
		{"wisp id", map[string]interface{}{"id": "abc-wisp-xyz", "title": "Something"}, true},
		{"help title", map[string]interface{}{"id": "gtd-abc", "title": "--help"}, true},
		{"usage title", map[string]interface{}{"id": "gtd-abc", "title": "Usage: bd create"}, true},
		{"testdb prefix", map[string]interface{}{"id": "testdb_something", "title": "Issue"}, true},
		{"offlinebrew prefix", map[string]interface{}{"id": "offlinebrew-xyz", "title": "Issue"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTestPollution(tt.record); got != tt.want {
				t.Errorf("isTestPollution() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterTestPollution(t *testing.T) {
	records := []map[string]interface{}{
		{"id": "gtd-abc", "title": "Real issue"},
		{"id": "bd-1", "title": "Test data"},
		{"id": "gtd-def", "title": "Another real issue"},
	}

	var data []byte
	for _, r := range records {
		line, _ := json.Marshal(r)
		data = append(data, line...)
		data = append(data, '\n')
	}

	filtered, removed := filterTestPollution(data)
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	// Verify remaining lines
	lines := 0
	for _, b := range filtered {
		if b == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("filtered has %d lines, want 2", lines)
	}
}

func TestValidDBName(t *testing.T) {
	tests := []struct {
		name  string
		db    string
		valid bool
	}{
		{"simple", "mydb", true},
		{"with underscore", "my_db", true},
		{"with number", "db123", true},
		{"with dash", "my-db", false},
		{"with space", "my db", false},
		{"with dot", "my.db", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validDBName.MatchString(tt.db)
			if got != tt.valid {
				t.Errorf("validDBName.MatchString(%q) = %v, want %v", tt.db, got, tt.valid)
			}
		})
	}
}

func TestSpikeBaseline(t *testing.T) {
	dir := t.TempDir()

	counts := map[string]int{"db1": 100, "db2": 200}
	if err := saveSpikeBaseline(dir, counts); err != nil {
		t.Fatalf("saveSpikeBaseline() error = %v", err)
	}

	baseline := loadSpikeBaseline(dir)
	if baseline == nil {
		t.Fatal("loadSpikeBaseline() returned nil")
	}
	if baseline.Counts["db1"] != 100 {
		t.Errorf("db1 count = %d, want 100", baseline.Counts["db1"])
	}
	if baseline.Counts["db2"] != 200 {
		t.Errorf("db2 count = %d, want 200", baseline.Counts["db2"])
	}

	removeSpikeBaseline(dir)
	if baseline := loadSpikeBaseline(dir); baseline != nil {
		t.Error("expected nil after removal")
	}
}

func TestFormatSpikeReport(t *testing.T) {
	spikes := []spikeInfo{
		{DB: "db1", Previous: 100, Current: 200, Delta: 1.0},
		{DB: "db2", Previous: 200, Current: 100, Delta: 0.5},
	}

	report := formatSpikeReport(spikes)
	if report == "" {
		t.Error("expected non-empty report")
	}
}

func floatPtr(f float64) *float64 { return &f }
