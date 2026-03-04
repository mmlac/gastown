package deps

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// MinDaytonaVersion is the minimum compatible Daytona CLI version.
// Requires 0.49.0 for --auto-stop-interval, --label, --volume, and pagination.
const MinDaytonaVersion = "0.49.0"

// DaytonaInstallURL is the installation page for the Daytona CLI.
const DaytonaInstallURL = "https://www.daytona.io/docs/installation/installation/"

// DaytonaStatus represents the state of the Daytona CLI installation.
type DaytonaStatus int

const (
	DaytonaOK         DaytonaStatus = iota // daytona found, version compatible
	DaytonaNotFound                        // daytona not in PATH
	DaytonaTooOld                          // daytona found but version too old
	DaytonaExecFailed                      // daytona found but 'daytona version' failed to execute
	DaytonaUnknown                         // daytona version ran but output couldn't be parsed
)

// CheckDaytona checks if the Daytona CLI is installed and compatible.
// Returns status, the installed version (if found), and diagnostic detail
// for failure cases (stderr/error output).
func CheckDaytona() (DaytonaStatus, string, string) {
	path, err := exec.LookPath("daytona")
	if err != nil {
		return DaytonaNotFound, "", ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return DaytonaExecFailed, "", fmt.Sprintf("at %s: %s", path, detail)
	}

	version := parseDaytonaVersion(string(output))
	if version == "" {
		return DaytonaUnknown, "", strings.TrimSpace(string(output))
	}

	if CompareVersions(version, MinDaytonaVersion) < 0 {
		return DaytonaTooOld, version, ""
	}

	return DaytonaOK, version, ""
}

// parseDaytonaVersion extracts version from daytona version output.
// Handles formats like "v0.49.0", "0.49.0", "Daytona version v0.49.0", etc.
func parseDaytonaVersion(output string) string {
	re := regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}
