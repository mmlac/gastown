package patrol

import (
	"context"
	"time"
)

const defaultDoctorDogInterval = 5 * time.Minute

// DoctorDogPatrol is a comprehensive Dolt health monitor: TCP connectivity,
// query latency, database count, gc status, zombie detection, backup
// staleness, and disk usage checks.
// The actual health check logic lives in the daemon; this handler provides
// metadata (name, interval, rig scope) for the patrol registry.
type DoctorDogPatrol struct{}

func (p *DoctorDogPatrol) Name() string                       { return "doctor_dog" }
func (p *DoctorDogPatrol) DefaultInterval() time.Duration     { return defaultDoctorDogInterval }
func (p *DoctorDogPatrol) RequiresRig() bool                  { return false }
func (p *DoctorDogPatrol) Run(_ context.Context, _ Env) error { return nil }
