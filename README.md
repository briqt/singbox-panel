# singbox-panel

Personal sing-box proxy node management panel. Full lifecycle: create node → SSH setup → install sing-box → auto-configure protocols → push config → manage users → subscriptions.

## Supported Protocols

| Protocol | Use Case | Requires Domain | Notes |
|----------|----------|----------------|-------|
| Hysteria2 | High speed | Yes | UDP/QUIC, adaptive congestion control |
| VLESS Reality | Domainless fallback | No | TCP, handshake target is probed from the node |
| VLESS HTTPUpgrade | CDN relay | Yes | For blocked IPs / IPv6-only nodes |

## Features

- **One-click node setup** — auto-selects protocols, generates keys, issues certs, pushes config
- **User management** — UUID-based auth, traffic limits, expiry, per-node access control
- **Multi-format subscriptions** — base64 (v2rayN/Shadowrocket), Clash Meta YAML (auto-detect via User-Agent)
- **Certificate management** — ACME via acme.sh with auto-renewal cron
- **Node health overview** — automatically checks SSH reachability, service state, version, resources, and per-inbound listeners when the node page opens
- **Admin Web UI** — i18n (zh/en), light theme, full node lifecycle controls
- **SSH-based operations** — key injection, sing-box install/upgrade, config push with validation
- **Per-user traffic accounting** — the panel polls each node's sing-box
  `v2ray_api` StatsService and attributes exact per-user uplink/downlink. This
  requires a sing-box binary built with the `with_v2ray_api` tag; `install`
  pulls such a build (see [briqt/sing-box](https://github.com/briqt/sing-box)),
  since upstream release binaries omit it.
- **Traffic enforcement** — over-limit users excluded from sing-box config (connection refused)

## Deployment

The panel serves **HTTP only** on a configurable port. Place behind a reverse proxy for TLS.

```
panel.example.com {
    reverse_proxy http://127.0.0.1:2082
}
```

### Quick Start

```bash
# Download latest release
wget https://github.com/briqt/singbox-panel/releases/latest/download/singbox-panel-linux-amd64

# Create working directory
mkdir -p /opt/singbox-panel/data

# Create config
cat > /opt/singbox-panel/.env << 'EOF'
PORT=2082
ADMIN_USER=admin
ADMIN_PASS=change-this-to-a-strong-password
JWT_SECRET=change-this-to-a-long-random-string
DATA_DIR=/opt/singbox-panel/data
SSH_KEY_PATH=/root/.ssh/id_ed25519
EOF

# Run
chmod +x singbox-panel-linux-amd64
./singbox-panel-linux-amd64
```

### systemd

Copy `deploy/singbox-panel.service` to `/etc/systemd/system/` and adjust paths.

## Configuration

Environment variables (or `.env` file in working directory):

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `ADMIN_USER` | `admin` | Admin login username |
| `ADMIN_PASS` | _(empty)_ | Admin login password. Empty disables admin login. |
| `JWT_SECRET` | _(random per start)_ | Signing key for session tokens. Set it explicitly so sessions survive a restart. |
| `DATA_DIR` | `/opt/singbox-panel/data` | SQLite database directory |
| `SSH_KEY_PATH` | `/root/.ssh/id_ed25519` | SSH private key for node management |

Authentication is username/password: `POST /api/login` with `ADMIN_USER`/`ADMIN_PASS`
returns a JWT used as `Authorization: Bearer <jwt>` for admin endpoints. Regular
users log in with their own credentials and see only their own subscription.

## Node Lifecycle

```
1. Create node          POST /api/nodes
2. Setup SSH            POST /api/nodes/{id}/setup-ssh
3. Install sing-box     POST /api/nodes/{id}/install
4. Assess the deployment mode with `GET /api/nodes/{id}/setup-assessment`
5. Auto-setup protocols with `POST /api/nodes/{id}/auto-setup`
6. Done — node is live, users can connect via subscription
```

Auto-setup logic:
- `auto` — chooses direct mode only when DNS resolves to the node; a possible
  CDN requires explicit confirmation
- `direct` — Hysteria2 + VLESS Reality, and DNS must resolve to the node
- `cdn` — VLESS HTTPUpgrade + VLESS Reality
- `reality` — VLESS Reality only; no domain required
- Manual override via `protocols` field

Reality handshake targets are tested from the node and the fastest TLS 1.3
candidate is selected. Existing Reality credentials and handshake settings are
preserved during repeat setup.

## API

All admin endpoints require `Authorization: Bearer <jwt>`, where the JWT comes
from `POST /api/login`.

### Users
- `GET/POST /api/users` — list / create
- `PUT/DELETE /api/users/{id}` — update / delete
  - update accepts optional `node_ids`; user fields and assignments are saved together
  - affected node configs are regenerated and pushed before the response returns
  - if any push fails, the database change is rolled back and the previous config is restored
  - per-node push status is returned in the `sync` field
- `POST /api/users/{id}/reset-traffic` — reset traffic counter and synchronize affected nodes
- `POST /api/users/{id}/reset-sub-token` — regenerate subscription token
- `POST /api/register` — public registration (disabled by default)

### Access Control
- `GET /api/users/{id}/access` — list accessible nodes
- `POST /api/users/{id}/access` — grant (`{node_id}` or `{all:true}`)
- `PUT /api/users/{id}/access` — atomically replace (`{node_ids:[1,2]}`)
- `DELETE /api/users/{id}/access` — revoke

Granting, replacing, or revoking access automatically synchronizes every
affected sing-box node. A failed synchronization rolls the access change back.
Existing access endpoints remain available for API compatibility; the admin UI
edits status and node access together.

### Nodes
- `GET/POST /api/nodes` — list / create
- `GET/PUT/DELETE /api/nodes/{id}` — get (with inbounds) / update / delete
- `POST /api/nodes/{id}/inbounds` — add inbound and synchronize the node
- `DELETE /api/inbounds/{id}` — remove inbound and synchronize the node

Inbound changes are rolled back when node synchronization fails. A node domain
used by Hysteria2 or HTTPUpgrade must be migrated through `auto-setup`; direct
domain edits are rejected so certificate paths and inbound settings cannot
become stale.

### Node Operations
- `GET /api/nodes/{id}/status` — SSH reachability, sing-box status, and per-inbound TCP/UDP listeners
- `GET /api/nodes/{id}/version` — sing-box version
- `POST /api/nodes/{id}/setup-ssh` — inject panel SSH key via password
- `POST /api/nodes/{id}/install` — install/upgrade sing-box
- `GET /api/nodes/{id}/setup-assessment?mode=auto&domain=X` — explain DNS and deployment-mode inference
- `POST /api/nodes/{id}/auto-setup` — idempotent protocol setup and domain migration (`mode`: `auto`, `direct`, `cdn`, or `reality`)
- `POST /api/nodes/{id}/cert-upload` — upload a TLS certificate + key (needed for CDN/HTTPUpgrade nodes)

Auto-setup issues Let's Encrypt certificates and verifies DNS on its own, so
there is no separate cert-issue or DNS-check endpoint.

### Config
- `POST /api/nodes/{id}/generate` — preview config (dry-run)
- `POST /api/nodes/{id}/push` — push + restart
- `POST /api/batch/push-all` — push all enabled nodes
- `GET /api/nodes/{id}/raw-config` — inspect deployed config (read-only)

### Subscription (no auth)
- `GET /sub/{token}` — auto-detect format by User-Agent
- `GET /sub/{token}?format=clash` — force Clash Meta YAML

### Stats
- `GET /api/stats/users` — per-user traffic
- `GET /api/stats/nodes` — per-node traffic

## Building

```bash
go build -trimpath -ldflags="-s -w" -o singbox-panel .
```

Cross-compile: `GOOS=linux GOARCH=amd64` or `GOARCH=arm64`.

## Agent Skill

Install the AI management skill globally:

```bash
npx skills add briqt/singbox-panel -g -y
```

Compatible agents (Claude Code, etc.) can then manage the panel through natural language.

## License

MIT
