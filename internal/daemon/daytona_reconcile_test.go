package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPatrolRegistry_DaytonaReconcile verifies registry-based enable check.
func TestPatrolRegistry_DaytonaReconcile(t *testing.T) {
	t.Parallel()

	// Nil config → disabled (opt-in patrol, not registered by default).
	r := configurePatrolRegistry(nil)
	if r.IsEnabled("daytona_reconcile") {
		t.Error("expected disabled for nil config")
	}

	// Empty patrols → disabled.
	config := &DaemonPatrolConfig{Patrols: &PatrolsConfig{}}
	r = configurePatrolRegistry(config)
	if r.IsEnabled("daytona_reconcile") {
		t.Error("expected disabled when not configured")
	}
}

// --- Test helpers ---

// setupTownConfig creates a valid mayor/town.json with the given installation ID.
func setupTownConfig(t *testing.T, townRoot, installationID string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	townCfg := map[string]interface{}{
		"type":            "town",
		"version":         1,
		"name":            "test",
		"installation_id": installationID,
		"created_at":      "2025-01-01T00:00:00Z",
	}
	data, err := json.Marshal(townCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// setupRigsJSON creates a mayor/rigs.json with the given rig names.
func setupRigsJSON(t *testing.T, townRoot string, rigNames ...string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigs := make(map[string]interface{})
	for _, name := range rigNames {
		rigs[name] = map[string]interface{}{}
	}
	data, err := json.Marshal(map[string]interface{}{"rigs": rigs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}
