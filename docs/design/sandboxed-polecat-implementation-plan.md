# Plan: Daytona Polecat Lifecycle Management

Integrate daytona workspace management into the existing polecat lifecycle (`Manager` + `SessionManager`) so polecats can be deployed to remote cloud containers via rig-level configuration. The approach: add a `RemoteBackend` config to `RigSettings`, create an `internal/daytona/` package wrapping the daytona CLI, add an installation-scoped UUID to `TownConfig` for multi-tenant workspace discovery, and fork the existing lifecycle at strategic seams — `AllocateAndAdd` skips local worktrees, `SessionManager.Start` wraps commands in `daytona exec`, `SessionManager.Stop` stops workspaces, and daemon restart discovers owned workspaces by prefix-filtered `daytona list`.

---

## Phase 1: Foundation — Identity & Config (no behavioral changes)

**Step 1.1 — Installation UUID in `TownConfig`**

Add `InstallationID string` to `TownConfig` ([internal/config/types.go](internal/config/types.go)). Auto-generated (uuid v4) on first load if empty, persisted to `~/gt/config.json`. This UUID scopes all daytona workspace names to this GasTown installation — two developers sharing a daytona provider won't see or manage each other's workspaces.

Workspace naming convention: `gt-<installID-short>-<rig>-<polecat>` where `installID-short` = first 12 chars. Discovery: `daytona list` filtered by `gt-<installID-short>-` prefix.

**Files**: [internal/config/types.go](internal/config/types.go) (add field), [internal/config/loader.go](internal/config/loader.go) (auto-generate on load)

**Step 1.2 — `RemoteBackend` config in `RigSettings`**

This is the "harness for remote execution" — it tells the lifecycle where and how to deploy polecats.

```go
type RemoteBackend struct {
    Provider     string            `json:"provider"`               // "daytona" only initially
    Image        string            `json:"image,omitempty"`        // container image override
    Profile      string            `json:"profile,omitempty"`      // devcontainer profile
    AutoStop     bool              `json:"auto_stop,omitempty"`    // stop workspace on session end
    AutoDelete   bool              `json:"auto_delete,omitempty"`  // delete workspace on session end
    ProxyAddr    string            `json:"proxy_addr,omitempty"`   // proxy address override
    Env          map[string]string `json:"env,omitempty"`          // extra env for containers
}
```

When `RigSettings.RemoteBackend != nil`, the polecat lifecycle uses daytona instead of local worktrees. Per-rig granularity: some rigs local, others remote.

**Files**: [internal/config/types.go](internal/config/types.go) (add struct + field on `RigSettings`)

**Step 1.3 — `DaytonaWorkspaceName` on `Polecat`**

Add `DaytonaWorkspaceName string` to the `Polecat` struct ([internal/polecat/types.go](internal/polecat/types.go)). Empty for local polecats. Persisted via agent bead label `daytona_workspace`.

---

## Phase 2: Daytona Client Package

**Step 2.1 — `internal/daytona/client.go`** (new package)

Thin wrapper around the `daytona` CLI. All daytona interactions go through this client.

```go
type Client struct {
    installPrefix string  // "gt-<installID-short>"
}

type Workspace struct {
    ID, Name, State, Rig, Polecat string
}

// Lifecycle
func (c *Client) Create(ctx, name, repoURL, branch string, opts CreateOptions) error
func (c *Client) Start(ctx, name string) error
func (c *Client) Stop(ctx, name string) error
func (c *Client) Delete(ctx, name string) error
func (c *Client) Exec(ctx, name string, env map[string]string, cmd ...string) (string, string, int, error)

// Discovery (multi-tenancy core)
func (c *Client) ListOwned(ctx) ([]Workspace, error)  // filters by installPrefix
func (c *Client) WorkspaceName(rig, polecat string) string
func (c *Client) ParseWorkspaceName(name string) (rig, polecat string, ok bool)
```

**Files**: `internal/daytona/client.go` (new), `internal/daytona/client_test.go` (new)

---

## Phase 3: Lifecycle Integration (core work)

**Step 3.1 — Fork `AllocateAndAdd` for remote mode** *depends on Phase 1 + 2*

Current `AllocateAndAdd` ([internal/polecat/manager.go](internal/polecat/manager.go)) creates local worktrees. For daytona mode:

1. Allocate name from pool (unchanged)
2. Create lightweight **marker directory** `polecats/<name>/` (no worktree — just for name pool tracking)
3. **SKIP** `WorktreeAddFromRef` — container clones from proxy
4. Create branch in `.repo.git`: `git branch polecat/<name>-<ts> origin/main`
5. Issue mTLS cert: `proxy.CA.IssuePolecat("gt-<rig>-<polecat>", ttl)`
6. Create agent bead with `daytona_workspace` label
7. Provision daytona workspace:
   - `daytona create <proxy-git-url> --name <ws> --branch <branch>`
   - Inject cert via `daytona exec <ws> -- tee /run/gt-proxy/...`
   - Post-create setup: `daytona exec <ws> -- gt prime --write-prime-md`

Steps 5–7 are slow (30–120s), run after pool lock release but before per-polecat lock release.

**Key decision**: Marker directories mean `Manager.List()`, `ReconcilePool()`, and `DetectStalePolecats()` continue to work — they scan `polecats/*/` and the directories exist. But `ClonePath` for remote polecats is empty/sentinel, so code that reads from the worktree must check for remote mode.

**Files**: [internal/polecat/manager.go](internal/polecat/manager.go) (modify `AllocateAndAdd`, add `addDaytona` helper)

**Step 3.2 — Fork `SessionManager.Start` for remote mode** *depends on 3.1*

Current `Start` ([internal/polecat/session_manager.go](internal/polecat/session_manager.go)) creates tmux with a local command. For daytona mode:

1. Ensure workspace is running: `daytona.Client.Start(ws)` (idempotent)
2. Build command: `daytona exec <ws> --env GT_RIG=... --env GT_POLECAT=... --env GT_PROXY_URL=... -- claude --mode=direct`
3. Create tmux session with that command (unchanged tmux mechanics)
4. Add `daytona` to `GT_PROCESS_NAMES` (liveness sees `daytona exec` as the live process — design doc §5.2)
5. All nudge delivery, observation, and wait-for-agent work unchanged — daytona exec tunnels stdin/stdout

**Files**: [internal/polecat/session_manager.go](internal/polecat/session_manager.go) (modify `Start`)

**Step 3.3 — Fork `SessionManager.Stop` for remote mode** *parallel with 3.2*

1. Kill tmux session (same as now — terminates `daytona exec` connection)
2. If `AutoStop`: `daytona.Client.Stop(ws)` (preserves state for next session)
3. Deny polecat mTLS cert via proxy admin API (`POST /v1/admin/deny-cert`)

**Files**: [internal/polecat/session_manager.go](internal/polecat/session_manager.go) (modify `Stop`)

**Step 3.4 — Fork `RemoveWithOptions` for remote mode** *parallel with 3.2*

1. Safety checks (against remote state via proxy exec)
2. Reset agent bead (unchanged — Dolt)
3. **SKIP** worktree removal
4. Delete daytona workspace (if `AutoDelete`) or Stop
5. Remove marker directory, release name (unchanged)

**Files**: [internal/polecat/manager.go](internal/polecat/manager.go) (modify `RemoveWithOptions`)

---

## Phase 4: Discovery & Reconciliation on Restart

**Step 4.1 — Workspace discovery at daemon startup** *depends on Phase 2*

In daemon `Run()` ([internal/daemon/daemon.go](internal/daemon/daemon.go)), after lockfile acquisition, for each rig with `RemoteBackend`:

1. `daytona.Client.ListOwned(ctx)` → all workspaces matching `gt-<installID-short>-<rig>-*`
2. Cross-reference with agent beads (`bd list --role_type=polecat`)
3. **Orphaned workspaces** (no bead) → stop + optionally delete
4. **Orphaned beads** (no workspace) → reset bead
5. **Healthy matches** → ensure tmux session, restart if needed

Also exposed as `gt polecat discover --rig <rig> [--reconcile]`.

**Files**: `internal/daytona/reconcile.go` (new), [internal/daemon/daemon.go](internal/daemon/daemon.go) (call at startup)

**Step 4.2 — Heartbeat patrol integration** *depends on 4.1*

Existing heartbeat already detects dead sessions and auto-restarts. For daytona polecats:
- If tmux pane dead (daytona exec exited): restart with fresh `daytona exec`
- If workspace stopped: `daytona start` first, then `daytona exec`

No new patrol loop — the existing heartbeat handles it.

**Files**: [internal/daemon/daemon.go](internal/daemon/daemon.go) (modify `restartPolecatSession`)

---

## Phase 5: Proxy Integration

**Step 5.1 — Cert lifecycle in spawn/cleanup** *parallel with Phase 3*

- **Spawn**: `proxy.CA.IssuePolecat(cn, ttl)` → store serial in agent bead → inject into container
- **Cleanup**: Read serial from bead → `POST /v1/admin/deny-cert`
- No-op if proxy server isn't running (local-only mode)

**Files**: [internal/polecat/manager.go](internal/polecat/manager.go), [internal/polecat/session_manager.go](internal/polecat/session_manager.go)

---

## Phase 6: CLI Integration

**Step 6.1** — `gt sling --daytona` flag (or auto-detect from rig config). Preflight: verify daytona CLI, proxy server, CA exist.

**Step 6.2** — `gt polecat list` shows `BACKEND` column (local/daytona) and workspace name.

**Step 6.3** — `gt polecat discover --rig <rig> [--reconcile]` for manual workspace discovery.

---

## Relevant Files

| File | Change |
|------|--------|
| [internal/config/types.go](internal/config/types.go) | Add `InstallationID` to `TownConfig`, `RemoteBackend` struct + field on `RigSettings` |
| [internal/config/loader.go](internal/config/loader.go) | Auto-generate InstallationID on first load |
| [internal/polecat/types.go](internal/polecat/types.go) | Add `DaytonaWorkspaceName` to `Polecat` |
| [internal/polecat/manager.go](internal/polecat/manager.go) | Fork `AllocateAndAdd` and `RemoveWithOptions` for daytona |
| [internal/polecat/session_manager.go](internal/polecat/session_manager.go) | Fork `Start` and `Stop` for daytona |
| `internal/daytona/client.go` | **New**: daytona CLI wrapper |
| `internal/daytona/reconcile.go` | **New**: workspace discovery and reconciliation |
| [internal/daemon/daemon.go](internal/daemon/daemon.go) | Add `reconcileDaytonaWorkspaces` at startup, modify `restartPolecatSession` |
| [internal/proxy/ca.go](internal/proxy/ca.go) | Already exists — used for cert issuance |
| [internal/cmd/sling.go](internal/cmd/sling.go) | Add `--daytona` flag |
| [internal/cmd/polecat_spawn.go](internal/cmd/polecat_spawn.go) | Pass `RemoteBackend` through to Manager |

## Verification

1. **Unit**: Mock daytona CLI → test workspace naming, list filtering, name parsing
2. **Unit**: `AllocateAndAdd` with mock daytona client → no worktree created, agent bead has workspace label
3. **Integration**: Proxy → cert → daytona workspace → `gt prime` inside container → proxy receives request
4. **Discovery**: 3 workspaces with different install prefixes → `ListOwned` returns only matching
5. **Reconciliation**: Orphan workspace + orphan bead → reconcile fixes both
6. **E2E**: `gt sling <bead> --daytona` → polecat in container → nudge → `gt done` → workspace stopped → cert denied
7. **Multi-tenancy**: Two installations → each sees only its own workspaces
8. **Regression**: Full polecat test suite with `RemoteBackend = nil` → zero changes

## Decisions

- **Per-rig, not per-polecat**: Remote backend at rig level. All polecats in a rig use the same backend.
- **Marker directories**: Remote polecats still create `polecats/<name>/` (empty) so name pool, listing, and staleness detection work unmodified.
- **InstallationID for tenancy**: UUID on `TownConfig`, first-8-chars in workspace name prefix. `daytona list` + prefix filter is the discovery/ownership boundary.
- **Stop, not delete by default**: `AutoStop=true, AutoDelete=false` preserves workspace state for fast re-spawn. Deletion is explicit.
- **Manual proxy start initially**: Daemon doesn't auto-start proxy in Phase 1.
- **No new tmux abstractions**: `daytona exec` behaves like a local process (§5.1); no `SessionBackend` interface needed.

## Further Considerations

1. **Warm workspace pool** — First `daytona create` takes 30–120s. Pre-provisioned pool could reduce to ~5s. Recommendation: defer to follow-up after core integration works.
2. **Proxy auto-start** — Daemon starting `gt-proxy-server` automatically when `RemoteBackend` is configured. Recommendation: add in a later phase.
3. **Cert rotation** — For long-lived workspaces, daemon should re-issue certs before expiry. Recommendation: add expiry check to heartbeat patrol, re-issue + re-inject if < 1h remaining.
