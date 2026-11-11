---
name: singbox-panel
description: 管理 singbox-panel 代理面板——用户管理（创建/启用/禁用/配额/权限）、节点管理（安装/配置/推送/升级）、订阅链接、流量统计。当用户提到代理面板、proxy panel、节点管理、用户流量、订阅链接、singbox-panel 时使用。也适用于：查看谁在用代理、给某人开通/关闭权限、推送配置到节点、查看流量统计、安装新节点。
---

# singbox-panel Skill

管理自建的 sing-box 代理节点面板（面板地址与节点清单不入库，见下方「连接信息」）。

## 连接信息

- **API**: `https://<你的面板域名>`
- **Admin UI**: 同域根路径 `/`
- **认证**: `POST /api/login` 拿 JWT，后续请求带 `Authorization: Bearer <jwt>`
- **管理员凭据**: 面板机 `.env` 里的 `ADMIN_USER` / `ADMIN_PASS`

## 支持的协议（仅此三种）

| 协议 | 用途 | 需要域名 | 特点 |
|------|------|---------|------|
| hysteria2 | UDP 路径 | 是 | BBR 自适应；标准 QUIC + 内置页面，或 Salamander |
| vless-reality | 稳定抗封 | 否 | TCP, 伪装正规网站, 零依赖 |
| vless-httpupgrade | CDN 中转 | 是 | 用于被墙IP/IPv6-only节点 |

## 核心流程

### 新增节点（一键）

```bash
# 1. 创建节点
POST /api/nodes
{"name":"my-node", "host":"ssh.example.com", "domain":"node.example.com"}

# 2. 配置 SSH（面板机的公钥还没进目标机器时）。密码只用这一次、不入库；
#    这次连接同样固定主机密钥（TOFU），之后不一致即拒连
POST /api/nodes/{id}/setup-ssh
{"password":"root密码"}
# 2b. 公钥可用后关闭密码登录（先验证公钥，回读 sshd -T 与实际服务端提供的方式，失败自动回滚）
POST /api/nodes/{id}/ssh-harden

# 3. 安装 sing-box
POST /api/nodes/{id}/install
{"version":"recommended"}  或 {"version":"1.14.4"}

# 4. 一键配置协议（自动选择、生成密钥、签证书、推送、启动）
POST /api/nodes/{id}/auto-setup
{"domain":"node.example.com", "mode":"direct"}
# direct → Hysteria2 + Reality
# cdn → HTTPUpgrade + Reality
# reality → Reality only
# 也可手动指定: {"protocols":["hysteria2","vless-reality","vless-httpupgrade"]}
# 可选 hysteria2_profile: "quic" / "salamander"。省略时保留原模式，新节点默认 quic。
# quic 的 HTTP/3 页面内置在项目中，由 sing-box 直接返回，不另装网站服务。
# 模式变化后客户端必须刷新订阅；Chrome QUIC 指纹由客户端内核实现，服务端开关不能代替。
# 客户端 Reality 被重置、节点侧探测却成功时，可用 reality_sni 指定已实测目标（也可用
# POST /api/nodes/{id}/reality-target 单独换，只动目标不动密钥）。仍校验目标 TLS，保留原密钥。
```

### 用户管理

```bash
# 创建（管理员，默认启用）
POST /api/users  {"name":"friend", "traffic_limit_bytes":107374182400}

# 注册（公开，默认禁用）
POST /api/register  {"username":"someone", "password":"123456"}

# 启用
PUT /api/users/{id}  {"enabled":true}

# 授权指定协议线路，自动同步受影响节点（包括同节点内授权变化）
PUT /api/users/{id}  {"inbound_ids":[11,12]}

# 授权当前所有线路的快照，不包括未来新增线路
POST /api/users/{id}/access  {"all":true}

# 无需额外 push-all；授权接口自身负责同步及失败回滚

# 订阅链接
GET /sub/{sub_token}            # base64 (v2rayN/Shadowrocket)
GET /sub/{sub_token}?format=clash  # Clash Meta YAML
```

### 常用操作

```bash
# 查看所有节点状态
GET /api/nodes/{id}/status  → {reachable, installed, version, running}

# 升级 sing-box
POST /api/nodes/{id}/install  {"version":"recommended"}

# DNS 与部署模式评估（不改任何东西，只解释判定依据）
GET /api/nodes/{id}/setup-assessment?mode=auto&domain=node.example.com

# 证书：auto-setup 自动签发 Let's Encrypt；CDN 场景手动上传
POST /api/nodes/{id}/cert-upload  {"domain":"...","cert":"...","key":"..."}

# 证书续期（剩余不足 30 天才重签，重签会重启该节点 sing-box）
POST /api/nodes/{id}/cert-renew            # 到期才续
POST /api/nodes/{id}/cert-renew  {"force":true}   # 强制重签
# 返回每张证书的 before/after 到期时间与 status: renewed | fresh | failed
# 到期天数也在节点状态里：GET /api/nodes/{id}/status → inbounds[].cert.days_left

# 查看原始配置（只读）
GET /api/nodes/{id}/raw-config

# 用量统计（谁、哪天、用了哪个节点）
# 所有用户/节点代理用量、当前周期额度和订阅 upload/download 均已由服务端 ×2 折算。
# 历史也统一 ×2；配额上限不变，前端/调用方不可再乘。DB 仍保存原始字节，禁止 UPDATE 翻倍。
# 整机 /api/host-traffic 的网卡/服务/端口计数是实测值，不折算。
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
- `PUT /api/users/{id}` — 更新 (name/enabled/traffic_limit_bytes/expire_at/inbound_ids)，用户与具体线路权限合并保存并同步受影响节点；同步失败精确恢复旧授权。兼容 node_ids 仅作为当前入站快照，不给未来线路授权
- `DELETE /api/users/{id}` — 先从节点移除用户，节点同步成功后删除；失败自动回滚
- `POST /api/users/{id}/reset-traffic` — 重置流量并同步节点；失败自动回滚
- `POST /api/users/{id}/reset-sub-token` — 重置订阅令牌

### 访问控制
- `GET /api/users/{id}/access` — 返回获授权入站（含 id/node_id/protocol/name）
- `POST /api/users/{id}/access` — 授权 ({inbound_id} 或 {all:true})，自动同步节点，失败回滚
- `PUT /api/users/{id}/access` — 原子替换权限 ({inbound_ids:[11,12]})，自动同步节点，失败回滚
- `DELETE /api/users/{id}/access` — 撤销 ({inbound_id} 或 {all:true})，自动同步节点，失败回滚

授权表以具体 inbound ID 为准；首次迁移仅把旧节点授权展开成已有线路快照。新增线路默认不授权，服务端不监听无授权线路，订阅也不输出；以后重启不能恢复已撤销权限。旧 node_id/node_ids 请求只作为兼容的当前线路快照，不表示未来继承。

### 节点
- `GET /api/nodes` / `POST /api/nodes` / `PUT /api/nodes/{id}` / `DELETE /api/nodes/{id}`
- `GET /api/nodes/{id}` — 详情含 inbounds
- `POST /api/nodes/{id}/inbounds` — 添加自定义协议线路并同步节点，失败回滚；支持 name/tag/protocol/port/outbound。Reality 可省略 settings 自动生成独立身份，新增线路默认没有用户权限
- `PUT /api/inbounds/{id}` — 编辑线路，未传 settings 保留身份；outbound:null 改为直连；失败恢复库和远端
- `DELETE /api/inbounds/{id}` — 删除协议及其线路授权并同步节点，失败恢复二者

### 单条线路使用上游

新增独立 Reality：

```json
{"name":"示例节点-上游","tag":"reality-upstream","protocol":"vless-reality","port":2053,"outbound":{"type":"socks","server":"192.0.2.10","server_port":443,"username":"example-user","password":"example-password","network":"tcp"}}
```

只给新入站生成专属 outbound/route，原线路仍直出；认证失败或上游故障不得回落直连。出口只保存在服务端，不进入订阅。目前仅支持 SOCKS5 TCP，新 Reality 的 Clash 条目 udp:false，不加入 Auto 组；只有上游线权限时需在 Proxy 组手选。auto-setup/reprovision 不接管 custom 线路。配置推送会重启该节点 sing-box，原连接可能短暂重连。

创建后用 `PUT /api/users/{id}` 的完整 `inbound_ids` 列表追加新 ID，不要覆盖掉原授权。上线验证须包含新线路实际出口、错误认证拒绝、未授权用户拒绝，以及原线路配置/订阅保持不变；只看监听或 check 结果不够。

可复现配置入口为 `scripts/provision_upstream.py`：从 stdin 读取上面的 outbound JSON，凭据从 `--env-file` 中的 ADMIN_USER/ADMIN_PASS 读取，JWT 仅保存在内存。默认只读预览，显式 `--apply` 才创建及授权；同 tag 同配置重复执行不重建密钥。工具回读授权、原订阅条目和所有节点生成配置，失败清理本次新增线路与授权。

```bash
python3 scripts/provision_upstream.py --env-file /opt/singbox-panel/.env \
  --node-id 1 --user-id 1 --name '示例节点-上游' --port 2053 < /受保护目录/upstream.json
# 确认预览后同一命令加 --apply；凭据文件须处于私密目录且权限为 600。
```

### SS2022 落地（节点间链路）

把一台节点当作落地出口，其他节点各加入口线路、经 Shadowsocks 2022 转发过去。节点间链路不经过 GFW，不需要伪装，但必须加密并且承载 UDP，所以选 SS2022 而不选 SOCKS5 或 Reality。

- **落地入站**：`protocol: "shadowsocks"`，只供节点之间使用：不进订阅、不按用户授权启停，由 SS2022 密钥和 `allowed_sources` 来源白名单共同把关。白名单之外的来源会被显式 `reject`（路由 final 是 direct，只写放行规则等于不设白名单）；白名单不能为空，也不能是 `0.0.0.0/0`。密钥由面板生成，`method` 和 `password` 不可修改，白名单可以调整。落地入站本身直接出网，不能再串接上游。
- **展示名与订阅一致**：节点详情接口返回的 `display_name`，与订阅用的是同一个命名函数（`singbox.SubscriptionNames`），面板优先显示它；`TestSubscriptionNamesMatchSubscriptions` 守住这一点。
- **入口线路**：普通的 `hysteria2` 或 `vless-reality` 自定义线路，`outbound` 设为 `{"type":"shadowsocks","server":<落地 IP>,"server_port":…,"method":…,"password":…}`。`network` 留空时 TCP 和 UDP 都走落地（Clash 条目 `udp: true`），设为 `tcp` 则 UDP 直接失败。自定义 hysteria2 线路如果省略 `settings`，会沿用节点默认 hysteria2 的域名、证书和混淆参数。
- **落地不通就失败**：该入站的全部流量只路由到专属 SS 出口。落地宕机、密钥不符或来源被拒时，TCP 和 UDP 都直接失败，绝不改为从入口机直出。`TestCoreRelayLandingFailsClosed` 用真实内核证伪这一点：源站只监听回环，入口一旦直出就会被命中。
- **限制**：自定义 hysteria2 复制的是创建时的域名和证书路径。默认线路如果经 auto-setup 迁移了域名，需要重建这条线路。

可复现的配置入口是 `scripts/provision_relay.py`。它在面板机上运行，默认只做预览，加 `--apply` 才写入；重复执行时会复用一致的线路，不会重建密钥：

```bash
python3 scripts/provision_relay.py --env-file /opt/singbox-panel/.env \
  --landing-node-id 8 --landing-server 198.51.100.7 --relay-port 8443 --suffix laxcg \
  --entry 1:2053:2053 --entry 2:8443:8443 \
  --source 203.0.113.4 --source 203.0.113.5
```

`--entry` 的格式是 `节点ID:hysteria2端口:reality端口`；`--source` 填入口机连接落地时的真实源 IP。新线路命名为"订阅名 + 后缀"（如 `laxpro-Hy2-laxcg`），排在该节点原有线路之后。脚本**不碰任何授权**：新线路在授权前不监听，由操作者在面板里授权给需要的用户。

**时间同步是前提**：SS2022 会拒绝时间戳偏差超过约 30 秒的包。入口机时钟一旦偏了，它的落地线路会直接失败（按设计不会回落直连），落地机日志里表现为 `serve UDP ... bad`。机房屏蔽 UDP 123 时，NTP 永远同步不上，可改用基于 HTTP Date 头的 htpdate。上线后应从真实客户端验证：经新线路访问时，出口 IP 是落地机；把入口机移出白名单后，该线路必须失败，不能变成直出。

回滚旧版本时必须同时恢复匹配的升级前数据库备份；不能仅换回旧二进制，因为旧版本只认识节点级权限。

### 机器流量（整机网卡口径，区别于按用户的代理流量）

- `POST /api/nodes/{id}/traffic-audit` — 安装/升级节点采集器（auto-setup 末步也会装）。回读判据：真实采样一次、定时器 active、sing-box MainPID 不变
- `GET /api/nodes/{id}/traffic-audit` — 采集器健康（最近采样多久前、有无错误、版本是否最新）
- `DELETE /api/nodes/{id}/traffic-audit` — 卸载并恢复其改动
- `POST /api/batch/traffic-audit` — 全部启用节点逐个安装，200/207/502
- `GET /api/host-traffic?node_id=&hours=24` — 按小时结论：网卡/公网总量、按服务、按端口、TCP SYN/RST、公网对端 Top、完整度

节点只留 48h 分钟级缓冲（≤48 MiB，CPU 10%、内存 96M 上限）；面板每 10 分钟拉取已结束的小时，幂等写入。小时结论与代理用量同保留期（3 个自然月），对端明细 7 天。服务计数含回环与内网，不可相加当公网总量。

### 节点运维
- `GET /api/nodes/{id}/status` — SSH 连通性、sing-box 状态、各入站 TCP/UDP 监听状态，
  以及各 TLS 入站的证书到期天数（`inbounds[].cert`、`cert_days_left` 取最差的一张）
- `GET /api/nodes/{id}/version` — sing-box 版本
- `POST /api/nodes/{id}/setup-ssh` — 注入公钥 ({password})
- `POST /api/nodes/{id}/install` — 安装/升级 sing-box ({version})
- `GET /api/nodes/{id}/setup-assessment?mode=auto&domain=X` — 检测 DNS 并解释部署模式建议；不会仅凭 DNS 不一致认定为 CDN
- `POST /api/nodes/{id}/auto-setup` — 幂等配置和域名迁移 ({domain, mode, protocols, ports})；mode 支持 auto/direct/cdn/reality。
  幂等的含义是**重新求值、结果相同才跳过**：重跑会重新校验 Reality 握手目标（要求 h2 + 非错误状态码，
  不合格才换）并把端口收敛到常规 HTTPS 端口（443 → 8443/2053/2083/2087/2096，跳过节点上已占用的）。
  密钥对、short_id、用户 UUID 一律保留，所以重跑不会让已发出去的订阅失效——但**端口或握手目标一变，
  客户端必须重新拉一次订阅**。

### 配置
- `POST /api/nodes/{id}/generate` — 预览配置
- `POST /api/nodes/{id}/push` — 推送并重启
- `POST /api/batch/push-all` — 推送所有节点
- `POST /api/batch/reprovision` — 对所有启用节点重跑 auto-setup（协议加固、握手目标
  或端口策略升级后用它，不要手工 ssh 上机循环调单节点接口）。
  `{"mode":"auto","node_ids":[1,2],"dry_run":true}` 各字段均可省。
  **逐个串行**（每个节点会重启 sing-box，并行等于全网同时断）；
  部分成功返回 **207**、全部失败返回 **502**，只看状态码不会把"半个机群"误读成成功。
  先用 `dry_run: true` 看会动哪些节点。
  可带 `"upgrade":true,"version":"1.14.4","hysteria2_profile":"quic"`：逐台升级稳定版再重配。
  升级先验版本/统计标签/现有配置，重启后回读运行的二进制；失败回滚二进制。
  升级成功而后续重配失败时，保留已升级的二进制，分别看 `install` 与 `response`。
  **CDN 节点要单独跑一次**：默认 `mode:"auto"` 对橙云域名会返回 422（不肯只凭
  DNS 不一致就断定是 CDN，要人明确表态），所以 de 这类节点补一条
  `{"node_ids":[4],"mode":"cdn"}`。整机群跑出 207 而失败的恰好是 CDN 节点时，
  先看是不是这个原因，别当成故障。
- `GET /api/nodes/{id}/raw-config` — 只读查看已部署配置；不支持手动写入

### 证书
- `POST /api/nodes/{id}/cert-upload` — 上传证书 + 私钥（CDN / HTTPUpgrade 场景）；
  直连场景由 auto-setup 自动签发 Let's Encrypt 并配置续期
- `POST /api/nodes/{id}/cert-renew` — 重签 TLS 入站证书（`{"force":true}` 无视有效期）。
  只碰证书，不动端口 / UUID / Reality 密钥，所以不会让已发出去的订阅失效。
  签完从节点回读到期时间，据此判定成败，不看 acme.sh 的退出码

### Reality 握手目标（测速与更换）
- `GET /api/nodes/{id}/reality-targets` — 在**这台节点上**实测全部候选，只读：
  返回每个目标的 `http` / `status` / `latency_ms` / `qualifies`，合格者按延迟排序，
  未应答的也列出来并注明原因。`current` 标出正在用的那个。
  同一目标换个机房可能一个通一个不通：`www.deepseek.com` 从 laxpro 连不上，从 laxcc
  答 h2 + 200 / 0.12s；`www.kimi.com` 反过来在 laxcc 是 403、在东京是 200。
  所以这个问题只能按节点问，不能全局断言。
  加 `?host=www.deepseek.com`（可重复）可把候选表之外的域名也一起测。
- `POST /api/nodes/{id}/reality-target` — `{"sni":"www.bing.com","inbound_id":11}` 精确更换某条 Reality 的伪装目标；省略 inbound_id 只选择唯一非 custom 默认线路。
  先在该节点上探测，过不了 TLS 1.3 + HTTP/2 + 非错误状态码就返 422；通过后只改
  `sni` 与 `handshake_server`，**密钥对 / short_id / 端口 / UUID 全部原样**，再推配置。
  推送失败会把库里记录回滚成原目标——数据库说一套、节点跑一套比不改更糟。
  目标改了客户端要刷新订阅；Reality 的密钥没变，所以只是换了个伪装站，不是新节点。

### 换 IP

- 使用 `scripts/migrate_dns.py`，以 `CLOUDFLARE_DNS_TOKEN`、`CLOUDFLARE_ZONE_ID` 提供凭据。
  `--old-ip OLD --new-ip NEW` 先预览，`--apply` 才写入；只改精确匹配记录的地址并回读，保留 TTL/灰云和其他元数据。
- DNS 生效且机器可达后，把节点 `host` 改为稳定的管理域名，再通过 auto-setup/reprovision 更新配置。
  Hysteria2 订阅使用证书域名；Reality 经直连 DNS 验证才使用该域名，SNI 仍是握手目标。
- `scripts/audit_ip.py --ip OLD --host HOST` 只输出文件名和出现次数，不输出凭据；可重复传 `--host`、`--root`。
  SQLite 需经应用/只读查询检查，不做文件替换。不可达机器必须记为未检查。

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

节点清单（名称 / IP / 域名）不入库：面板自己就是权威源，用 `GET /api/nodes` 取。

## 部署

- 仓库: `github.com/briqt/singbox-panel`
- 服务: 面板机 `/opt/singbox-panel/`, systemd `singbox-panel.service`
- 反代: Caddy `panel.example.com → 127.0.0.1:2082`
- 部署: 在仓库根目录 `make deploy DEPLOY_HOST=<你的 ssh 主机别名>`
- 安装 skill: `npx skills add briqt/singbox-panel -g -y`
