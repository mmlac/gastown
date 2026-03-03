package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/daytona"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	polecatDiscoverReconcile bool
	polecatDiscoverDryRun    bool
	polecatDiscoverJSON      bool
)

var polecatDiscoverCmd = &cobra.Command{
	Use:   "discover <rig>",
	Short: "Discover daytona workspaces and reconcile with beads",
	Long: `Discover daytona workspaces owned by this installation for a rig.

Lists all daytona workspaces matching the rig's install prefix and cross-references
them with polecat agent beads to identify:

  - Matched: workspace has a corresponding agent bead (healthy)
  - Orphaned workspace: daytona workspace exists but no agent bead
  - Orphaned bead: agent bead references a workspace that doesn't exist

Use --reconcile to automatically fix orphans:
  - Orphaned workspaces are stopped (preserving state for investigation)
  - Orphaned beads have their daytona_workspace label cleared

Use --dry-run with --reconcile to preview what would happen without acting.

Only works for rigs with remote_backend configured.

Examples:
  gt polecat discover MyRig
  gt polecat discover MyRig --reconcile --dry-run
  gt polecat discover MyRig --reconcile
  gt polecat discover MyRig --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatDiscover,
}

// DiscoverResult holds the full discovery output.
type DiscoverResult struct {
	Rig               string             `json:"rig"`
	InstallPrefix     string             `json:"install_prefix"`
	Matched           []DiscoverMatch    `json:"matched,omitempty"`
	OrphanWorkspaces  []DiscoverOrphan   `json:"orphan_workspaces,omitempty"`
	OrphanBeads       []DiscoverOrphan   `json:"orphan_beads,omitempty"`
	Reconciled        bool               `json:"reconciled"`
	DryRun            bool               `json:"dry_run,omitempty"`
	ReconcileActions  []ReconcileAction  `json:"reconcile_actions,omitempty"`
}

// DiscoverMatch represents a workspace with a matching agent bead.
type DiscoverMatch struct {
	Polecat       string `json:"polecat"`
	WorkspaceName string `json:"workspace_name"`
	WorkspaceState string `json:"workspace_state"`
	BeadID        string `json:"bead_id"`
}

// DiscoverOrphan represents an orphaned workspace or bead.
type DiscoverOrphan struct {
	Polecat        string `json:"polecat,omitempty"`
	WorkspaceName  string `json:"workspace_name,omitempty"`
	WorkspaceState string `json:"workspace_state,omitempty"`
	BeadID         string `json:"bead_id,omitempty"`
}

// ReconcileAction records what reconciliation did.
type ReconcileAction struct {
	Type    string `json:"type"`    // "stop_workspace" or "clear_bead"
	Target  string `json:"target"`  // workspace name or bead ID
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func init() {
	polecatDiscoverCmd.Flags().BoolVar(&polecatDiscoverReconcile, "reconcile", false, "Automatically fix orphaned workspaces and beads")
	polecatDiscoverCmd.Flags().BoolVar(&polecatDiscoverDryRun, "dry-run", false, "Preview reconcile actions without performing them (requires --reconcile)")
	polecatDiscoverCmd.Flags().BoolVar(&polecatDiscoverJSON, "json", false, "Output as JSON")

	polecatCmd.AddCommand(polecatDiscoverCmd)
}

func runPolecatDiscover(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Resolve rig
	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	// Load rig settings to check for RemoteBackend
	settingsPath := config.RigSettingsPath(r.Path)
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading rig settings: %w (is %s configured with remote_backend?)", err, rigName)
	}

	if settings.RemoteBackend == nil {
		return fmt.Errorf("rig %s does not have remote_backend configured — discovery only applies to daytona-backed rigs", rigName)
	}

	if settings.RemoteBackend.Provider != "daytona" {
		return fmt.Errorf("unsupported remote backend provider: %s (only 'daytona' is supported)", settings.RemoteBackend.Provider)
	}

	// Load town config for installation prefix
	townConfigPath := constants.MayorTownPath(townRoot)
	townCfg, err := config.LoadTownConfig(townConfigPath)
	if err != nil {
		return fmt.Errorf("loading town config: %w", err)
	}

	shortID := townCfg.ShortInstallationID()
	if shortID == "" {
		return fmt.Errorf("installation ID not set in town config — run 'gt install' to initialize")
	}

	installPrefix := "gt-" + shortID

	// Create daytona client and discover workspaces
	client := daytona.NewClient(installPrefix)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := discoverWorkspaces(ctx, client, r, townRoot, rigName, installPrefix)
	if err != nil {
		return err
	}

	// Validate --dry-run requires --reconcile
	if polecatDiscoverDryRun && !polecatDiscoverReconcile {
		return fmt.Errorf("--dry-run requires --reconcile")
	}

	// Reconcile if requested
	if polecatDiscoverReconcile {
		result.Reconciled = true
		result.DryRun = polecatDiscoverDryRun
		reconcile(ctx, client, r, townRoot, result, polecatDiscoverDryRun)
	}

	// Output
	if polecatDiscoverJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	printDiscoverResult(result)
	return nil
}

func discoverWorkspaces(ctx context.Context, client *daytona.Client, r *rig.Rig, townRoot, rigName, installPrefix string) (*DiscoverResult, error) {
	result := &DiscoverResult{
		Rig:           rigName,
		InstallPrefix: installPrefix,
	}

	// List all owned workspaces from daytona
	workspaces, err := client.ListOwned(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing daytona workspaces: %w", err)
	}

	// Filter to workspaces belonging to this rig
	rigWorkspaces := make(map[string]daytona.Workspace) // polecat name -> workspace
	for _, ws := range workspaces {
		if ws.Rig == rigName {
			rigWorkspaces[ws.Polecat] = ws
		}
	}

	// Get agent beads with daytona workspace labels
	bd := beads.New(r.Path)
	prefix := rigPrefix(r)
	beadWorkspaces := make(map[string]string) // polecat name -> bead ID
	polecatNames := listPolecatNames(r)

	for _, name := range polecatNames {
		beadID := beads.PolecatBeadIDWithPrefix(prefix, rigName, name)
		_, fields, err := bd.GetAgentBead(beadID)
		if err != nil || fields == nil {
			continue
		}
		if fields.DaytonaWorkspace != "" {
			beadWorkspaces[name] = beadID
		}
	}

	// Cross-reference: find matches, orphan workspaces, and orphan beads
	matchedPolecats := make(map[string]bool)

	for polecatName, ws := range rigWorkspaces {
		if beadID, hasBead := beadWorkspaces[polecatName]; hasBead {
			// Matched: both workspace and bead exist
			result.Matched = append(result.Matched, DiscoverMatch{
				Polecat:        polecatName,
				WorkspaceName:  ws.Name,
				WorkspaceState: ws.State,
				BeadID:         beadID,
			})
			matchedPolecats[polecatName] = true
		} else {
			// Orphaned workspace: workspace exists but no bead
			result.OrphanWorkspaces = append(result.OrphanWorkspaces, DiscoverOrphan{
				Polecat:        polecatName,
				WorkspaceName:  ws.Name,
				WorkspaceState: ws.State,
			})
		}
	}

	for polecatName, beadID := range beadWorkspaces {
		if !matchedPolecats[polecatName] {
			// Orphaned bead: bead references a workspace that doesn't exist
			result.OrphanBeads = append(result.OrphanBeads, DiscoverOrphan{
				Polecat: polecatName,
				BeadID:  beadID,
			})
		}
	}

	return result, nil
}

// listPolecatNames returns all polecat names for a rig by scanning the polecats directory.
func listPolecatNames(r *rig.Rig) []string {
	polecatsDir := filepath.Join(r.Path, "polecats")
	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !isHiddenDir(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

func isHiddenDir(name string) bool {
	return len(name) > 0 && name[0] == '.'
}

func reconcile(ctx context.Context, client *daytona.Client, r *rig.Rig, townRoot string, result *DiscoverResult, dryRun bool) {
	bd := beads.New(r.Path)

	// Stop orphaned workspaces
	for _, orphan := range result.OrphanWorkspaces {
		action := ReconcileAction{
			Type:   "stop_workspace",
			Target: orphan.WorkspaceName,
		}
		if dryRun {
			action.Success = true
		} else if err := client.Stop(ctx, orphan.WorkspaceName); err != nil {
			action.Error = err.Error()
		} else {
			action.Success = true
		}
		result.ReconcileActions = append(result.ReconcileActions, action)
	}

	// Clear daytona_workspace field on orphaned beads
	empty := ""
	for _, orphan := range result.OrphanBeads {
		action := ReconcileAction{
			Type:   "clear_bead",
			Target: orphan.BeadID,
		}
		if dryRun {
			action.Success = true
		} else if err := bd.UpdateAgentDescriptionFields(orphan.BeadID, beads.AgentFieldUpdates{
			DaytonaWorkspace: &empty,
		}); err != nil {
			action.Error = err.Error()
		} else {
			action.Success = true
		}
		result.ReconcileActions = append(result.ReconcileActions, action)
	}
}

func printDiscoverResult(result *DiscoverResult) {
	fmt.Printf("%s Daytona workspace discovery for rig %s\n", style.Bold.Render("🔍"), style.Bold.Render(result.Rig))
	fmt.Printf("   Install prefix: %s\n\n", style.Dim.Render(result.InstallPrefix))

	// Matched
	if len(result.Matched) > 0 {
		fmt.Printf("%s Matched (%d):\n", style.Success.Render("✓"), len(result.Matched))
		for _, m := range result.Matched {
			stateStr := formatWorkspaceState(m.WorkspaceState)
			fmt.Printf("  %s  %s  %s\n", style.Bold.Render(m.Polecat), stateStr, style.Dim.Render(m.WorkspaceName))
		}
		fmt.Println()
	}

	// Orphaned workspaces
	if len(result.OrphanWorkspaces) > 0 {
		fmt.Printf("%s Orphaned workspaces (%d) — workspace exists, no agent bead:\n", style.Warning.Render("⚠"), len(result.OrphanWorkspaces))
		for _, o := range result.OrphanWorkspaces {
			stateStr := formatWorkspaceState(o.WorkspaceState)
			fmt.Printf("  %s  %s  %s\n", style.Bold.Render(o.Polecat), stateStr, style.Dim.Render(o.WorkspaceName))
		}
		fmt.Println()
	}

	// Orphaned beads
	if len(result.OrphanBeads) > 0 {
		fmt.Printf("%s Orphaned beads (%d) — bead references workspace that doesn't exist:\n", style.Warning.Render("⚠"), len(result.OrphanBeads))
		for _, o := range result.OrphanBeads {
			fmt.Printf("  %s  %s\n", style.Bold.Render(o.Polecat), style.Dim.Render(o.BeadID))
		}
		fmt.Println()
	}

	// Summary
	total := len(result.Matched) + len(result.OrphanWorkspaces) + len(result.OrphanBeads)
	if total == 0 {
		fmt.Println("No daytona workspaces found for this rig.")
		return
	}

	// Reconciliation results
	if result.Reconciled && len(result.ReconcileActions) > 0 {
		header := "🔧"
		if result.DryRun {
			header = "🔍"
			fmt.Printf("%s Reconciliation preview (dry-run — no changes made):\n", style.Bold.Render(header))
		} else {
			fmt.Printf("%s Reconciliation actions:\n", style.Bold.Render(header))
		}
		for _, a := range result.ReconcileActions {
			prefix := ""
			if result.DryRun {
				prefix = "would "
			}
			if a.Success {
				fmt.Printf("  %s %s%s: %s\n", style.Success.Render("✓"), prefix, a.Type, a.Target)
			} else {
				fmt.Printf("  %s %s%s: %s — %s\n", style.Error.Render("✗"), prefix, a.Type, a.Target, a.Error)
			}
		}
		fmt.Println()
	} else if !result.Reconciled && (len(result.OrphanWorkspaces) > 0 || len(result.OrphanBeads) > 0) {
		fmt.Printf("Run with %s to fix orphans (use %s to preview first).\n",
			style.Bold.Render("--reconcile"), style.Bold.Render("--reconcile --dry-run"))
	}
}

func formatWorkspaceState(state string) string {
	switch state {
	case "running", "Running":
		return style.Success.Render("running")
	case "stopped", "Stopped":
		return style.Dim.Render("stopped")
	default:
		return style.Warning.Render(state)
	}
}
