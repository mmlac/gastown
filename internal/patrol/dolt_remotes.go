package patrol

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultDoltRemotesInterval = 15 * time.Minute
	doltPushTimeout            = 60 * time.Second
	doltCmdTimeout             = 10 * time.Second
)

// DoltRemotesPatrol pushes Dolt databases to their configured remotes.
type DoltRemotesPatrol struct {
	// DataDir is the Dolt data directory. If empty, auto-detected from town root.
	DataDir string

	// Remote is the remote name to push to. If empty, auto-detects per database.
	Remote string

	// Branch is the branch to push. Defaults to "main".
	Branch string

	// Databases lists specific databases to push. If empty, auto-discovers.
	Databases []string
}

func (p *DoltRemotesPatrol) Name() string                  { return "dolt_remotes" }
func (p *DoltRemotesPatrol) DefaultInterval() time.Duration { return defaultDoltRemotesInterval }
func (p *DoltRemotesPatrol) RequiresRig() bool              { return false }

func (p *DoltRemotesPatrol) Run(ctx context.Context, env Env) error {
	dataDir := p.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(env.TownRoot, ".dolt-data")
	}
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		env.Logger.Info("dolt_remotes: data dir does not exist, skipping", "dir", dataDir)
		return nil
	}

	branch := p.Branch
	if branch == "" {
		branch = "main"
	}

	databases := p.Databases
	if len(databases) == 0 {
		var err error
		if p.Remote != "" {
			databases, err = discoverDatabasesWithRemotes(ctx, dataDir, p.Remote)
		} else {
			databases, err = discoverDatabasesWithAnyRemote(ctx, dataDir)
		}
		if err != nil {
			return fmt.Errorf("discovering databases: %w", err)
		}
	}

	if len(databases) == 0 {
		env.Logger.Info("dolt_remotes: no databases with remotes found")
		return nil
	}

	env.Logger.Info("dolt_remotes: pushing databases", "count", len(databases))

	pushed := 0
	for _, db := range databases {
		pushRemote := p.Remote
		if pushRemote == "" {
			pushRemote = findDatabaseRemote(ctx, dataDir, db)
			if pushRemote == "" {
				env.Logger.Warn("dolt_remotes: no remote found, skipping", "db", db)
				continue
			}
		}
		if err := pushDatabase(ctx, dataDir, db, pushRemote, branch); err != nil {
			env.Logger.Error("dolt_remotes: push failed", "db", db, "error", err)
		} else {
			pushed++
		}
	}

	env.Logger.Info("dolt_remotes: push complete", "pushed", pushed, "total", len(databases))
	return nil
}

// pushDatabase commits pending changes and pushes a single database to its remote.
func pushDatabase(ctx context.Context, dataDir, db, remote, branch string) error {
	for _, prefix := range []string{"test", "beads_t", "beads_pt", "doctest_"} {
		if strings.HasPrefix(db, prefix) {
			return fmt.Errorf("REFUSED: %q looks like a test database (prefix %q)", db, prefix)
		}
	}

	addQuery := fmt.Sprintf("USE `%s`; CALL DOLT_ADD('-A')", db)
	_ = runDoltSQL(ctx, dataDir, addQuery) // non-fatal

	commitQuery := fmt.Sprintf(
		"USE `%s`; CALL DOLT_COMMIT('-m', 'daemon: auto-commit pending changes', '--author', 'Gas Town Daemon <daemon@gastown.local>')",
		db,
	)
	if err := runDoltSQL(ctx, dataDir, commitQuery); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			// non-fatal, just log
		}
	}

	pushQuery := fmt.Sprintf("USE `%s`; CALL DOLT_PUSH('%s', '%s')", db, remote, branch)
	if err := runDoltSQL(ctx, dataDir, pushQuery); err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	return nil
}

// runDoltSQL executes a SQL query against the Dolt data directory.
func runDoltSQL(ctx context.Context, dataDir, query string) error {
	ctx, cancel := context.WithTimeout(ctx, doltPushTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dolt", "sql", "-q", query)
	cmd.Dir = dataDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}

	return nil
}

// discoverDatabasesWithRemotes lists databases with the specified remote.
func discoverDatabasesWithRemotes(ctx context.Context, dataDir, remote string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("reading data dir: %w", err)
	}

	var databases []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		doltDir := filepath.Join(dataDir, entry.Name(), ".dolt")
		if _, err := os.Stat(doltDir); os.IsNotExist(err) {
			continue
		}
		if databaseHasRemote(ctx, dataDir, entry.Name(), remote) {
			databases = append(databases, entry.Name())
		}
	}
	return databases, nil
}

// discoverDatabasesWithAnyRemote lists databases with any remote configured.
func discoverDatabasesWithAnyRemote(ctx context.Context, dataDir string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("reading data dir: %w", err)
	}

	var databases []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		doltDir := filepath.Join(dataDir, entry.Name(), ".dolt")
		if _, err := os.Stat(doltDir); os.IsNotExist(err) {
			continue
		}
		if databaseHasAnyRemote(ctx, dataDir, entry.Name()) {
			databases = append(databases, entry.Name())
		}
	}
	return databases, nil
}

// databaseHasRemote checks if a database has the specified remote configured.
func databaseHasRemote(ctx context.Context, dataDir, db, remote string) bool {
	ctx, cancel := context.WithTimeout(ctx, doltCmdTimeout)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT name FROM dolt_remotes WHERE name = '%s'", db, remote)
	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "csv", "-q", query)
	cmd.Dir = dataDir

	output, err := cmd.Output()
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return len(lines) > 1
}

// databaseHasAnyRemote checks if a database has any remote configured.
func databaseHasAnyRemote(ctx context.Context, dataDir, db string) bool {
	ctx, cancel := context.WithTimeout(ctx, doltCmdTimeout)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT name FROM dolt_remotes LIMIT 1", db)
	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "csv", "-q", query)
	cmd.Dir = dataDir

	output, err := cmd.Output()
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return len(lines) > 1
}

// findDatabaseRemote returns the name of the first remote for a database.
func findDatabaseRemote(ctx context.Context, dataDir, db string) string {
	ctx, cancel := context.WithTimeout(ctx, doltCmdTimeout)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT name FROM dolt_remotes LIMIT 1", db)
	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "csv", "-q", query)
	cmd.Dir = dataDir

	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return ""
	}
	return strings.TrimSpace(lines[1])
}
