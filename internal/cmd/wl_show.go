package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/workspace"
)

var wlShowJSON bool

var wlShowCmd = &cobra.Command{
	Use:   "show <work-id>",
	Short: "Show full details of a wanted item",
	Long: `Display all fields of a single wanted item from the wl-commons database.

Unlike 'gt wl browse' which truncates titles and omits descriptions,
'gt wl show' displays every field of the item.

The local wl-commons database is queried directly (kept in sync by gt wl sync).

EXAMPLES:
  gt wl show w-abc123             # Display all fields
  gt wl show w-abc123 --json      # JSON output`,
	Args: cobra.ExactArgs(1),
	RunE: runWLShow,
}

func init() {
	wlShowCmd.Flags().BoolVar(&wlShowJSON, "json", false, "Output as JSON")
	wlCmd.AddCommand(wlShowCmd)
}

func runWLShow(cmd *cobra.Command, args []string) error {
	wantedID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	if !doltserver.DatabaseExists(townRoot, doltserver.WLCommonsDB) {
		return fmt.Errorf("database %q not found\nSync the wanted board first with: gt wl sync", doltserver.WLCommonsDB)
	}

	store := doltserver.NewWLCommons(townRoot)
	return showWanted(store, wantedID, wlShowJSON)
}

// showWanted contains the testable logic for displaying a wanted item.
func showWanted(store doltserver.WLCommonsStore, wantedID string, asJSON bool) error {
	item, err := store.QueryWantedFull(wantedID)
	if err != nil {
		return err
	}

	if asJSON {
		data, err := json.MarshalIndent(item, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	return renderWantedItem(item)
}

func renderWantedItem(item *doltserver.WantedItem) error {
	tags := strings.Join(item.Tags, ", ")

	rows := []struct{ label, value string }{
		{"ID", item.ID},
		{"Title", item.Title},
		{"Description", item.Description},
		{"Project", item.Project},
		{"Type", item.Type},
		{"Priority", fmt.Sprintf("%d", item.Priority)},
		{"Tags", tags},
		{"Posted By", item.PostedBy},
		{"Claimed By", item.ClaimedBy},
		{"Status", item.Status},
		{"Effort", item.EffortLevel},
		{"Evidence URL", item.EvidenceURL},
		{"Created", item.CreatedAt},
		{"Updated", item.UpdatedAt},
	}

	labelWidth := 12
	for _, r := range rows {
		fmt.Printf("%-*s  %s\n", labelWidth, r.label+":", r.value)
	}
	return nil
}
