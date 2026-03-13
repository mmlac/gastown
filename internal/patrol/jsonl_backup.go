package patrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultJsonlGitBackupInterval = 15 * time.Minute
	jsonlExportTimeout            = 60 * time.Second
	gitPushTimeout                = 120 * time.Second
	gitCmdTimeout                 = 30 * time.Second
	maxConsecutivePushFailures    = 3
	defaultSpikeThreshold         = 0.50
)

// testPollutionPatterns matches issue IDs or titles that indicate test data.
var testPollutionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^Test Issue`),
	regexp.MustCompile(`(?i)^test[_\s]`),
	regexp.MustCompile(`^bd-[0-9]{1,2}$`),
	regexp.MustCompile(`^bd-[a-z]{3,5}[0-9]{1,2}$`),
	regexp.MustCompile(`^(testdb_|beads_t|beads_pt|doctest_)`),
	regexp.MustCompile(`(?i)^--help`),
	regexp.MustCompile(`(?i)^Usage:\s`),
	regexp.MustCompile(`^offlinebrew-`),
	regexp.MustCompile(`-wisp-`),
}

var validDBName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

const scrubWhereClause = ` WHERE (ephemeral IS NULL OR ephemeral != 1)` +
	` AND status != 'tombstone'` +
	` AND issue_type NOT IN ('message', 'event', 'agent', 'convoy', 'molecule', 'role', 'merge-request', 'rig')` +
	` AND id NOT LIKE '%-wisp-%'` +
	` AND id NOT LIKE '%-cv-%'` +
	` AND id NOT LIKE 'test%'` +
	` AND id NOT LIKE 'beads\_t%'` +
	` AND id NOT LIKE 'beads\_pt%'` +
	` AND id NOT LIKE 'doctest\_%'` +
	` AND id NOT LIKE 'offlinebrew-%'` +
	` AND title NOT LIKE '--%'` +
	` AND title NOT LIKE 'Usage: %'` +
	` ORDER BY id`

var supplementalTables = []string{
	"comments", "config", "dependencies", "events", "labels", "metadata",
}

const spikeBaselineFile = ".spike-counts.json"

// JsonlGitBackupPatrol exports issues to JSONL files, scrubs ephemeral data,
// and commits/pushes to a git repository.
type JsonlGitBackupPatrol struct {
	// GitRepo is the path to the git repository for backup.
	GitRepo string

	// DataDir is the Dolt data directory.
	DataDir string

	// Databases lists specific databases to export.
	Databases []string

	// Scrub controls whether ephemeral data is filtered out.
	Scrub *bool

	// SpikeThreshold is the max allowed percentage change between exports.
	SpikeThreshold *float64

	// pushFailures tracks consecutive push failures for escalation.
	pushFailures int
}

func (p *JsonlGitBackupPatrol) Name() string                  { return "jsonl_git_backup" }
func (p *JsonlGitBackupPatrol) DefaultInterval() time.Duration { return defaultJsonlGitBackupInterval }
func (p *JsonlGitBackupPatrol) RequiresRig() bool              { return false }

func (p *JsonlGitBackupPatrol) Run(ctx context.Context, env Env) error {
	gitRepo := p.GitRepo
	if gitRepo == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home dir: %w", err)
		}
		gitRepo = filepath.Join(homeDir, ".dolt-archive", "git")
	}

	if _, err := os.Stat(filepath.Join(gitRepo, ".git")); os.IsNotExist(err) {
		env.Logger.Info("jsonl_git_backup: git repo does not exist, skipping", "repo", gitRepo)
		return nil
	}

	scrub := true
	if p.Scrub != nil {
		scrub = *p.Scrub
	}

	databases := p.Databases
	if len(databases) == 0 {
		env.Logger.Info("jsonl_git_backup: no databases configured, skipping")
		return nil
	}

	dataDir := p.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(env.TownRoot, ".dolt-data")
	}
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		env.Logger.Info("jsonl_git_backup: data dir does not exist, skipping", "dir", dataDir)
		return nil
	}

	env.Logger.Info("jsonl_git_backup: exporting databases", "count", len(databases), "repo", gitRepo, "scrub", scrub)

	exported := 0
	var failed []string
	counts := make(map[string]int)
	for _, db := range databases {
		n, err := exportDatabaseToJsonl(db, gitRepo, dataDir, scrub, env)
		if err != nil {
			env.Logger.Error("jsonl_git_backup: export failed", "db", db, "error", err)
			failed = append(failed, db)
		} else {
			counts[db] = n
			exported++
		}
	}

	if exported == 0 {
		return fmt.Errorf("no databases exported successfully")
	}

	removed := applyPollutionFilter(gitRepo, databases, env)
	if removed > 0 {
		env.Logger.Info("jsonl_git_backup: filtered test-pollution records", "removed", removed)
		recountAfterFilter(gitRepo, databases, counts)
	}

	if remaining := verifyNoPollution(gitRepo, databases, env); remaining > 0 {
		env.Logger.Warn("jsonl_git_backup: suspicious records survived scrub+filter", "count", remaining)
	}

	threshold := p.spikeThreshold()
	spikes := verifyExportCounts(gitRepo, databases, counts, threshold, env)
	if len(spikes) > 0 {
		return fmt.Errorf("spike detected: %s", formatSpikeReport(spikes))
	}

	if err := commitAndPushJsonlBackup(gitRepo, databases, counts, failed, env); err != nil {
		p.pushFailures++
		if p.pushFailures >= maxConsecutivePushFailures {
			p.pushFailures = 0
		}
		return fmt.Errorf("git operations failed: %w", err)
	}
	p.pushFailures = 0

	env.Logger.Info("jsonl_git_backup: complete", "exported", exported, "total", len(databases))
	return nil
}

func (p *JsonlGitBackupPatrol) spikeThreshold() float64 {
	if p.SpikeThreshold != nil {
		t := *p.SpikeThreshold
		if t > 0 && t <= 1.0 {
			return t
		}
	}
	return defaultSpikeThreshold
}

func exportDatabaseToJsonl(db, gitRepo, dataDir string, scrub bool, env Env) (int, error) {
	if !validDBName.MatchString(db) {
		return 0, fmt.Errorf("invalid database name: %q", db)
	}

	dbDir := filepath.Join(gitRepo, db)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return 0, fmt.Errorf("creating dir %s: %w", dbDir, err)
	}

	total := 0

	var query string
	if scrub {
		query = "SELECT * FROM `" + db + "`.issues" + scrubWhereClause
	} else {
		query = "SELECT * FROM `" + db + "`.issues ORDER BY id"
	}
	n, err := exportTableToJsonl("issues", query, dbDir, dataDir)
	if err != nil {
		return 0, fmt.Errorf("issues: %w", err)
	}
	total += n

	for _, table := range supplementalTables {
		tQuery := fmt.Sprintf("SELECT * FROM `%s`.`%s` ORDER BY 1", db, table)
		tn, err := exportTableToJsonl(table, tQuery, dbDir, dataDir)
		if err != nil {
			env.Logger.Warn("jsonl_git_backup: supplemental table export failed", "db", db, "table", table, "error", err)
			continue
		}
		total += tn
	}

	return total, nil
}

func exportTableToJsonl(table, query, dir, dataDir string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), jsonlExportTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "json", "-q", query)
	cmd.Dir = dataDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return 0, fmt.Errorf("%s: %s", err, errMsg)
		}
		return 0, err
	}

	var result struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return 0, fmt.Errorf("parsing dolt output: %w", err)
	}

	outPath := filepath.Join(dir, table+".jsonl")
	tmpPath := outPath + ".tmp"

	var buf bytes.Buffer
	for _, row := range result.Rows {
		var compact bytes.Buffer
		if err := json.Compact(&compact, row); err != nil {
			return 0, fmt.Errorf("compacting JSON row: %w", err)
		}
		buf.Write(compact.Bytes())
		buf.WriteByte('\n')
	}

	if err := os.WriteFile(tmpPath, buf.Bytes(), 0644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("renaming %s: %w", tmpPath, err)
	}

	return len(result.Rows), nil
}

func isTestPollution(record map[string]interface{}) bool {
	for _, field := range []string{"id", "title"} {
		val, ok := record[field]
		if !ok {
			continue
		}
		s, ok := val.(string)
		if !ok {
			continue
		}
		for _, pat := range testPollutionPatterns {
			if pat.MatchString(s) {
				return true
			}
		}
	}
	return false
}

func filterTestPollution(data []byte) ([]byte, int) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	var out bytes.Buffer
	removed := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record map[string]interface{}
		if err := json.Unmarshal(line, &record); err != nil {
			out.Write(line)
			out.WriteByte('\n')
			continue
		}
		if isTestPollution(record) {
			removed++
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), removed
}

func applyPollutionFilter(gitRepo string, databases []string, env Env) int {
	totalRemoved := 0
	for _, db := range databases {
		issuesPath := filepath.Join(gitRepo, db, "issues.jsonl")
		data, err := os.ReadFile(issuesPath)
		if err != nil {
			continue
		}
		filtered, removed := filterTestPollution(data)
		if removed > 0 {
			env.Logger.Info("jsonl_git_backup: filtered test-pollution records", "db", db, "removed", removed)
			if err := os.WriteFile(issuesPath, filtered, 0644); err != nil {
				continue
			}
			totalRemoved += removed
		}
	}
	return totalRemoved
}

func verifyNoPollution(gitRepo string, databases []string, env Env) int {
	total := 0
	for _, db := range databases {
		issuesPath := filepath.Join(gitRepo, db, "issues.jsonl")
		data, err := os.ReadFile(issuesPath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var record map[string]interface{}
			if err := json.Unmarshal(line, &record); err != nil {
				continue
			}
			if isTestPollution(record) {
				total++
			}
		}
	}
	return total
}

type spikeInfo struct {
	DB       string
	File     string
	Previous int
	Current  int
	Delta    float64
}

type spikeBaseline struct {
	Counts    map[string]int `json:"counts"`
	Timestamp string         `json:"timestamp"`
}

func loadSpikeBaseline(gitRepo string) *spikeBaseline {
	path := filepath.Join(gitRepo, spikeBaselineFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var sb spikeBaseline
	if err := json.Unmarshal(data, &sb); err != nil {
		return nil
	}
	return &sb
}

func saveSpikeBaseline(gitRepo string, counts map[string]int) error {
	sb := spikeBaseline{
		Counts:    counts,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(sb, "", "  ")
	if err != nil {
		return err
	}
	ensureGitIgnore(gitRepo, spikeBaselineFile)
	return os.WriteFile(filepath.Join(gitRepo, spikeBaselineFile), data, 0644)
}

func ensureGitIgnore(gitRepo, entry string) {
	ignorePath := filepath.Join(gitRepo, ".gitignore")
	data, _ := os.ReadFile(ignorePath)
	content := string(data)
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == entry {
			return
		}
	}
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += entry + "\n"
	_ = os.WriteFile(ignorePath, []byte(content), 0644)
}

func removeSpikeBaseline(gitRepo string) {
	os.Remove(filepath.Join(gitRepo, spikeBaselineFile))
}

func previousCommitLineCount(gitRepo, relPath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", gitRepo, "show", "HEAD:"+filepath.ToSlash(relPath))
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return 0, nil
	}

	lines := 0
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		lines++
	}
	return lines, nil
}

func verifyExportCounts(gitRepo string, databases []string, counts map[string]int, threshold float64, env Env) []spikeInfo {
	const minAbsoluteDelta = 20

	var spikes []spikeInfo
	spikeBase := loadSpikeBaseline(gitRepo)

	for _, db := range databases {
		currentCount, ok := counts[db]
		if !ok {
			continue
		}

		relPath := filepath.Join(db, "issues.jsonl")
		prevCount, err := previousCommitLineCount(gitRepo, relPath)
		if err != nil {
			continue
		}
		if prevCount == 0 {
			continue
		}

		absDelta := currentCount - prevCount
		if absDelta < 0 {
			absDelta = -absDelta
		}
		if absDelta < minAbsoluteDelta {
			continue
		}

		fractionalDelta := math.Abs(float64(currentCount-prevCount)) / float64(prevCount)

		effectiveThreshold := threshold
		if currentCount > prevCount {
			effectiveThreshold = threshold * 2
		}

		if fractionalDelta > effectiveThreshold {
			if spikeBase != nil {
				if baseCount, ok := spikeBase.Counts[db]; ok && baseCount > 0 {
					baseDelta := math.Abs(float64(currentCount-baseCount)) / float64(baseCount)
					if baseDelta <= threshold {
						continue
					}
				}
			}

			spikes = append(spikes, spikeInfo{
				DB:       db,
				File:     relPath,
				Previous: prevCount,
				Current:  currentCount,
				Delta:    fractionalDelta,
			})
		}
	}

	if len(spikes) > 0 {
		_ = saveSpikeBaseline(gitRepo, counts)
	}

	return spikes
}

func formatSpikeReport(spikes []spikeInfo) string {
	var b strings.Builder
	b.WriteString("JSONL export spike detection triggered:\n")
	for _, s := range spikes {
		direction := "JUMP (possible pollution)"
		if s.Current < s.Previous {
			direction = "DROP (possible data loss)"
		}
		fmt.Fprintf(&b, "  %s: %d → %d (%.1f%% change) — %s\n",
			s.DB, s.Previous, s.Current, s.Delta*100, direction)
	}
	b.WriteString("Export halted. Manual review required.")
	return b.String()
}

func countFileLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			count++
		}
	}
	return count, scanner.Err()
}

func recountAfterFilter(gitRepo string, databases []string, counts map[string]int) {
	for _, db := range databases {
		if _, ok := counts[db]; !ok {
			continue
		}
		issuesPath := filepath.Join(gitRepo, db, "issues.jsonl")
		n, err := countFileLines(issuesPath)
		if err != nil {
			continue
		}
		counts[db] = n
	}
}

func commitAndPushJsonlBackup(gitRepo string, databases []string, counts map[string]int, failed []string, env Env) error {
	if err := runGitCmd(gitRepo, gitCmdTimeout, "add", "-A", "."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	if err := runGitCmd(gitRepo, gitCmdTimeout, "diff", "--cached", "--quiet"); err == nil {
		env.Logger.Info("jsonl_git_backup: no changes to commit")
		return nil
	}

	timestamp := time.Now().Format("2006-01-02 15:04")
	var parts []string
	for _, db := range databases {
		if n, ok := counts[db]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", db, n))
		}
	}
	msg := fmt.Sprintf("backup %s: %s", timestamp, strings.Join(parts, " "))
	if len(failed) > 0 {
		sort.Strings(failed)
		msg += fmt.Sprintf(" [FAILED: %s]", strings.Join(failed, ", "))
	}

	if err := runGitCmd(gitRepo, gitCmdTimeout, "commit", "-m", msg,
		"--author=Gas Town Daemon <daemon@gastown.local>"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	removeSpikeBaseline(gitRepo)

	if hasGitRemote(gitRepo, "origin") {
		branch := currentGitBranch(gitRepo)
		if branch == "" {
			branch = "main"
		}
		if err := runGitCmd(gitRepo, gitPushTimeout, "push", "origin", branch); err != nil {
			return fmt.Errorf("git push: %w", err)
		}
	}

	return nil
}

func hasGitRemote(gitRepo, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", gitRepo, "remote", "get-url", name)
	return cmd.Run() == nil
}

func currentGitBranch(gitRepo string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", gitRepo, "rev-parse", "--abbrev-ref", "HEAD")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

func runGitCmd(dir string, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)

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

func parseLineCount(s string) (int, error) {
	s = strings.TrimSpace(s)
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty input")
	}
	return strconv.Atoi(fields[0])
}
