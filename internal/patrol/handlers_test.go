package patrol

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/sandbox"
)

// TestHandlerMetadata verifies Name, DefaultInterval, and RequiresRig for all handlers.
func TestHandlerMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		handler         Handler
		wantName        string
		wantInterval    time.Duration
		wantRequiresRig bool
	}{
		{&DoltRemotesPatrol{}, "dolt_remotes", 15 * time.Minute, false},
		{&DoltBackupPatrol{}, "dolt_backup", 15 * time.Minute, false},
		{&JsonlGitBackupPatrol{}, "jsonl_git_backup", 15 * time.Minute, false},
		{&WispReaperPatrol{}, "wisp_reaper", 1 * time.Hour, false},
		{&DoctorDogPatrol{}, "doctor_dog", 5 * time.Minute, false},
		{&CompactorDogPatrol{}, "compactor_dog", 24 * time.Hour, false},
		{&ScheduledMaintenancePatrol{}, "scheduled_maintenance", 5 * time.Minute, false},
		{&SandboxReconcilePatrol{}, "sandbox_reconcile", 30 * time.Minute, true},
	}

	for _, tt := range tests {
		t.Run(tt.wantName, func(t *testing.T) {
			if got := tt.handler.Name(); got != tt.wantName {
				t.Errorf("Name() = %q, want %q", got, tt.wantName)
			}
			if got := tt.handler.DefaultInterval(); got != tt.wantInterval {
				t.Errorf("DefaultInterval() = %v, want %v", got, tt.wantInterval)
			}
			if got := tt.handler.RequiresRig(); got != tt.wantRequiresRig {
				t.Errorf("RequiresRig() = %v, want %v", got, tt.wantRequiresRig)
			}
		})
	}
}

// TestMetadataOnlyHandlersReturnNil verifies that metadata-only handlers
// (those whose Run logic lives in the daemon) return nil from Run().
func TestMetadataOnlyHandlersReturnNil(t *testing.T) {
	t.Parallel()

	env := Env{TownRoot: "/tmp/test", Logger: slog.Default()}
	ctx := context.Background()

	handlers := []Handler{
		&DoltRemotesPatrol{},
		&DoltBackupPatrol{},
		&JsonlGitBackupPatrol{},
		&WispReaperPatrol{},
		&DoctorDogPatrol{},
		&CompactorDogPatrol{},
		&ScheduledMaintenancePatrol{},
	}

	for _, h := range handlers {
		t.Run(h.Name(), func(t *testing.T) {
			if err := h.Run(ctx, env); err != nil {
				t.Errorf("Run() returned error: %v", err)
			}
		})
	}
}

// mockSandbox implements sandbox.Lifecycle for testing SandboxReconcilePatrol.
type mockSandbox struct {
	reconcileCalled bool
	reconcileOpts   sandbox.ReconcileOpts
	reconcileErr    error
}

func (m *mockSandbox) PreStart(_ context.Context, _ sandbox.SandboxOpts) (map[string]string, error) {
	return nil, nil
}
func (m *mockSandbox) PostStop(_ context.Context, _ sandbox.SandboxOpts) error { return nil }
func (m *mockSandbox) Reconcile(_ context.Context, opts sandbox.ReconcileOpts) error {
	m.reconcileCalled = true
	m.reconcileOpts = opts
	return m.reconcileErr
}
func (m *mockSandbox) WorkspaceName(_, _ string) string { return "" }

func TestSandboxReconcilePatrol_CallsReconcile(t *testing.T) {
	t.Parallel()

	mock := &mockSandbox{}
	p := &SandboxReconcilePatrol{InstallPrefix: "gt-abc123"}

	env := Env{
		TownRoot: "/tmp/test",
		RigName:  "testrig",
		Sandbox:  mock,
		Logger:   slog.Default(),
	}

	if err := p.Run(context.Background(), env); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !mock.reconcileCalled {
		t.Error("Reconcile was not called")
	}
	if mock.reconcileOpts.Rig != "testrig" {
		t.Errorf("Reconcile rig = %q, want %q", mock.reconcileOpts.Rig, "testrig")
	}
	if mock.reconcileOpts.InstallPrefix != "gt-abc123" {
		t.Errorf("Reconcile prefix = %q, want %q", mock.reconcileOpts.InstallPrefix, "gt-abc123")
	}
}

func TestSandboxReconcilePatrol_NilSandbox(t *testing.T) {
	t.Parallel()

	p := &SandboxReconcilePatrol{}
	env := Env{TownRoot: "/tmp/test", RigName: "testrig"}

	if err := p.Run(context.Background(), env); err != nil {
		t.Errorf("Run() with nil Sandbox returned error: %v", err)
	}
}

func TestSandboxReconcilePatrol_ErrorPropagation(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("reconcile failed")
	mock := &mockSandbox{reconcileErr: wantErr}
	p := &SandboxReconcilePatrol{}

	env := Env{
		TownRoot: "/tmp/test",
		RigName:  "testrig",
		Sandbox:  mock,
	}

	if err := p.Run(context.Background(), env); !errors.Is(err, wantErr) {
		t.Errorf("Run() error = %v, want %v", err, wantErr)
	}
}

func TestDefaultRegistryContainsAllHandlers(t *testing.T) {
	t.Parallel()

	r := DefaultRegistry()
	expectedNames := []string{
		"deacon",
		"witness",
		"refinery",
		"handler",
		"dolt_remotes",
		"dolt_backup",
		"jsonl_git_backup",
		"wisp_reaper",
		"doctor_dog",
		"compactor_dog",
		"scheduled_maintenance",
		"sandbox_reconcile",
	}

	for _, name := range expectedNames {
		_, _, ok := r.Get(name)
		if !ok {
			t.Errorf("DefaultRegistry missing patrol %q", name)
		}
	}

	// Session lifecycle patrols (deacon, witness, refinery, handler) should be enabled.
	// Opt-in patrols should be disabled.
	for _, name := range []string{"deacon", "witness", "refinery", "handler"} {
		if !r.IsEnabled(name) {
			t.Errorf("DefaultRegistry: %q should be enabled by default", name)
		}
	}
	for _, name := range []string{"dolt_remotes", "dolt_backup", "jsonl_git_backup",
		"wisp_reaper", "doctor_dog", "compactor_dog", "scheduled_maintenance", "sandbox_reconcile"} {
		if r.IsEnabled(name) {
			t.Errorf("DefaultRegistry: %q should be disabled by default", name)
		}
	}
}

func TestDefaultRegistryRunEnabled(t *testing.T) {
	t.Parallel()

	r := DefaultRegistry()
	// With all disabled, RunEnabled should be a no-op (no panics).
	env := Env{TownRoot: "/tmp/test", Logger: slog.Default()}
	r.RunEnabled(context.Background(), env)
}
