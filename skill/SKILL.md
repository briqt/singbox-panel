---
name: singbox-panel
description: 管理 singbox-panel 代理面板——用户管理（创建/启用/禁用/配额/权限）、节点管理（安装/配置/推送/升级）、订阅链接、流量统计。当用户提到代理面板、proxy panel、节点管理、用户流量、订阅链接、singbox-panel、panel.example.com 时使用。也适用于：查看谁在用代理、给某人开通/关闭权限、推送配置到节点、查看流量统计、安装新节点。
---

# singbox-panel Skill

管理部署在 `panel.example.com` 的代理节点面板。

## 连接信息

- **API**: `https://panel.example.com`
- **Admin UI**: `https://panel.example.com/admin`
- **认证**: `Authorization: Bearer <TOKEN>`
- **获取 Token**: `ssh $(DEPLOY_HOST) 'grep ADMIN_TOKEN /opt/singbox-panel/.env' | cut -d= -f2`

## 支持的协议（仅此三种）

| 协议 | 用途 | 需要域名 | 特点 |
|------|------|---------|------|
| hysteria2 | 日常高速 | 是 | UDP/QUIC, 最快 |
| vless-reality | 稳定抗封 | 否 | TCP, 伪装正规网站, 零依赖 |
| vless-httpupgrade | CDN 中转 | 是 | 用于被墙IP/IPv6-only节点 |

## 核心流程

### 新增节点（一键）

```bash
# 1. 创建节点
POST /api/nodes
{"name":"my-node", "host":"1.2.3.4", "domain":"node.example.com"}

# 2. 配置 SSH（如果 tokyo 的 key 没在目标机器上）
POST /api/nodes/{id}/setup-ssh
{"password":"root密码"}

# 3. 安装 sing-box
POST /api/nodes/{id}/install
{"version":"latest"}  或 {"version":"1.13.8"}

# 4. 一键配置协议（自动选择、生成密钥、签证书、推送、启动）
POST /api/nodes/{id}/auto-setup
{"domain":"node.example.com", "mode":"direct"}
# direct → Hysteria2 + Reality
# cdn → HTTPUpgrade + Reality
# reality → Reality only
# 也可手动指定: {"protocols":["hysteria2","vless-reality","vless-httpupgrade"]}
```

### 用户管理

```bash
# 创建（管理员，默认启用）
POST /api/users  {"name":"friend", "traffic_limit_bytes":107374182400}

# 注册（公开，默认禁用）
POST /api/register  {"username":"someone", "password":"123456"}

# 启用
PUT /api/users/{id}  {"enabled":true}

# 授权所有节点
POST /api/users/{id}/access  {"all":true}

# 推送配置使生效
POST /api/batch/push-all

# 订阅链接
GET /sub/{sub_token}            # base64 (v2rayN/Shadowrocket)
GET /sub/{sub_token}?format=clash  # Clash Meta YAML
```

### 常用操作

```bash
# 查看所有节点状态
GET /api/nodes/{id}/status  → {reachable, installed, version, running}

# 升级 sing-box
POST /api/nodes/{id}/install  {"version":"latest"}

# DNS 校验
GET /api/validate/dns?domain=x.example.com&ip=1.2.3.4

# 手动签证书
POST /api/nodes/{id}/cert?domain=x.example.com

# 查看原始配置（只读）
GET /api/nodes/{id}/raw-config

# 用量统计（谁、哪天、用了哪个节点）
GET /api/stats/usage?from=2026-08-01&to=2026-08-06&group=day,user,node
GET /api/stats/usage?from=2026-08-01&to=2026-08-06&group=user   # 只看每人合计
GET /api/stats/users   # 累计计数器（配额判定用）
GET /api/stats/nodes

# 重置流量
POST /api/users/{id}/reset-traffic
```

## 完整 API 列表

### 用户
- `GET /api/users` — 列表
- `POST /api/users` — 创建
- `PUT /api/users/{id}` — 更新 (name/enabled/traffic_limit_bytes/expire_at/node_ids)，用户与权限合并保存并同步受影响节点；同步失败自动回滚
- `DELETE /api/users/{id}` — 先从节点移除用户，节点同步成功后删除；失败自动回滚
- `POST /api/users/{id}/reset-traffic` — 重置流量并同步节点；失败自动回滚
- `POST /api/users/{id}/reset-sub-token` — 重置订阅令牌

### 访问控制
- `GET /api/users/{id}/access` — 查看可访问节点
- `POST /api/users/{id}/access` — 授权 ({node_id} 或 {all:true})，自动同步节点，失败回滚
- `PUT /api/users/{id}/access` — 原子替换权限 ({node_ids:[1,2]})，自动同步节点，失败回滚
- `DELETE /api/users/{id}/access` — 撤销 ({node_id} 或 {all:true})，自动同步节点，失败回滚

### 节点
- `GET /api/nodes` / `POST /api/nodes` / `PUT /api/nodes/{id}` / `DELETE /api/nodes/{id}`
- `GET /api/nodes/{id}` — 详情含 inbounds
- `POST /api/nodes/{id}/inbounds` — 添加协议并立即同步节点，失败回滚
- `DELETE /api/inbounds/{id}` — 删除协议并立即同步节点，失败回滚

### 节点运维
- `GET /api/nodes/{id}/status` — SSH 连通性、sing-box 状态和各入站 TCP/UDP 监听状态
- `GET /api/nodes/{id}/version` — sing-box 版本
- `POST /api/nodes/{id}/setup-ssh` — 注入公钥 ({password})
- `POST /api/nodes/{id}/install` — 安装/升级 sing-box ({version})
- `GET /api/nodes/{id}/setup-assessment?mode=auto&domain=X` — 检测 DNS 并解释部署模式建议；不会仅凭 DNS 不一致认定为 CDN
- `POST /api/nodes/{id}/auto-setup` — 幂等配置和域名迁移 ({domain, mode, protocols, ports})；mode 支持 auto/direct/cdn/reality
- `POST /api/nodes/{id}/cert` — 签发证书 (?domain=)

### 配置
- `POST /api/nodes/{id}/generate` — 预览配置
- `POST /api/nodes/{id}/push` — 推送并重启
- `POST /api/batch/push-all` — 推送所有节点
- `GET /api/nodes/{id}/raw-config` — 只读查看已部署配置；不支持手动写入

### 校验
- `GET /api/validate/dns?domain=X&ip=Y` — DNS 解析校验

### 订阅
- `GET /sub/{token}` — 订阅 (自动识别客户端格式)
- `GET /sub/{token}?format=clash` — 强制 Clash 格式

### 统计
- `GET /api/stats/meta` — 统计时区、今天、可查询的最早一天（留存边界）
- `GET /api/stats/usage?from&to&group&user_id&node_id` — 唯一的聚合接口：`group` 可任意组合
  `day` / `user` / `node`（留空=总计一行），`from`/`to` 为面板时区的 `YYYY-MM-DD` 闭区间；
  超出留存的区间自动裁剪，整段过期则报错
- `GET /api/stats/users` — 用户累计计数器（配额判定用）
- `GET /api/stats/nodes` — 节点流量（留存窗口内）
- `GET /api/me/usage?from&to&group` — 同一套聚合，锁定为调用者自己

用量样本保留 **3 个自然月**（当月 + 前两个月），每天裁剪一次；查询超出边界会被拒绝。
时区由面板 `TIMEZONE` 决定（默认 `Asia/Shanghai`）。

### 公开
- `POST /api/register` — 用户注册 ({username, password})
- `GET /api/health` — 健康检查

## 节点信息

| 节点 | IP | 域名 | 状态 |
|------|-----|------|------|
| node-a | <node-ip> | node-a.example.com | 面板服务器 |
| node-b | <node-ip> | node-b.example.com | 运行中 |
| node-c | <node-ip> | node-c.example.com | 面板已接管 |
| de | <node-ipv6> | de.example.com | IPv6/ProxyJump |

## 部署

- 仓库: `github.com/briqt/singbox-panel`
- 服务: tokyo `/opt/singbox-panel/`, systemd `singbox-panel.service`
- 反代: Caddy `panel.example.com → 127.0.0.1:2082`
- 安装 skill: `npx skills add briqt/singbox-panel -g -y`
