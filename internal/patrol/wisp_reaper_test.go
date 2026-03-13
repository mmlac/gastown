package patrol

import (
	"testing"
	"time"
)

func TestWispReaperPatrol_Interface(t *testing.T) {
	var h Handler = &WispReaperPatrol{}
	if h.Name() != "wisp_reaper" {
		t.Errorf("Name() = %q, want %q", h.Name(), "wisp_reaper")
	}
	if h.DefaultInterval() != 1*time.Hour {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 1*time.Hour)
	}
	if h.RequiresRig() {
		t.Error("RequiresRig() = true, want false")
	}
}

func TestWispReaperPatrol_DefaultValues(t *testing.T) {
	p := &WispReaperPatrol{}
	if p.MaxAge != 0 {
		t.Errorf("MaxAge = %v, want 0 (uses default in Run)", p.MaxAge)
	}
	if p.DeleteAge != 0 {
		t.Errorf("DeleteAge = %v, want 0 (uses default in Run)", p.DeleteAge)
	}
	if p.DoltPort != 0 {
		t.Errorf("DoltPort = %d, want 0 (uses default in Run)", p.DoltPort)
	}
}
