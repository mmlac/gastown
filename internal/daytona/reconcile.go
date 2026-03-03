// Package daytona provides workspace lifecycle management via the daytona CLI.
// reconcile.go implements workspace discovery and reconciliation:
// cross-references daytona workspaces with agent beads to detect orphans.
package daytona

import (
	"context"
	"fmt"
	"log"
)

// ReconcileAction describes what should be done for a discovered workspace or bead.
type ReconcileAction string

const (
	// ActionHealthy means the workspace and bead are matched and consistent.
	ActionHealthy ReconcileAction = "healthy"

	// ActionOrphanedWorkspace means a daytona workspace exists with no matching agent bead.
	ActionOrphanedWorkspace ReconcileAction = "orphaned_workspace"

	// ActionOrphanedBead means an agent bead references a daytona workspace that doesn't exist.
	ActionOrphanedBead ReconcileAction = "orphaned_bead"
)

// DiscoveryResult represents the outcome of workspace discovery for one item.
type DiscoveryResult struct {
	Action  ReconcileAction
	Rig     string // rig name
	Polecat string // polecat name

	// Workspace is non-nil for healthy matches and orphaned workspaces.
	Workspace *Workspace

	// BeadID is set for healthy matches and orphaned beads.
	BeadID string
}

// ReconcileReport summarizes the full reconciliation for a rig.
type ReconcileReport struct {
	Rig     string
	Results []DiscoveryResult

	// Counts for quick summary.
	Healthy            int
	OrphanedWorkspaces int
	OrphanedBeads      int
}

// AgentBead represents the minimal polecat bead info needed for reconciliation.
// The caller provides these by querying beads (bd list --label=gt:agent).
type AgentBead struct {
	ID                 string // agent bead ID (e.g., "gtd-GasTownDaytona-polecat-garnet")
	Polecat            string // polecat name
	DaytonaWorkspaceID string // workspace name from bead description field
	AgentState         string // agent_state field (spawning, working, idle, etc.)
}

// DiscoverWorkspaces cross-references daytona workspaces with agent beads for a single rig.
// It returns a report categorizing each item as healthy, orphaned workspace, or orphaned bead.
//
// Parameters:
//   - client: daytona client scoped to this installation
//   - rigName: the rig to reconcile
//   - workspaces: all workspaces from ListOwned, pre-filtered to this rig
//   - beads: all polecat agent beads for this rig that have a DaytonaWorkspaceID
func DiscoverWorkspaces(client *Client, rigName string, workspaces []Workspace, beads []AgentBead) *ReconcileReport {
	report := &ReconcileReport{Rig: rigName}

	// Index workspaces by polecat name (workspace names are deterministic).
	wsByPolecat := make(map[string]*Workspace, len(workspaces))
	for i := range workspaces {
		ws := &workspaces[i]
		if ws.Rig == rigName {
			wsByPolecat[ws.Polecat] = ws
		}
	}

	// Index beads by polecat name.
	beadsByPolecat := make(map[string]*AgentBead, len(beads))
	for i := range beads {
		b := &beads[i]
		beadsByPolecat[b.Polecat] = b
	}

	// Check workspaces against beads.
	for polecatName, ws := range wsByPolecat {
		if bead, ok := beadsByPolecat[polecatName]; ok {
			// Healthy match: workspace exists and bead references it.
			report.Results = append(report.Results, DiscoveryResult{
				Action:    ActionHealthy,
				Rig:       rigName,
				Polecat:   polecatName,
				Workspace: ws,
				BeadID:    bead.ID,
			})
			report.Healthy++
		} else {
			// Orphaned workspace: workspace exists but no bead references it.
			report.Results = append(report.Results, DiscoveryResult{
				Action:    ActionOrphanedWorkspace,
				Rig:       rigName,
				Polecat:   polecatName,
				Workspace: ws,
			})
			report.OrphanedWorkspaces++
		}
	}

	// Check beads for workspaces that don't exist.
	for polecatName, bead := range beadsByPolecat {
		if _, ok := wsByPolecat[polecatName]; !ok {
			// Orphaned bead: bead references a workspace that doesn't exist.
			report.Results = append(report.Results, DiscoveryResult{
				Action:  ActionOrphanedBead,
				Rig:     rigName,
				Polecat: polecatName,
				BeadID:  bead.ID,
			})
			report.OrphanedBeads++
		}
	}

	return report
}

// ReconcileOptions controls reconciliation behavior.
type ReconcileOptions struct {
	// DryRun logs actions without performing them.
	DryRun bool

	// AutoDelete removes orphaned workspaces. If false, they're only stopped.
	AutoDelete bool
}

// ReconcileResult holds outcomes of reconciliation actions taken.
type ReconcileResult struct {
	WorkspacesStopped int
	WorkspacesDeleted int
	BeadsReset        int
	Errors            []error
}

// Reconcile acts on a discovery report: stops/deletes orphaned workspaces and
// resets orphaned beads. The beadResetter callback handles bead state reset
// since the daytona package doesn't import beads directly.
func Reconcile(ctx context.Context, client *Client, report *ReconcileReport, opts ReconcileOptions, beadResetter func(beadID string) error, logger *log.Logger) *ReconcileResult {
	result := &ReconcileResult{}

	for _, item := range report.Results {
		switch item.Action {
		case ActionOrphanedWorkspace:
			if opts.DryRun {
				logger.Printf("[dry-run] would stop orphaned workspace %s (rig=%s, polecat=%s)",
					item.Workspace.Name, item.Rig, item.Polecat)
				if opts.AutoDelete {
					logger.Printf("[dry-run] would delete orphaned workspace %s", item.Workspace.Name)
				}
				continue
			}

			// Stop the workspace first.
			if item.Workspace.State != "stopped" {
				if err := client.Stop(ctx, item.Workspace.Name); err != nil {
					logger.Printf("Warning: failed to stop orphaned workspace %s: %v", item.Workspace.Name, err)
					result.Errors = append(result.Errors, fmt.Errorf("stop %s: %w", item.Workspace.Name, err))
				} else {
					logger.Printf("Stopped orphaned workspace %s (rig=%s, polecat=%s)",
						item.Workspace.Name, item.Rig, item.Polecat)
					result.WorkspacesStopped++
				}
			}

			// Delete if configured.
			if opts.AutoDelete {
				if err := client.Delete(ctx, item.Workspace.Name); err != nil {
					logger.Printf("Warning: failed to delete orphaned workspace %s: %v", item.Workspace.Name, err)
					result.Errors = append(result.Errors, fmt.Errorf("delete %s: %w", item.Workspace.Name, err))
				} else {
					logger.Printf("Deleted orphaned workspace %s", item.Workspace.Name)
					result.WorkspacesDeleted++
				}
			}

		case ActionOrphanedBead:
			if beadResetter == nil {
				continue
			}
			if opts.DryRun {
				logger.Printf("[dry-run] would reset orphaned bead %s (rig=%s, polecat=%s)",
					item.BeadID, item.Rig, item.Polecat)
				continue
			}

			if err := beadResetter(item.BeadID); err != nil {
				logger.Printf("Warning: failed to reset orphaned bead %s: %v", item.BeadID, err)
				result.Errors = append(result.Errors, fmt.Errorf("reset bead %s: %w", item.BeadID, err))
			} else {
				logger.Printf("Reset orphaned bead %s (rig=%s, polecat=%s)",
					item.BeadID, item.Rig, item.Polecat)
				result.BeadsReset++
			}

		case ActionHealthy:
			if logger != nil {
				logger.Printf("Healthy match: workspace %s ↔ bead %s (rig=%s, polecat=%s)",
					item.Workspace.Name, item.BeadID, item.Rig, item.Polecat)
			}
		}
	}

	return result
}
