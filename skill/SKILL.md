---
name: singbox-panel
description: 管理 singbox-panel 代理面板——用户管理（创建/启用/禁用/配额/权限）、节点管理（CRUD/配置推送/原始编辑）、订阅链接、流量统计。当用户提到代理面板、proxy panel、节点管理、用户流量、订阅链接、singbox-panel、panel.briqt.dev 时使用。也适用于：查看谁在用代理、给某人开通/关闭权限、推送配置到节点、查看流量统计。
---

# singbox-panel Skill

管理部署在 `panel.briqt.dev` 的代理节点面板。

## 连接信息

- **API 地址**: `https://panel.briqt.dev`
- **认证**: 所有 `/api/*` 管理接口需要 `Authorization: Bearer <ADMIN_TOKEN>`
- **Token 存储**: tokyo 服务器 `/opt/singbox-panel/.env` 中的 `ADMIN_TOKEN`

获取 token:
```bash
ssh tokyo 'grep ADMIN_TOKEN /opt/singbox-panel/.env' | cut -d= -f2
```

## 核心概念

### 权限模型

```
用户注册（enabled=false, 无节点权限）
  → 管理员启用（enabled=true）
    → 管理员分配节点（grant access）
      → 用户订阅显示可用节点
        → push config 后节点端认证该用户
```

- 用户默认禁用，需管理员启用
- 节点访问权限独立配置（grant/revoke per node）
- 流量超限或过期 → 自动从节点配置中排除 → 连接被拒
- push config 是使权限/用户变更生效的关键步骤

### 配置生效流程

面板数据变更（增删用户、改权限）不会立即影响节点。需要：
1. 修改面板数据（用户/权限/inbound）
2. `POST /api/nodes/{id}/push` 或 `POST /api/batch/push-all`
3. 节点 sing-box 重启，新配置生效

## API 参考

### 用户管理

```bash
# 列出所有用户
GET /api/users

# 创建用户（管理员直接创建，默认启用）
POST /api/users
Body: {"name":"username", "uuid":"可选，不填自动生成", "traffic_limit_bytes": 107374182400, "expire_at": "2027-01-01 00:00:00"}

# 启用/禁用用户
PUT /api/users/{id}
Body: {"enabled": true}

# 设置流量限额（bytes，0=无限）
PUT /api/users/{id}
Body: {"traffic_limit_bytes": 107374182400}

# 重置已用流量
POST /api/users/{id}/reset-traffic

# 重新生成订阅 token
POST /api/users/{id}/reset-sub-token

# 删除用户
DELETE /api/users/{id}
```

### 节点访问控制

```bash
# 查看用户可访问的节点
GET /api/users/{id}/access

# 授权用户访问指定节点
POST /api/users/{id}/access
Body: {"node_id": 1}

# 授权用户访问所有节点
POST /api/users/{id}/access
Body: {"all": true}

# 撤销指定节点权限
DELETE /api/users/{id}/access
Body: {"node_id": 1}

# 撤销所有节点权限
DELETE /api/users/{id}/access
Body: {"all": true}
```

### 节点管理

```bash
# 列出节点
GET /api/nodes

# 查看节点详情（含 inbounds）
GET /api/nodes/{id}

# 创建节点
POST /api/nodes
Body: {"name":"name", "host":"ip", "domain":"domain.briqt.dev", "port":22}

# 更新节点
PUT /api/nodes/{id}
Body: {"domain":"new.briqt.dev", "enabled": true}

# 添加协议
POST /api/nodes/{id}/inbounds
Body: {"tag":"hysteria2", "protocol":"hysteria2", "port":443, "settings":{...}}

# 删除协议
DELETE /api/inbounds/{id}
```

### 配置操作

```bash
# 预览生成的配置（不推送）
POST /api/nodes/{id}/generate

# 生成并推送配置到节点（会重启 sing-box）
POST /api/nodes/{id}/push

# 推送到所有 singbox 节点
POST /api/batch/push-all

# 读取节点当前原始配置
GET /api/nodes/{id}/raw-config

# 写入自定义配置（覆盖模板）
PUT /api/nodes/{id}/raw-config?restart=true
Body: <完整 JSON 配置>
```

### 订阅

```bash
# 用户订阅（base64 URI 格式，兼容 v2rayN/Shadowrocket）
GET /sub/{sub_token}

# Clash 格式（自动检测 UA 或手动指定）
GET /sub/{sub_token}?format=clash
```

### 统计

```bash
# 用户流量概览
GET /api/stats/users

# 节点流量概览
GET /api/stats/nodes
```

### 公开注册

```bash
# 用户自助注册（默认禁用，需管理员启用+授权节点）
POST /api/register
Body: {"username":"newuser", "password":"123456"}
```

## 常用操作组合

### 给朋友开通代理

```bash
# 1. 创建用户（100GB 限额，一年有效期）
POST /api/users {"name":"friend", "traffic_limit_bytes":107374182400, "expire_at":"2027-06-21 00:00:00"}
# 2. 授权所有节点
POST /api/users/{id}/access {"all":true}
# 3. 推送配置使生效
POST /api/batch/push-all
# 4. 给朋友发订阅链接
# https://panel.briqt.dev/sub/{sub_token}
```

### 某人流量超限，重置

```bash
POST /api/users/{id}/reset-traffic
POST /api/batch/push-all  # 重新加入节点配置
```

### 关闭某人的代理

```bash
PUT /api/users/{id} {"enabled":false}
POST /api/batch/push-all  # 从节点移除
```

### 查看系统状态

```bash
GET /api/stats/users   # 各用户流量
GET /api/stats/nodes   # 各节点流量
GET /api/users         # 所有用户列表
GET /api/nodes         # 所有节点列表
```

## 节点信息

| 节点 | IP | 协议 | 域名 |
|------|-----|------|------|
| tokyo | 154.83.93.162 | Vision+Hy2+Reality | jpiij.briqt.dev |
| laxpro | 23.158.136.46 | Vision+Hy2+Reality | laxpro.briqt.dev |
| lax02 | 192.129.134.166 | Vision+Hy2+Reality | lax02.briqt.dev |
| de | 2a01:4f8:242:1d0e::1461:158 | xray (WS+Reality) | de.briqt.dev |
| lax03 | 72.11.134.212 | Reality only | — |

## 部署信息

- 项目仓库: `github.com/briqt/singbox-panel`
- 运行位置: tokyo `/opt/singbox-panel/`
- 服务: systemd `singbox-panel.service`
- 反代: Caddy (`panel.briqt.dev` → `127.0.0.1:2082`)
- 数据库: SQLite at `/opt/singbox-panel/data/panel.db`

### 重新部署

```bash
cd /home/briqt/codes/singbox-panel
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/singbox-panel .
ssh tokyo 'systemctl stop singbox-panel'
scp bin/singbox-panel tokyo:/opt/singbox-panel/singbox-panel
ssh tokyo 'systemctl start singbox-panel'
```
