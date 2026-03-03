package daemon

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

// TestBuildDaytonaExecCommand verifies the command string construction for
// restarting a polecat inside a daytona workspace via daytona exec.
func TestBuildDaytonaExecCommand(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		logger: log.New(&strings.Builder{}, "", 0),
	}

	envVars := map[string]string{
		"GT_RIG":     "myrig",
		"GT_POLECAT": "amber",
		"GT_RUN":     "test-run-id",
	}

	rc := &config.RuntimeConfig{
		Provider: "claude",
		Command:  "claude",
		Args:     []string{"--dangerously-skip-permissions"},
	}

	cmd := d.buildDaytonaExecCommand("gt-abc12345-myrig-amber", envVars, rc)

	// Must start with exec daytona exec <workspace>
	if !strings.HasPrefix(cmd, "exec daytona exec gt-abc12345-myrig-amber") {
		t.Errorf("command should start with 'exec daytona exec <ws>', got: %q", cmd)
	}

	// Must contain --env flags for each env var
	if !strings.Contains(cmd, "--env GT_RIG=") {
		t.Errorf("command should contain --env GT_RIG=, got: %q", cmd)
	}
	if !strings.Contains(cmd, "--env GT_POLECAT=") {
		t.Errorf("command should contain --env GT_POLECAT=, got: %q", cmd)
	}
	if !strings.Contains(cmd, "--env GT_RUN=") {
		t.Errorf("command should contain --env GT_RUN=, got: %q", cmd)
	}

	// Must contain -- separator before the agent command
	if !strings.Contains(cmd, "-- claude") {
		t.Errorf("command should contain '-- claude', got: %q", cmd)
	}

	// Must contain the agent args
	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Errorf("command should contain agent args, got: %q", cmd)
	}
}

// TestBuildDaytonaExecCommand_EnvKeysSorted verifies env keys are sorted
// for deterministic output.
func TestBuildDaytonaExecCommand_EnvKeysSorted(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		logger: log.New(&strings.Builder{}, "", 0),
	}

	envVars := map[string]string{
		"ZZ_LAST":  "last",
		"AA_FIRST": "first",
		"MM_MID":   "mid",
	}

	rc := &config.RuntimeConfig{
		Command: "claude",
		Args:    []string{"--dangerously-skip-permissions"},
	}

	cmd := d.buildDaytonaExecCommand("ws", envVars, rc)

	// AA should appear before MM, which should appear before ZZ
	aaIdx := strings.Index(cmd, "AA_FIRST")
	mmIdx := strings.Index(cmd, "MM_MID")
	zzIdx := strings.Index(cmd, "ZZ_LAST")

	if aaIdx == -1 || mmIdx == -1 || zzIdx == -1 {
		t.Fatalf("expected all env vars in command, got: %q", cmd)
	}
	if aaIdx > mmIdx || mmIdx > zzIdx {
		t.Errorf("env vars should be sorted, got: %q", cmd)
	}
}

// TestBuildDaytonaExecCommand_CustomAgent verifies the command works with
// non-Claude agents (e.g., codex).
func TestBuildDaytonaExecCommand_CustomAgent(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		logger: log.New(&strings.Builder{}, "", 0),
	}

	envVars := map[string]string{
		"GT_RIG": "myrig",
	}

	rc := &config.RuntimeConfig{
		Provider: "codex",
		Command:  "codex",
		Args:     []string{"--approval-mode", "full-auto"},
	}

	cmd := d.buildDaytonaExecCommand("gt-abc-myrig-garnet", envVars, rc)

	if !strings.Contains(cmd, "-- codex") {
		t.Errorf("command should use codex agent, got: %q", cmd)
	}
	if !strings.Contains(cmd, "--approval-mode") {
		t.Errorf("command should contain codex args, got: %q", cmd)
	}
}

// TestRestartPolecatSession_DelegatesToDaytona verifies that when a rig has
// RemoteBackend configured, restartPolecatSession delegates to the daytona
// restart path rather than the local worktree path.
func TestRestartPolecatSession_DelegatesToDaytona(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()

	// Create rig settings with RemoteBackend
	rigDir := filepath.Join(townRoot, "myrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, "settings.json"),
		[]byte(`{"remote_backend":{"provider":"daytona"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	var logBuf strings.Builder
	d := &Daemon{
		config:  &Config{TownRoot: townRoot},
		logger:  log.New(&logBuf, "", 0),
		metrics: &daemonMetrics{},
	}

	// restartDaytonaPolecatSession will fail (no town config), but we can
	// verify it was called by checking the error message.
	err := d.restartPolecatSession("myrig", "amber", "gt-myrig-amber")

	if err == nil {
		t.Fatal("expected error (no town config), got nil")
	}
	// The error should come from the daytona path, not the local path.
	// Local path would say "worktree does not exist".
	if strings.Contains(err.Error(), "worktree does not exist") {
		t.Errorf("should delegate to daytona path, not local path; error: %v", err)
	}
	if !strings.Contains(err.Error(), "daytona") && !strings.Contains(err.Error(), "town config") {
		t.Errorf("expected daytona-related error, got: %v", err)
	}
}

// TestRestartPolecatSession_LocalWhenNoRemoteBackend verifies that when a rig
// has no RemoteBackend, the local restart path is used.
func TestRestartPolecatSession_LocalWhenNoRemoteBackend(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()

	// Create rig settings without RemoteBackend
	rigDir := filepath.Join(townRoot, "myrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, "settings.json"),
		[]byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	var logBuf strings.Builder
	d := &Daemon{
		config:  &Config{TownRoot: townRoot},
		logger:  log.New(&logBuf, "", 0),
		metrics: &daemonMetrics{},
	}

	err := d.restartPolecatSession("myrig", "amber", "gt-myrig-amber")

	if err == nil {
		t.Fatal("expected error (no worktree), got nil")
	}
	// Local path error: worktree doesn't exist
	if !strings.Contains(err.Error(), "worktree does not exist") {
		t.Errorf("expected local worktree error, got: %v", err)
	}
}
