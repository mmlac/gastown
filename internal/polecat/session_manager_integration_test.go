package polecat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Integration tests for local, exitbox, and Daytona polecat execution modes.
// These tests verify the SessionManager integration across execution modes
// using mock sandbox implementations (not real Daytona).
//
// The three modes differ in how Start() creates the tmux session:
//
//  1. Local: No sandbox, no exec-wrapper. tmux runs the agent command directly
//     in the local git worktree.
//
//  2. Exitbox: No sandbox lifecycle, but RuntimeConfig has ExecWrapper configured.
//     The tmux command is prefixed with the exitbox wrapper. No PreStart/PostStop.
//
//  3. Daytona: Sandbox lifecycle (PreStart/PostStop) + exec-wrapper. PreStart
//     provisions the remote workspace and returns inner env vars. PostStop
//     handles cleanup (cert revocation, workspace stop).
//
// Note: Tests avoid calling Start() with real commands because Start() internally
// calls WaitForRuntimeReady which polls for a Claude prompt prefix. Instead,
// tests use:
//   - Sentinel errors on PreStart to capture opts without completing startup
//   - Manual tmux session creation + Stop() to test teardown paths
//   - Direct function calls (InjectInnerEnv, ExpandWrapper) for command structure

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupIntegrationRig creates a temporary rig directory structure suitable for
// SessionManager integration tests. Returns the rig root path.
func setupIntegrationRig(t *testing.T, rigName string, polecats ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range polecats {
		dir := filepath.Join(root, "polecats", p)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return root
}

// setupIntegrationRegistry registers a unique prefix to isolate tests from
// real running sessions.
func setupIntegrationRegistry(t *testing.T, prefix, rigName string) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register(prefix, rigName)
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// requireTmuxIntegration skips the test if tmux is not available.
func requireTmuxIntegration(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed — skipping integration test")
	}
}

// ---------------------------------------------------------------------------
// 1. Local Polecat Mode (no sandbox)
// ---------------------------------------------------------------------------

// TestIntegration_LocalMode_StopKillsSession verifies that Stop kills the tmux
// session without calling any sandbox hooks when no sandbox is configured.
func TestIntegration_LocalMode_StopKillsSession(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "ls", "local-stop")

	rigName := "local-stop"
	polecatName := fmt.Sprintf("local-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r)

	// Verify: sandbox is nil (pure local mode).
	if sm.sandbox != nil {
		t.Fatal("local mode: sandbox should be nil")
	}

	// Create a tmux session manually (simulating a running local polecat).
	sessionID := sm.SessionName(polecatName)
	if err := tm.NewSession(sessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionID) })

	// Verify session is running.
	running, err := tm.HasSession(sessionID)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !running {
		t.Fatal("expected tmux session to be running")
	}

	// Stop (force) — should succeed without sandbox hooks.
	if err := sm.Stop(context.Background(), polecatName, true); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	// Verify: tmux session is gone.
	running, _ = tm.HasSession(sessionID)
	if running {
		t.Error("expected tmux session to be killed after Stop()")
	}
}

// TestIntegration_LocalMode_NoPostStop verifies that Stop does NOT call any
// sandbox PostStop when no sandbox is configured (local mode).
func TestIntegration_LocalMode_NoPostStop(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "ln", "local-nopost")

	rigName := "local-nopost"
	polecatName := fmt.Sprintf("nopost-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r)

	sessionID := sm.SessionName(polecatName)
	if err := tm.NewSession(sessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionID) })

	if err := sm.Stop(context.Background(), polecatName, true); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	running, _ := tm.HasSession(sessionID)
	if running {
		t.Error("session should be killed")
	}
}

// TestIntegration_LocalMode_WorkDir verifies that local mode uses the clone
// path (git worktree) as working directory, not the marker directory.
func TestIntegration_LocalMode_WorkDir(t *testing.T) {
	rigName := "local-workdir"
	polecatName := "dirtest"
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	sm := NewSessionManager(tmux.NewTmux(), r)

	// Local mode: clonePath should be used (polecats/<name>/<rigname>).
	// Without the subdirectory existing, it defaults to new structure.
	clonePath := sm.clonePath(polecatName)
	expectedClone := filepath.Join(root, "polecats", polecatName, rigName)
	if clonePath != expectedClone {
		t.Errorf("local mode clonePath = %q, want %q", clonePath, expectedClone)
	}

	// polecatDir is the marker directory — used by sandbox mode.
	polecatDir := sm.polecatDir(polecatName)
	expectedDir := filepath.Join(root, "polecats", polecatName)
	if polecatDir != expectedDir {
		t.Errorf("polecatDir = %q, want %q", polecatDir, expectedDir)
	}

	// In local mode, the start workdir should be clonePath, not polecatDir.
	if clonePath == polecatDir {
		t.Error("local mode: clonePath should differ from polecatDir")
	}
}

// TestIntegration_LocalMode_EnvVarsNotInnerEnv verifies that in local mode,
// there is no sandbox inner env — all env vars are set via tmux SetEnvironment
// (outer env only). This contrasts with Daytona mode where inner env vars
// are injected after the exec-wrapper delimiter.
func TestIntegration_LocalMode_EnvVarsNotInnerEnv(t *testing.T) {
	rigName := "local-env"
	polecatName := "envtest"
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	sm := NewSessionManager(tmux.NewTmux(), r)

	// Local mode: no sandbox, so no inner env injection.
	if sm.sandbox != nil {
		t.Fatal("local mode should have nil sandbox")
	}

	// Verify the expected env vars that AgentEnv generates for polecats.
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:      "polecat",
		Rig:       rigName,
		AgentName: polecatName,
		TownRoot:  filepath.Dir(root),
	})

	// Key vars should be present.
	requiredKeys := []string{"GT_RIG", "GT_POLECAT", "GT_ROLE", "BD_ACTOR"}
	for _, key := range requiredKeys {
		if _, ok := envVars[key]; !ok {
			t.Errorf("AgentEnv missing key %q", key)
		}
	}

	// GT_ROLE should be compound format.
	expectedRole := rigName + "/polecats/" + polecatName
	if envVars["GT_ROLE"] != expectedRole {
		t.Errorf("GT_ROLE = %q, want %q", envVars["GT_ROLE"], expectedRole)
	}
}

// TestIntegration_LocalMode_DoubleStartBlocked verifies that starting a session
// that is already running returns ErrSessionRunning (without needing Start()
// to complete — the check happens before any sandbox/config work).
func TestIntegration_LocalMode_DoubleStartBlocked(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "ld", "local-double")

	rigName := "local-double"
	polecatName := fmt.Sprintf("double-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r)

	// Create a tmux session manually to simulate a running polecat.
	sessionID := sm.SessionName(polecatName)
	if err := tm.NewSession(sessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionID) })

	// Start should fail with ErrSessionRunning (before reaching config/sandbox).
	err := sm.Start(context.Background(), polecatName, SessionStartOptions{
		WorkDir: filepath.Join(root, "polecats", polecatName),
	})
	if err == nil {
		t.Fatal("expected error on double start")
	}
	if !errors.Is(err, ErrSessionRunning) {
		t.Errorf("expected ErrSessionRunning, got: %v", err)
	}
}

// TestIntegration_LocalMode_PolecatNotFound verifies that Start returns
// ErrPolecatNotFound when the polecat directory doesn't exist.
func TestIntegration_LocalMode_PolecatNotFound(t *testing.T) {
	root := t.TempDir()
	r := &rig.Rig{
		Name:     "localrig",
		Path:     root,
		Polecats: []string{"nonexistent"},
	}
	sm := NewSessionManager(tmux.NewTmux(), r)

	err := sm.Start(context.Background(), "nonexistent", SessionStartOptions{})
	if err == nil {
		t.Fatal("expected error for nonexistent polecat")
	}
	if !errors.Is(err, ErrPolecatNotFound) {
		t.Errorf("expected ErrPolecatNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. Exitbox-Wrapped Polecat Mode (exec-wrapper, no sandbox lifecycle)
// ---------------------------------------------------------------------------

// TestIntegration_ExitboxMode_NoSandboxLifecycle verifies that when an
// exec-wrapper is configured (e.g., exitbox) but no sandbox is set,
// SessionManager does NOT call any PreStart/PostStop hooks. The wrapper
// is applied at the command level by BuildStartupCommand, not by
// SessionManager directly.
func TestIntegration_ExitboxMode_NoSandboxLifecycle(t *testing.T) {
	rigName := "exitbox-rig"
	polecatName := "wrapped"
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}

	// No sandbox — exec-wrapper is handled by BuildStartupCommand via RuntimeConfig.
	sm := NewSessionManager(tmux.NewTmux(), r)

	if sm.sandbox != nil {
		t.Fatal("exitbox mode: sandbox should be nil (wrapper is config-level, not lifecycle)")
	}

	// Verify workDir: should use clonePath (local git worktree), not polecatDir.
	clonePath := sm.clonePath(polecatName)
	polecatDir := sm.polecatDir(polecatName)
	if clonePath == polecatDir {
		t.Error("exitbox mode: clonePath should differ from polecatDir (uses local worktree)")
	}
}

// TestIntegration_ExitboxMode_CommandStructure verifies the expected command
// structure when an exitbox exec-wrapper is configured. The command should be:
//
//	exec env OUTER_VARS... exitbox run --profile=X -- agent-cmd
func TestIntegration_ExitboxMode_CommandStructure(t *testing.T) {
	wrapper := []string{"exitbox", "run", "--profile=gastown-polecat", "--"}
	ctx := config.WrapperContext{
		Rig:     "testrig",
		Polecat: "testpolecat",
	}
	expanded := config.ExpandWrapper(wrapper, ctx)

	if len(expanded) != 4 {
		t.Fatalf("expected 4 wrapper args, got %d: %v", len(expanded), expanded)
	}
	if expanded[0] != "exitbox" {
		t.Errorf("wrapper[0] = %q, want 'exitbox'", expanded[0])
	}
	if expanded[1] != "run" {
		t.Errorf("wrapper[1] = %q, want 'run'", expanded[1])
	}
	if expanded[2] != "--profile=gastown-polecat" {
		t.Errorf("wrapper[2] = %q, want '--profile=gastown-polecat'", expanded[2])
	}
	if expanded[3] != "--" {
		t.Errorf("wrapper[3] = %q, want '--'", expanded[3])
	}
}

// TestIntegration_ExitboxMode_InnerEnvInjection verifies that InjectInnerEnv
// correctly inserts env vars after the exec-wrapper's -- delimiter.
func TestIntegration_ExitboxMode_InnerEnvInjection(t *testing.T) {
	command := "exec env GT_RIG=testrig exitbox run --profile=gastown-polecat -- claude --dangerously-skip-permissions"
	innerEnv := map[string]string{
		"GT_PROXY_URL": "https://127.0.0.1:9876",
	}

	result := config.InjectInnerEnv(command, innerEnv)

	if !strings.Contains(result, "-- env GT_PROXY_URL=") {
		t.Errorf("inner env not injected after '--' delimiter.\nCommand: %s", result)
	}
	if !strings.Contains(result, "claude --dangerously-skip-permissions") {
		t.Errorf("agent command lost after injection.\nCommand: %s", result)
	}
}

// TestIntegration_ExitboxMode_StopNoSandbox verifies that an exitbox-mode
// polecat session stops correctly without calling any sandbox hooks.
func TestIntegration_ExitboxMode_StopNoSandbox(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "ex", "exitbox-integ")

	rigName := "exitbox-integ"
	polecatName := fmt.Sprintf("exitbox-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r)

	sessionID := sm.SessionName(polecatName)
	if err := tm.NewSession(sessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionID) })

	running, _ := tm.HasSession(sessionID)
	if !running {
		t.Fatal("expected session to be running")
	}

	// Stop — no sandbox cleanup needed.
	if err := sm.Stop(context.Background(), polecatName, true); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	running, _ = tm.HasSession(sessionID)
	if running {
		t.Error("expected session to be killed after Stop()")
	}
}

// TestIntegration_ExitboxMode_WrapperTemplateExpansion verifies that wrapper
// template variables ({{rig}}, {{polecat}}, etc.) are correctly expanded.
func TestIntegration_ExitboxMode_WrapperTemplateExpansion(t *testing.T) {
	wrapper := []string{"exitbox", "run", "--rig={{rig}}", "--polecat={{polecat}}", "--"}
	ctx := config.WrapperContext{
		Rig:     "myrig",
		Polecat: "mypolecat",
	}
	expanded := config.ExpandWrapper(wrapper, ctx)

	if expanded[2] != "--rig=myrig" {
		t.Errorf("rig template not expanded: %q", expanded[2])
	}
	if expanded[3] != "--polecat=mypolecat" {
		t.Errorf("polecat template not expanded: %q", expanded[3])
	}
}

// ---------------------------------------------------------------------------
// 3. Daytona Polecat Mode (sandbox lifecycle + exec-wrapper)
// ---------------------------------------------------------------------------

// TestIntegration_DaytonaMode_PreStartCalled verifies that Start calls
// sandbox.PreStart with correctly populated SandboxOpts before creating
// the tmux session. Uses sentinel error to capture opts without completing.
func TestIntegration_DaytonaMode_PreStartCalled(t *testing.T) {
	rigName := "daytona-rig"
	polecatName := "remote"
	root := setupIntegrationRig(t, rigName, polecatName)

	sbx := newMockSandbox("gt-dtn")
	captureErr := errors.New("capture-prestart-sentinel")
	sbx.preStartErr = captureErr

	settings := &config.RigSettings{
		RemoteBackend: &config.RemoteBackendConfig{
			Image:   "gastown:latest",
			Profile: "standard",
		},
	}

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	sm := NewSessionManager(tmux.NewTmux(), r,
		WithSandbox(sbx),
		WithSettings(settings),
		WithInstallPrefix("gt-dtn"),
	)

	_ = sm.Start(context.Background(), polecatName, SessionStartOptions{
		Branch: "feat/daytona-four",
	})

	if len(sbx.preStartCalls) != 1 {
		t.Fatalf("expected 1 PreStart call, got %d", len(sbx.preStartCalls))
	}

	opts := sbx.preStartCalls[0]
	if opts.Rig != rigName {
		t.Errorf("PreStart opts.Rig = %q, want %q", opts.Rig, rigName)
	}
	if opts.Polecat != polecatName {
		t.Errorf("PreStart opts.Polecat = %q, want %q", opts.Polecat, polecatName)
	}
	if opts.InstallPrefix != "gt-dtn" {
		t.Errorf("PreStart opts.InstallPrefix = %q, want %q", opts.InstallPrefix, "gt-dtn")
	}
	expectedWs := "gt-dtn-daytona-rig--remote"
	if opts.WorkspaceName != expectedWs {
		t.Errorf("PreStart opts.WorkspaceName = %q, want %q", opts.WorkspaceName, expectedWs)
	}
	if opts.RigSettings != settings {
		t.Error("PreStart opts.RigSettings should be the configured settings")
	}
	if opts.Branch != "feat/daytona-four" {
		t.Errorf("PreStart opts.Branch = %q, want %q", opts.Branch, "feat/daytona-four")
	}
}

// TestIntegration_DaytonaMode_PostStopCalled verifies that Stop calls
// sandbox.PostStop after killing the tmux session, with correct SandboxOpts
// including cert serial read from tmux environment.
func TestIntegration_DaytonaMode_PostStopCalled(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "dp", "daytona-post")

	rigName := "daytona-post"
	polecatName := fmt.Sprintf("dtn-stop-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	sbx := newMockSandbox("gt-dtn")
	settings := &config.RigSettings{
		RemoteBackend: &config.RemoteBackendConfig{
			Image:    "gastown:latest",
			AutoStop: true,
		},
	}

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r,
		WithSandbox(sbx),
		WithSettings(settings),
		WithInstallPrefix("gt-dtn"),
	)

	// Create tmux session manually (simulating a running Daytona polecat).
	sessionID := sm.SessionName(polecatName)
	if err := tm.NewSession(sessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionID) })

	// Simulate cert serial stored by Start().
	_ = tm.SetEnvironment(sessionID, "GT_CERT_SERIAL", "deadbeef01")

	if err := sm.Stop(context.Background(), polecatName, true); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if len(sbx.postStopCalls) != 1 {
		t.Fatalf("expected 1 PostStop call, got %d", len(sbx.postStopCalls))
	}

	opts := sbx.postStopCalls[0]
	if opts.Rig != rigName {
		t.Errorf("PostStop opts.Rig = %q, want %q", opts.Rig, rigName)
	}
	if opts.Polecat != polecatName {
		t.Errorf("PostStop opts.Polecat = %q, want %q", opts.Polecat, polecatName)
	}
	if opts.CertSerial != "deadbeef01" {
		t.Errorf("PostStop opts.CertSerial = %q, want %q", opts.CertSerial, "deadbeef01")
	}
	if opts.InstallPrefix != "gt-dtn" {
		t.Errorf("PostStop opts.InstallPrefix = %q, want %q", opts.InstallPrefix, "gt-dtn")
	}
	expectedWs := "gt-dtn-daytona-post--" + polecatName
	if opts.WorkspaceName != expectedWs {
		t.Errorf("PostStop opts.WorkspaceName = %q, want %q", opts.WorkspaceName, expectedWs)
	}
	if opts.RigSettings != settings {
		t.Error("PostStop opts.RigSettings should be the configured settings")
	}

	running, _ := tm.HasSession(sessionID)
	if running {
		t.Error("tmux session should have been killed")
	}
}

// TestIntegration_DaytonaMode_InnerEnvFromPreStart verifies that inner env vars
// returned by sandbox.PreStart are correctly injected into the command via
// InjectInnerEnv. Tests the data flow: PreStart → innerEnv → InjectInnerEnv.
func TestIntegration_DaytonaMode_InnerEnvFromPreStart(t *testing.T) {
	innerEnv := map[string]string{
		"GT_PROXY_URL":   "https://127.0.0.1:9876",
		"GT_CERT_PATH":   "/etc/certs/client.pem",
		"GT_CERT_SERIAL": "abc123",
	}

	// Daytona-style command with exec-wrapper:
	command := "exec env GT_RIG=testrig daytona exec gt-dtn-testrig--mypolecat --tty -- claude --dangerously-skip-permissions"
	result := config.InjectInnerEnv(command, innerEnv)

	if !strings.Contains(result, "-- env ") {
		t.Errorf("inner env not injected after '--' delimiter.\nResult: %s", result)
	}
	for k := range innerEnv {
		if !strings.Contains(result, k+"=") {
			t.Errorf("inner env var %s not found in command.\nResult: %s", k, result)
		}
	}
	if !strings.Contains(result, "claude --dangerously-skip-permissions") {
		t.Errorf("agent command lost after inner env injection.\nResult: %s", result)
	}
}

// TestIntegration_DaytonaMode_CommandStructure verifies the expected Daytona
// exec-wrapper command structure with template expansion:
//
//	exec env OUTER... daytona exec WS --tty -- env INNER... agent-cmd
func TestIntegration_DaytonaMode_CommandStructure(t *testing.T) {
	wrapper := []string{"daytona", "exec", "{{workspace}}", "--tty", "--"}
	ctx := config.WrapperContext{
		Rig:           "myrig",
		Polecat:       "mypolecat",
		InstallPrefix: "gt-dtn",
		WorkspaceName: "gt-dtn-myrig--mypolecat",
	}
	expanded := config.ExpandWrapper(wrapper, ctx)

	if len(expanded) != 5 {
		t.Fatalf("expected 5 wrapper args, got %d: %v", len(expanded), expanded)
	}
	expected := []string{"daytona", "exec", "gt-dtn-myrig--mypolecat", "--tty", "--"}
	for i, want := range expected {
		if expanded[i] != want {
			t.Errorf("wrapper[%d] = %q, want %q", i, expanded[i], want)
		}
	}
}

// TestIntegration_DaytonaMode_RollbackOnPreStartFailure verifies the rollback
// semantics: when sandbox.PreStart fails, no tmux session is created and
// PostStop is NOT called (since PreStart didn't succeed, there's nothing to
// clean up). This is complementary to the existing unit test
// TestSessionManager_Start_SandboxFailure.
func TestIntegration_DaytonaMode_RollbackOnPreStartFailure(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "dr", "daytona-rollback")

	rigName := "daytona-rollback"
	polecatName := fmt.Sprintf("rollback-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	sbx := newMockSandbox("gt-dtn")
	sbx.preStartErr = errors.New("workspace creation failed: disk full")
	sbx.preStartEnv = map[string]string{
		"GT_PROXY_URL":   "https://127.0.0.1:9876",
		"GT_CERT_SERIAL": "rollback-serial-42",
	}

	settings := &config.RigSettings{
		RemoteBackend: &config.RemoteBackendConfig{
			Image: "gastown:latest",
		},
	}

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r,
		WithSandbox(sbx),
		WithSettings(settings),
		WithInstallPrefix("gt-dtn"),
	)

	err := sm.Start(context.Background(), polecatName, SessionStartOptions{
		Command: "exec sleep 300",
		WorkDir: filepath.Join(root, "polecats", polecatName),
		Branch:  "feat/test",
	})
	if err == nil {
		sessionID := sm.SessionName(polecatName)
		_ = tm.KillSession(sessionID)
		t.Fatal("expected Start() to fail when PreStart fails")
	}

	// PreStart should have been called exactly once.
	if len(sbx.preStartCalls) != 1 {
		t.Fatalf("expected 1 PreStart call, got %d", len(sbx.preStartCalls))
	}

	// Verify the opts passed to PreStart.
	opts := sbx.preStartCalls[0]
	if opts.Rig != rigName {
		t.Errorf("PreStart opts.Rig = %q, want %q", opts.Rig, rigName)
	}
	if opts.Branch != "feat/test" {
		t.Errorf("PreStart opts.Branch = %q, want %q", opts.Branch, "feat/test")
	}
	expectedWs := "gt-dtn-daytona-rollback--" + polecatName
	if opts.WorkspaceName != expectedWs {
		t.Errorf("PreStart opts.WorkspaceName = %q, want %q", opts.WorkspaceName, expectedWs)
	}

	// PostStop should NOT be called when PreStart fails (nothing to roll back).
	if len(sbx.postStopCalls) != 0 {
		t.Errorf("expected 0 PostStop calls after PreStart failure, got %d", len(sbx.postStopCalls))
	}

	// No tmux session should exist.
	sessionID := sm.SessionName(polecatName)
	running, _ := tm.HasSession(sessionID)
	if running {
		_ = tm.KillSession(sessionID)
		t.Error("tmux session should not exist after PreStart failure")
	}
}

// TestIntegration_DaytonaMode_PostStopNonFatal verifies that Stop succeeds
// even when sandbox.PostStop returns an error (non-fatal behavior).
func TestIntegration_DaytonaMode_PostStopNonFatal(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "dn", "daytona-nonfatal")

	rigName := "daytona-nonfatal"
	polecatName := fmt.Sprintf("nonfatal-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	sbx := newMockSandbox("gt-dtn")
	sbx.postStopErr = errors.New("cert revocation timed out")

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r,
		WithSandbox(sbx),
		WithSettings(&config.RigSettings{
			RemoteBackend: &config.RemoteBackendConfig{},
		}),
	)

	sessionID := sm.SessionName(polecatName)
	if err := tm.NewSession(sessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(sessionID) })

	err := sm.Stop(context.Background(), polecatName, true)
	if err != nil {
		t.Fatalf("Stop() should succeed despite PostStop error, got: %v", err)
	}

	if len(sbx.postStopCalls) != 1 {
		t.Fatalf("expected 1 PostStop call, got %d", len(sbx.postStopCalls))
	}
}

// TestIntegration_DaytonaMode_WorkDirUsesMarker verifies that when a sandbox
// is configured, Start uses the polecatDir (marker directory) as the tmux cwd
// instead of the clone path.
func TestIntegration_DaytonaMode_WorkDirUsesMarker(t *testing.T) {
	rigName := "daytona-marker"
	polecatName := "marker"
	root := setupIntegrationRig(t, rigName, polecatName)

	sbx := newMockSandbox("gt-dtn")
	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	sm := NewSessionManager(tmux.NewTmux(), r, WithSandbox(sbx))

	polecatDir := sm.polecatDir(polecatName)
	expectedDir := filepath.Join(root, "polecats", polecatName)
	if polecatDir != expectedDir {
		t.Errorf("polecatDir = %q, want %q", polecatDir, expectedDir)
	}

	clonePath := sm.clonePath(polecatName)
	if polecatDir == clonePath {
		t.Error("sandbox mode: polecatDir should differ from clonePath")
	}
}

// TestIntegration_DaytonaMode_WorkspaceName verifies the deterministic
// workspace naming convention: <installPrefix>-<rig>--<polecat>.
func TestIntegration_DaytonaMode_WorkspaceName(t *testing.T) {
	sbx := newMockSandbox("gt-xyz")

	tests := []struct {
		rig     string
		polecat string
		want    string
	}{
		{"myrig", "toast", "gt-xyz-myrig--toast"},
		{"gastown", "quartz", "gt-xyz-gastown--quartz"},
		{"test-rig", "my-polecat", "gt-xyz-test-rig--my-polecat"},
	}

	for _, tc := range tests {
		got := sbx.WorkspaceName(tc.rig, tc.polecat)
		if got != tc.want {
			t.Errorf("WorkspaceName(%q, %q) = %q, want %q",
				tc.rig, tc.polecat, got, tc.want)
		}
	}
}

// TestIntegration_DaytonaMode_PreStartFailure verifies that when
// sandbox.PreStart fails, no tmux session is created and the error is
// properly wrapped.
func TestIntegration_DaytonaMode_PreStartFailure(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "df", "daytona-fail")

	rigName := "daytona-fail"
	polecatName := fmt.Sprintf("fail-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecatName)

	sbx := newMockSandbox("gt-dtn")
	sbx.preStartErr = errors.New("workspace quota exceeded")

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}
	sm := NewSessionManager(tmux.NewTmux(), r,
		WithSandbox(sbx),
		WithSettings(&config.RigSettings{
			RemoteBackend: &config.RemoteBackendConfig{
				Image: "gastown:latest",
			},
		}),
	)

	err := sm.Start(context.Background(), polecatName, SessionStartOptions{})
	if err == nil {
		t.Fatal("Start() should fail when PreStart returns error")
	}
	if !strings.Contains(err.Error(), "sandbox pre-start") {
		t.Errorf("error should mention 'sandbox pre-start', got: %v", err)
	}
	if !strings.Contains(err.Error(), "workspace quota exceeded") {
		t.Errorf("error should wrap original error, got: %v", err)
	}

	sessionID := sm.SessionName(polecatName)
	running, _ := tmux.NewTmux().HasSession(sessionID)
	if running {
		_ = tmux.NewTmux().KillSession(sessionID)
		t.Error("tmux session should not exist after PreStart failure")
	}
}

// ---------------------------------------------------------------------------
// Cross-mode comparison tests
// ---------------------------------------------------------------------------

// TestIntegration_ModeComparison_WorkDir verifies that local and Daytona modes
// use different working directory resolution strategies.
func TestIntegration_ModeComparison_WorkDir(t *testing.T) {
	rigName := "compare-rig"
	polecatName := "compare"
	root := setupIntegrationRig(t, rigName, polecatName)

	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecatName},
	}

	// Local mode: uses clonePath (polecats/<name>/<rigname>).
	localSM := NewSessionManager(tmux.NewTmux(), r)
	localClone := localSM.clonePath(polecatName)

	// Daytona mode: uses polecatDir (polecats/<name>).
	sbx := newMockSandbox("gt-dtn")
	daytonaSM := NewSessionManager(tmux.NewTmux(), r, WithSandbox(sbx))
	daytonaDir := daytonaSM.polecatDir(polecatName)

	if localClone == daytonaDir {
		t.Error("local clonePath and daytona polecatDir should differ")
	}
	if !strings.HasSuffix(localClone, "/"+rigName) {
		t.Errorf("local clonePath should end with /%s, got %q", rigName, localClone)
	}
	if strings.HasSuffix(daytonaDir, "/"+rigName) {
		t.Errorf("daytona polecatDir should NOT end with /%s, got %q", rigName, daytonaDir)
	}
}

// TestIntegration_ModeComparison_SandboxHooks verifies that sandbox hooks are
// only called when a sandbox is configured (Daytona mode), not in local or
// exitbox modes.
func TestIntegration_ModeComparison_SandboxHooks(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "mc", "mode-compare")

	rigName := "mode-compare"
	tm := tmux.NewTmux()

	// Local polecat — no sandbox.
	localName := fmt.Sprintf("local-pc-%d", testSessionCounter.Add(1))
	localRoot := setupIntegrationRig(t, rigName, localName)
	localR := &rig.Rig{Name: rigName, Path: localRoot, Polecats: []string{localName}}
	localSM := NewSessionManager(tm, localR)

	localSessionID := localSM.SessionName(localName)
	if err := tm.NewSession(localSessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession (local): %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(localSessionID) })

	if err := localSM.Stop(context.Background(), localName, true); err != nil {
		t.Fatalf("local Stop: %v", err)
	}

	// Daytona polecat — with sandbox.
	sbx := newMockSandbox("gt-dtn")
	daytonaName := fmt.Sprintf("daytona-pc-%d", testSessionCounter.Add(1))
	daytonaRoot := setupIntegrationRig(t, rigName, daytonaName)
	daytonaR := &rig.Rig{Name: rigName, Path: daytonaRoot, Polecats: []string{daytonaName}}
	daytonaSM := NewSessionManager(tm, daytonaR,
		WithSandbox(sbx),
		WithSettings(&config.RigSettings{RemoteBackend: &config.RemoteBackendConfig{}}),
	)

	daytonaSessionID := daytonaSM.SessionName(daytonaName)
	if err := tm.NewSession(daytonaSessionID, os.TempDir()); err != nil {
		t.Fatalf("NewSession (daytona): %v", err)
	}
	t.Cleanup(func() { _ = tm.KillSession(daytonaSessionID) })

	if err := daytonaSM.Stop(context.Background(), daytonaName, true); err != nil {
		t.Fatalf("daytona Stop: %v", err)
	}

	// Local: no sandbox to call.
	if localSM.sandbox != nil {
		t.Error("local mode should have nil sandbox")
	}

	// Daytona: PostStop was called.
	if len(sbx.postStopCalls) != 1 {
		t.Errorf("daytona mode: expected 1 PostStop call, got %d", len(sbx.postStopCalls))
	}
}

// TestIntegration_DaytonaMode_StopAllWithSandbox verifies that StopAll calls
// PostStop for each polecat session when a sandbox is configured.
func TestIntegration_DaytonaMode_StopAllWithSandbox(t *testing.T) {
	requireTmuxIntegration(t)
	setupIntegrationRegistry(t, "sa", "stopall-rig")

	rigName := "stopall-rig"
	polecat1 := fmt.Sprintf("stop1-%d", testSessionCounter.Add(1))
	polecat2 := fmt.Sprintf("stop2-%d", testSessionCounter.Add(1))
	root := setupIntegrationRig(t, rigName, polecat1, polecat2)

	sbx := newMockSandbox("gt-dtn")
	r := &rig.Rig{
		Name:     rigName,
		Path:     root,
		Polecats: []string{polecat1, polecat2},
	}
	tm := tmux.NewTmux()
	sm := NewSessionManager(tm, r,
		WithSandbox(sbx),
		WithSettings(&config.RigSettings{RemoteBackend: &config.RemoteBackendConfig{}}),
	)

	for _, name := range []string{polecat1, polecat2} {
		sessionID := sm.SessionName(name)
		if err := tm.NewSession(sessionID, os.TempDir()); err != nil {
			t.Fatalf("NewSession(%s): %v", name, err)
		}
		t.Cleanup(func() { _ = tm.KillSession(sessionID) })
	}

	if err := sm.StopAll(context.Background(), true); err != nil {
		t.Fatalf("StopAll() error: %v", err)
	}

	for _, name := range []string{polecat1, polecat2} {
		sessionID := sm.SessionName(name)
		running, _ := tm.HasSession(sessionID)
		if running {
			t.Errorf("session %s should have been killed by StopAll", sessionID)
		}
	}

	if len(sbx.postStopCalls) != 2 {
		t.Errorf("expected 2 PostStop calls, got %d", len(sbx.postStopCalls))
	}
}
