package daemon

import (
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/patrol"
)

// parseDurationOr parses a duration string, returning fallback on error or empty.
func parseDurationOr(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// configurePatrolRegistry builds a patrol.Registry from the DaemonPatrolConfig.
// It starts with DefaultRegistry() (lifecycle patrols enabled, opt-in disabled)
// and overlays configuration from daemon.json.
func configurePatrolRegistry(cfg *DaemonPatrolConfig) *patrol.Registry {
	r := patrol.DefaultRegistry()

	if cfg == nil || cfg.Patrols == nil {
		return r
	}
	p := cfg.Patrols

	// Session lifecycle patrols — re-register with config overrides.
	if p.Deacon != nil {
		r.Register(patrol.NewSessionLifecyclePatrol(constants.RoleDeacon, 0), &patrol.Config{
			Enabled: p.Deacon.Enabled,
			Rigs:    p.Deacon.Rigs,
		})
	}
	if p.Witness != nil {
		r.Register(patrol.NewSessionLifecyclePatrol(constants.RoleWitness, 0), &patrol.Config{
			Enabled: p.Witness.Enabled,
			Rigs:    p.Witness.Rigs,
		})
	}
	if p.Refinery != nil {
		r.Register(patrol.NewSessionLifecyclePatrol(constants.RoleRefinery, 0), &patrol.Config{
			Enabled: p.Refinery.Enabled,
			Rigs:    p.Refinery.Rigs,
		})
	}
	if p.Handler != nil {
		r.Register(patrol.NewSessionLifecyclePatrol("handler", 0), &patrol.Config{
			Enabled: p.Handler.Enabled,
		})
	}

	// Opt-in patrols — re-register with config overrides.
	if p.DoltRemotes != nil {
		r.Register(&patrol.DoltRemotesPatrol{}, &patrol.Config{
			Enabled:  p.DoltRemotes.Enabled,
			Interval: p.DoltRemotes.Interval,
		})
	}
	if p.DoltBackup != nil {
		r.Register(&patrol.DoltBackupPatrol{}, &patrol.Config{
			Enabled:  p.DoltBackup.Enabled,
			Interval: parseDurationOr(p.DoltBackup.IntervalStr, 0),
		})
	}
	if p.JsonlGitBackup != nil {
		r.Register(&patrol.JsonlGitBackupPatrol{}, &patrol.Config{
			Enabled:  p.JsonlGitBackup.Enabled,
			Interval: parseDurationOr(p.JsonlGitBackup.IntervalStr, 0),
		})
	}
	if p.WispReaper != nil {
		r.Register(&patrol.WispReaperPatrol{}, &patrol.Config{
			Enabled:  p.WispReaper.Enabled,
			Interval: parseDurationOr(p.WispReaper.IntervalStr, 0),
		})
	}
	if p.DoctorDog != nil {
		r.Register(&patrol.DoctorDogPatrol{}, &patrol.Config{
			Enabled:  p.DoctorDog.Enabled,
			Interval: parseDurationOr(p.DoctorDog.IntervalStr, 0),
		})
	}
	if p.CompactorDog != nil {
		r.Register(&patrol.CompactorDogPatrol{}, &patrol.Config{
			Enabled:  p.CompactorDog.Enabled,
			Interval: parseDurationOr(p.CompactorDog.IntervalStr, 0),
		})
	}
	if p.ScheduledMaintenance != nil {
		r.Register(&patrol.ScheduledMaintenancePatrol{}, &patrol.Config{
			Enabled: p.ScheduledMaintenance.Enabled,
		})
	}
	if p.SandboxReconcile != nil {
		r.Register(&patrol.SandboxReconcilePatrol{}, &patrol.Config{
			Enabled:  p.SandboxReconcile.Enabled,
			Interval: parseDurationOr(p.SandboxReconcile.IntervalStr, 0),
		})
	}

	return r
}
