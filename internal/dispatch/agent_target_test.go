package dispatch

import (
	"context"
	"fmt"
	"testing"
)

func TestExistingAgentTarget_AgentID(t *testing.T) {
	at := NewExistingAgentTarget("GasTown/polecats/quartz")
	// Before Prepare, AgentID falls back to target.
	if got := at.AgentID(); got != "GasTown/polecats/quartz" {
		t.Errorf("AgentID() = %q, want %q", got, "GasTown/polecats/quartz")
	}
}

func TestExistingAgentTarget_TargetType(t *testing.T) {
	at := NewExistingAgentTarget("mayor")
	if got := at.TargetType(); got != "agent" {
		t.Errorf("TargetType() = %q, want %q", got, "agent")
	}
}

func TestExistingAgentTarget_WorkDir_BeforePrepare(t *testing.T) {
	at := NewExistingAgentTarget("deacon/boot")
	if got := at.WorkDir(); got != "" {
		t.Errorf("WorkDir() before Prepare = %q, want empty", got)
	}
}

func TestExistingAgentTarget_ImplementsInterface(t *testing.T) {
	var _ DispatchTarget = (*ExistingAgentTarget)(nil)
}

func TestExistingAgentTarget_StartSession_RequiresPrepare(t *testing.T) {
	at := NewExistingAgentTarget("mayor")
	_, err := at.StartSession(context.Background(), StartOpts{})
	if err == nil {
		t.Error("StartSession without Prepare should return error")
	}
}

func TestExistingAgentTarget_Rollback_IsNoop(t *testing.T) {
	at := NewExistingAgentTarget("mayor")
	if err := at.Rollback(context.Background()); err != nil {
		t.Errorf("Rollback should be no-op, got: %v", err)
	}
}

func TestExistingAgentTarget_IsSessionRunning_BeforePrepare(t *testing.T) {
	at := NewExistingAgentTarget("mayor")
	running, err := at.IsSessionRunning(context.Background())
	if err != nil {
		t.Errorf("IsSessionRunning before Prepare should not error, got: %v", err)
	}
	if running {
		t.Error("IsSessionRunning before Prepare should return false")
	}
}

func TestExistingAgentTarget_Prepare_WithMockResolver(t *testing.T) {
	// Inject a mock resolver that simulates resolution failure.
	original := resolveRoleToSessionFn
	defer func() { resolveRoleToSessionFn = original }()

	resolveRoleToSessionFn = func(role string) (string, error) {
		return "", fmt.Errorf("session not found for %s", role)
	}

	at := NewExistingAgentTarget("nonexistent/agent")
	err := at.Prepare(context.Background())
	if err == nil {
		t.Error("Prepare with non-existent agent should return error")
	}
}

func TestSetResolveRoleToSession(t *testing.T) {
	original := resolveRoleToSessionFn
	defer func() { resolveRoleToSessionFn = original }()

	called := false
	SetResolveRoleToSession(func(role string) (string, error) {
		called = true
		return "test-session", nil
	})

	_, err := resolveRoleToSessionFn("test")
	if err != nil {
		t.Errorf("injected resolver should not error, got: %v", err)
	}
	if !called {
		t.Error("injected resolver was not called")
	}
}

func TestSessionToAgentIDLocal(t *testing.T) {
	tests := []struct {
		name        string
		sessionName string
		wantNonEmpty bool
	}{
		{"unparseable falls back", "random-session", true},
		{"hq prefix stripped", "hq-deacon", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionToAgentIDLocal(tt.sessionName)
			if tt.wantNonEmpty && got == "" {
				t.Error("expected non-empty agent ID")
			}
		})
	}
}
