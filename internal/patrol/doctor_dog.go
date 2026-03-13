package patrol

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

const (
	defaultDoctorDogInterval         = 5 * time.Minute
	defaultDoctorDogLatencyAlertMs   = 5000.0
	defaultDoctorDogOrphanAlertCount = 20
	defaultDoctorDogBackupStaleSec   = 3600.0
)

// DoctorDogPatrol pours a mol-dog-doctor molecule for agent execution.
// The daemon is a thin ticker — agents (Deacon) execute the formula steps.
type DoctorDogPatrol struct {
	// DoltPort is the Dolt server port. Defaults to 3307.
	DoltPort int

	// LatencyAlertMs is the latency threshold in ms. Default: 5000.
	LatencyAlertMs float64

	// OrphanAlertCount is the database count threshold. Default: 20.
	OrphanAlertCount int

	// BackupStaleSeconds is the backup age threshold in seconds. Default: 3600.
	BackupStaleSeconds float64
}

func (p *DoctorDogPatrol) Name() string                  { return "doctor_dog" }
func (p *DoctorDogPatrol) DefaultInterval() time.Duration { return defaultDoctorDogInterval }
func (p *DoctorDogPatrol) RequiresRig() bool              { return false }

func (p *DoctorDogPatrol) Run(ctx context.Context, env Env) error {
	port := p.DoltPort
	if port == 0 {
		port = 3307
	}

	latency := p.LatencyAlertMs
	if latency == 0 {
		latency = defaultDoctorDogLatencyAlertMs
	}
	orphanCount := p.OrphanAlertCount
	if orphanCount == 0 {
		orphanCount = defaultDoctorDogOrphanAlertCount
	}
	backupStaleSec := p.BackupStaleSeconds
	if backupStaleSec == 0 {
		backupStaleSec = defaultDoctorDogBackupStaleSec
	}

	env.Logger.Info("doctor_dog: pouring molecule for agent execution")

	// Pour molecule via gt sling.
	args := []string{"sling", "mol-dog-doctor", "deacon/dogs",
		"--var", fmt.Sprintf("port=%d", port),
		"--var", fmt.Sprintf("latency_threshold=%sms", strconv.FormatFloat(latency, 'f', 0, 64)),
		"--var", fmt.Sprintf("orphan_threshold=%d", orphanCount),
		"--var", fmt.Sprintf("backup_threshold=%ss", strconv.FormatFloat(backupStaleSec, 'f', 0, 64)),
	}

	cmd := exec.CommandContext(ctx, "gt", args...)
	cmd.Dir = env.TownRoot
	if err := cmd.Run(); err != nil {
		env.Logger.Warn("doctor_dog: molecule pour failed (non-fatal)", "error", err)
		return nil // Non-fatal
	}

	env.Logger.Info("doctor_dog: poured mol-dog-doctor")
	return nil
}
