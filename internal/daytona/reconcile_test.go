package daytona

import (
	"context"
	"log"
	"os"
	"testing"
)

func TestDiscoverWorkspaces_AllHealthy(t *testing.T) {
	t.Parallel()
	client := NewClientWithRunner("gt-abc12345", &mockRunner{})

	workspaces := []Workspace{
		{ID: "ws1", Name: "gt-abc12345-myrig-onyx", State: "running", Rig: "myrig", Polecat: "onyx"},
		{ID: "ws2", Name: "gt-abc12345-myrig-amber", State: "stopped", Rig: "myrig", Polecat: "amber"},
	}
	beads := []AgentBead{
		{ID: "gtd-myrig-polecat-onyx", Polecat: "onyx", DaytonaWorkspaceID: "gt-abc12345-myrig-onyx"},
		{ID: "gtd-myrig-polecat-amber", Polecat: "amber", DaytonaWorkspaceID: "gt-abc12345-myrig-amber"},
	}

	report := DiscoverWorkspaces(client, "myrig", workspaces, beads)

	if report.Healthy != 2 {
		t.Errorf("Healthy = %d, want 2", report.Healthy)
	}
	if report.OrphanedWorkspaces != 0 {
		t.Errorf("OrphanedWorkspaces = %d, want 0", report.OrphanedWorkspaces)
	}
	if report.OrphanedBeads != 0 {
		t.Errorf("OrphanedBeads = %d, want 0", report.OrphanedBeads)
	}
	if len(report.Results) != 2 {
		t.Errorf("len(Results) = %d, want 2", len(report.Results))
	}
}

func TestDiscoverWorkspaces_OrphanedWorkspace(t *testing.T) {
	t.Parallel()
	client := NewClientWithRunner("gt-abc12345", &mockRunner{})

	workspaces := []Workspace{
		{ID: "ws1", Name: "gt-abc12345-myrig-onyx", State: "running", Rig: "myrig", Polecat: "onyx"},
		{ID: "ws2", Name: "gt-abc12345-myrig-ghost", State: "running", Rig: "myrig", Polecat: "ghost"},
	}
	beads := []AgentBead{
		{ID: "gtd-myrig-polecat-onyx", Polecat: "onyx", DaytonaWorkspaceID: "gt-abc12345-myrig-onyx"},
	}

	report := DiscoverWorkspaces(client, "myrig", workspaces, beads)

	if report.Healthy != 1 {
		t.Errorf("Healthy = %d, want 1", report.Healthy)
	}
	if report.OrphanedWorkspaces != 1 {
		t.Errorf("OrphanedWorkspaces = %d, want 1", report.OrphanedWorkspaces)
	}

	// Find the orphaned workspace result.
	var orphan *DiscoveryResult
	for i := range report.Results {
		if report.Results[i].Action == ActionOrphanedWorkspace {
			orphan = &report.Results[i]
			break
		}
	}
	if orphan == nil {
		t.Fatal("expected orphaned workspace result")
	}
	if orphan.Polecat != "ghost" {
		t.Errorf("orphan.Polecat = %q, want %q", orphan.Polecat, "ghost")
	}
	if orphan.Workspace.Name != "gt-abc12345-myrig-ghost" {
		t.Errorf("orphan.Workspace.Name = %q", orphan.Workspace.Name)
	}
}

func TestDiscoverWorkspaces_OrphanedBead(t *testing.T) {
	t.Parallel()
	client := NewClientWithRunner("gt-abc12345", &mockRunner{})

	workspaces := []Workspace{
		{ID: "ws1", Name: "gt-abc12345-myrig-onyx", State: "running", Rig: "myrig", Polecat: "onyx"},
	}
	beads := []AgentBead{
		{ID: "gtd-myrig-polecat-onyx", Polecat: "onyx", DaytonaWorkspaceID: "gt-abc12345-myrig-onyx"},
		{ID: "gtd-myrig-polecat-vanished", Polecat: "vanished", DaytonaWorkspaceID: "gt-abc12345-myrig-vanished"},
	}

	report := DiscoverWorkspaces(client, "myrig", workspaces, beads)

	if report.Healthy != 1 {
		t.Errorf("Healthy = %d, want 1", report.Healthy)
	}
	if report.OrphanedBeads != 1 {
		t.Errorf("OrphanedBeads = %d, want 1", report.OrphanedBeads)
	}

	// Find the orphaned bead result.
	var orphan *DiscoveryResult
	for i := range report.Results {
		if report.Results[i].Action == ActionOrphanedBead {
			orphan = &report.Results[i]
			break
		}
	}
	if orphan == nil {
		t.Fatal("expected orphaned bead result")
	}
	if orphan.Polecat != "vanished" {
		t.Errorf("orphan.Polecat = %q, want %q", orphan.Polecat, "vanished")
	}
	if orphan.BeadID != "gtd-myrig-polecat-vanished" {
		t.Errorf("orphan.BeadID = %q", orphan.BeadID)
	}
}

func TestDiscoverWorkspaces_Empty(t *testing.T) {
	t.Parallel()
	client := NewClientWithRunner("gt-abc12345", &mockRunner{})

	report := DiscoverWorkspaces(client, "myrig", nil, nil)

	if report.Healthy != 0 || report.OrphanedWorkspaces != 0 || report.OrphanedBeads != 0 {
		t.Errorf("expected all zeros, got healthy=%d orphanWs=%d orphanBead=%d",
			report.Healthy, report.OrphanedWorkspaces, report.OrphanedBeads)
	}
}

func TestDiscoverWorkspaces_FiltersToRig(t *testing.T) {
	t.Parallel()
	client := NewClientWithRunner("gt-abc12345", &mockRunner{})

	// Include workspaces from different rigs.
	workspaces := []Workspace{
		{ID: "ws1", Name: "gt-abc12345-myrig-onyx", State: "running", Rig: "myrig", Polecat: "onyx"},
		{ID: "ws2", Name: "gt-abc12345-otherrig-pearl", State: "running", Rig: "otherrig", Polecat: "pearl"},
	}
	beads := []AgentBead{
		{ID: "gtd-myrig-polecat-onyx", Polecat: "onyx", DaytonaWorkspaceID: "gt-abc12345-myrig-onyx"},
	}

	report := DiscoverWorkspaces(client, "myrig", workspaces, beads)

	// otherrig workspace should be ignored (different rig).
	if report.Healthy != 1 {
		t.Errorf("Healthy = %d, want 1", report.Healthy)
	}
	if report.OrphanedWorkspaces != 0 {
		t.Errorf("OrphanedWorkspaces = %d, want 0", report.OrphanedWorkspaces)
	}
}

func TestDiscoverWorkspaces_Mixed(t *testing.T) {
	t.Parallel()
	client := NewClientWithRunner("gt-abc12345", &mockRunner{})

	workspaces := []Workspace{
		{ID: "ws1", Name: "gt-abc12345-rig-alpha", State: "running", Rig: "rig", Polecat: "alpha"},
		{ID: "ws2", Name: "gt-abc12345-rig-beta", State: "stopped", Rig: "rig", Polecat: "beta"},
		{ID: "ws3", Name: "gt-abc12345-rig-orphan", State: "running", Rig: "rig", Polecat: "orphan"},
	}
	beads := []AgentBead{
		{ID: "gtd-rig-polecat-alpha", Polecat: "alpha", DaytonaWorkspaceID: "gt-abc12345-rig-alpha"},
		{ID: "gtd-rig-polecat-beta", Polecat: "beta", DaytonaWorkspaceID: "gt-abc12345-rig-beta"},
		{ID: "gtd-rig-polecat-gone", Polecat: "gone", DaytonaWorkspaceID: "gt-abc12345-rig-gone"},
	}

	report := DiscoverWorkspaces(client, "rig", workspaces, beads)

	if report.Healthy != 2 {
		t.Errorf("Healthy = %d, want 2", report.Healthy)
	}
	if report.OrphanedWorkspaces != 1 {
		t.Errorf("OrphanedWorkspaces = %d, want 1", report.OrphanedWorkspaces)
	}
	if report.OrphanedBeads != 1 {
		t.Errorf("OrphanedBeads = %d, want 1", report.OrphanedBeads)
	}
	if len(report.Results) != 4 {
		t.Errorf("len(Results) = %d, want 4", len(report.Results))
	}
}

func TestReconcile_DryRun(t *testing.T) {
	t.Parallel()

	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	client := NewClientWithRunner("gt-abc12345", mock)
	logger := log.New(os.Stderr, "test: ", 0)

	report := &ReconcileReport{
		Rig: "myrig",
		Results: []DiscoveryResult{
			{
				Action:    ActionOrphanedWorkspace,
				Rig:       "myrig",
				Polecat:   "ghost",
				Workspace: &Workspace{Name: "gt-abc12345-myrig-ghost", State: "running"},
			},
			{
				Action:  ActionOrphanedBead,
				Rig:     "myrig",
				Polecat: "vanished",
				BeadID:  "gtd-myrig-polecat-vanished",
			},
		},
		OrphanedWorkspaces: 1,
		OrphanedBeads:      1,
	}

	resetCalled := false
	beadResetter := func(beadID string) error {
		resetCalled = true
		return nil
	}

	result := Reconcile(context.Background(), client, report, ReconcileOptions{DryRun: true}, beadResetter, logger)

	// Dry run should not have called any daytona commands.
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 daytona calls in dry-run, got %d", len(mock.calls))
	}
	if resetCalled {
		t.Error("bead resetter should not be called in dry-run")
	}
	if result.WorkspacesStopped != 0 || result.WorkspacesDeleted != 0 || result.BeadsReset != 0 {
		t.Errorf("expected all zeros in dry-run, got stopped=%d deleted=%d reset=%d",
			result.WorkspacesStopped, result.WorkspacesDeleted, result.BeadsReset)
	}
}

func TestReconcile_StopsOrphanedWorkspace(t *testing.T) {
	t.Parallel()

	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	client := NewClientWithRunner("gt-abc12345", mock)
	logger := log.New(os.Stderr, "test: ", 0)

	report := &ReconcileReport{
		Rig: "myrig",
		Results: []DiscoveryResult{
			{
				Action:    ActionOrphanedWorkspace,
				Rig:       "myrig",
				Polecat:   "ghost",
				Workspace: &Workspace{Name: "gt-abc12345-myrig-ghost", State: "running"},
			},
		},
		OrphanedWorkspaces: 1,
	}

	result := Reconcile(context.Background(), client, report, ReconcileOptions{}, nil, logger)

	if result.WorkspacesStopped != 1 {
		t.Errorf("WorkspacesStopped = %d, want 1", result.WorkspacesStopped)
	}
	if result.WorkspacesDeleted != 0 {
		t.Errorf("WorkspacesDeleted = %d, want 0", result.WorkspacesDeleted)
	}
	// Verify stop was called.
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	if mock.calls[0].Args[0] != "stop" {
		t.Errorf("expected stop command, got %v", mock.calls[0].Args)
	}
}

func TestReconcile_StopsAndDeletesOrphanedWorkspace(t *testing.T) {
	t.Parallel()

	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	client := NewClientWithRunner("gt-abc12345", mock)
	logger := log.New(os.Stderr, "test: ", 0)

	report := &ReconcileReport{
		Rig: "myrig",
		Results: []DiscoveryResult{
			{
				Action:    ActionOrphanedWorkspace,
				Rig:       "myrig",
				Polecat:   "ghost",
				Workspace: &Workspace{Name: "gt-abc12345-myrig-ghost", State: "running"},
			},
		},
		OrphanedWorkspaces: 1,
	}

	result := Reconcile(context.Background(), client, report, ReconcileOptions{AutoDelete: true}, nil, logger)

	if result.WorkspacesStopped != 1 {
		t.Errorf("WorkspacesStopped = %d, want 1", result.WorkspacesStopped)
	}
	if result.WorkspacesDeleted != 1 {
		t.Errorf("WorkspacesDeleted = %d, want 1", result.WorkspacesDeleted)
	}
	// Verify both stop and delete were called.
	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(mock.calls))
	}
}

func TestReconcile_SkipsStopForAlreadyStopped(t *testing.T) {
	t.Parallel()

	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	client := NewClientWithRunner("gt-abc12345", mock)
	logger := log.New(os.Stderr, "test: ", 0)

	report := &ReconcileReport{
		Rig: "myrig",
		Results: []DiscoveryResult{
			{
				Action:    ActionOrphanedWorkspace,
				Rig:       "myrig",
				Polecat:   "ghost",
				Workspace: &Workspace{Name: "gt-abc12345-myrig-ghost", State: "stopped"},
			},
		},
		OrphanedWorkspaces: 1,
	}

	result := Reconcile(context.Background(), client, report, ReconcileOptions{}, nil, logger)

	// Already stopped — no stop call needed.
	if result.WorkspacesStopped != 0 {
		t.Errorf("WorkspacesStopped = %d, want 0", result.WorkspacesStopped)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 calls for already-stopped workspace, got %d", len(mock.calls))
	}
}

func TestReconcile_ResetsOrphanedBead(t *testing.T) {
	t.Parallel()

	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	client := NewClientWithRunner("gt-abc12345", mock)
	logger := log.New(os.Stderr, "test: ", 0)

	report := &ReconcileReport{
		Rig: "myrig",
		Results: []DiscoveryResult{
			{
				Action:  ActionOrphanedBead,
				Rig:     "myrig",
				Polecat: "vanished",
				BeadID:  "gtd-myrig-polecat-vanished",
			},
		},
		OrphanedBeads: 1,
	}

	var resetID string
	beadResetter := func(beadID string) error {
		resetID = beadID
		return nil
	}

	result := Reconcile(context.Background(), client, report, ReconcileOptions{}, beadResetter, logger)

	if result.BeadsReset != 1 {
		t.Errorf("BeadsReset = %d, want 1", result.BeadsReset)
	}
	if resetID != "gtd-myrig-polecat-vanished" {
		t.Errorf("resetID = %q, want %q", resetID, "gtd-myrig-polecat-vanished")
	}
}
