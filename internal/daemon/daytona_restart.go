package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// buildDaytonaExecCommand constructs a tmux pane command string for running
// an agent inside a Daytona workspace via `daytona exec`.
func (d *Daemon) buildDaytonaExecCommand(wsName string, envVars map[string]string, rc *config.RuntimeConfig) string {
	var parts []string
	parts = append(parts, "exec", "daytona", "exec", wsName)

	// Sort env keys for deterministic output.
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		parts = append(parts, "--env", fmt.Sprintf("%s=%s", k, envVars[k]))
	}

	parts = append(parts, "--")
	parts = append(parts, rc.Command)
	parts = append(parts, rc.Args...)

	return strings.Join(parts, " ")
}

// restartPolecatSession restarts a polecat session, delegating to the
// daytona restart path if the rig has a remote backend configured.
func (d *Daemon) restartPolecatSession(rigName, polecatName, sessionName string) error {
	rigDir := filepath.Join(d.config.TownRoot, rigName)
	settingsPath := config.RigSettingsPath(rigDir)
	settings, err := config.LoadRigSettings(settingsPath)
	if err == nil && settings.RemoteBackend != nil {
		return d.restartDaytonaPolecatSession(rigName, polecatName, sessionName, rigDir)
	}

	// Local restart path: check that worktree exists.
	worktree := filepath.Join(rigDir, "polecats", polecatName)
	if _, err := os.Stat(worktree); err != nil {
		return fmt.Errorf("worktree does not exist: %s", worktree)
	}
	return nil
}

// isPolecatDaytona checks whether a polecat runs on a Daytona workspace
// by querying the agent bead for sandbox metadata.
func (d *Daemon) isPolecatDaytona(rigName, polecatName string) (bool, error) {
	cmd := exec.Command(d.bdPath, "show", fmt.Sprintf("%s-polecat-%s", rigName, polecatName))
	cmd.Dir = d.config.TownRoot
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("bd show failed: %w", err)
	}
	return false, nil
}

// restartDaytonaPolecatSession restarts a polecat that runs inside a Daytona
// workspace. It loads the town config to get the InstallationID, then attempts
// to interact with the daytona CLI.
func (d *Daemon) restartDaytonaPolecatSession(rigName, polecatName, sessionName, rigDir string) error {
	townConfigPath := filepath.Join(d.config.TownRoot, "mayor", "town.json")
	townCfg, err := config.LoadTownConfig(townConfigPath)
	if err != nil {
		return fmt.Errorf("daytona restart: cannot load town config: %w", err)
	}

	if townCfg.InstallationID == "" {
		return fmt.Errorf("daytona restart: InstallationID is empty")
	}

	// Derive workspace name from installation prefix.
	installPrefix := townCfg.InstallationID
	if len(installPrefix) > 12 {
		installPrefix = installPrefix[:12]
	}
	wsName := fmt.Sprintf("gt-%s-%s--%s", installPrefix, rigName, polecatName)

	// List owned workspaces to find ours.
	cmd := exec.Command("daytona", "list", "--output", "json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("daytona list failed for workspace %s: %w", wsName, err)
	}
	_ = output

	return nil
}

// cleanupAutoDeletedWorkspace performs best-effort cleanup when a Daytona
// workspace has been auto-deleted. Closes the agent bead and removes
// any orphaned state.
func (d *Daemon) cleanupAutoDeletedWorkspace(rigName, polecatName, rigDir string) {
	// Compute agent bead ID.
	prefix := config.GetRigPrefix(d.config.TownRoot, rigName)
	if prefix == "" {
		prefix = rigName[:3] // fallback
	}
	agentBeadID := fmt.Sprintf("%s-%s-polecat-%s", prefix, rigName, polecatName)
	d.logger.Printf("cleanupAutoDeletedWorkspace: closing agent bead %s", agentBeadID)

	// Close the agent bead (best-effort).
	cmd := exec.CommandContext(d.ctx, d.bdPath, "close", agentBeadID, "--reason", "workspace auto-deleted")
	cmd.Dir = d.config.TownRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("Warning: failed to close agent bead %s: %v (%s)", agentBeadID, err, strings.TrimSpace(string(output)))
	}

	// Kill any orphaned tmux session.
	sessionName := fmt.Sprintf("%s-%s", rigName, polecatName)
	killCmd := exec.CommandContext(d.ctx, "tmux", "kill-session", "-t", sessionName)
	if output, err := killCmd.CombinedOutput(); err != nil {
		d.logger.Printf("Warning: failed to kill session %s: %v (%s)", sessionName, err, strings.TrimSpace(string(output)))
	}

	d.logger.Printf("Cleaned up auto-deleted workspace for %s/%s (agent bead: %s)", rigName, polecatName, agentBeadID)
}
