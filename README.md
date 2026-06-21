# singbox-panel

Personal sing-box proxy node management panel.

## Features

- User management with UUID-based proxy authentication
- Multi-node management (sing-box nodes via SSH)
- sing-box config generation with multi-user support
- Per-user subscription URLs (base64 encoded, compatible with v2rayN/sing-box/Clash Meta)
- SSH-based config push with validation (`sing-box check`)
- Raw config viewing and editing via SSH
- Traffic statistics per user per node
- Protocol support: VLESS Vision, Hysteria2, VLESS Reality

## Deployment

The panel serves **HTTP only** on a configurable port. For production use, place it behind a reverse proxy (Caddy, nginx) for TLS termination, or access directly via IP:port.

Example with Caddy:
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
ADMIN_TOKEN=your-secret-token-here
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

Environment variables (or `.env` file):

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `ADMIN_TOKEN` | `changeme` | Bearer token for admin API |
| `DATA_DIR` | `/opt/singbox-panel/data` | SQLite database directory |
| `SSH_KEY_PATH` | `/root/.ssh/id_ed25519` | SSH private key for node management |

## API

All admin endpoints require `Authorization: Bearer <ADMIN_TOKEN>`.

### Users
- `GET /api/users` — list users
- `POST /api/users` — create user
- `GET /api/users/{id}` — get user
- `PUT /api/users/{id}` — update user
- `DELETE /api/users/{id}` — delete user

### Nodes
- `GET /api/nodes` — list nodes
- `POST /api/nodes` — create node
- `GET /api/nodes/{id}` — get node with inbounds
- `PUT /api/nodes/{id}` — update node
- `DELETE /api/nodes/{id}` — delete node
- `POST /api/nodes/{id}/inbounds` — add inbound
- `DELETE /api/inbounds/{id}` — remove inbound

### Config Operations
- `POST /api/nodes/{id}/generate` — preview generated config (dry-run)
- `POST /api/nodes/{id}/push` — generate + push + restart
- `GET /api/nodes/{id}/raw-config` — read current config from node via SSH
- `PUT /api/nodes/{id}/raw-config` — write raw config to node via SSH

### Subscription (no auth)
- `GET /sub/{token}` — user subscription URL

### Traffic
- `POST /api/node/report` — node agent traffic report endpoint
- `GET /api/stats/users` — per-user traffic summary
- `GET /api/stats/nodes` — per-node traffic summary

## Building

```bash
go build -trimpath -ldflags="-s -w" -o singbox-panel .
```

## License

MIT
