package dispatch

import (
	"context"
	"testing"
)

func TestDogTarget_AgentID(t *testing.T) {
	tests := []struct {
		name    string
		dogName string
		want    string
	}{
		{"specific dog", "alpha", "deacon/dogs/alpha"},
		{"pool dispatch", "", "deacon/dogs/<pool>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dt := NewDogTarget(DogTargetConfig{DogName: tt.dogName})
			if got := dt.AgentID(); got != tt.want {
				t.Errorf("AgentID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDogTarget_TargetType(t *testing.T) {
	dt := NewDogTarget(DogTargetConfig{DogName: "alpha"})
	if got := dt.TargetType(); got != "dog" {
		t.Errorf("TargetType() = %q, want %q", got, "dog")
	}
}

func TestDogTarget_WorkDir(t *testing.T) {
	dt := NewDogTarget(DogTargetConfig{
		DogName:  "bravo",
		TownRoot: "/tmp/town",
	})
	want := "/tmp/town/deacon/dogs/bravo"
	if got := dt.WorkDir(); got != want {
		t.Errorf("WorkDir() = %q, want %q", got, want)
	}
}

func TestDogTarget_WorkDir_Empty(t *testing.T) {
	dt := NewDogTarget(DogTargetConfig{})
	if got := dt.WorkDir(); got != "" {
		t.Errorf("WorkDir() = %q, want empty", got)
	}
}

func TestDogTarget_ImplementsInterface(t *testing.T) {
	var _ DispatchTarget = (*DogTarget)(nil)
}

func TestDogTarget_StartSession_RequiresPrepare(t *testing.T) {
	dt := NewDogTarget(DogTargetConfig{DogName: "alpha"})
	_, err := dt.StartSession(context.Background(), StartOpts{})
	if err == nil {
		t.Error("StartSession without Prepare should return error")
	}
}

func TestDogTarget_Rollback_NilSafe(t *testing.T) {
	dt := NewDogTarget(DogTargetConfig{DogName: "alpha"})
	// Rollback before Prepare should be safe (no-op).
	if err := dt.Rollback(context.Background()); err != nil {
		t.Errorf("Rollback before Prepare should not error, got: %v", err)
	}
}

func TestDogTarget_IsSessionRunning_BeforePrepare(t *testing.T) {
	dt := NewDogTarget(DogTargetConfig{DogName: "alpha"})
	running, err := dt.IsSessionRunning(context.Background())
	if err != nil {
		t.Errorf("IsSessionRunning before Prepare should not error, got: %v", err)
	}
	if running {
		t.Error("IsSessionRunning before Prepare should return false")
	}
}

func TestNewDogTarget_Config(t *testing.T) {
	cfg := DogTargetConfig{
		DogName:       "charlie",
		TownRoot:      "/gt",
		WorkDesc:      "gtd-abc",
		Create:        true,
		AgentOverride: "codex",
	}
	dt := NewDogTarget(cfg)

	if dt.dogName != "charlie" {
		t.Errorf("dogName = %q, want %q", dt.dogName, "charlie")
	}
	if dt.townRoot != "/gt" {
		t.Errorf("townRoot = %q, want %q", dt.townRoot, "/gt")
	}
	if dt.workDesc != "gtd-abc" {
		t.Errorf("workDesc = %q, want %q", dt.workDesc, "gtd-abc")
	}
	if !dt.create {
		t.Error("create should be true")
	}
	if dt.agentOver != "codex" {
		t.Errorf("agentOver = %q, want %q", dt.agentOver, "codex")
	}
}
