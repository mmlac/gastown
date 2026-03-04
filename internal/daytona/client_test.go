package daytona

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockRunner records calls and returns preconfigured responses.
type mockRunner struct {
	calls []mockCall
	// responses keyed by "<command> <arg0> <arg1>..." — matched by prefix.
	responses map[string]mockResponse
	// defaultResponse is returned when no keyed response matches.
	defaultResponse mockResponse
	// interceptRun is called (if non-nil) with the context and args before returning.
	interceptRun func(ctx context.Context, name string, args ...string)
}

type mockCall struct {
	Name string
	Args []string
}

type mockResponse struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	m.calls = append(m.calls, mockCall{Name: name, Args: args})
	if m.interceptRun != nil {
		m.interceptRun(ctx, name, args...)
	}
	key := name + " " + strings.Join(args, " ")
	for prefix, resp := range m.responses {
		if strings.HasPrefix(key, prefix) {
			return resp.stdout, resp.stderr, resp.exitCode, resp.err
		}
	}
	return m.defaultResponse.stdout, m.defaultResponse.stderr, m.defaultResponse.exitCode, m.defaultResponse.err
}

func TestWorkspaceName(t *testing.T) {
	c := NewClientWithRunner("gt-abc12345", &mockRunner{})

	got := c.WorkspaceName("myrig", "onyx")
	want := "gt-abc12345-myrig--onyx"
	if got != want {
		t.Errorf("WorkspaceName() = %q, want %q", got, want)
	}
}

func TestParseWorkspaceName(t *testing.T) {
	c := NewClientWithRunner("gt-abc12345", &mockRunner{})

	tests := []struct {
		name        string
		input       string
		wantRig     string
		wantPolecat string
		wantOK      bool
	}{
		{
			name:        "valid simple",
			input:       "gt-abc12345-myrig--onyx",
			wantRig:     "myrig",
			wantPolecat: "onyx",
			wantOK:      true,
		},
		{
			name:        "rig with hyphen",
			input:       "gt-abc12345-my-rig--onyx",
			wantRig:     "my-rig",
			wantPolecat: "onyx",
			wantOK:      true,
		},
		{
			name:        "polecat with hyphen",
			input:       "gt-abc12345-myrig--bullet-farmer",
			wantRig:     "myrig",
			wantPolecat: "bullet-farmer",
			wantOK:      true,
		},
		{
			name:        "both with hyphens",
			input:       "gt-abc12345-my-rig--road-warrior",
			wantRig:     "my-rig",
			wantPolecat: "road-warrior",
			wantOK:      true,
		},
		{
			name:   "wrong prefix",
			input:  "gt-other123-myrig--onyx",
			wantOK: false,
		},
		{
			name:   "no delimiter",
			input:  "gt-abc12345-onlyrig",
			wantOK: false,
		},
		{
			name:   "single hyphen delimiter (old format)",
			input:  "gt-abc12345-myrig-onyx",
			wantOK: false,
		},
		{
			name:   "empty after prefix",
			input:  "gt-abc12345-",
			wantOK: false,
		},
		{
			name:   "empty polecat",
			input:  "gt-abc12345-myrig--",
			wantOK: false,
		},
		{
			name:        "rig with double hyphen",
			input:       "gt-abc12345-my--rig--onyx",
			wantRig:     "my--rig",
			wantPolecat: "onyx",
			wantOK:      true,
		},
		{
			name:   "completely different",
			input:  "some-other-workspace",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig, polecat, ok := c.ParseWorkspaceName(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ParseWorkspaceName(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if rig != tt.wantRig {
				t.Errorf("ParseWorkspaceName(%q) rig = %q, want %q", tt.input, rig, tt.wantRig)
			}
			if polecat != tt.wantPolecat {
				t.Errorf("ParseWorkspaceName(%q) polecat = %q, want %q", tt.input, polecat, tt.wantPolecat)
			}
		})
	}
}

func TestWorkspaceNameRoundTrip(t *testing.T) {
	c := NewClientWithRunner("gt-abc12345", &mockRunner{})

	name := c.WorkspaceName("testrig", "amber")
	rig, polecat, ok := c.ParseWorkspaceName(name)
	if !ok {
		t.Fatalf("ParseWorkspaceName(WorkspaceName()) returned ok=false")
	}
	if rig != "testrig" {
		t.Errorf("round-trip rig = %q, want %q", rig, "testrig")
	}
	if polecat != "amber" {
		t.Errorf("round-trip polecat = %q, want %q", polecat, "amber")
	}
}

func TestCreate(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Create(context.Background(), "gt-abc12345-rig--onyx", CreateOptions{
		Image: "ubuntu:22.04",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	call := mock.calls[0]
	if call.Name != "daytona" {
		t.Errorf("command = %q, want %q", call.Name, "daytona")
	}
	args := strings.Join(call.Args, " ")
	if !strings.Contains(args, "create") {
		t.Errorf("args missing 'create': %s", args)
	}
	if !strings.Contains(args, "--name gt-abc12345-rig--onyx") {
		t.Errorf("args missing --name: %s", args)
	}
	if !strings.Contains(args, "--image ubuntu:22.04") {
		t.Errorf("args missing --image: %s", args)
	}
	// Verify removed flags are NOT present
	if strings.Contains(args, "--branch") {
		t.Errorf("args should not contain --branch: %s", args)
	}
	if strings.Contains(args, "--yes") {
		t.Errorf("args should not contain --yes: %s", args)
	}
}

func TestCreateFailure(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stderr:   "Error: quota exceeded\nUsage: daytona create ...",
			exitCode: 1,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Create(context.Background(), "ws", CreateOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "quota exceeded")
	}
}

func TestStart(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Start(context.Background(), "ws-name")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	call := mock.calls[0]
	args := strings.Join(call.Args, " ")
	if !strings.Contains(args, "start ws-name") {
		t.Errorf("args = %q, want to contain 'start ws-name'", args)
	}
}

func TestStop(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Stop(context.Background(), "ws-name")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	call := mock.calls[0]
	args := strings.Join(call.Args, " ")
	if !strings.Contains(args, "stop ws-name") {
		t.Errorf("args = %q, want to contain 'stop ws-name'", args)
	}
}

func TestDelete(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Delete(context.Background(), "ws-name")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	call := mock.calls[0]
	args := strings.Join(call.Args, " ")
	if !strings.Contains(args, "delete ws-name") {
		t.Errorf("args = %q, want to contain 'delete ws-name'", args)
	}
}

func TestStartFailure(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stderr:   "Error: workspace not found\nUsage: daytona start ...",
			exitCode: 1,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Start(context.Background(), "ws-name")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "workspace not found") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "workspace not found")
	}
}

func TestStopFailure(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stderr:   "Error: timeout stopping workspace\nUsage: daytona stop ...",
			exitCode: 1,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Stop(context.Background(), "ws-name")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout stopping workspace") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "timeout stopping workspace")
	}
}

func TestDeleteFailure(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stderr:   "Error: cannot delete running workspace\nUsage: daytona delete ...",
			exitCode: 1,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Delete(context.Background(), "ws-name")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot delete running workspace") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "cannot delete running workspace")
	}
}

func TestExec(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stdout:   "hello world\n",
			exitCode: 0,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	stdout, stderr, exitCode, err := c.Exec(context.Background(), "ws-name",
		map[string]string{"FOO": "bar"},
		"echo", "hello",
	)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello world\n")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}

	call := mock.calls[0]
	args := strings.Join(call.Args, " ")
	if !strings.Contains(args, "exec ws-name") {
		t.Errorf("args missing 'exec ws-name': %s", args)
	}
	if !strings.Contains(args, "--env FOO=bar") {
		t.Errorf("args missing env: %s", args)
	}
	if !strings.Contains(args, "-- echo hello") {
		t.Errorf("args missing command after --: %s", args)
	}
}

func TestExecNonZeroExit(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stderr:   "command not found\n",
			exitCode: 127,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	_, stderr, exitCode, err := c.Exec(context.Background(), "ws", nil, "badcmd")
	if err != nil {
		t.Fatalf("Exec() should not return error for non-zero exit: %v", err)
	}
	if exitCode != 127 {
		t.Errorf("exitCode = %d, want 127", exitCode)
	}
	if !strings.Contains(stderr, "command not found") {
		t.Errorf("stderr = %q, want to contain 'command not found'", stderr)
	}
}

func TestListOwned(t *testing.T) {
	jsonOutput := `[
		{"id": "ws1", "name": "gt-abc12345-myrig--onyx", "state": "running"},
		{"id": "ws2", "name": "gt-abc12345-myrig--amber", "state": "stopped"},
		{"id": "ws3", "name": "gt-other000-theirrig--pearl", "state": "running"},
		{"id": "ws4", "name": "unrelated-workspace", "state": "running"}
	]`

	mock := &mockRunner{
		defaultResponse: mockResponse{
			stdout:   jsonOutput,
			exitCode: 0,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	workspaces, err := c.ListOwned(context.Background())
	if err != nil {
		t.Fatalf("ListOwned() error = %v", err)
	}

	if len(workspaces) != 2 {
		t.Fatalf("ListOwned() returned %d workspaces, want 2", len(workspaces))
	}

	// Verify first workspace
	if workspaces[0].Name != "gt-abc12345-myrig--onyx" {
		t.Errorf("workspaces[0].Name = %q", workspaces[0].Name)
	}
	if workspaces[0].State != "running" {
		t.Errorf("workspaces[0].State = %q, want %q", workspaces[0].State, "running")
	}
	if workspaces[0].Rig != "myrig" {
		t.Errorf("workspaces[0].Rig = %q, want %q", workspaces[0].Rig, "myrig")
	}
	if workspaces[0].Polecat != "onyx" {
		t.Errorf("workspaces[0].Polecat = %q, want %q", workspaces[0].Polecat, "onyx")
	}

	// Verify second workspace
	if workspaces[1].Name != "gt-abc12345-myrig--amber" {
		t.Errorf("workspaces[1].Name = %q", workspaces[1].Name)
	}
	if workspaces[1].Polecat != "amber" {
		t.Errorf("workspaces[1].Polecat = %q, want %q", workspaces[1].Polecat, "amber")
	}

	// Verify pagination args were used
	call := mock.calls[0]
	args := strings.Join(call.Args, " ")
	if !strings.Contains(args, "--page") {
		t.Errorf("ListOwned should use --page flag, got args: %s", args)
	}
	if !strings.Contains(args, "--limit") {
		t.Errorf("ListOwned should use --limit flag, got args: %s", args)
	}
}

func TestListOwnedEmpty(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stdout:   `[]`,
			exitCode: 0,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	workspaces, err := c.ListOwned(context.Background())
	if err != nil {
		t.Fatalf("ListOwned() error = %v", err)
	}
	if len(workspaces) != 0 {
		t.Errorf("ListOwned() returned %d, want 0", len(workspaces))
	}
}

func TestListOwnedEmptyStdout(t *testing.T) {
	for _, stdout := range []string{"", " ", "\n", "  \n  "} {
		t.Run(fmt.Sprintf("stdout=%q", stdout), func(t *testing.T) {
			mock := &mockRunner{
				defaultResponse: mockResponse{
					stdout:   stdout,
					exitCode: 0,
				},
			}
			c := NewClientWithRunner("gt-abc12345", mock)

			workspaces, err := c.ListOwned(context.Background())
			if err != nil {
				t.Fatalf("ListOwned() error = %v", err)
			}
			if len(workspaces) != 0 {
				t.Errorf("ListOwned() returned %d, want 0", len(workspaces))
			}
		})
	}
}

func TestListOwnedFailure(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stderr:   "connection refused",
			exitCode: 1,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	_, err := c.ListOwned(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListOwnedBadJSON(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stdout:   "not json",
			exitCode: 0,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	_, err := c.ListOwned(context.Background())
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parse JSON") {
		t.Errorf("error = %q, want to contain 'parse JSON'", err.Error())
	}
}

func TestInstallPrefix(t *testing.T) {
	c := NewClientWithRunner("gt-xyz99999", &mockRunner{})
	if c.InstallPrefix() != "gt-xyz99999" {
		t.Errorf("InstallPrefix() = %q, want %q", c.InstallPrefix(), "gt-xyz99999")
	}
}

func TestListOwnedPagination(t *testing.T) {
	// Page 1: 2 workspaces (== pageSize), Page 2: 1 workspace (< pageSize, stops).
	page1 := `[
		{"id": "ws1", "name": "gt-abc12345-rig--alpha", "state": "running"},
		{"id": "ws2", "name": "gt-abc12345-rig--beta", "state": "stopped"}
	]`
	page2 := `[
		{"id": "ws3", "name": "gt-abc12345-rig--gamma", "state": "running"}
	]`

	mock := &mockRunner{
		responses: map[string]mockResponse{
			"daytona list -f json --page 1": {stdout: page1, exitCode: 0},
			"daytona list -f json --page 2": {stdout: page2, exitCode: 0},
		},
	}

	c := NewClientWithRunner("gt-abc12345", mock)
	c.listPageSize = 2 // small page size so page 1 is "full"

	workspaces, err := c.ListOwned(context.Background())
	if err != nil {
		t.Fatalf("ListOwned() error = %v", err)
	}

	if len(workspaces) != 3 {
		t.Fatalf("ListOwned() returned %d workspaces, want 3", len(workspaces))
	}

	// Verify all three workspaces were collected across pages.
	names := make([]string, len(workspaces))
	for i, ws := range workspaces {
		names[i] = ws.Polecat
	}
	want := []string{"alpha", "beta", "gamma"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("workspace[%d].Polecat = %q, want %q", i, names[i], w)
		}
	}

	// Should have made exactly 2 calls (page 1 full, page 2 partial → stop).
	if len(mock.calls) != 2 {
		t.Errorf("expected 2 calls, got %d", len(mock.calls))
	}
}

func TestListOwnedPaginationStopsOnPartialPage(t *testing.T) {
	// If a page returns fewer than listPageSize entries, no more pages are fetched.
	mock := &mockRunner{
		responses: map[string]mockResponse{
			"daytona list -f json --page 1": {
				stdout:   `[{"id": "ws1", "name": "gt-abc12345-rig--onyx", "state": "running"}]`,
				exitCode: 0,
			},
			// Page 2 should never be called since page 1 had < listPageSize entries.
			"daytona list -f json --page 2": {
				stderr:   "should not reach page 2",
				exitCode: 1,
			},
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	workspaces, err := c.ListOwned(context.Background())
	if err != nil {
		t.Fatalf("ListOwned() error = %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("ListOwned() returned %d workspaces, want 1", len(workspaces))
	}
	if len(mock.calls) != 1 {
		t.Errorf("expected 1 call (partial page should stop pagination), got %d", len(mock.calls))
	}
}

func TestRunnerError(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			err: fmt.Errorf("exec: daytona not found"),
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Create(context.Background(), "ws", CreateOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

func TestCreateWithEnv(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.Create(context.Background(), "ws", CreateOptions{
		Env: map[string]string{"KEY": "val"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	args := strings.Join(mock.calls[0].Args, " ")
	if !strings.Contains(args, "--env KEY=val") {
		t.Errorf("args missing --env: %s", args)
	}
}

func TestCloneRepo(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{exitCode: 0},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.CloneRepo(context.Background(), "ws-name", "https://proxy:9876/v1/git/myrig", "main", "/workspaces/repo")
	if err != nil {
		t.Fatalf("CloneRepo() error = %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	args := strings.Join(mock.calls[0].Args, " ")
	if !strings.Contains(args, "exec ws-name") {
		t.Errorf("args missing 'exec ws-name': %s", args)
	}
	if !strings.Contains(args, "git clone") {
		t.Errorf("args missing 'git clone': %s", args)
	}
	if !strings.Contains(args, "--branch") {
		t.Errorf("args missing '--branch': %s", args)
	}
}

func TestCloneRepoFailure(t *testing.T) {
	mock := &mockRunner{
		defaultResponse: mockResponse{
			stderr:   "fatal: repository not found",
			exitCode: 128,
		},
	}
	c := NewClientWithRunner("gt-abc12345", mock)

	err := c.CloneRepo(context.Background(), "ws", "https://proxy/repo", "main", "/workspaces/repo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("error = %q, want to contain 'repository not found'", err.Error())
	}
}
