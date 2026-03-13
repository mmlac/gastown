package patrol

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	defaultCompactorDogInterval     = 24 * time.Hour
	defaultCompactorCommitThreshold = 500
	compactorQueryTimeout           = 30 * time.Second
	compactorGCTimeout              = 5 * time.Minute
	surgicalMaxRetries              = 1
)

// CompactorDogPatrol flattens Dolt commit history to reclaim graph storage.
type CompactorDogPatrol struct {
	// DoltPort is the Dolt server port. Defaults to 3307.
	DoltPort int

	// Threshold is the minimum commit count before compaction triggers.
	Threshold int

	// Databases lists specific databases to compact. If empty, uses defaults.
	Databases []string

	// Mode selects the compaction strategy: "flatten" (default) or "surgical".
	Mode string

	// KeepRecent is the number of recent commits to preserve in surgical mode.
	KeepRecent int
}

func (p *CompactorDogPatrol) Name() string                  { return "compactor_dog" }
func (p *CompactorDogPatrol) DefaultInterval() time.Duration { return defaultCompactorDogInterval }
func (p *CompactorDogPatrol) RequiresRig() bool              { return false }

func (p *CompactorDogPatrol) Run(ctx context.Context, env Env) error {
	threshold := p.Threshold
	if threshold == 0 {
		threshold = defaultCompactorCommitThreshold
	}
	mode := p.Mode
	if mode == "" {
		mode = "flatten"
	}
	port := p.DoltPort
	if port == 0 {
		port = 3307
	}

	env.Logger.Info("compactor_dog: starting compaction cycle", "threshold", threshold, "mode", mode)

	databases := p.Databases
	if len(databases) == 0 {
		env.Logger.Info("compactor_dog: no databases configured")
		return nil
	}

	compacted := 0
	skipped := 0
	errors := 0

	for _, dbName := range databases {
		commitCount, err := compactorCountCommits(dbName, port)
		if err != nil {
			env.Logger.Error("compactor_dog: error counting commits", "db", dbName, "error", err)
			errors++
			continue
		}

		if commitCount < threshold {
			env.Logger.Info("compactor_dog: below threshold, skipping", "db", dbName, "commits", commitCount, "threshold", threshold)
			skipped++
			continue
		}

		env.Logger.Info("compactor_dog: compacting", "db", dbName, "commits", commitCount, "mode", mode)

		var compactErr error
		if mode == "surgical" {
			keepRecent := p.KeepRecent
			if keepRecent == 0 {
				keepRecent = 50
			}
			compactErr = surgicalRebase(dbName, keepRecent, port, env)
		} else {
			compactErr = compactDatabase(dbName, port, env)
		}
		if compactErr != nil {
			env.Logger.Error("compactor_dog: compaction FAILED", "db", dbName, "error", compactErr)
			errors++
		} else {
			compacted++
			if err := compactorRunGC(dbName, port); err != nil {
				env.Logger.Warn("compactor_dog: gc after compaction failed", "db", dbName, "error", err)
			}
		}
	}

	env.Logger.Info("compactor_dog: cycle complete", "compacted", compacted, "skipped", skipped, "errors", errors)

	if errors > 0 {
		return fmt.Errorf("%d databases had compaction errors", errors)
	}
	return nil
}

func compactorOpenDB(dbName string, port int) (*sql.DB, error) {
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=30s&writeTimeout=30s",
		"127.0.0.1", port, dbName)
	return sql.Open("mysql", dsn)
}

func compactorCountCommits(dbName string, port int) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	db, err := compactorOpenDB(dbName, port)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_log", dbName)
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("count dolt_log: %w", err)
	}
	return count, nil
}

func compactDatabase(dbName string, port int, env Env) error {
	db, err := compactorOpenDB(dbName, port)
	if err != nil {
		return err
	}
	defer db.Close()

	preCounts, err := compactorGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("pre-flight row counts: %w", err)
	}

	rootHash, err := compactorGetRootCommit(db, dbName)
	if err != nil {
		return fmt.Errorf("find root commit: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		return fmt.Errorf("use database: %w", err)
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_RESET('--soft', '%s')", rootHash)); err != nil {
		return fmt.Errorf("soft reset to root: %w", err)
	}

	commitMsg := fmt.Sprintf("compaction: flatten %s history to single commit", dbName)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil {
		return fmt.Errorf("commit flattened data: %w", err)
	}

	postCounts, err := compactorGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("post-compact row counts: %w", err)
	}

	for table, preCount := range preCounts {
		postCount, ok := postCounts[table]
		if !ok {
			return fmt.Errorf("integrity check: table %q missing after compaction", table)
		}
		if preCount != postCount {
			return fmt.Errorf("integrity check: table %q count mismatch: pre=%d post=%d", table, preCount, postCount)
		}
	}

	env.Logger.Info("compactor_dog: compaction complete", "db", dbName)
	return nil
}

func surgicalRebase(dbName string, keepRecent, port int, env Env) error {
	var lastErr error
	for attempt := 0; attempt <= surgicalMaxRetries; attempt++ {
		if attempt > 0 {
			env.Logger.Info("compactor_dog: surgical rebase retry", "db", dbName, "attempt", attempt)
			time.Sleep(2 * time.Second)
		}
		lastErr = surgicalRebaseOnce(dbName, keepRecent, port, env)
		if lastErr == nil {
			return nil
		}
		if !isConcurrentWriteError(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("surgical rebase failed after %d retries: %w", surgicalMaxRetries, lastErr)
}

func isConcurrentWriteError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "rebase execution failed") ||
		strings.Contains(msg, "concurrency abort") ||
		strings.Contains(msg, "graph") ||
		strings.Contains(msg, "cannot rebase")
}

func surgicalRebaseOnce(dbName string, keepRecent, port int, env Env) error {
	db, err := compactorOpenDB(dbName, port)
	if err != nil {
		return err
	}
	defer db.Close()

	preHead, err := compactorGetHead(db, dbName)
	if err != nil {
		return fmt.Errorf("pre-flight HEAD: %w", err)
	}
	preCounts, err := compactorGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("pre-flight row counts: %w", err)
	}

	rootHash, err := compactorGetRootCommit(db, dbName)
	if err != nil {
		return fmt.Errorf("find root commit: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		return fmt.Errorf("use database: %w", err)
	}

	const baseBranch = "compact-base"
	const workBranch = "compact-work"

	surgicalCleanup(db, baseBranch, workBranch)

	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('%s', '%s')", baseBranch, rootHash)); err != nil {
		return fmt.Errorf("create base branch: %w", err)
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('%s', 'main')", workBranch)); err != nil {
		surgicalCleanupBase(db, baseBranch)
		return fmt.Errorf("create work branch: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_CHECKOUT('%s')", workBranch)); err != nil {
		surgicalCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("checkout work branch: %w", err)
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_REBASE('--interactive', '%s')", baseBranch)); err != nil {
		surgicalCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("start interactive rebase: %w", err)
	}

	var minOrder, maxOrder int
	if err := db.QueryRowContext(ctx, "SELECT MIN(rebase_order), MAX(rebase_order) FROM dolt_rebase").Scan(&minOrder, &maxOrder); err != nil {
		surgicalAbortAndCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("read rebase bounds: %w", err)
	}

	squashThreshold := maxOrder - keepRecent
	if squashThreshold <= minOrder {
		surgicalAbortAndCleanup(db, baseBranch, workBranch)
		return nil
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"UPDATE dolt_rebase SET action = 'squash' WHERE rebase_order > %d AND rebase_order <= %d",
		minOrder, squashThreshold)); err != nil {
		surgicalAbortAndCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("update rebase plan: %w", err)
	}

	if _, err := db.ExecContext(ctx, "CALL DOLT_REBASE('--continue')"); err != nil {
		surgicalCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("rebase execution failed: %w", err)
	}

	postCounts, err := compactorGetRowCounts(db, dbName)
	if err == nil {
		for table, preCount := range preCounts {
			postCount, ok := postCounts[table]
			if !ok {
				surgicalCleanup(db, baseBranch, workBranch)
				return fmt.Errorf("integrity: table %q missing after rebase", table)
			}
			if preCount != postCount {
				surgicalCleanup(db, baseBranch, workBranch)
				return fmt.Errorf("integrity: table %q count mismatch: pre=%d post=%d", table, preCount, postCount)
			}
		}
	}

	currentHead, err := compactorGetHead(db, dbName)
	if err != nil {
		surgicalCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("concurrency check: %w", err)
	}
	if currentHead != preHead {
		surgicalCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("concurrency abort: main HEAD moved from %s to %s", preHead[:8], currentHead[:8])
	}

	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'main')"); err != nil {
		return fmt.Errorf("delete old main: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-m', '%s', 'main')", workBranch)); err != nil {
		return fmt.Errorf("rename work to main: %w", err)
	}
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return fmt.Errorf("checkout new main: %w", err)
	}

	return nil
}

func surgicalCleanup(db *sql.DB, baseBranch, workBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
}

func surgicalAbortAndCleanup(db *sql.DB, baseBranch, workBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()
	_, _ = db.ExecContext(ctx, "CALL DOLT_REBASE('--abort')")
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
}

func surgicalCleanupBase(db *sql.DB, baseBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
}

func compactorGetHead(db *sql.DB, dbName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	var hash string
	query := fmt.Sprintf("SELECT DOLT_HASHOF('main') FROM `%s`.dual", dbName)
	if err := db.QueryRowContext(ctx, query).Scan(&hash); err != nil {
		query = fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date DESC LIMIT 1", dbName)
		if err := db.QueryRowContext(ctx, query).Scan(&hash); err != nil {
			return "", err
		}
	}
	return hash, nil
}

func compactorGetRootCommit(db *sql.DB, dbName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	var hash string
	query := fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date ASC LIMIT 1", dbName)
	if err := db.QueryRowContext(ctx, query).Scan(&hash); err != nil {
		return "", err
	}
	return hash, nil
}

func compactorGetRowCounts(db *sql.DB, dbName string) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	query := fmt.Sprintf("SELECT table_name FROM information_schema.tables WHERE table_schema = '%s' AND table_name NOT LIKE 'dolt_%%'", dbName)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}

	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.`%s`", dbName, table)
		if err := db.QueryRowContext(ctx, countQuery).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		counts[table] = count
	}

	return counts, nil
}

func compactorRunGC(dbName string, port int) error {
	db, err := compactorOpenDB(dbName, port)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), compactorGCTimeout)
	defer cancel()

	if _, err := db.ExecContext(ctx, "CALL dolt_gc()"); err != nil {
		return fmt.Errorf("dolt_gc: %w", err)
	}

	return nil
}
