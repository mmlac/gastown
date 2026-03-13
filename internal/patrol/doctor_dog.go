package patrol

import (
	"context"
	"time"
)

const defaultDoctorDogInterval = 5 * time.Minute

// DoctorDogPatrol is a comprehensive Dolt health monitor: TCP connectivity,
// query latency, database count, gc status, zombie detection, backup
// staleness, and disk usage checks.
type DoctorDogPatrol struct {
	// RunFn is the implementation injected by the daemon.
	RunFn func(ctx context.Context, env Env) error
}

func (p *DoctorDogPatrol) Name() string               { return "doctor_dog" }
func (p *DoctorDogPatrol) DefaultInterval() time.Duration { return defaultDoctorDogInterval }
func (p *DoctorDogPatrol) RequiresRig() bool            { return false }

func (p *DoctorDogPatrol) Run(ctx context.Context, env Env) error {
	if p.RunFn != nil {
		return p.RunFn(ctx, env)
	}
	return nil
}
