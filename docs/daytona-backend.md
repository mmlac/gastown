# Daytona Remote Backend

Gas Town can run polecats in [Daytona](https://www.daytona.io/) cloud workspaces
instead of local git worktrees. Each polecat gets an isolated container with its
own filesystem, network, and process tree. Git traffic routes through a host-side
mTLS proxy — containers never contact GitHub directly.

This guide covers setup, configuration, and operations.

## Prerequisites

| Requirement | Notes |
|---|---|
| **`daytona` CLI** | Installed and authenticated (`daytona login`). See [installation docs](https://www.daytona.io/docs/en/getting-started/#cli). |
| **Gas Town installation** | A working `gt install` with at least one rig. |
| **`gt-proxy-server`** | Running on the host. Manages mTLS certs, proxies git, and relays `gt`/`bd` commands. See [proxy-server.md](proxy-server.md). |
| **Container image** | Must include `claude-code` (or your agent), `git`, and `gt-proxy-client` installed as both `/usr/local/bin/gt` and `/usr/local/bin/bd`. |

The proxy server generates a self-signed CA on first start at `~/.gt/.runtime/ca/`.
Polecat client certs are issued from this CA and injected into containers at spawn time.

## Quick Start

1. Start the proxy server:

   ```bash
   gt-proxy-server --town-root ~/gt
   ```

2. Configure the rig:

   ```bash
   # Edit <rig>/settings/config.json
   cat myrig/settings/config.json
   ```

   ```json
   {
     "remote_backend": {
       "provider": "daytona",
       "image": "ghcr.io/your-org/polecat-image:latest",
       "auto_stop": true,
       "auto_delete": false,
       "proxy_addr": "172.17.0.1:9876",
       "proxy_admin_addr": "127.0.0.1:9877"
     }
   }
   ```

3. Sling a bead:

   ```bash
   gt sling my-bead myrig
   ```

   Daytona mode is auto-detected from the rig config. No extra flags needed.

## Configuration Reference

The `remote_backend` block lives in `<rig>/settings/config.json` under the
`RigSettings` object. When present and non-null, all polecats for that rig
spawn as Daytona workspaces instead of local worktrees.

### Fields

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `provider` | string | **yes** | — | Must be `"daytona"`. Only supported provider. |
| `image` | string | no | Daytona default | Container image for workspaces. |
| `dockerfile` | string | no | — | Path to Dockerfile for sandbox snapshot (passed as `--dockerfile`). |
| `auto_stop` | bool | no | `false` | Stop the workspace when a polecat session ends. Preserves state for restart. |
| `auto_delete` | bool | no | `false` | Delete the workspace when the polecat is removed. Permanent. |
| `proxy_addr` | string | no | `localhost:8443` | Host:port of the mTLS proxy server (as reachable from containers). |
| `proxy_admin_addr` | string | no | `127.0.0.1:9877` | Host:port of the proxy admin API (localhost only, no TLS). |
| `env` | object | no | `{}` | Extra environment variables injected into every container for this rig. |

### Example: Minimal

```json
{
  "remote_backend": {
    "provider": "daytona"
  }
}
```

Uses Daytona defaults for everything. Proxy must be reachable at `localhost:8443`.

### Example: Full

```json
{
  "remote_backend": {
    "provider": "daytona",
    "image": "ghcr.io/your-org/gt-polecat:v2",
    "dockerfile": ".devcontainer/Dockerfile",
    "auto_stop": true,
    "auto_delete": false,
    "proxy_addr": "172.17.0.1:9876",
    "proxy_admin_addr": "127.0.0.1:9877",
    "env": {
      "ANTHROPIC_API_KEY": "sk-ant-...",
      "NODE_OPTIONS": "--max-old-space-size=4096"
    }
  }
}
```

### Per-Rig Granularity

Configuration is per-rig. Some rigs can use Daytona while others use local
worktrees. This is determined by whether `remote_backend` is present in each
rig's `settings/config.json`.

## How It Works

### Workspace Naming

Each workspace is named:

```
gt-<installID>-<rig>--<polecat>
```

- `<installID>` — first 8 characters of the town's `installation_id` (UUID v4,
  auto-generated on `gt install`)
- `<rig>` — rig name
- `<polecat>` — polecat name
- `--` — double-hyphen delimiter (allows single hyphens in rig/polecat names)

Example: `gt-a1b2c3d4-myrig--Toast`

The install prefix scopes all operations to this Gas Town installation, so
multiple developers sharing the same Daytona provider never see each other's
workspaces.

### Spawn Flow

When `gt sling <bead> <rig>` runs with a Daytona-configured rig:

1. **Preflight checks** — verifies `daytona` CLI is on PATH, proxy server is
   reachable (pings admin API), and CA cert/key exist.
2. **Workspace creation** — calls `daytona create <repoURL> --name <wsName>
   --branch <branch> --yes` with optional `--image` and `--dockerfile`.
   The `--yes` flag suppresses interactive confirmation prompts so the command
   runs unattended.
3. **Certificate injection** — issues an mTLS client cert from the proxy CA,
   then injects `client.crt`, `client.key`, and `ca.crt` into the container at
   `/run/gt-proxy/` via `daytona exec -- tee`.
4. **Session start** — launches `daytona exec <wsName> --env ... -- sh -c
   '<agent command>'` inside a local tmux pane. The local `daytona exec`
   process is the liveness signal.

### Git Access

Containers use the proxy's git endpoint instead of GitHub:

```
https://<proxy_addr>/v1/git/<rig>
```

The proxy serves the rig's `.repo.git` bare repository over git smart-HTTP.
Branch-scoped push authorization is enforced: a polecat cert with CN
`gt-<rig>-<name>` may only push refs under `polecat/<name>-*`.

These git environment variables are injected via `daytona exec --env`:

| Variable | Value | Purpose |
|---|---|---|
| `GIT_SSL_CERT` | `/run/gt-proxy/client.crt` | Client certificate for mTLS |
| `GIT_SSL_KEY` | `/run/gt-proxy/client.key` | Client private key |
| `GIT_SSL_CAINFO` | `/run/gt-proxy/ca.crt` | CA cert to verify proxy server |

### Command Proxy

The container's `gt` and `bd` binaries are actually `gt-proxy-client` — a thin
shim that forwards commands to the host proxy via `POST /v1/exec`. The proxy
authenticates the request via the polecat's mTLS cert and runs the real binary
on the host.

Allowed commands:

| Binary | Subcommands |
|---|---|
| `gt` | `prime`, `hook`, `done`, `mail`, `nudge`, `mol`, `status`, `handoff`, `version`, `convoy`, `sling` |
| `bd` | `create`, `update`, `close`, `show`, `list`, `ready`, `dep`, `export`, `prime`, `stats`, `blocked`, `doctor` |

### Session Lifecycle

| Event | Action |
|---|---|
| **Spawn** | Create workspace (`--yes`), inject certs, start `daytona exec` session |
| **Session end** | Kill tmux pane; if `auto_stop: true`, stop workspace (`--yes`) |
| **Polecat removal** | Revoke mTLS cert via admin API; if `auto_delete: true`, delete workspace (`--yes`) |
| **Idle** | Workspace stays running as a warm slot for the next sling (persistent polecat model) |

### Environment Variables in Containers

These are injected via `daytona exec --env`:

| Variable | Value |
|---|---|
| `GT_PROXY_URL` | `https://<proxy_addr>` |
| `GT_PROXY_CERT` | `/run/gt-proxy/client.crt` |
| `GT_PROXY_KEY` | `/run/gt-proxy/client.key` |
| `GT_PROXY_CA` | `/run/gt-proxy/ca.crt` |
| `GIT_SSL_CERT` | `/run/gt-proxy/client.crt` |
| `GIT_SSL_KEY` | `/run/gt-proxy/client.key` |
| `GIT_SSL_CAINFO` | `/run/gt-proxy/ca.crt` |
| `GT_RIG` | Rig name |
| `GT_POLECAT` | Polecat name |
| `GT_ROLE` | `<rig>/polecats/<polecat>` |
| `GT_TOWN_ROOT` | Town root path |
| `GT_RUN` | Session run ID |
| `BD_DOLT_AUTO_COMMIT` | `off` |

`GT_PROXY_CERT/KEY/CA` are read by `gt-proxy-client` to authenticate against the proxy. `GIT_SSL_CERT/KEY/CAINFO` are the git-native equivalents — git reads these directly for any HTTPS operation, so `git push origin` and `git fetch` use mTLS automatically without any extra configuration in `.gitconfig`.

Plus any entries from `remote_backend.env`.

## The `--daytona` Flag

You can force Daytona mode even without a `remote_backend` config:

```bash
gt sling my-bead myrig --daytona
```

This synthesizes a minimal `RemoteBackend{Provider: "daytona"}` with defaults.
Useful for one-off testing. The proxy must still be running.

When `remote_backend` is configured in the rig settings, the flag is not needed —
Daytona mode is auto-detected.

## Non-Interactive Operation (`--yes`)

All mutating `daytona` CLI commands (`create`, `start`, `stop`, `delete`) are
invoked with the `--yes` flag. This suppresses interactive confirmation prompts
so that Gas Town can drive workspace lifecycle unattended.

The flag is passed automatically by the Go client (`internal/daytona/client.go`)
— operators do not need to set any environment variable or configuration option.

| Command | Flag |
|---|---|
| `daytona create` | `--yes` |
| `daytona start` | `--yes` |
| `daytona stop` | `--yes` |
| `daytona delete` | `--yes` |

Commands that only read state (`daytona list`, `daytona exec`) do not prompt and
therefore do not need `--yes`.

## Discovery and Reconciliation

### Discovering Workspaces

List all Daytona workspaces owned by this Gas Town installation for a rig:

```bash
gt polecat discover myrig
```

This cross-references `daytona list` output (filtered by install prefix) against
polecat agent beads, producing a report:

| Status | Meaning |
|---|---|
| **healthy** | Workspace and bead are matched |
| **orphaned_workspace** | Workspace exists but no corresponding bead |
| **orphaned_bead** | Bead references a workspace that no longer exists |

### Auto-Reconciliation

```bash
gt polecat discover myrig --reconcile
```

- **Orphaned workspaces** are stopped (and deleted if `auto_delete` is set)
- **Orphaned beads** have their `daytona_workspace` label cleared

Preview what would happen:

```bash
gt polecat discover myrig --reconcile --dry-run
```

### Daemon Startup Reconciliation

The daemon runs `reconcileDaytonaWorkspaces()` on startup, catching any
workspaces that were left running after an unclean shutdown.

## Container Image Requirements

The container image must include:

1. **Your agent** — e.g., `claude-code` via `npm install -g @anthropic-ai/claude-code`
2. **`gt-proxy-client`** — installed as both `/usr/local/bin/gt` and
   `/usr/local/bin/bd` (symlinks to the same binary)
3. **`git`** — for repository operations through the proxy
4. **Standard POSIX tools** — `sh`, `tee`, `mkdir` (used during cert injection)

The proxy client binary detects proxy mode by checking for `GT_PROXY_URL`,
`GT_PROXY_CERT`, `GT_PROXY_KEY`, and `GT_PROXY_CA` environment variables. When
all four are set, it forwards commands to the proxy. Otherwise, it falls through
to the real binary at `/usr/local/bin/gt.real` (or `GT_REAL_BIN`).

## Proxy Server Setup

See [proxy-server.md](proxy-server.md) for full details. Quick reference:

```bash
# Start with defaults
gt-proxy-server --town-root ~/gt

# Custom listen addresses
gt-proxy-server \
  --town-root ~/gt \
  --listen 0.0.0.0:9876 \
  --admin-listen 127.0.0.1:9877
```

The proxy must be reachable from inside Daytona containers. If using Docker-based
Daytona, `172.17.0.1` (the Docker bridge gateway) is typically the right address
for `proxy_addr`. The `proxy_admin_addr` stays on localhost since only the host
needs admin access.

### TLS Details

- TLS 1.3, mutual authentication (`RequireAndVerifyClientCert`)
- CA: ECDSA P-256, self-signed, 10-year validity, CN `GasTown CA`
- Server cert: includes all local interface IPs as SANs
- Polecat certs: 30-day TTL, CN format `gt-<rig>-<name>`
- In-memory cert deny-list checked at TLS handshake

## Troubleshooting

### Preflight Failures

| Error | Fix |
|---|---|
| `daytona CLI not found` | Install from https://www.daytona.io/docs/installation/installation/ |
| `proxy server not reachable` | Start `gt-proxy-server --town-root <path>` |
| `CA certificate not found` | The proxy server generates the CA on first start. Ensure it has started at least once. |

### Container Cannot Reach Proxy

- Check that `proxy_addr` is reachable from inside the container network
- For Docker-based Daytona, use the Docker bridge IP (usually `172.17.0.1`)
- Verify firewall rules allow the proxy port

### Workspace Stuck or Orphaned

```bash
# See what Daytona knows about
gt polecat discover myrig

# Clean up orphans
gt polecat discover myrig --reconcile

# Manual cleanup
daytona stop <workspace-name>
daytona delete <workspace-name>
```

### Certificate Issues

- Certs are injected at spawn time into `/run/gt-proxy/` inside the container
- If a cert is revoked (polecat removed), the workspace's TLS connections will
  fail immediately at handshake
- The deny-list is in-memory only — restarting the proxy clears it

### Checking Workspace State

```bash
# List all workspaces for this installation
daytona list -f json | jq '.[] | select(.name | startswith("gt-"))'

# Check a specific workspace
daytona info <workspace-name>

# Execute a command in a workspace
daytona exec <workspace-name> -- whoami
```
