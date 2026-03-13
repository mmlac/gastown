package patrol

import (
	"errors"
	"testing"
	"time"
)

func TestCompactorDogPatrol_Interface(t *testing.T) {
	var h Handler = &CompactorDogPatrol{}
	if h.Name() != "compactor_dog" {
		t.Errorf("Name() = %q, want %q", h.Name(), "compactor_dog")
	}
	if h.DefaultInterval() != 24*time.Hour {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 24*time.Hour)
	}
	if h.RequiresRig() {
		t.Error("RequiresRig() = true, want false")
	}
}

func TestCompactorDogPatrol_RunSkipsNoDatabases(t *testing.T) {
	p := &CompactorDogPatrol{
		Databases: []string{},
	}
	env := testEnv(t)

	err := p.Run(testCtx(), env)
	if err != nil {
		t.Errorf("Run() error = %v, want nil for no databases", err)
	}
}

func TestIsConcurrentWriteError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("connection refused"), false},
		{"rebase execution", errors.New("rebase execution failed"), true},
		{"concurrency abort", errors.New("concurrency abort: main HEAD moved"), true},
		{"graph change", errors.New("commit graph changed"), true},
		{"cannot rebase", errors.New("cannot rebase: branch modified"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConcurrentWriteError(tt.err); got != tt.want {
				t.Errorf("isConcurrentWriteError() = %v, want %v", got, tt.want)
			}
		})
	}
}
