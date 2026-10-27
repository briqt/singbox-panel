# singbox-panel

Personal sing-box proxy node management panel. Full lifecycle: create node → SSH setup → install sing-box → auto-configure protocols → push config → manage users → subscriptions.

## Supported Protocols

| Protocol | Use Case | Requires Domain | Notes |
|----------|----------|----------------|-------|
| Hysteria2 | UDP path | Yes | Adaptive BBR; standard QUIC with an embedded HTTP/3 page, or Salamander |
| VLESS Reality | Domainless fallback | No | TCP, handshake target is probed from the node |
| VLESS HTTPUpgrade | CDN relay | Yes | For blocked IPs / IPv6-only nodes |

## Features

- **One-click node setup** — auto-selects protocols, generates keys, issues certs, pushes config
- **User management** — UUID-based auth, traffic limits, expiry, per-node access control
- **Multi-format subscriptions** — base64 (v2rayN/Shadowrocket), Clash Meta YAML (auto-detect via User-Agent)
- **Certificate management** — ACME via acme.sh with auto-renewal cron. Renewal
  decisions are made on the certificate's **expiry**, read back from the node,
  not on whether the files exist: an expired cert is a file like any other, and
  keying off existence let a node serve a dead certificate while reporting
  healthy. `POST /api/nodes/{id}/cert-renew` re-issues without touching ports,
  UUIDs or Reality keys; node status reports days remaining per inbound.
- **Node health overview** — automatically checks SSH reachability, service state, version, resources, and per-inbound listeners when the node page opens
- **Admin Web UI** — React + Vite + antd SPA compiled into the binary, i18n (zh/en), light theme, full node lifecycle controls
- **SSH-based operations** — key injection, sing-box install/upgrade, config push with validation
- **Per-user traffic accounting** — the panel polls each node's sing-box
  `v2ray_api` StatsService and attributes exact per-user uplink/downlink. This
  requires a sing-box binary built with the `with_v2ray_api` tag; `install`
  pulls such a build (see [briqt/sing-box](https://github.com/briqt/sing-box)),
  since upstream release binaries omit it.
- **Usage analytics** — per user, per day, per node in any combination: daily
  trend (stacked by user or node), per-user totals with the nodes they used,
  per-node totals with the users on them, and a day × user × node detail table
  with CSV export. Users see the same breakdown for their own account.
- **Node kernel baseline** — setup writes a sysctl drop-in tuned for the node's
  RAM (QUIC/UDP socket buffers for Hysteria2, BBR + fq for Reality) and then
  **reads the values back**, reporting any key that did not take effect.
  `/etc/sysctl.conf` is applied after everything in `/etc/sysctl.d`, so a stale
  tuning script there silently wins; writing the file is not evidence it
  applied. Re-run on an existing node with `POST /api/nodes/{id}/tune`.
- **Pinned SSH host keys** — a node's host key is recorded on first contact
  (`<DATA_DIR>/known_hosts`, standard OpenSSH format) and verified on every
  later connection; a mismatch refuses the connection. Clear a deliberately
  rebuilt node with `ssh-keygen -f <DATA_DIR>/known_hosts -R <host>`.
- **Traffic enforcement** — over-limit users excluded from sing-box config (connection refused)

## Deployment

The panel serves **HTTP only** on a configurable port. Place behind a reverse proxy for TLS.

```
panel.example.com {
    reverse_proxy http://127.0.0.1:2082
}
```

Probes: `GET /api/health` reports only that the process is up (a dependency blip
must not trigger a restart); `GET /api/ready` checks the database and answers
**503** when it is unreachable. `GET /api/version` reports the running build
(`version`, `commit`, `dirty`) — `make deploy` polls it to confirm the new
binary is actually serving, since a successful upload is not evidence that
systemd restarted into it.

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
| `TIMEZONE` | `Asia/Shanghai` | Calendar used by usage statistics: which day a sample belongs to and where the retention window starts. Samples themselves are stored in UTC. |

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

Reality handshake targets are tested from the node. A candidate qualifies only
if it answers over **HTTP/2 with a non-error status**; latency merely orders the
candidates that already pass. Ranking on latency alone selects CDN edges, and a
CDN edge is the wrong answer: REALITY forwards a failed probe to this target, so
a prober would see a big-site certificate on a server that speaks no h2 and
refuses the root path — an inconsistency the real site never shows.

A node-side probe cannot prove the client path works. If an otherwise qualified
target resets on the client network, `reality_sni` in setup/reprovision can select
an explicitly tested domain. It must still pass the TLS/HTTP probe and does not
rotate Reality keys. Verify the actual tunnel after applying it.

Repeat setup **re-evaluates** the stored handshake target and listen port rather
than skipping an inbound because it exists, so later improvements reach nodes
that were provisioned earlier. Reality credentials (keypair, short ID) and user
UUIDs are preserved. Refresh subscriptions after a change of address, port,
handshake target or Hysteria2 profile.

Hysteria2 subscriptions connect to the TLS domain, rather than the SSH IP.
Reality also uses that domain after setup verifies direct DNS; its camouflage
SNI remains independent. A CDN domain is never used as a Reality destination.
The SSH `host` accepts a domain too. DNS checks resolve both names and reject
mixed destinations (including a stale AAAA), rather than accepting one matching A.

`hysteria2_profile` selects `quic` or `salamander` in `auto-setup`:

- `quic` is the default for new nodes. sing-box itself serves the embedded
  [page](singbox/site.html) over HTTP/3 to unauthenticated visitors. No extra
  service or Caddy installation is needed. This is an HTTP/3 response, not a
  separate TCP website. sing-box 1.14 clients enable Chrome QUIC parroting by
  default; a URI subscription cannot enable this feature in other client cores.
- `salamander` wraps QUIC packets with a shared obfuscation password. It cannot
  serve ordinary browser QUIC. The server and both subscription formats use
  the same profile and password. Existing profiles/passwords are preserved
  unless explicitly changed.

Both profiles use adaptive BBR without a configured bandwidth ceiling. This
does not bypass provider/ISP shaping or an IP block. Port hopping, Mimic and
alpha-only transports are not enabled by default.

Listen ports are chosen from conventional HTTPS ports (443, then 8443/2053/2083/
2087/2096), skipping whatever the node already has bound — a panel host running
Caddy on 443 keeps it. A random high port is an anomaly no protocol can paper
over: no Apple or Microsoft edge serves TLS on port 31795.

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
  (`version`: `recommended`/empty = reviewed **1.14.4**, `latest` = latest
  upstream fork release, or a pinned stable `x.y.z`). The candidate must report
  the requested version, include `with_v2ray_api`, and accept the current config
  before replacing the binary. Running nodes restart and verify the executable;
  a failed restart restores the previous binary.
- `GET /api/nodes/{id}/setup-assessment?mode=auto&domain=X` — explain DNS and deployment-mode inference
- `POST /api/nodes/{id}/auto-setup` — idempotent protocol setup and domain migration (`mode`: `auto`, `direct`, `cdn`, or `reality`)
- `POST /api/nodes/{id}/cert-upload` — upload a TLS certificate + key (needed for CDN/HTTPUpgrade nodes)

Auto-setup issues Let's Encrypt certificates and verifies DNS on its own, so
there is no separate cert-issue or DNS-check endpoint.

### Config
- `POST /api/nodes/{id}/generate` — preview config (dry-run)
- `POST /api/nodes/{id}/push` — push + restart
- `POST /api/batch/push-all` — push all enabled nodes
- `POST /api/batch/reprovision` — re-run auto-setup across enabled nodes
  (`{"mode":"auto","node_ids":[1,2],"dry_run":true}`; all fields optional).
  Nodes are processed **one at a time** — each one restarts sing-box, so a
  parallel run would take the whole fleet down together. Answers **207** when
  only some nodes succeed and **502** when none do, so a caller that checks
  only the status code cannot read a partial rollout as a complete one.
  Start with `dry_run` to see which nodes a run would touch.
  Add `"upgrade":true,"version":"1.14.4"` to upgrade each node before setup;
  failed upgrades stop that node's setup. `"hysteria2_profile":"quic"` explicitly
  migrates its UDP appearance. Already upgraded binaries remain upgraded if
  a later setup step fails; the per-node `install` and `response` report both stages.

  Rolling a protocol change across every node is an operation the panel owns,
  not something to drive with ad-hoc SSH loops: those exist only in one
  terminal session, cannot be re-run, and tend to park an admin token on disk.
- `GET /api/nodes/{id}/raw-config` — inspect deployed config (read-only)

### Subscription (no auth)
- `GET /sub/{token}` — auto-detect format by User-Agent
- `GET /sub/{token}?format=clash` — force Clash Meta YAML

### Stats
- `GET /api/stats/meta` — timezone, today, and the oldest queryable day (`retention_from`)
- `GET /api/stats/usage?from&to&group&user_id&node_id` — the one aggregation endpoint:
  `group` takes any combination of `day`, `user`, `node` (empty = a single total row),
  `from`/`to` are inclusive `YYYY-MM-DD` days in panel time. A range reaching past
  retention is clamped; a range entirely past it is rejected.
- `GET /api/stats/users` — cumulative per-user counters (used against the quota)
- `GET /api/stats/nodes` — per-node traffic over the retained window
- `GET /api/me/usage?from&to&group` — same aggregation, locked to the caller's account

Usage samples are kept for **3 calendar months** (the current month plus the two
before it) and pruned daily; the stats API refuses to answer beyond that boundary,
so charts never imply data the panel no longer has.

## Changing a node IP

Keep the same public domain and certificates when an address changes. Migrate
DNS before updating the panel's management host and re-running setup:

```bash
# Requires CLOUDFLARE_DNS_TOKEN and CLOUDFLARE_ZONE_ID in the environment.
# Default: preview all records whose content exactly matches the old address.
python3 scripts/migrate_dns.py --old-ip 203.0.113.1 --new-ip 203.0.113.2
# Add --name node.example.com to restrict the scope; --apply writes and reads back.
python3 scripts/migrate_dns.py --old-ip 203.0.113.1 --new-ip 203.0.113.2 --apply

# Read-only filename audit, using existing SSH auth/host-key verification.
python3 scripts/audit_ip.py --ip 203.0.113.1 --host node --host other-node
```

The DNS script changes only address content, preserving proxy state, TTL and
metadata. CNAME aliases follow automatically. It rejects concurrent changes
and failed read-back; Cloudflare read-back does not prove resolver propagation
or network reachability. A/AAAA records from the other IP family are unchanged.
Never blanket-replace IPs in network configuration, SQLite files or SSH host keys.
Unreachable hosts remain unaudited; repeat their audit once networking recovers.

## Building

The admin SPA lives in `web/` (React + Vite + TypeScript + antd, pnpm) and is
embedded into the binary, so it must be built first:

```bash
make web    # cd web && pnpm install && pnpm build  → web/dist
go build -trimpath -ldflags="-s -w" -o singbox-panel .
```

`make build` does both and cross-compiles for linux/amd64; `make deploy` builds and
ships it to the panel host. `make test` runs `go test ./...` plus the frontend lint.
It also runs the standard-library Python DNS migration tests. On a Linux test
host, `sh scripts/check_core.sh` downloads the reviewed fork and exercises both
Hysteria2 profiles through a real client/server pair on temporary loopback ports.
For frontend work, `pnpm dev` in `web/` proxies `/api` to a panel running on :2082.

Cross-compile: `GOOS=linux GOARCH=amd64` or `GOARCH=arm64`.

## Agent Skill

Install the AI management skill globally:

```bash
npx skills add briqt/singbox-panel -g -y
```

Compatible agents (Claude Code, etc.) can then manage the panel through natural language.

## License

MIT
