package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

func captureOutput(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestRenderWantedItem_PriorityFormatted(t *testing.T) {
	item := &doltserver.WantedItem{
		ID:       "w-abc123",
		Title:    "Test item",
		Priority: 2,
		Status:   "open",
	}

	out := captureOutput(func() {
		renderWantedItem(item) //nolint:errcheck
	})

	if !strings.Contains(out, "P2") {
		t.Errorf("expected priority 'P2', got output:\n%s", out)
	}
	if strings.Contains(out, "Priority:        2\n") {
		t.Errorf("priority should not render as raw integer '2'")
	}
}

func TestRenderWantedItem_LabelAlignment(t *testing.T) {
	item := &doltserver.WantedItem{
		ID:          "w-abc123",
		Title:       "Test item",
		Status:      "open",
		EvidenceURL: "https://example.com/pr/1",
	}

	out := captureOutput(func() {
		renderWantedItem(item) //nolint:errcheck
	})

	// "Evidence URL:" is 13 chars — with labelWidth=13 it should align correctly.
	// Every line should have the value starting at column 15+ (label 13 + 2 spaces).
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		// After the colon there should be at least one space (two-space separator)
		rest := line[colonIdx+1:]
		if len(rest) > 0 && rest[0] != ' ' {
			t.Errorf("label too wide or missing separator in line: %q", line)
		}
	}

	// Specifically verify Evidence URL line is properly formatted
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Evidence URL:") {
			found = true
			// Should have two spaces after the colon
			if !strings.Contains(line, "Evidence URL:  ") {
				t.Errorf("Evidence URL label alignment broken: %q", line)
			}
		}
	}
	if !found {
		t.Error("Evidence URL line not found in output")
	}
}

func TestRenderWantedItem_EmptyFieldsShowNone(t *testing.T) {
	item := &doltserver.WantedItem{
		ID:     "w-abc123",
		Title:  "Test item",
		Status: "open",
		// Description, ClaimedBy, Tags, etc. are empty
	}

	out := captureOutput(func() {
		renderWantedItem(item) //nolint:errcheck
	})

	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "" {
			t.Errorf("empty value should show '(none)', got blank line: %q", line)
		}
	}

	// Specifically check that description shows (none) when empty
	if !strings.Contains(out, "(none)") {
		t.Error("expected at least one '(none)' for empty fields")
	}
}

func TestRenderWantedItem_MultiLineDescriptionIndented(t *testing.T) {
	item := &doltserver.WantedItem{
		ID:          "w-abc123",
		Title:       "Test item",
		Status:      "open",
		Description: "First line\nSecond line\nThird line",
	}

	out := captureOutput(func() {
		renderWantedItem(item) //nolint:errcheck
	})

	// Second and third lines should be indented, not at column 0
	lines := strings.Split(out, "\n")
	foundDesc := false
	for i, line := range lines {
		if strings.Contains(line, "Description:") {
			foundDesc = true
			// Next lines (until empty or new label) should be indented
			if i+1 < len(lines) {
				nextLine := lines[i+1]
				if nextLine != "" && !strings.HasPrefix(nextLine, " ") {
					t.Errorf("continuation line should be indented: %q", nextLine)
				}
			}
			break
		}
	}
	if !foundDesc {
		t.Error("Description line not found in output")
	}

	// "Second line" should appear in output (not be missing)
	if !strings.Contains(out, "Second line") {
		t.Errorf("multi-line description content missing from output:\n%s", out)
	}
}

func TestRenderWantedItem_SandboxRequiredShown(t *testing.T) {
	item := &doltserver.WantedItem{
		ID:              "w-abc123",
		Title:           "Test item",
		Status:          "open",
		SandboxRequired: true,
	}

	out := captureOutput(func() {
		renderWantedItem(item) //nolint:errcheck
	})

	if !strings.Contains(out, "Sandbox:") {
		t.Errorf("expected 'Sandbox:' in output when SandboxRequired=true:\n%s", out)
	}
	if !strings.Contains(out, "Yes") {
		t.Errorf("expected 'Yes' for sandbox required:\n%s", out)
	}
}

func TestRenderWantedItem_SandboxOmittedWhenFalse(t *testing.T) {
	item := &doltserver.WantedItem{
		ID:              "w-abc123",
		Title:           "Test item",
		Status:          "open",
		SandboxRequired: false,
	}

	out := captureOutput(func() {
		renderWantedItem(item) //nolint:errcheck
	})

	if strings.Contains(out, "Sandbox:") {
		t.Errorf("Sandbox field should be omitted when SandboxRequired=false:\n%s", out)
	}
}

func TestShowWanted_NotFound(t *testing.T) {
	store := newFakeWLCommonsStore()
	err := showWanted(store, "w-notfound", false)
	if err == nil {
		t.Error("expected error for non-existent item")
	}
}

func TestShowWanted_JSON(t *testing.T) {
	store := newFakeWLCommonsStore()
	item := &doltserver.WantedItem{
		ID:     "w-abc123",
		Title:  "Test item",
		Status: "open",
	}
	if err := store.InsertWanted(item); err != nil {
		t.Fatal(err)
	}

	out := captureOutput(func() {
		if err := showWanted(store, "w-abc123", true); err != nil {
			fmt.Println(err)
		}
	})

	if !strings.Contains(out, `"id"`) {
		t.Errorf("expected JSON output, got:\n%s", out)
	}
}
