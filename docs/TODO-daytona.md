# Daytona Integration TODO

Audit of the current daytona implementation against the official Daytona CLI/SDK
documentation (`docs/llm-info/daytona-llms-full.txt`, generated 2026-02-27).

Items are grouped by severity. Each item includes: what is wrong, where the
affected code lives, and the recommended fix.

---

## 🔴 Bugs — Likely Broken Today

### 1. `daytona exec` does not support `--env` flag

**Affected code:**
- `internal/daytona/client.go` — `Exec()` method
- `internal/daemon/daemon.go` — `buildDaytonaExecCommand()` and its callers
  (`restartDaytonaPolecatSession`)
- `internal/polecat/session_manager.go` — `buildDaytonaCommand()` emits
  `exec daytona exec <ws> --env K=V ...`

**What is wrong:**
The current Daytona CLI reference for `daytona exec` lists only two flags:
`--cwd` and `--timeout`. There is no `--env` flag. The documented way to pass
environment variables via the CLI is shell syntax:
```bash
daytona exec my-sandbox -- sh -c 'KEY=VALUE cmd arg'
```
If `--env` is rejected by daytona exec, the container agents start without any
of the proxy/cert/GT_* environment variables — they cannot reach the proxy,
authenticate via mTLS, or interact with the gt back-end.

**Fix options (pick one):**
a. Wrap the agent command in a shell one-liner that inlines env vars:
   ```bash
   sh -c 'export KEY1=V1; export KEY2=V2; exec <agent-cmd>'
   ```
   Build this in `buildDaytonaExecCommand` / `buildDaytonaCommand` when there
   are env vars to inject.
b. Write an env file (e.g. `/run/gt/env`) into the workspace at creation time
   (via `writeFileInWorkspace`), then source it in the startup command.
c. Pass all env vars via `daytona create --env KEY=VALUE` at workspace-creation
   time so they are permanently baked into the container environment. This only
   works for static vars; `GT_RUN` (per-session UUID) would still need the
   shell-inline approach.

**Recommendation:** Use option (c) for the static cert/proxy vars (inject at
create time via `CreateOptions.Env`, which already passes `--env` to `create`,
and `create` does support `--env`). Use option (a) for the per-session `GT_RUN`
UUID.

---

### 2. `daytona list` uses `-o json` — flag should be `-f json`

**Affected code:**
- `internal/daytona/client.go` — `ListOwned()`
  ```go
  c.runner.Run(ctx, "daytona", "list", "-o", "json")
  ```

**What is wrong:**
The CLI reference shows `--format`/`-f` (not `-o`) as the output format flag for
`daytona list`. Using `-o` may cause the command to fail or output non-JSON text,
causing the JSON parse in `ListOwned` to return an error.

**Fix:** Change `-o` to `-f`:
```go
c.runner.Run(ctx, "daytona", "list", "-f", "json")
```
The same flag is also used by `daytona info --format json` and `daytona org list
--format json`; those paths should be consistent.

---

### 3. `daytona create` positional `repoURL` and `--branch` may not exist

**Affected code:**
- `internal/daytona/client.go` — `Create()`:
  ```go
  args := []string{"create", repoURL, "--name", name, "--branch", branch, "--yes"}
  ```

**What is wrong:**
The current CLI reference shows `daytona create [flags]` with no positional
argument and no `--branch` or `--yes` flags. The new daytona creates sandboxes
from images, snapshots, or Dockerfiles — not from git repos.

The old behaviour (clone a git repo) was a feature of the self-hosted daytona
(pre-cloud). If the target deployment uses the newer daytona cloud CLI, this call
will fail with an "unknown flag" or "unexpected argument" error, preventing any
polecat from being created.

**Fix:**
- Identify which version of daytona is expected (document in `INSTALLING.md` or
  `docs/daytona-backend.md`).
- If using the new cloud CLI: replace `Create()` with an image/snapshot-based
  approach. The git repo clone can be done inside the container as a post-create
  step via `daytona exec` (since workspaces already have git available).
- If staying on old self-hosted CLI: document the minimum supported version and
  add a `daytona version` check in `gt doctor`.

---

### 4. `--devcontainer-path` flag not in current CLI reference

**Affected code:**
- `internal/daytona/client.go` — `Create()`:
  ```go
  args = append(args, "--devcontainer-path", opts.DevcontainerPath)
  ```
- `internal/config/types.go` — `RemoteBackend.Profile` maps to this flag.

**What is wrong:**
`daytona create` flags in the CLI reference do not include `--devcontainer-path`.
If the flag no longer exists, passing it will cause `daytona create` to fail.

**Fix:** Verify against the installed CLI version. If the flag is gone, remove
the `Profile` field from `RemoteBackend` or replace with an equivalent option
(e.g. `--dockerfile`).

---

### 5. `--yes` flag not documented for start / stop / delete

**Affected code:**
- `internal/daytona/client.go` — `Start()`, `Stop()`, `Delete()` all append
  `"--yes"`.

**What is wrong:**
The CLI reference for `daytona start`, `daytona stop`, and `daytona delete` does
not list any `--yes` / `--force` / `--confirm` flags. In automated flows,
interactive prompts would hang the process.

**Fix:**
- Verify whether unattended operation is controlled by a config flag or an
  environment variable (e.g. `DAYTONA_NONINTERACTIVE=1`).
- If `--yes` is simply not needed (the new cloud CLI is non-interactive by
  default), remove it. Keeping an unknown flag may silently cause failures.

---

## 🟡 Functionality Gaps — Missing Features

### 6. `daytona create` auto-stop / auto-archive / auto-delete intervals not passed

**Affected code:**
- `internal/config/types.go` — `RemoteBackend` has boolean `AutoStop` and
  `AutoDelete` but no interval durations.
- `internal/daytona/client.go` — `CreateOptions` has no interval fields.
- `internal/polecat/manager.go` — `createOpts` does not set any lifecycle flags.

**What is wrong:**
`daytona create` supports three interval flags:
- `--auto-stop N` — stop after N minutes of inactivity (0 = disabled)
- `--auto-archive N` — archive N minutes after stopping (0 = max interval)
- `--auto-delete N` — delete N minutes after stopping (negative = disabled)

Without these flags, Daytona applies its own defaults. For long-lived agents, the
default auto-stop might kill the workspace prematurely; for short-lived polecats,
the default auto-archive might be too long.

**Fix:**
1. Extend `RemoteBackend` with optional interval fields:
   ```go
   AutoStopMinutes    *int `json:"auto_stop_minutes,omitempty"`    // nil = Daytona default
   AutoArchiveMinutes *int `json:"auto_archive_minutes,omitempty"` // nil = Daytona default
   AutoDeleteMinutes  *int `json:"auto_delete_minutes,omitempty"`  // nil = Daytona default
   ```
2. Pass them through `CreateOptions` and emit the corresponding `--auto-stop /
   --auto-archive / --auto-delete` flags in `Create()`.

---

### 7. No resource sizing (CPU / memory / disk / class)

**Affected code:**
- `internal/config/types.go` — `RemoteBackend` has no resource fields.
- `internal/daytona/client.go` — `CreateOptions` has no resource fields.

**What is wrong:**
`daytona create` supports `--class small/medium/large`, `--cpu N`, `--memory MB`,
and `--disk GB`. Without these, every polecat gets the provider's default class.
AI agent workloads are memory-heavy; using a `small` class by default may lead to
OOM kills.

**Fix:** Add resource fields to `RemoteBackend` and `CreateOptions`, emit the
appropriate flags in `Create()`.

---

### 8. Target region not configurable

**Affected code:**
- `internal/config/types.go` — `RemoteBackend`.
- `internal/daytona/client.go` — `CreateOptions`.

**What is wrong:**
`daytona create --target eu/us` allows choosing the geographic region where the
container runs. Latency and data-residency requirements may require this.

**Fix:** Add a `Target string` field to `RemoteBackend` / `CreateOptions` and
emit `--target <target>` in `Create()`.

---

### 9. Labels not used for workspace tagging or filtering

**Affected code:**
- `internal/daytona/client.go` — `Create()`, `ListOwned()`.

**What is wrong:**
`daytona create --label KEY=VALUE` attaches metadata to sandboxes. Labels allow
filtering via `daytona list` and SDK `findOne(labels={...})`. Currently the only
multi-tenancy guard is the name prefix (`installPrefix`). Labels offer a
complementary (and potentially more reliable) filtering mechanism.

**Fix:**
- Emit `--label gt-install-id=<shortID>` and `--label gt-rig=<rigName>` at
  creation time.
- Optionally use label-based filtering in `ListOwned` as a secondary check.

---

### 10. `ListOwned` has no pagination — will miss sandboxes beyond first page

**Affected code:**
- `internal/daytona/client.go` — `ListOwned()`.

**What is wrong:**
`daytona list` now supports `--limit` and `--page` flags. The default page size
is unknown; if a single Daytona organisation has many sandboxes, the first page
may not include all workspaces owned by this installation. The reconciliation loop
would fail to detect orphaned workspaces on subsequent pages.

**Fix:** Add pagination to `ListOwned`: fetch pages in a loop until an empty page
is returned.

---

### 11. No support for Daytona volumes (persistent cert storage)

**Affected code:**
- `internal/polecat/manager.go` — `injectCertsIntoWorkspace()` — re-injects
  certs every time via `daytona exec` base64 shell trick.

**What is wrong:**
Re-injecting certs at each workspace creation is fragile (requires exec to
succeed immediately after create) and slow. Daytona supports named volumes that
persist across sandbox restarts:
```shell
daytona volume create gt-certs-<installID>
daytona create --volume gt-certs-<installID>:/run/gt-proxy
```
Certs written to the volume are available instantly on every restart, and the
volume is shared (read-only) across polecats in the same installation.

**Fix:**
- During `gt prime` or first daytona polecat creation, create a shared cert
  volume and write certs into it.
- Pass `--volume gt-certs-<installID>:/run/gt-proxy` in `CreateOptions`.
- On cert rotation, update the volume content once rather than per-workspace.

---

### 12. No snapshot-based creation for faster cold-start

**Affected code:**
- `internal/daytona/client.go` — `Create()`.
- `internal/polecat/manager.go` — calls `Create` with an image.

**What is wrong:**
Every polecat creation pulls the image and runs post-create setup (`runGtPrime`,
cert injection). Daytona snapshots are pre-built images with all dependencies
already installed. Creating from a snapshot (`daytona create --snapshot
my-gt-snapshot`) eliminates the pull-and-build phase, cutting cold-start from
minutes to seconds.

**Fix:**
1. Add an optional `Snapshot string` field to `RemoteBackend` / `CreateOptions`.
2. Emit `--snapshot <snapshot>` in `Create()` instead of `--image` when set.
3. Document a `gt snapshot create` workflow for operators who want fast
   cold-starts (see `docs/daytona-backend.md`).

---

### 13. `daytona archive` not exposed — wasted cost during polecat downtime

**Affected code:**
- `internal/daytona/client.go` — no `Archive()` method.

**What is wrong:**
`daytona archive <sandbox>` moves the filesystem to object storage at reduced
cost while preserving state. Archives can be unarchived (started) on demand.
Currently, stopped workspaces keep their filesystem on hot storage until the
auto-archive interval fires. Explicitly archiving on polecat removal or after a
configurable idle period would reduce hosting cost.

**Fix:** Add `Archive(ctx, name string) error` to `Client` and call it from the
polecat manager when `RemoteBackend.AutoStop` is true and the polecat is being
retired.

---

### 14. Network isolation not configurable

**Affected code:**
- `internal/config/types.go` — `RemoteBackend` has no network fields.
- `internal/daytona/client.go` — `CreateOptions` has no network fields.

**What is wrong:**
`daytona create --network-block-all` and `--network-allow-list <CIDRs>` provide
container-level network isolation. This is a strong security control for untrusted
agent workloads: an agent that attempts to exfiltrate data via the network can be
blocked except for the explicitly allowed proxy CIDR.

**Fix:**
- Add `NetworkBlockAll bool` and `NetworkAllowList string` to `RemoteBackend`.
- Pass `--network-block-all` / `--network-allow-list` in `CreateOptions` /
  `Create()`.
- Document the security benefit in `docs/daytona-backend.md`.

---

### 15. `daytona info -f json` not used — reconcile relies only on `list` state

**Affected code:**
- `internal/daemon/daemon.go` — `restartDaytonaPolecatSession` uses
  `ListOwned` state.

**What is wrong:**
`daytona info -f json` returns richer per-sandbox state (network, last activity
time, resource usage). During reconcile and restart decisions, having the detailed
state could allow smarter choices (e.g. detect a zombie workspace that shows
`running` but has no active processes).

**Fix (low priority):** Add an `Info(ctx, name string)` method to `Client` that
calls `daytona info -f json <name>` and returns a richer struct. Use it in the
restart handler to make finer-grained state decisions.

---

## 🔵 Improvements & Polish

### 16. `daytona exec --cwd` not used for cert injection working directory

**Affected code:**
- `internal/polecat/manager.go` — `writeFileInWorkspace` does not pass `--cwd`.

**What is wrong:**  
The `daytona exec --cwd <path>` flag sets the working directory for the exec'd
command. This is not strictly necessary for cert injection (the command uses
absolute paths) but could simplify post-create scripts that assume a specific cwd.

**Fix (low priority):** Use `--cwd` in `Exec()` when the caller provides a
working directory. Expose it via `ExecOptions`.

---

### 17. Exec API: accept `ExecOptions` struct instead of loose env map

**Affected code:**
- `internal/daytona/client.go` — `Exec(ctx, name, env, cmd...)` signature.

**What is wrong:**
The CLI supports `--cwd` and `--timeout` in exec but the `Client.Exec()` API only
exposes `env`. Callers cannot set a working directory or timeout without changing
the method signature later.

**Fix:** Refactor to:
```go
type ExecOptions struct {
    Env     map[string]string
    Cwd     string
    Timeout time.Duration
}
func (c *Client) Exec(ctx context.Context, name string, opts ExecOptions, cmd ...string) (string, string, int, error)
```

---

### 18. `daytona version` check missing from `gt doctor`

**Affected code:**
- `internal/doctor/` — no daytona version check.

**What is wrong:**
Our `Create` and `Exec` methods make assumptions about CLI flag names that may
break across daytona versions. A `gt doctor` check that verifies `daytona
version` is within a known-good range would catch mismatches early.

**Fix:** Add a doctor check that runs `daytona version` and validates the output
against a minimum version. Emit a warning if the version is older or newer than
the tested range.

---

### 19. `reconcileDaytonaWorkspaces` runs only at daemon startup

**Affected code:**
- `internal/daemon/daemon.go` — `reconcileDaytonaWorkspaces()` called once in
  `Start()`.

**What is wrong:**
If a workspace is auto-deleted (inactivity timeout, resource limits) between
daemon restarts, the orphaned bead state is only cleaned up on the next crash
detection cycle, not proactively. A periodic reconcile (e.g., every 30 minutes)
would keep bead state consistent without waiting for a crash.

**Fix:** Schedule `reconcileDaytonaWorkspaces` as a recurring background task,
not just a one-shot startup check.

---

### 20. No metric for workspace start latency (cold-start vs warm-start)

**Affected code:**
- `internal/polecat/manager.go` — `AddPolecat` calls `Create` then sets up the
  session but does not record timing.

**What is wrong:**
Cold-starts (image pull + post-create) are much slower than warm restarts
(stopped → running). Without a metric, it is impossible to know if snapshot-based
creation (item #12) meaningfully reduces user-visible latency.

**Fix:** Record a `daytona_create_duration_seconds` histogram metric in the
polecat manager, labelled by `cold` vs `warm` (warm = workspace already existed,
only `Start` was called).

---

## Reference: Current vs Required Create Flags

| Feature | Our code today | Daytona create flag | Status |
|---|---|---|---|
| Name | `--name` | `--name` | ✅ OK |
| Image | `--image` | `--image` (via snapshot) | ⚠️ Verify |
| Snapshot | not used | `--snapshot` | 🔴 Missing |
| Env vars | `--env` | `--env` | ✅ OK (at create time) |
| Labels | not used | `--label` | 🟡 Missing |
| CPU/memory | not used | `--cpu / --memory / --disk / --class` | 🟡 Missing |
| Region | not used | `--target eu/us` | 🟡 Missing |
| Auto-stop interval | not set | `--auto-stop N` | 🟡 Missing |
| Auto-archive interval | not set | `--auto-archive N` | 🟡 Missing |
| Auto-delete interval | not set | `--auto-delete N` | 🟡 Missing |
| Volume mount | not used | `--volume NAME:PATH` | 🟡 Missing |
| Network isolation | not used | `--network-block-all` / `--network-allow-list` | 🟡 Missing |
| Repo URL (positional) | used | not in docs | 🔴 Verify version |
| `--branch` | used | not in docs | 🔴 Verify version |
| `--yes` / `--force` | used | not in docs | 🔴 Verify |
| `--devcontainer-path` | used | not in docs | 🔴 Verify |

## Reference: Exec Flags

| Feature | Our code today | Daytona exec flag | Status |
|---|---|---|---|
| Env vars via `--env` | used everywhere | **not listed in CLI docs** | 🔴 Likely broken |
| Working dir | not used | `--cwd` | 🟡 Unused |
| Timeout | not used | `--timeout` | 🟡 Unused |

## Reference: List Flags

| Feature | Our code today | Daytona list flag | Status |
|---|---|---|---|
| Format `-o json` | used | `-f json` / `--format json` | 🔴 Wrong short flag |
| Pagination | not used | `--page / --limit` | 🟡 Missing |
