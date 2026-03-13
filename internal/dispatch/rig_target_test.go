package dispatch

import (
	"context"
	"errors"
	"testing"
)

func testSpawnResult() *SpawnResult {
	return &SpawnResult{
		RigName:     "gastown",
		PolecatName: "Toast",
		ClonePath:   "/gt/gastown/polecats/Toast/gastown",
		SessionName: "gt-gastown-p-Toast",
		BaseBranch:  "main",
		Branch:      "polecat/Toast-abc123",
	}
}

func successSpawn(result *SpawnResult) SpawnFunc {
	return func(ctx context.Context) (*SpawnResult, error) {
		return result, nil
	}
}

func failSpawn(err error) SpawnFunc {
	return func(ctx context.Context) (*SpawnResult, error) {
		return nil, err
	}
}

func successStart(pane string) SessionStartFunc {
	return func(ctx context.Context, spawn *SpawnResult, opts StartOpts) (string, error) {
		return pane, nil
	}
}

func failStart(err error) SessionStartFunc {
	return func(ctx context.Context, spawn *SpawnResult, opts StartOpts) (string, error) {
		return "", err
	}
}

func noopRollback() RollbackFunc {
	return func(ctx context.Context, spawn *SpawnResult) error {
		return nil
	}
}

func sessionRunning(running bool) SessionCheckFunc {
	return func(ctx context.Context, spawn *SpawnResult) (bool, error) {
		return running, nil
	}
}

func TestRigTarget_TargetType(t *testing.T) {
	rt := NewRigTarget("gastown", nil, nil, nil, nil)
	if got := rt.TargetType(); got != "rig" {
		t.Errorf("TargetType() = %q, want %q", got, "rig")
	}
}

func TestRigTarget_BeforePrepare(t *testing.T) {
	rt := NewRigTarget("gastown", nil, nil, nil, nil)

	if got := rt.AgentID(); got != "" {
		t.Errorf("AgentID() before Prepare = %q, want empty", got)
	}
	if got := rt.WorkDir(); got != "" {
		t.Errorf("WorkDir() before Prepare = %q, want empty", got)
	}

	running, err := rt.IsSessionRunning(context.Background())
	if err != nil {
		t.Fatalf("IsSessionRunning() before Prepare err = %v", err)
	}
	if running {
		t.Error("IsSessionRunning() before Prepare = true, want false")
	}
}

func TestRigTarget_PrepareSuccess(t *testing.T) {
	result := testSpawnResult()
	rt := NewRigTarget("gastown", successSpawn(result), nil, nil, nil)

	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	if got := rt.AgentID(); got != "gastown/polecats/Toast" {
		t.Errorf("AgentID() = %q, want %q", got, "gastown/polecats/Toast")
	}
	if got := rt.WorkDir(); got != result.ClonePath {
		t.Errorf("WorkDir() = %q, want %q", got, result.ClonePath)
	}
}

func TestRigTarget_PrepareFailure(t *testing.T) {
	spawnErr := errors.New("rig not found")
	rt := NewRigTarget("badrig", failSpawn(spawnErr), nil, nil, nil)

	err := rt.Prepare(context.Background())
	if err == nil {
		t.Fatal("Prepare() expected error, got nil")
	}
	if !errors.Is(err, spawnErr) {
		t.Errorf("Prepare() err = %v, want %v", err, spawnErr)
	}

	// AgentID and WorkDir should still be empty after failed Prepare
	if got := rt.AgentID(); got != "" {
		t.Errorf("AgentID() after failed Prepare = %q, want empty", got)
	}
}

func TestRigTarget_PrepareCalledTwice(t *testing.T) {
	result := testSpawnResult()
	rt := NewRigTarget("gastown", successSpawn(result), nil, nil, nil)

	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("first Prepare() err = %v", err)
	}
	if err := rt.Prepare(context.Background()); err == nil {
		t.Fatal("second Prepare() expected error, got nil")
	}
}

func TestRigTarget_StartSessionSuccess(t *testing.T) {
	result := testSpawnResult()
	rt := NewRigTarget("gastown",
		successSpawn(result),
		successStart("%1"),
		nil,
		nil,
	)

	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	pane, err := rt.StartSession(context.Background(), StartOpts{})
	if err != nil {
		t.Fatalf("StartSession() err = %v", err)
	}
	if pane != "%1" {
		t.Errorf("StartSession() pane = %q, want %%1", pane)
	}
}

func TestRigTarget_StartSessionWithoutPrepare(t *testing.T) {
	rt := NewRigTarget("gastown", nil, nil, nil, nil)

	_, err := rt.StartSession(context.Background(), StartOpts{})
	if err == nil {
		t.Fatal("StartSession() without Prepare expected error, got nil")
	}
}

func TestRigTarget_StartSessionFailure(t *testing.T) {
	result := testSpawnResult()
	startErr := errors.New("tmux session creation failed")
	rt := NewRigTarget("gastown",
		successSpawn(result),
		failStart(startErr),
		nil,
		nil,
	)

	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	_, err := rt.StartSession(context.Background(), StartOpts{})
	if !errors.Is(err, startErr) {
		t.Errorf("StartSession() err = %v, want %v", err, startErr)
	}
}

func TestRigTarget_StartSessionPassesOpts(t *testing.T) {
	result := testSpawnResult()
	var capturedOpts StartOpts
	startFn := func(ctx context.Context, spawn *SpawnResult, opts StartOpts) (string, error) {
		capturedOpts = opts
		return "%1", nil
	}

	rt := NewRigTarget("gastown", successSpawn(result), startFn, nil, nil)
	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	opts := StartOpts{
		AgentCommand: "claude",
		AgentArgs:    []string{"--prompt", "hello"},
		FormulaEnv:   map[string]string{"GT_RIG": "gastown"},
	}
	_, err := rt.StartSession(context.Background(), opts)
	if err != nil {
		t.Fatalf("StartSession() err = %v", err)
	}

	if capturedOpts.AgentCommand != "claude" {
		t.Errorf("captured AgentCommand = %q, want %q", capturedOpts.AgentCommand, "claude")
	}
	if len(capturedOpts.AgentArgs) != 2 {
		t.Errorf("captured AgentArgs len = %d, want 2", len(capturedOpts.AgentArgs))
	}
	if capturedOpts.FormulaEnv["GT_RIG"] != "gastown" {
		t.Errorf("captured FormulaEnv[GT_RIG] = %q, want %q", capturedOpts.FormulaEnv["GT_RIG"], "gastown")
	}
}

func TestRigTarget_RollbackBeforePrepare(t *testing.T) {
	rt := NewRigTarget("gastown", nil, nil, noopRollback(), nil)

	// Rollback before Prepare should be a no-op
	if err := rt.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback() before Prepare err = %v", err)
	}
}

func TestRigTarget_RollbackAfterPrepare(t *testing.T) {
	result := testSpawnResult()
	var rolledBack bool
	rollbackFn := func(ctx context.Context, spawn *SpawnResult) error {
		rolledBack = true
		if spawn.PolecatName != "Toast" {
			return errors.New("unexpected polecat name")
		}
		return nil
	}

	rt := NewRigTarget("gastown", successSpawn(result), nil, rollbackFn, nil)
	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	if err := rt.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback() err = %v", err)
	}
	if !rolledBack {
		t.Error("rollback function was not called")
	}
}

func TestRigTarget_IsSessionRunning(t *testing.T) {
	result := testSpawnResult()
	rt := NewRigTarget("gastown",
		successSpawn(result),
		nil,
		nil,
		sessionRunning(true),
	)

	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	running, err := rt.IsSessionRunning(context.Background())
	if err != nil {
		t.Fatalf("IsSessionRunning() err = %v", err)
	}
	if !running {
		t.Error("IsSessionRunning() = false, want true")
	}
}

func TestRigTarget_IsSessionNotRunning(t *testing.T) {
	result := testSpawnResult()
	rt := NewRigTarget("gastown",
		successSpawn(result),
		nil,
		nil,
		sessionRunning(false),
	)

	if err := rt.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	running, err := rt.IsSessionRunning(context.Background())
	if err != nil {
		t.Fatalf("IsSessionRunning() err = %v", err)
	}
	if running {
		t.Error("IsSessionRunning() = true, want false")
	}
}

func TestRigTarget_ImplementsDispatchTarget(t *testing.T) {
	// Compile-time check that RigTarget implements DispatchTarget
	var _ DispatchTarget = (*RigTarget)(nil)
}

func TestRigTarget_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	spawnFn := func(ctx context.Context) (*SpawnResult, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return testSpawnResult(), nil
	}

	rt := NewRigTarget("gastown", spawnFn, nil, nil, nil)
	err := rt.Prepare(ctx)
	if err == nil {
		t.Fatal("Prepare() with cancelled context expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Prepare() err = %v, want context.Canceled", err)
	}
}

func TestRigTarget_FullLifecycle(t *testing.T) {
	result := testSpawnResult()
	var lifecycle []string

	spawnFn := func(ctx context.Context) (*SpawnResult, error) {
		lifecycle = append(lifecycle, "spawn")
		return result, nil
	}
	startFn := func(ctx context.Context, spawn *SpawnResult, opts StartOpts) (string, error) {
		lifecycle = append(lifecycle, "start")
		return "%1", nil
	}
	rollbackFn := func(ctx context.Context, spawn *SpawnResult) error {
		lifecycle = append(lifecycle, "rollback")
		return nil
	}
	checkFn := func(ctx context.Context, spawn *SpawnResult) (bool, error) {
		lifecycle = append(lifecycle, "check")
		return true, nil
	}

	rt := NewRigTarget("gastown", spawnFn, startFn, rollbackFn, checkFn)
	ctx := context.Background()

	// Prepare
	if err := rt.Prepare(ctx); err != nil {
		t.Fatalf("Prepare() err = %v", err)
	}

	// Start session
	pane, err := rt.StartSession(ctx, StartOpts{AgentCommand: "claude"})
	if err != nil {
		t.Fatalf("StartSession() err = %v", err)
	}
	if pane != "%1" {
		t.Errorf("pane = %q, want %%1", pane)
	}

	// Check session
	running, err := rt.IsSessionRunning(ctx)
	if err != nil {
		t.Fatalf("IsSessionRunning() err = %v", err)
	}
	if !running {
		t.Error("IsSessionRunning() = false, want true")
	}

	// Verify lifecycle order
	expected := []string{"spawn", "start", "check"}
	if len(lifecycle) != len(expected) {
		t.Fatalf("lifecycle = %v, want %v", lifecycle, expected)
	}
	for i, got := range lifecycle {
		if got != expected[i] {
			t.Errorf("lifecycle[%d] = %q, want %q", i, got, expected[i])
		}
	}
}
