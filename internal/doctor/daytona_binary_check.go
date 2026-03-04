package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/deps"
)

// DaytonaBinaryCheck verifies that the daytona CLI is installed, accessible in PATH,
// and meets the minimum version requirement. Daytona is used for dev environment
// management, and Gas Town's Create/Exec methods assume compatible CLI flags.
type DaytonaBinaryCheck struct {
	BaseCheck
}

// NewDaytonaBinaryCheck creates a new daytona binary availability check.
func NewDaytonaBinaryCheck() *DaytonaBinaryCheck {
	return &DaytonaBinaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "daytona-binary",
			CheckDescription: "Check that daytona CLI is installed and meets minimum version",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks if daytona is available in PATH and reports its version status.
func (c *DaytonaBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	status, version, detail := deps.CheckDaytona()

	switch status {
	case deps.DaytonaOK:
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  fmt.Sprintf("daytona %s", version),
			Category: c.CheckCategory,
		}

	case deps.DaytonaNotFound:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusWarning,
			Message: "daytona not found in PATH",
			Details: []string{
				"Daytona CLI is used for dev environment management",
			},
			FixHint:  fmt.Sprintf("Install daytona: %s", deps.DaytonaInstallURL),
			Category: c.CheckCategory,
		}

	case deps.DaytonaTooOld:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusWarning,
			Message: fmt.Sprintf("daytona %s is too old (minimum: %s)", version, deps.MinDaytonaVersion),
			Details: []string{
				fmt.Sprintf("Installed version %s does not meet the minimum requirement of %s", version, deps.MinDaytonaVersion),
				"CLI flag names may differ across versions, causing Create/Exec failures",
			},
			FixHint:  fmt.Sprintf("Upgrade daytona: %s", deps.DaytonaInstallURL),
			Category: c.CheckCategory,
		}

	case deps.DaytonaExecFailed:
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  fmt.Sprintf("daytona found but 'daytona version' failed: %s", detail),
			Details:  []string{"The daytona binary exists but could not report its version"},
			FixHint:  fmt.Sprintf("Reinstall daytona: %s", deps.DaytonaInstallURL),
			Category: c.CheckCategory,
		}

	case deps.DaytonaUnknown:
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  fmt.Sprintf("daytona found but version could not be parsed: %s", detail),
			FixHint:  fmt.Sprintf("Reinstall daytona: %s", deps.DaytonaInstallURL),
			Category: c.CheckCategory,
		}
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusWarning,
		Message:  "unexpected daytona check status",
		Category: c.CheckCategory,
	}
}
