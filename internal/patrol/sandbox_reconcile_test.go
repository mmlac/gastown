package patrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/sandbox"
)

func TestSandboxReconcilePatrol_Interface(t *testing.T) {
	var h Handler = &SandboxReconcilePatrol{}
	if h.Name() != "sandbox_reconcile" {
		t.Errorf("Name() = %q, want %q", h.Name(), "sandbox_reconcile")
	}
	if h.DefaultInterval() != 30*time.Minute {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 30*time.Minute)
	}
	if !h.RequiresRig() {
		t.Error("RequiresRig() = false, want true")
	}
}

func TestSandboxReconcilePatrol_RunSkipsNilSandbox(t *testing.T) {
	p := &SandboxReconcilePatrol{}
	env := testEnv(t)
	env.RigName = "testrig"
	env.Sandbox = nil

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil for nil sandbox", err)
	}
}

func TestSandboxReconcilePatrol_RunCallsReconcile(t *testing.T) {
	p := &SandboxReconcilePatrol{}
	mock := &mockSandbox{}
	env := testEnv(t)
	env.RigName = "testrig"
	env.Sandbox = mock

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil", err)
	}
	if !mock.reconcileCalled {
		t.Error("expected Reconcile to be called")
	}
	if mock.lastOpts.Rig != "testrig" {
		t.Errorf("Reconcile called with rig %q, want %q", mock.lastOpts.Rig, "testrig")
	}
}

func TestSandboxReconcilePatrol_RunPropagatesError(t *testing.T) {
	p := &SandboxReconcilePatrol{}
	expectedErr := errors.New("reconcile failed")
	mock := &mockSandbox{err: expectedErr}
	env := testEnv(t)
	env.RigName = "testrig"
	env.Sandbox = mock

	err := p.Run(testCtx(), env)
	if !errors.Is(err, expectedErr) {
		t.Errorf("Run() error = %v, want %v", err, expectedErr)
	}
}

// mockSandbox implements sandbox.Lifecycle for testing.
type mockSandbox struct {
	reconcileCalled bool
	lastOpts        sandbox.ReconcileOpts
	err             error
}

func (m *mockSandbox) PreStart(_ context.Context, _ sandbox.SandboxOpts) (map[string]string, error) {
	return nil, nil
}

func (m *mockSandbox) PostStop(_ context.Context, _ sandbox.SandboxOpts) error {
	return nil
}

func (m *mockSandbox) Reconcile(_ context.Context, opts sandbox.ReconcileOpts) error {
	m.reconcileCalled = true
	m.lastOpts = opts
	return m.err
}

func (m *mockSandbox) WorkspaceName(_, _ string) string {
	return ""
}
