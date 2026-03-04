package doctor

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/deps"
)

// DaytonaBinaryCheck verifies that the Daytona CLI is installed, accessible
// in PATH, and meets the minimum version requirement. Daytona is required for
// remote workspace provisioning.
type DaytonaBinaryCheck struct {
	BaseCheck
}

// NewDaytonaBinaryCheck creates a new Daytona CLI availability check.
func NewDaytonaBinaryCheck() *DaytonaBinaryCheck {
	return &DaytonaBinaryCheck{
		BaseCheck: BaseCheck{
			CheckName:        "daytona-binary",
			CheckDescription: "Check that daytona CLI is installed and meets minimum version",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// Run checks if the Daytona CLI is available in PATH and reports its version status.
func (c *DaytonaBinaryCheck) Run(ctx *CheckContext) *CheckResult {
	status, version, detail := deps.CheckDaytona()

	switch status {
	case deps.DaytonaOK:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("daytona %s", version),
		}

	case deps.DaytonaNotFound:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusError,
			Message: "daytona not found in PATH",
			Details: []string{
				"Daytona CLI is required for remote workspace provisioning",
			},
			FixHint: fmt.Sprintf("Install daytona: %s", deps.DaytonaInstallURL),
		}

	case deps.DaytonaTooOld:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusError,
			Message: fmt.Sprintf("daytona %s is too old (minimum: %s)", version, deps.MinDaytonaVersion),
			Details: []string{
				fmt.Sprintf("Installed version %s does not meet the minimum requirement of %s", version, deps.MinDaytonaVersion),
			},
			FixHint: fmt.Sprintf("Upgrade daytona: %s", deps.DaytonaInstallURL),
		}

	case deps.DaytonaExecFailed:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusError,
			Message: fmt.Sprintf("daytona found but 'daytona version' failed: %s", detail),
			Details: []string{
				"The daytona binary exists but could not report its version",
			},
			FixHint: fmt.Sprintf("Reinstall daytona: %s", deps.DaytonaInstallURL),
		}

	case deps.DaytonaUnknown:
		return &CheckResult{
			Name:   c.Name(),
			Status: StatusWarning,
			Message: fmt.Sprintf("daytona found but version could not be parsed: %s", detail),
			FixHint: fmt.Sprintf("Reinstall daytona: %s", deps.DaytonaInstallURL),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "unexpected daytona check status",
	}
}
