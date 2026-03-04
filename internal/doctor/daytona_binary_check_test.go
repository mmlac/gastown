package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/deps"
)

func TestDaytonaBinaryCheck_Metadata(t *testing.T) {
	check := NewDaytonaBinaryCheck()

	if check.Name() != "daytona-binary" {
		t.Errorf("Name() = %q, want %q", check.Name(), "daytona-binary")
	}
	if check.Description() != "Check that daytona CLI is installed and meets minimum version" {
		t.Errorf("Description() = %q", check.Description())
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryInfrastructure)
	}
	if check.CanFix() {
		t.Error("CanFix() should return false (user must install daytona manually)")
	}
}

// writeFakeDaytona creates a platform-appropriate fake "daytona" executable in dir.
func writeFakeDaytona(t *testing.T, dir string, script string, batScript string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "daytona.bat")
		if err := os.WriteFile(path, []byte(batScript), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		path := filepath.Join(dir, "daytona")
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDaytonaBinaryCheck_DaytonaInstalled(t *testing.T) {
	if _, err := exec.LookPath("daytona"); err != nil {
		t.Skip("daytona not installed, skipping installed-path test")
	}

	check := NewDaytonaBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	switch result.Status {
	case StatusOK:
		if !strings.Contains(result.Message, "daytona") {
			t.Errorf("expected version string in message, got %q", result.Message)
		}
	case StatusWarning:
		if !strings.Contains(result.Message, "too old") {
			t.Errorf("expected 'too old' in warning message, got %q", result.Message)
		}
	default:
		t.Errorf("unexpected status %v when daytona is installed: %s", result.Status, result.Message)
	}
}

func TestDaytonaBinaryCheck_HermeticSuccess(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeDaytona(t, fakeDir,
		fmt.Sprintf("#!/bin/sh\necho 'Daytona CLI version %s'\n", deps.MinDaytonaVersion),
		fmt.Sprintf("@echo off\r\necho Daytona CLI version %s\r\n", deps.MinDaytonaVersion),
	)

	t.Setenv("PATH", fakeDir)

	check := NewDaytonaBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK with fake daytona at min version, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, deps.MinDaytonaVersion) {
		t.Errorf("expected version in message, got %q", result.Message)
	}
}

func TestDaytonaBinaryCheck_DaytonaNotInPath(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	check := NewDaytonaBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when daytona is not in PATH, got %v: %s", result.Status, result.Message)
	}
	if result.Message != "daytona not found in PATH" {
		t.Errorf("unexpected message: %q", result.Message)
	}
	if result.FixHint == "" {
		t.Error("expected a fix hint with install instructions")
	}
	if !strings.Contains(result.FixHint, "daytona") {
		t.Errorf("fix hint should reference daytona, got %q", result.FixHint)
	}
}

func TestDaytonaBinaryCheck_DaytonaTooOld(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeDaytona(t, fakeDir,
		"#!/bin/sh\necho 'Daytona CLI version 0.0.1'\n",
		"@echo off\r\necho Daytona CLI version 0.0.1\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewDaytonaBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for too-old daytona, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "too old") {
		t.Errorf("expected 'too old' in message, got %q", result.Message)
	}
	if result.FixHint == "" {
		t.Error("expected a fix hint with upgrade instructions")
	}
}

func TestDaytonaBinaryCheck_DaytonaVersionFails(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeDaytona(t, fakeDir,
		"#!/bin/sh\nexit 1\n",
		"@echo off\r\nexit /b 1\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewDaytonaBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when daytona version fails, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "failed") {
		t.Errorf("expected 'failed' in message, got %q", result.Message)
	}
}

func TestDaytonaBinaryCheck_DaytonaVersionUnparseable(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeDaytona(t, fakeDir,
		"#!/bin/sh\necho 'some garbage output'\n",
		"@echo off\r\necho some garbage output\r\n",
	)

	t.Setenv("PATH", fakeDir)

	check := NewDaytonaBinaryCheck()
	ctx := &CheckContext{TownRoot: t.TempDir()}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning when daytona version unparseable, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "could not be parsed") {
		t.Errorf("expected parse failure detail in message, got %q", result.Message)
	}
}
