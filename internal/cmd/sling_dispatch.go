package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/dispatch"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
)

// SlingParams captures everything needed to sling one bead to a rig.
// This is the serialization boundary for queue dispatch: at enqueue time,
// these fields are stored as queue metadata; at dispatch time, they are
// reconstructed into a SlingParams and passed to executeSling().
type SlingParams struct {
	// What to sling
	BeadID      string   // Base bead
	FormulaName string   // Formula to apply ("mol-polecat-work", user formula, or "")
	RigName     string   // Target rig (always a rig for queue)

	// CLI flag passthrough
	Args       string   // --args
	Vars       []string // --var (key=value pairs)
	Merge      string   // --merge (convoy strategy)
	BaseBranch string   // --base-branch
	Account    string   // --account
	Agent      string   // --agent
	NoConvoy   bool     // --no-convoy
	Owned      bool     // --owned
	NoMerge    bool     // --no-merge
	Force      bool     // --force
	HookRawBead bool    // --hook-raw-bead
	NoBoot     bool     // --no-boot
	Mode       string   // --ralph: "" (normal) or "ralph"

	// Execution behavior (set by caller, not serialized to queue)
	SkipCook         bool   // Batch optimization: formula already cooked
	FormulaFailFatal bool   // true=rollback+error (single/queue), false=hook raw bead (batch)
	CallerContext    string // Identifies the caller for shutdown messages (e.g., "queue-dispatch", "batch-sling")
	TownRoot         string
	BeadsDir         string

	// Target is an optional pre-resolved DispatchTarget. If nil, a RigTarget
	// is constructed from RigName (backward-compatible default).
	Target dispatch.DispatchTarget
}

// SlingResult captures the outcome of executeSling for caller-level tracking.
type SlingResult struct {
	BeadID           string
	PolecatName      string
	SpawnInfo        *SpawnedPolecatInfo
	Success          bool
	ErrMsg           string
	AttachedMolecule string
}

// executeSling performs the unified per-bead dispatch via the DispatchTarget
// interface. The target is resolved (rig, dog, or existing agent), prepared,
// and then the formula/hook/session flow runs through the polymorphic target.
//
// Batch sling and queue dispatch call this function. The single-sling path
// (runSling) retains its own implementation for now (handles dogs, mayor,
// nudge, and other non-rig targets). See TODO in sling.go.
//
// Caller responsibilities (NOT handled by executeSling):
//   - Cross-rig guard: callers must call checkCrossRigGuard() before executeSling
//     to verify the bead's prefix matches the target rig. Batch sling does this
//     pre-loop; queue dispatch skips the guard because the bead prefix was
//     validated at enqueue time and is immutable.
//   - wakeRigAgents: callers must call wakeRigAgents() after the dispatch loop
//     when NoBoot is false. Batch sling calls it post-loop; queue dispatch sets
//     NoBoot=true to avoid lock contention in the daemon.
//
// Steps:
//  1. Get bead info + status check
//  2. Burn stale molecules (if formula and force)
//  3. Prepare target (via DispatchTarget.Prepare — spawns polecat, resolves dog, etc.)
//  4. Auto-convoy (if !NoConvoy)
//  5. Cook formula (unless SkipCook)
//  6. Instantiate formula on bead (wisp + bond)
//  7. Hook bead with retry
//  8. Log sling event
//  9. Update agent hook_bead state
//  10. Store fields in bead (dispatcher, args, attached_molecule, no_merge)
//  11. Start session (via DispatchTarget.StartSession)
func executeSling(params SlingParams) (*SlingResult, error) {
	townRoot := params.TownRoot
	if townRoot == "" {
		var err error
		townRoot, err = findTownRoot()
		if err != nil {
			return nil, err
		}
	}

	// Acquire per-bead flock to prevent concurrent dispatch races (TOCTOU).
	// The CLI path (runSling) has its own flock; this closes the gap where
	// batch sling and queue dispatch could race against each other or against
	// a concurrent CLI invocation.
	releaseLock, err := tryAcquireSlingBeadLock(townRoot, params.BeadID)
	if err != nil {
		return &SlingResult{BeadID: params.BeadID, ErrMsg: err.Error()}, err
	}
	defer releaseLock()

	beadsDir := params.BeadsDir
	if beadsDir == "" {
		beadsDir = filepath.Join(townRoot, ".beads")
	}

	result := &SlingResult{
		BeadID: params.BeadID,
	}

	// 0. Check if rig is parked or docked before dispatching (gt-4owfd.1, gt-11y)
	if params.RigName != "" {
		if blocked, reason := IsRigParkedOrDocked(townRoot, params.RigName); blocked {
			result.ErrMsg = "rig " + reason
			undoCmd := "gt rig unpark"
			if reason == "docked" {
				undoCmd = "gt rig undock"
			}
			return result, fmt.Errorf("cannot sling to %s rig %q\n%s %s", reason, params.RigName, undoCmd, params.RigName)
		}
	}

	// 1. Get bead info + status check
	info, err := getBeadInfo(params.BeadID)
	if err != nil {
		result.ErrMsg = err.Error()
		return result, fmt.Errorf("could not get bead info: %w", err)
	}

	// Guard against dispatching closed/tombstone beads (defense-in-depth).
	// Not bypassed by --force — if you need to re-dispatch, reopen the bead first.
	if info.Status == "closed" || info.Status == "tombstone" {
		result.ErrMsg = "already " + info.Status
		return result, fmt.Errorf("bead %s is %s (work already completed)", params.BeadID, info.Status)
	}

	// Save explicit force state before dead-agent auto-force, so the deferred
	// gate below still requires an explicit --force for deferred beads.
	explicitForce := params.Force

	if (info.Status == "pinned" || info.Status == "hooked" || info.Status == "in_progress") && !params.Force {
		// Auto-force when hooked/in_progress agent's session is confirmed dead (gt-npzy, GH#1380).
		// Mirrors the dead-agent detection in runSling (sling.go) so that
		// programmatic dispatch also handles stale hooks from nuked polecats.
		if (info.Status == "hooked" || info.Status == "in_progress") && info.Assignee != "" && isHookedAgentDeadFn(info.Assignee) {
			fmt.Printf("  %s Hooked agent %s has no active session, auto-forcing dispatch...\n",
				style.Warning.Render("⚠"), info.Assignee)
			params.Force = true
		} else {
			result.ErrMsg = "already " + info.Status
			return result, fmt.Errorf("already %s (use --force to re-sling)", info.Status)
		}
	}

	// Guard against slinging deferred beads (gt-1326mw).
	// Uses explicitForce (not params.Force) so dead-agent auto-force doesn't
	// accidentally bypass the deferred gate.
	if isDeferredBead(info) && !explicitForce {
		result.ErrMsg = "deferred"
		return result, fmt.Errorf("bead %s is deferred (use --force to override)", params.BeadID)
	}

	// Send LIFECYCLE:Shutdown to the witness when force-stealing a bead from a
	// live polecat. Without this, the old polecat becomes a zombie — still running
	// but unaware it lost its hook. Mirrors the same logic in runSling (sling.go).
	if (info.Status == "hooked" || info.Status == "in_progress") && params.Force && info.Assignee != "" {
		assigneeParts := strings.Split(info.Assignee, "/")
		if len(assigneeParts) >= 3 && assigneeParts[1] == "polecats" {
			oldRigName := assigneeParts[0]
			oldPolecatName := assigneeParts[2]
			if townRoot != "" {
				callerCtx := params.CallerContext
				if callerCtx == "" {
					callerCtx = "sling"
				}
				router := mail.NewRouter(townRoot)
				shutdownMsg := &mail.Message{
					From:     callerCtx,
					To:       fmt.Sprintf("%s/witness", oldRigName),
					Subject:  fmt.Sprintf("LIFECYCLE:Shutdown %s", oldPolecatName),
					Body:     fmt.Sprintf("Reason: work_reassigned\nRequestedBy: %s\nBead: %s\nNewAssignee: %s", callerCtx, params.BeadID, params.RigName),
					Type:     mail.TypeTask,
					Priority: mail.PriorityHigh,
				}
				if err := router.Send(shutdownMsg); err != nil {
					fmt.Printf("  %s Could not send shutdown to witness: %v\n", style.Dim.Render("Warning:"), err)
				} else {
					fmt.Printf("  %s Sent LIFECYCLE:Shutdown to %s/witness for %s\n", style.Bold.Render("→"), oldRigName, oldPolecatName)
				}
				router.WaitPendingNotifications()
			}
		}
	}

	// 2. Burn stale molecules (if formula applies)
	if params.FormulaName != "" {
		existingMolecules := collectExistingMolecules(info)
		if len(existingMolecules) > 0 {
			// Auto-burn when bead is unassigned (molecules are definitionally stale),
			// or when the assigned agent's session is dead. This unblocks the daemon's
			// stranded convoy scan which never passes --force.
			stale := params.Force ||
				(info.Assignee == "" && (info.Status == "open" || info.Status == "in_progress")) ||
				(info.Assignee != "" && isHookedAgentDeadFn(info.Assignee))
			if stale {
				fmt.Printf("  %s Burning %d stale molecule(s): %s\n",
					style.Warning.Render("⚠"), len(existingMolecules), strings.Join(existingMolecules, ", "))
				if err := burnExistingMolecules(existingMolecules, params.BeadID, townRoot); err != nil {
					result.ErrMsg = fmt.Sprintf("burn failed: %v", err)
					return result, fmt.Errorf("burning stale molecules: %w", err)
				}
			} else {
				result.ErrMsg = "has existing molecule(s)"
				return result, fmt.Errorf("bead %s has existing molecule(s) (use --force)", params.BeadID)
			}
		}
	}

	// 3. Resolve and prepare the dispatch target.
	// If a pre-resolved Target was provided, use it directly.
	// Otherwise, build a RigTarget from RigName (backward-compatible default).
	target := params.Target
	var capture *spawnInfoCapture // populated by RigTarget.Prepare for backward compat
	if target == nil {
		target, capture = buildRigTargetFromParams(params)
	}

	ctx := context.Background()
	if err := target.Prepare(ctx); err != nil {
		result.ErrMsg = err.Error()
		return result, fmt.Errorf("failed to prepare target: %w", err)
	}

	// Populate backward-compatible result fields from the prepared target.
	targetAgent := target.AgentID()
	hookWorkDir := target.WorkDir()
	if rt, ok := target.(*dispatch.RigTarget); ok {
		result.PolecatName = rt.PolecatName()
		// Populate SpawnInfo for callers that still need it.
		if capture != nil && capture.info != nil {
			result.SpawnInfo = capture.info
		}
	}

	// 4. Auto-convoy (if !NoConvoy)
	convoyID := ""
	if !params.NoConvoy {
		existingConvoy := isTrackedByConvoy(params.BeadID)
		if existingConvoy == "" {
			var err error
			convoyID, err = createAutoConvoy(params.BeadID, info.Title, params.Owned, params.Merge, params.BaseBranch)
			if err != nil {
				fmt.Printf("  %s Could not create auto-convoy: %v\n", style.Dim.Render("Warning:"), err)
			} else {
				fmt.Printf("  %s Created convoy %s\n", style.Bold.Render("→"), convoyID)
			}
		} else {
			fmt.Printf("  %s Already tracked by convoy %s\n", style.Dim.Render("○"), existingConvoy)
		}
	}

	// rollbackOnError is a helper that cleans up both the target and bead artifacts.
	rollbackOnError := func(beadID, workDir, convoy string) {
		if rerr := target.Rollback(ctx); rerr != nil {
			fmt.Printf("  %s Target rollback failed: %v\n", style.Dim.Render("Warning:"), rerr)
		}
		// Also run the legacy bead-level rollback (molecule burn, unhook, convoy cleanup).
		if capture != nil && capture.info != nil {
			rollbackSlingArtifactsFn(capture.info, beadID, workDir, convoy)
		}
	}

	// 5. Cook formula (unless SkipCook)
	formulaCooked := params.SkipCook
	if params.FormulaName != "" && !formulaCooked {
		workDir := beads.ResolveHookDir(townRoot, params.BeadID, hookWorkDir)
		if err := CookFormula(params.FormulaName, workDir, townRoot); err != nil {
			if params.FormulaFailFatal {
				// Rollback spawned target on fatal cook failure
				rollbackOnError(params.BeadID, hookWorkDir, convoyID)
				result.ErrMsg = fmt.Sprintf("cook failed: %v", err)
				return result, fmt.Errorf("cooking formula %s: %w", params.FormulaName, err)
			}
			fmt.Printf("  %s Could not cook formula %s: %v\n", style.Dim.Render("Warning:"), params.FormulaName, err)
		} else {
			formulaCooked = true
		}
	}

	// 6. Instantiate formula on bead (wisp + bond)
	beadToHook := params.BeadID
	attachedMoleculeID := ""
	var allVars []string
	if params.FormulaName != "" && formulaCooked {
		// Auto-inject rig command vars as defaults (user --var flags override)
		rigCmdVars := loadRigCommandVars(townRoot, params.RigName)
		// Build per-bead vars: rig defaults first, then user vars (higher priority)
		allVars = append(rigCmdVars, params.Vars...)
		// Inject base_branch from the target if available (rig targets resolve
		// the effective base branch during Prepare, which may differ from the
		// CLI flag due to epic integration branch auto-detection).
		effectiveBaseBranch := params.BaseBranch
		if rt, ok := target.(*dispatch.RigTarget); ok && rt.BaseBranch() != "" {
			effectiveBaseBranch = rt.BaseBranch()
		}
		if effectiveBaseBranch != "" && effectiveBaseBranch != "main" {
			allVars = append(allVars, fmt.Sprintf("base_branch=%s", effectiveBaseBranch))
		}
		formulaResult, err := InstantiateFormulaOnBead(ctx, params.FormulaName, params.BeadID, info.Title, hookWorkDir, townRoot, true, allVars)
		if err != nil {
			if params.FormulaFailFatal {
				// Rollback spawned target on fatal formula failure
				rollbackOnError(params.BeadID, hookWorkDir, convoyID)
				result.ErrMsg = fmt.Sprintf("formula failed: %v", err)
				return result, fmt.Errorf("instantiating formula %s: %w", params.FormulaName, err)
			}
			// Best-effort: in batch mode, a formula instantiation failure should not abort or rollback the
			// spawned polecat. We still hook the raw bead so work can proceed (e.g., missing required vars).
			fmt.Printf("  %s Could not apply formula: %v (hooking raw bead)\n", style.Dim.Render("Warning:"), err)
		} else {
			fmt.Printf("  %s Formula %s applied\n", style.Bold.Render("✓"), params.FormulaName)
			beadToHook = formulaResult.BeadToHook
			attachedMoleculeID = formulaResult.WispRootID
		}
	}
	result.AttachedMolecule = attachedMoleculeID

	// 7. Hook bead with retry
	hookDir := beads.ResolveHookDir(townRoot, beadToHook, hookWorkDir)
	if err := hookBeadWithRetry(beadToHook, targetAgent, hookDir); err != nil {
		// Clean up orphaned target to avoid leaving spawned-but-unhookable artifacts
		if capture != nil && capture.info != nil {
			cleanupSpawnedPolecat(capture.info, params.RigName, convoyID)
		} else if rerr := target.Rollback(ctx); rerr != nil {
			fmt.Printf("  %s Target rollback failed: %v\n", style.Dim.Render("Warning:"), rerr)
		}
		result.ErrMsg = "hook failed"
		return result, fmt.Errorf("failed to hook bead: %w", err)
	}

	displayName := targetAgent
	if result.PolecatName != "" {
		displayName = result.PolecatName
	}
	fmt.Printf("  %s Work attached to %s\n", style.Bold.Render("✓"), displayName)

	// 8. Log sling event
	actor := detectActor()
	_ = events.LogFeed(events.TypeSling, actor, events.SlingPayload(beadToHook, targetAgent))

	// 9. Update agent hook_bead state
	updateAgentHookBead(targetAgent, beadToHook, hookWorkDir, beadsDir)

	// 10. Store fields in bead (dispatcher, args, attached_molecule, no_merge, mode)
	fieldUpdates := beadFieldUpdates{
		Dispatcher:       actor,
		Args:             params.Args,
		Vars:             append([]string(nil), params.Vars...),
		AttachedMolecule: attachedMoleculeID,
		AttachedFormula:  params.FormulaName,
		NoMerge:          params.NoMerge,
		Mode:             params.Mode,
		FormulaVars:      strings.Join(allVars, "\n"),
	}
	// Use beadToHook for the update target (may differ from beadID when formula-on-bead)
	if err := storeFieldsInBead(beadToHook, fieldUpdates); err != nil {
		fmt.Printf("  %s Could not store fields in bead: %v\n", style.Dim.Render("Warning:"), err)
	}

	// Update agent bead mode (for stuck detector to identify ralphcats)
	if params.Mode != "" {
		updateAgentMode(targetAgent, params.Mode, hookWorkDir, beadsDir)
	}

	// 11. Start session (via DispatchTarget.StartSession)
	pane, err := target.StartSession(ctx, dispatch.StartOpts{})
	if err != nil {
		fmt.Printf("  %s Could not start session: %v, cleaning up partial state...\n", style.Dim.Render("✗"), err)
		rollbackOnError(beadToHook, hookWorkDir, convoyID)
		result.ErrMsg = fmt.Sprintf("session failed: %v", err)
		return result, fmt.Errorf("starting session: %w", err)
	}
	fmt.Printf("  %s Session started for %s\n", style.Bold.Render("▶"), displayName)
	_ = pane

	result.Success = true
	return result, nil
}

// spawnInfoCapture holds a SpawnedPolecatInfo that is populated by closures
// during RigTarget.Prepare(). This allows executeSling to access the spawn
// info for backward-compatible result fields after the target is prepared.
type spawnInfoCapture struct {
	info *SpawnedPolecatInfo
}

// buildRigTargetFromParams constructs a RigTarget with closures that delegate
// to the existing spawn/session infrastructure. This provides backward
// compatibility: callers that set SlingParams.RigName (without a pre-resolved
// Target) get the same behavior as before the DispatchTarget refactor.
//
// Returns both the target and a capture struct whose info field is populated
// when Prepare() is called (for backward-compat result fields).
func buildRigTargetFromParams(params SlingParams) (*dispatch.RigTarget, *spawnInfoCapture) {
	capture := &spawnInfoCapture{}

	spawnFn := func(ctx context.Context) (*dispatch.SpawnResult, error) {
		opts := SlingSpawnOptions{
			Force:      params.Force,
			Account:    params.Account,
			HookBead:   params.BeadID,
			Agent:      params.Agent,
			BaseBranch: params.BaseBranch,
			// Create is always true for rig targets: executeSling only handles
			// rig-targeted dispatch (batch sling + queue dispatch), where a fresh
			// polecat must be spawned. The single-sling path (runSling) handles
			// the --create flag for non-rig targets via resolveTarget.
			Create: true,
		}
		info, err := spawnPolecatForSling(params.RigName, opts)
		if err != nil {
			return nil, err
		}
		capture.info = info
		return &dispatch.SpawnResult{
			RigName:     params.RigName,
			PolecatName: info.PolecatName,
			ClonePath:   info.ClonePath,
			SessionName: info.SessionName,
			BaseBranch:  info.BaseBranch,
			Branch:      info.Branch,
		}, nil
	}

	startFn := func(ctx context.Context, spawn *dispatch.SpawnResult, opts dispatch.StartOpts) (string, error) {
		if capture.info == nil {
			return "", fmt.Errorf("spawn info not available (Prepare not called?)")
		}
		return capture.info.StartSession()
	}

	rollbackFn := func(ctx context.Context, spawn *dispatch.SpawnResult) error {
		if capture.info != nil {
			cleanupSpawnedPolecat(capture.info, params.RigName, "")
		}
		return nil
	}

	checkFn := func(ctx context.Context, spawn *dispatch.SpawnResult) (bool, error) {
		if capture.info == nil {
			return false, nil
		}
		return false, nil
	}

	rt := dispatch.NewRigTarget(params.RigName, spawnFn, startFn, rollbackFn, checkFn)
	return rt, capture
}

// findTownRoot is defined in hook.go
