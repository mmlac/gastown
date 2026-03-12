# Execution Backend Abstraction Layer

> Design document for pluggable execution backends in Gas Town.
> Replaces the hardcoded tmux dependency with an interface-based architecture
> that supports local (tmux), remote (Daytona), and future backends.

## Motivation

Gas Town currently has tmux hardcoded throughout session management, dispatch,
and patrol systems. The Daytona integration (PR #2594, `feat/daytona-polecats`)
demonstrated that remote execution is viable — the engineering produced a
complete mTLS proxy, reconciliation state machine, and lifecycle manager across
~6,600 lines — but the integration threads through ~20 internal packages because
there is no execution backend abstraction.

The reviewer feedback identified three missing abstractions:

1. **ExecutionBackend interface** on SessionManager (tmux becomes one impl)
2. **DispatchTarget interface** in the sling system
3. **Pluggable patrol registry** in the daemon

This document specifies all three interfaces and the migration path to implement
them, informed by the existing Daytona code.

## Inventory: What the Daytona Branch Built

Before designing abstractions, here is what exists in `feat/daytona-polecats`
and should inform the new architecture:

### internal/daytona/ — Client, Reconciliation, Retry

| File | Lines | Purpose |
|------|-------|---------|
| `client.go` | 670 | CLI wrapper: workspace CRUD, snapshot lifecycle, exec, cert volumes |
| `reconcile.go` | 360 | Discovery of orphaned workspaces/beads, state machine cleanup |
| `retry.go` | 142 | Exponential backoff with transience classification |
| `client_test.go` | 1,041 | Workspace naming, options, lifecycle, pagination |
| `reconcile_test.go` | 1,091 | Discovery, state machine, zombie detection, cert revocation |
| `retry_test.go` | 417 | Transience classification, backoff, context cancellation |

**Key design decisions to preserve:**
- **Multi-tenancy scoping**: Workspace names `gt-<installID>-<rig>--<polecat>`
- **Transience classification**: Distinguishes retryable (OS, timeout) from
  permanent (auth, quota) failures — retry only on transient
- **Spawning bead protection**: Beads in "spawning" state skipped during
  reconciliation to prevent race with concurrent provisioning
- **Per-operation timeouts**: Each orphan gets independent deadline during
  reconciliation (30s default), preventing one slow op from starving others
- **Cert revocation ordering**: Revoke BEFORE bead reset (reset clears serial)
- **Idempotent operations**: Snapshot creation handles "already exists";
  deletion ignores "not found"

### internal/proxy/ — mTLS Proxy Server

| File | Lines | Purpose |
|------|-------|---------|
| `admin_client.go` | 137 | Cert issuance and revocation via admin API |
| `server.go` | ~500 | mTLS HTTP server: /v1/exec, /v1/git/, /v1/admin/ |
| `exec.go` | ~200 | Command execution with allowlisting |
| `git.go` | ~300 | Git smart-HTTP protocol bridge with ref authorization |

**Key design decisions to preserve:**
- **Command allowlisting**: Only pre-approved commands, resolved at startup
  via `exec.LookPath` to prevent PATH hijacking
- **Git push authorization**: Polecats can only push to
  `refs/heads/polecat/<name>-*` or `refs/heads/polecat/<name>/*`
- **Environment isolation**: Subprocesses get minimal env (HOME, PATH) plus
  identity injection (GT_PROXY_IDENTITY, GT_RIG, GT_POLECAT)
- **Nil-safe AdminClient**: Methods return nil on nil receiver, allowing
  graceful degradation when proxy isn't running

### internal/daemon/ — Integration Tests

| File | Lines | Purpose |
|------|-------|---------|
| `daytona_reconcile_test.go` | 306 | Workspace discovery, per-rig reconciliation |
| `daytona_restart_test.go` | 450 | Session restart, state machine, cert cleanup |
| `daytona_statemachine_test.go` | 592 | Full state transition testing with mock daytona |

### Dockerfile.daytona (106 lines)

Multi-stage build: Go builder → Debian Bookworm slim runtime with Node.js,
Go SDK, claude-code, and gt-proxy-client. Non-root user (uid 1000).

---

## Interface 1: ExecutionBackend

### Problem

`SessionManager` (internal/polecat/session_manager.go, 1074 lines) holds
`*tmux.Tmux` directly. All session operations — create, kill, env, capture,
health check, attach — go through tmux subprocess calls. The Daytona
integration added `daytonaClient` and `rigSettings` fields plus an
`isRemoteMode()` check, but still uses tmux as the session wrapper
(running `daytona exec` as a tmux command).

### Current coupling points

```
session_manager.go:
  Line 46:  tmux *tmux.Tmux                    ← struct field
  Line 270: m.tmux.HasSession(sessionID)        ← existence check
  Line 276: m.tmux.KillSessionWithProcesses()   ← killing
  Line 431: m.tmux.NewSessionWithCommand()      ← creation
  Line 486: m.tmux.SetEnvironment()             ← env management
  Line 520: m.tmux.WaitForCommand()             ← startup detection
  Line 691: m.tmux.CheckSessionHealth()         ← health checking
```

### Proposed interface

```go
// ExecutionBackend abstracts how agent sessions are created, managed,
// and observed. tmux is one implementation; Daytona, SSH, and local
// process backends are others.
type ExecutionBackend interface {
    // Lifecycle
    HasSession(ctx context.Context, id string) (bool, error)
    CreateSession(ctx context.Context, id string, opts CreateSessionOpts) error
    KillSession(ctx context.Context, id string, force bool) error

    // Environment
    SetEnv(ctx context.Context, id string, key, value string) error
    GetEnv(ctx context.Context, id string, key string) (string, error)

    // Interaction
    SendKeys(ctx context.Context, id string, keys string) error
    CaptureOutput(ctx context.Context, id string, lines int) (string, error)

    // Health & Readiness
    CheckHealth(ctx context.Context, id string) (HealthStatus, error)
    WaitReady(ctx context.Context, id string, timeout time.Duration) error

    // User attachment (interactive)
    Attach(ctx context.Context, id string) error

    // Enumeration
    ListSessions(ctx context.Context) ([]SessionInfo, error)
}

type CreateSessionOpts struct {
    WorkDir     string
    Command     string
    Env         map[string]string
    WindowName  string   // optional, backend-specific
}

type HealthStatus int
const (
    StatusHealthy   HealthStatus = iota
    StatusIdle
    StatusCrashed
    StatusNotFound
)

type SessionInfo struct {
    ID           string
    Created      time.Time
    LastActivity time.Time
    IsAttached   bool
}
```

### Implementations

**TmuxBackend** — wraps existing `internal/tmux.Tmux`:

```go
type TmuxBackend struct {
    tmux *tmux.Tmux
}

func (t *TmuxBackend) HasSession(_ context.Context, id string) (bool, error) {
    return t.tmux.HasSession(id)
}

func (t *TmuxBackend) CreateSession(_ context.Context, id string, opts CreateSessionOpts) error {
    return t.tmux.NewSessionWithCommand(id, opts.WorkDir, opts.Command)
}
// ... delegates all methods to tmux.Tmux
```

**DaytonaBackend** — wraps `internal/daytona.Client`:

```go
type DaytonaBackend struct {
    client      *daytona.Client
    proxyAdmin  *proxy.AdminClient
    rigSettings *config.RigSettings
}

func (d *DaytonaBackend) CreateSession(ctx context.Context, id string, opts CreateSessionOpts) error {
    // 1. Create or find workspace
    // 2. Issue mTLS cert via proxyAdmin
    // 3. Inject cert into workspace via daytona exec
    // 4. Start agent session via daytona exec with env vars
    return nil
}

func (d *DaytonaBackend) KillSession(ctx context.Context, id string, force bool) error {
    // 1. Stop workspace
    // 2. Revoke cert
    // 3. Archive workspace (if configured)
    return nil
}
```

### SessionManager migration

```go
// Before
type SessionManager struct {
    tmux          *tmux.Tmux
    daytonaClient *daytona.Client
    rigSettings   *config.RigSettings
    // ...
}

// After
type SessionManager struct {
    backend ExecutionBackend  // injected at construction
    // ...
}

func NewSessionManager(backend ExecutionBackend, r *rig.Rig) *SessionManager {
    return &SessionManager{backend: backend, rig: r}
}
```

The `isRemoteMode()` check disappears — the caller constructs either a
TmuxBackend or DaytonaBackend based on rig config and injects it.

---

## Interface 2: DispatchTarget

### Problem

`executeSling()` (internal/cmd/sling_dispatch.go) has three hardcoded dispatch
paths with different types:

```go
if rigName, isRig := IsRigName(target); isRig {
    spawnInfo, err := spawnPolecatForSling(rigName, ...)  // → SpawnedPolecatInfo
}
if dogName, isDog := IsDogTarget(target); isDog {
    dispatchInfo, err := DispatchToDog(dogName, ...)      // → DogDispatchInfo
}
agentID, pane, workDir, err := resolveTargetAgentFn(target)  // → (string, string, string)
```

Each path has different return types, different session start methods, and
different rollback logic. Adding a new dispatch type requires modifying
the dispatch switch in multiple places.

### Proposed interface

```go
// DispatchTarget represents a destination for slung work.
type DispatchTarget interface {
    // Identity
    AgentID() string        // "gastown/polecats/Toast", "deacon/dogs/alpha"
    TargetType() string     // "rig", "dog", "agent"
    WorkDir() string        // where bead files live

    // Lifecycle
    Prepare(ctx context.Context) error
    StartSession(ctx context.Context, opts StartOpts) (paneID string, err error)
    Rollback(ctx context.Context) error

    // State
    IsSessionRunning(ctx context.Context) (bool, error)
}

type StartOpts struct {
    FormulaEnv   map[string]string
    AgentCommand string
    AgentArgs    []string
}
```

### Implementations

```go
// RigTarget spawns a polecat in a rig
type RigTarget struct {
    rigName   string
    backend   ExecutionBackend
    spawnInfo *SpawnedPolecatInfo  // populated by Prepare()
}

func (r *RigTarget) Prepare(ctx context.Context) error {
    info, err := spawnPolecatForSling(r.rigName, r.spawnOpts)
    r.spawnInfo = info
    return err
}

func (r *RigTarget) StartSession(ctx context.Context, opts StartOpts) (string, error) {
    return r.spawnInfo.StartSession()
}

func (r *RigTarget) Rollback(ctx context.Context) error {
    return nukePolecatDir(r.spawnInfo)
}
```

```go
// DogTarget dispatches to a dog plugin worker
type DogTarget struct { ... }

// ExistingAgentTarget slings to an already-running agent
type ExistingAgentTarget struct { ... }
```

### Resolver

```go
// ResolveTarget creates the appropriate DispatchTarget from a target string.
func ResolveTarget(target string, backend ExecutionBackend) (DispatchTarget, error) {
    if rigName, ok := IsRigName(target); ok {
        return NewRigTarget(rigName, backend), nil
    }
    if dogName, ok := IsDogTarget(target); ok {
        return NewDogTarget(dogName, backend), nil
    }
    return NewExistingAgentTarget(target, backend)
}
```

### Unified dispatch flow

```go
func executeSling(ctx context.Context, target DispatchTarget, bead string) error {
    if err := target.Prepare(ctx); err != nil {
        return err
    }
    defer func() {
        if err != nil { target.Rollback(ctx) }
    }()

    cookFormula(bead)
    hookBead(bead, target.AgentID())

    _, err = target.StartSession(ctx, StartOpts{...})
    return err
}
```

---

## Interface 3: PatrolRegistry

### Problem

The daemon (internal/daemon/daemon.go, 2776 lines) has hardcoded patrol
checks using `IsPatrolEnabled()` — a 50+ line function with string matching:

```go
if IsPatrolEnabled(config, "dolt_remotes") { ... }
if IsPatrolEnabled(config, "dolt_backup") { ... }
if IsPatrolEnabled(config, "daytona_reconcile") { ... }
// ... 10+ more
```

Each patrol has its own config struct field in `PatrolsConfig`, its own
interval function, and its own execution block in the heartbeat loop.
Adding a new patrol requires touching 3+ locations.

### Proposed interface

```go
// PatrolHandler is implemented by each patrol type.
type PatrolHandler interface {
    Name() string
    Run(ctx context.Context, env PatrolEnv) error
    DefaultInterval() time.Duration
    RequiresRig() bool  // true = called once per rig; false = once per town
}

// PatrolEnv provides context to patrol handlers.
type PatrolEnv struct {
    TownRoot string
    RigName  string  // non-empty only when RequiresRig() == true
    Backend  ExecutionBackend
    Logger   *slog.Logger
}

// PatrolRegistry manages patrol registration and execution.
type PatrolRegistry struct {
    patrols map[string]registeredPatrol
}

type registeredPatrol struct {
    handler  PatrolHandler
    enabled  bool
    interval time.Duration
    rigs     []string  // optional rig filter
}

func (r *PatrolRegistry) Register(handler PatrolHandler, config *PatrolConfig) {
    r.patrols[handler.Name()] = registeredPatrol{
        handler:  handler,
        enabled:  config.Enabled,
        interval: config.IntervalOr(handler.DefaultInterval()),
    }
}

func (r *PatrolRegistry) RunEnabled(ctx context.Context, env PatrolEnv) {
    for _, p := range r.patrols {
        if !p.enabled { continue }
        p.handler.Run(ctx, env)
    }
}
```

### Built-in patrol registrations

```go
func DefaultRegistry() *PatrolRegistry {
    r := &PatrolRegistry{}
    r.Register(&WitnessPatrol{}, &PatrolConfig{Enabled: true})
    r.Register(&RefineryPatrol{}, &PatrolConfig{Enabled: true})
    r.Register(&DeaconPatrol{}, &PatrolConfig{Enabled: true})
    r.Register(&DoltRemotesPatrol{}, &PatrolConfig{Enabled: false})
    r.Register(&DoltBackupPatrol{}, &PatrolConfig{Enabled: false})
    r.Register(&DaytonaReconcilePatrol{}, &PatrolConfig{Enabled: false})
    // ... etc
    return r
}
```

The daemon heartbeat loop becomes:

```go
func (d *Daemon) heartbeat(ctx context.Context) {
    env := PatrolEnv{TownRoot: d.townRoot, Backend: d.backend, Logger: d.logger}
    d.registry.RunEnabled(ctx, env)
}
```

---

## Migration Plan

### Phase 1: ExecutionBackend interface (foundation)

1. Define `ExecutionBackend` interface in `internal/backend/`
2. Implement `TmuxBackend` wrapping existing `internal/tmux.Tmux`
3. Update `SessionManager` to accept `ExecutionBackend` instead of `*tmux.Tmux`
4. All callers construct `TmuxBackend` — no behavior change
5. **Tests**: Verify all existing tests pass with TmuxBackend wrapper

**Estimated scope**: ~15 files touched. The interface is thin; TmuxBackend
delegates 1:1 to existing tmux methods.

### Phase 2: DispatchTarget interface

1. Define `DispatchTarget` interface in `internal/dispatch/`
2. Implement `RigTarget`, `DogTarget`, `ExistingAgentTarget`
3. Create `ResolveTarget()` function
4. Refactor `executeSling()` to use `DispatchTarget`
5. Move spawn/dispatch logic into target implementations

**Estimated scope**: ~10 files. The dispatch logic already exists; this
restructures it behind an interface.

### Phase 3: PatrolRegistry

1. Define `PatrolHandler` interface and `PatrolRegistry` in `internal/patrol/`
2. Extract each patrol's logic into a handler implementation
3. Replace `IsPatrolEnabled()` with registry lookups
4. Refactor daemon heartbeat to iterate registry

**Estimated scope**: ~8 files. The patrol logic stays the same; the registry
replaces the switch/case dispatch.

### Phase 4: DaytonaBackend implementation

1. Implement `DaytonaBackend` satisfying `ExecutionBackend`
2. Move Daytona client, proxy, reconciliation code behind the backend
3. Backend selection in rig config: `execution_backend = "tmux" | "daytona"`
4. `DaytonaReconcilePatrol` uses the backend's reconciliation logic
5. Remove `daytonaClient` and `rigSettings` fields from SessionManager

**Estimated scope**: Primarily wrapping existing `internal/daytona/` code.
The client, reconcile, retry, and proxy code from `feat/daytona-polecats`
slots in directly as the backend implementation.

### Phase 5: Cleanup

1. Remove `isRemoteMode()` checks from SessionManager
2. Remove Daytona-specific fields from SessionManager struct
3. Audit all `m.tmux` references (should be zero after Phase 1)
4. Integration tests with both backends

---

## Backend Selection

Rig configuration determines which backend is used:

```json
{
  "execution_backend": "daytona",
  "remote_backend": {
    "provider": "daytona",
    "image": "ghcr.io/anthropics/gas-town-polecat:latest",
    "class": "medium",
    "proxy_addr": "proxy.example.com:8443",
    "network_block_all": true,
    "auto_stop_interval": "30m"
  }
}
```

The daemon reads `execution_backend` per rig and constructs the appropriate
backend:

```go
func backendForRig(rig *rig.Rig, settings *config.RigSettings) ExecutionBackend {
    switch settings.ExecutionBackend {
    case "daytona":
        return NewDaytonaBackend(settings.RemoteBackend)
    default:
        return NewTmuxBackend()
    }
}
```

Mixed-mode is supported: some rigs use tmux (local), others use Daytona
(remote). The daemon, sling, and patrol systems all receive the appropriate
backend per rig.

---

## What Stays, What Changes

### Preserved from feat/daytona-polecats

| Component | Status |
|-----------|--------|
| `internal/daytona/client.go` | Preserved as-is, used by DaytonaBackend |
| `internal/daytona/reconcile.go` | Preserved, called by DaytonaReconcilePatrol |
| `internal/daytona/retry.go` | Preserved, used by DaytonaBackend |
| `internal/proxy/` (all) | Preserved, proxy is backend-independent |
| `Dockerfile.daytona` | Preserved as polecat container image |
| `docs/daytona-backend.md` | Updated to reference new architecture |
| All test files | Preserved, tests are self-contained |

### Changed

| Component | Change |
|-----------|--------|
| `SessionManager` | `*tmux.Tmux` → `ExecutionBackend` interface |
| `executeSling()` | Three-path switch → `DispatchTarget.StartSession()` |
| `daemon.go` heartbeat | Hardcoded patrols → `PatrolRegistry.RunEnabled()` |
| `IsPatrolEnabled()` | Removed, replaced by registry |
| `isRemoteMode()` | Removed, backend selection at construction |

### New

| Component | Purpose |
|-----------|---------|
| `internal/backend/` | ExecutionBackend interface + TmuxBackend |
| `internal/dispatch/` | DispatchTarget interface + implementations |
| `internal/patrol/` | PatrolRegistry + PatrolHandler interface |
| `internal/backend/daytona.go` | DaytonaBackend (wraps internal/daytona/) |

---

## Open Questions

1. **Pane concept**: Tmux has panes; Daytona has exec sessions. Should the
   interface expose pane IDs or abstract them away? Current recommendation:
   abstract away — return a session-scoped identifier that backends can
   interpret (tmux pane ID, daytona exec PID, etc.).

2. **User attachment**: `tmux attach` is a terminal operation. For Daytona,
   attachment might mean `daytona exec -it` or opening a browser. Should
   `Attach()` be part of the core interface or a separate `InteractiveBackend`?

3. **Prompt detection**: `IsAtPrompt()` currently reads tmux pane content.
   Remote backends need an alternative readiness signal (process exit code,
   health endpoint, sentinel file).

4. **Volume management**: The Daytona implementation uses shared cert volumes.
   Should the ExecutionBackend interface expose volume operations, or should
   cert management be handled outside the backend?

5. **Patrol interval ownership**: Should intervals live in the registry config
   or on the PatrolHandler? Current design: handler provides default, config
   overrides.
