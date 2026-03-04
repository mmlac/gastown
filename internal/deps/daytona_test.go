package deps

import "testing"

func TestParseDaytonaVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"v0.49.0", "0.49.0"},
		{"v0.49.0\n", "0.49.0"},
		{"0.49.0", "0.49.0"},
		{"Daytona version v0.55.0", "0.55.0"},
		{"daytona version 0.49.0", "0.49.0"},
		{"1.0.0", "1.0.0"},
		{"v10.20.30", "10.20.30"},
		{"some other output", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := parseDaytonaVersion(tt.input)
		if result != tt.expected {
			t.Errorf("parseDaytonaVersion(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestCheckDaytona(t *testing.T) {
	status, version, _ := CheckDaytona()

	if status == DaytonaNotFound {
		t.Skip("daytona not installed, skipping integration test")
	}

	if status == DaytonaOK && version == "" {
		t.Error("CheckDaytona returned DaytonaOK but empty version")
	}

	t.Logf("CheckDaytona: status=%d, version=%s", status, version)
}
