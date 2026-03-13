package patrol

import (
	"testing"
	"time"
)

func TestDoctorDogPatrol_Interface(t *testing.T) {
	var h Handler = &DoctorDogPatrol{}
	if h.Name() != "doctor_dog" {
		t.Errorf("Name() = %q, want %q", h.Name(), "doctor_dog")
	}
	if h.DefaultInterval() != 5*time.Minute {
		t.Errorf("DefaultInterval() = %v, want %v", h.DefaultInterval(), 5*time.Minute)
	}
	if h.RequiresRig() {
		t.Error("RequiresRig() = true, want false")
	}
}

func TestDoctorDogPatrol_DefaultValues(t *testing.T) {
	p := &DoctorDogPatrol{}
	if p.DoltPort != 0 {
		t.Errorf("DoltPort = %d, want 0 (uses default in Run)", p.DoltPort)
	}
	if p.LatencyAlertMs != 0 {
		t.Errorf("LatencyAlertMs = %f, want 0 (uses default in Run)", p.LatencyAlertMs)
	}
}
