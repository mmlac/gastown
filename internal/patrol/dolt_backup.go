package patrol

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultDoltBackupInterval = 15 * time.Minute
	doltBackupTimeout         = 120 * time.Second
)

// DoltBackupPatrol syncs Dolt databases to local filesystem backups.
type DoltBackupPatrol struct {
	// DataDir is the Dolt data directory. If empty, uses conventional path.
	DataDir string

	// Databases lists specific databases to back up. If empty, auto-discovers.
	Databases []string
}

func (p *DoltBackupPatrol) Name() string                  { return "dolt_backup" }
func (p *DoltBackupPatrol) DefaultInterval() time.Duration { return defaultDoltBackupInterval }
func (p *DoltBackupPatrol) RequiresRig() bool              { return false }

func (p *DoltBackupPatrol) Run(ctx context.Context, env Env) error {
	dataDir := p.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(env.TownRoot, ".dolt-data")
	}
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		env.Logger.Info("dolt_backup: data dir does not exist, skipping", "dir", dataDir)
		return nil
	}

	databases := p.Databases
	if len(databases) == 0 {
		databases = discoverDatabasesWithBackups(dataDir, env)
	}

	if len(databases) == 0 {
		env.Logger.Info("dolt_backup: no databases with backup remotes found")
		return nil
	}

	env.Logger.Info("dolt_backup: syncing databases", "count", len(databases))

	synced := 0
	for _, db := range databases {
		backupName := db + "-backup"
		if err := syncBackup(ctx, dataDir, db, backupName); err != nil {
			env.Logger.Error("dolt_backup: sync failed", "db", db, "error", err)
		} else {
			synced++
		}
	}

	env.Logger.Info("dolt_backup: sync complete", "synced", synced, "total", len(databases))

	if synced > 0 {
		syncOffsiteBackup(env)
	}

	return nil
}

// syncBackup runs `dolt backup sync <backup-name>` for a single database.
func syncBackup(ctx context.Context, dataDir, db, backupName string) error {
	ctx, cancel := context.WithTimeout(ctx, doltBackupTimeout)
	defer cancel()

	dbDir := filepath.Join(dataDir, db)
	cmd := exec.CommandContext(ctx, "dolt", "backup", "sync", backupName)
	cmd.Dir = dbDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// syncOffsiteBackup rsyncs the local backup directory to iCloud Drive.
func syncOffsiteBackup(env Env) {
	backupDir := filepath.Join(env.TownRoot, ".dolt-backup")
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	icloudDir := filepath.Join(homeDir, "Library", "Mobile Documents", "com~apple~CloudDocs", "gt-dolt-backup")
	if err := os.MkdirAll(icloudDir, 0755); err != nil {
		env.Logger.Warn("dolt_backup: offsite: cannot create iCloud dir", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rsync", "-a", "--delete", backupDir+"/", icloudDir+"/")
	if output, err := cmd.CombinedOutput(); err != nil {
		env.Logger.Warn("dolt_backup: offsite sync failed", "error", err, "output", strings.TrimSpace(string(output)))
	} else {
		env.Logger.Info("dolt_backup: offsite synced to iCloud")
	}
}

// discoverDatabasesWithBackups lists databases that have a backup remote configured.
func discoverDatabasesWithBackups(dataDir string, env Env) []string {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		env.Logger.Error("dolt_backup: error reading data dir", "error", err)
		return nil
	}

	var databases []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		backupName := entry.Name() + "-backup"
		if hasBackupRemote(dataDir, entry.Name(), backupName) {
			databases = append(databases, entry.Name())
		}
	}
	return databases
}

// hasBackupRemote checks if a database has the specified backup remote configured.
func hasBackupRemote(dataDir, db, backupName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dbDir := filepath.Join(dataDir, db)
	cmd := exec.CommandContext(ctx, "dolt", "backup")
	cmd.Dir = dbDir

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == backupName {
			return true
		}
	}
	return false
}
