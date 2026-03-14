package cmd

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

func TestShowWantedJSON(t *testing.T) {
	store := newFakeWLCommonsStore()
	item := &doltserver.WantedItem{
		ID:          "w-abc123",
		Title:       "Fix the parser",
		Description: "Parser crashes on empty input",
		Project:     "gastown",
		Type:        "bug",
		Priority:    1,
		Tags:        []string{"parser", "crash"},
		PostedBy:    "alice-rig",
		ClaimedBy:   "bob-rig",
		Status:      "claimed",
		EffortLevel: "small",
		EvidenceURL: "https://example.com/pr/42",
		CreatedAt:   "2026-01-01 10:00:00",
		UpdatedAt:   "2026-01-02 11:00:00",
	}
	if err := store.InsertWanted(item); err != nil {
		t.Fatalf("InsertWanted() error: %v", err)
	}

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-abc123", true); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	// Verify valid JSON
	var got doltserver.WantedItem
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", err, out)
	}

	// Verify key fields
	if got.ID != "w-abc123" {
		t.Errorf("ID = %q, want %q", got.ID, "w-abc123")
	}
	if got.Title != "Fix the parser" {
		t.Errorf("Title = %q, want %q", got.Title, "Fix the parser")
	}
	if got.Description != "Parser crashes on empty input" {
		t.Errorf("Description = %q, want %q", got.Description, "Parser crashes on empty input")
	}
	if got.Project != "gastown" {
		t.Errorf("Project = %q, want %q", got.Project, "gastown")
	}
	if got.Type != "bug" {
		t.Errorf("Type = %q, want %q", got.Type, "bug")
	}
	if got.Priority != 1 {
		t.Errorf("Priority = %d, want 1", got.Priority)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "parser" || got.Tags[1] != "crash" {
		t.Errorf("Tags = %v, want [parser crash]", got.Tags)
	}
	if got.PostedBy != "alice-rig" {
		t.Errorf("PostedBy = %q, want %q", got.PostedBy, "alice-rig")
	}
	if got.Status != "claimed" {
		t.Errorf("Status = %q, want %q", got.Status, "claimed")
	}
	if got.EvidenceURL != "https://example.com/pr/42" {
		t.Errorf("EvidenceURL = %q, want %q", got.EvidenceURL, "https://example.com/pr/42")
	}
	if got.CreatedAt != "2026-01-01 10:00:00" {
		t.Errorf("CreatedAt = %q, want %q", got.CreatedAt, "2026-01-01 10:00:00")
	}
}

func TestShowWantedText(t *testing.T) {
	store := newFakeWLCommonsStore()
	item := &doltserver.WantedItem{
		ID:          "w-text01",
		Title:       "Improve logging",
		Description: "Add structured log fields",
		Project:     "gastown",
		Type:        "feature",
		Priority:    2,
		Tags:        []string{"logging"},
		PostedBy:    "carol-rig",
		Status:      "open",
		EffortLevel: "medium",
		CreatedAt:   "2026-02-01 09:00:00",
		UpdatedAt:   "2026-02-01 09:00:00",
	}
	if err := store.InsertWanted(item); err != nil {
		t.Fatalf("InsertWanted() error: %v", err)
	}

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-text01", false); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	checks := []struct{ label, value string }{
		{"ID:", "w-text01"},
		{"Title:", "Improve logging"},
		{"Description:", "Add structured log fields"},
		{"Project:", "gastown"},
		{"Type:", "feature"},
		{"Priority:", "2"},
		{"Tags:", "logging"},
		{"Posted By:", "carol-rig"},
		{"Status:", "open"},
		{"Effort:", "medium"},
		{"Created:", "2026-02-01 09:00:00"},
		{"Updated:", "2026-02-01 09:00:00"},
	}

	for _, c := range checks {
		if !strings.Contains(out, c.label) {
			t.Errorf("output missing label %q", c.label)
		}
		if !strings.Contains(out, c.value) {
			t.Errorf("output missing value %q for label %q", c.value, c.label)
		}
	}
}

func TestShowWantedNotFound(t *testing.T) {
	store := newFakeWLCommonsStore()

	err := showWanted(store, "w-doesnotexist", false)
	if err == nil {
		t.Fatal("showWanted() expected error for non-existent ID")
	}
	if !strings.Contains(err.Error(), "w-doesnotexist") {
		t.Errorf("error message should contain the ID, got: %q", err.Error())
	}
}

func TestShowWantedEmptyFields(t *testing.T) {
	store := newFakeWLCommonsStore()
	item := &doltserver.WantedItem{
		ID:     "w-empty01",
		Title:  "Minimal item",
		Status: "open",
	}
	if err := store.InsertWanted(item); err != nil {
		t.Fatalf("InsertWanted() error: %v", err)
	}

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-empty01", false); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	// Required fields should appear
	if !strings.Contains(out, "w-empty01") {
		t.Errorf("output missing ID")
	}
	if !strings.Contains(out, "Minimal item") {
		t.Errorf("output missing Title")
	}

	// Labels for optional fields should still appear (with empty values)
	for _, label := range []string{"Description:", "Project:", "Type:", "Tags:", "Effort:", "Evidence URL:"} {
		if !strings.Contains(out, label) {
			t.Errorf("output missing label %q even when value is empty", label)
		}
	}
}

func TestShowWantedMultilineDescription(t *testing.T) {
	store := newFakeWLCommonsStore()
	item := &doltserver.WantedItem{
		ID:          "w-multi01",
		Title:       "Multiline item",
		Description: "First line\nSecond line\nThird line",
		Status:      "open",
	}
	if err := store.InsertWanted(item); err != nil {
		t.Fatalf("InsertWanted() error: %v", err)
	}

	out := captureStdout(t, func() {
		if err := showWanted(store, "w-multi01", false); err != nil {
			t.Errorf("showWanted() error: %v", err)
		}
	})

	// Description label must appear
	if !strings.Contains(out, "Description:") {
		t.Errorf("output missing Description label")
	}
	// All lines of the description must appear in output
	for _, line := range []string{"First line", "Second line", "Third line"} {
		if !strings.Contains(out, line) {
			t.Errorf("output missing description line %q", line)
		}
	}
}

func TestShowWantedJSONQueryError(t *testing.T) {
	store := newFakeWLCommonsStore()
	store.QueryWantedErr = io.ErrUnexpectedEOF

	err := showWanted(store, "w-any", true)
	if err == nil {
		t.Fatal("showWanted() expected error when store returns error")
	}
}
