# singbox-panel

个人 sing-box 节点管理面板。一个仓、一个二进制、一个 SQLite 库，前端 SPA 由 `go:embed` 打进二进制，发布物是**单文件**。

全生命周期：建节点 → 注入 SSH 公钥 → 装 sing-box → 自动配协议 → 推配置 → 管用户 → 发订阅。

功能与部署说明见 [README.md](README.md)，操作手册见 [skill/SKILL.md](skill/SKILL.md)。本文是工程规范。

## 快速开始

```bash
make web      # 前端产物；go build / go test 之前必须先跑（go:embed 需要 web/dist 存在）
make test     # 质量闸：go test ./... + 前端 oxlint
make build    # 交叉编译 linux/amd64 到 bin/singbox-panel
make deploy DEPLOY_HOST=<ssh别名>   # 构建 + 上传 + 重启 systemd
```

前端单独开发：`cd web && corepack pnpm dev`（`/api`、`/sub` 代理到 `127.0.0.1:2082`）。

| 地址 | 是什么 |
|------|--------|
| `GET /api/health` | 存活探针。**只报进程活着**，不查任何依赖——依赖抖动不该触发重启 |
| `GET /api/ready` | 就绪探针。查数据库可达性，不可达返 **503** |
| `POST /api/nodes/{id}/tune` | 对存量节点重新应用内核基线并回读校验 |

## 仓库结构

```
.
├── AGENTS.md               # 本文件，工程规范
├── CLAUDE.md -> AGENTS.md  # 符号链接，从结构上保证两者不漂移
├── README.md               # 功能与部署
├── skill/SKILL.md          # Agent 操作手册（API 调用序列）
├── main.go                 # 路由装配、探针、SPA fallback
├── config/                 # 环境变量 → Config
├── db/                     # SQLite 打开与迁移
├── model/                  # 数据访问：user / node / access / traffic
├── handler/                # 业务层
│   ├── config.go           #   配置生成与推送、sshConnect
│   ├── sshpool.go          #   轮询器专用的 SSH 长连接池
│   ├── hostkey.go          #   节点主机密钥固定（TOFU）
│   ├── nodetune.go         #   节点内核基线 + 回读校验
│   ├── traffic.go          #   流量轮询器
│   ├── nodeops.go          #   装/升级/状态/tune
│   └── setup.go            #   一键纳管
├── singbox/                # sing-box 配置与 Clash 订阅生成
├── web/                    # React + Vite + antd SPA（dist 入库占位，node_modules 不入库）
└── deploy/                 # systemd 单元
```

## 工程规范

### 依赖与命令

- Go 版本以 `go.mod` 为准，CI 用 `go-version-file` 读它，不另写死。
- 前端依赖一律 pnpm，经 corepack 调用（版本锁在 `web/package.json` 的 `packageManager`），不要求全局安装。`package.json` 与 `pnpm-lock.yaml` 同一提交。
- **任何 `go build` / `go test` 之前必须先 `make web`**：`main.go` 的 `//go:embed all:web/dist` 在 `web/dist` 不存在时直接编译失败。

### 质量闸

推之前本地跑绿：

```bash
make test && go vet ./... && go test -race ./...
```

CI（`.github/workflows`）跑 `make test` 后交叉编译 linux 的 amd64 与 arm64。

### 判据

- **写入不等于生效，要回读。** `/etc/sysctl.conf` 在整个 `/etc/sysctl.d/` **之后**应用，遗留调优脚本会静默覆盖 drop-in。`nodetune.go` 因此逐键读回生效值，不符的进 `ineffective` 并指明覆盖来源。任何"改了远端配置"的操作都按这个模式写。
- **新增的检查/门禁，当轮做证伪测试。** 故意把它该抓的东西弄坏，确认真的报警。绿灯只证明被测的量在范围内，不证明要防的事没发生。`TestHostKeyRejectsMismatch` 是范例。
- **阈值只调到仍能捕捉回归的位置，不调到消音。** 见 `web/vite.config.ts` 的 `chunkSizeWarningLimit`。

## 关键不变量

### SSH 连接

- **轮询器走 `sshpool`，运维操作走 `sshConnect`。** 轮询每分钟触达每个节点，逐次新建连接会让节点侧反复拉起销毁 systemd user manager（实测一台节点每周约 26 万条日志）。运维操作低频且失败要直接反馈给操作者，用新连接更合适。
- **`sshPool.get` 返回的 client 不可 `Close`**，失败时调 `drop` 让下轮重拨。
- **主机密钥必须校验。** 面板以 root 连所有节点、推送含全部用户 UUID 与 Reality 私钥的配置、执行任意命令，且走公网。首次连接固定到 `<DATA_DIR>/known_hosts`（标准 OpenSSH 格式），此后不一致直接拒连。节点重建后用 `ForgetHostKey` 或 `ssh-keygen -R` 清除。
  - 遗留局限：首次连接若本身在被劫持链路上，固定的就是攻击者的密钥。要更强保证需从可信路径 `ssh-keyscan` 预置。

### 流量统计

- 走节点 sing-box 的 `v2ray_api` StatsService，`reset=true` 每次取增量，所以不需要基线跟踪，节点重启就是从零重新计数。
- 这要求 sing-box 用 `with_v2ray_api` tag 构建；上游 release 二进制**不含**该 tag，`install` 拉的是 [briqt/sing-box](https://github.com/briqt/sing-box) 的构建。

### 时区

`Config.Location` 是面板所有日历判断的唯一口径（流量样本归属哪一天、留存窗口从哪天算）。样本以 UTC 存储，只有这一处做转换。

## 部署

面板只服务 HTTP，TLS 交给反向代理。`make deploy` 会覆盖 `/etc/systemd/system/singbox-panel.service` 并重启。

发布前确认线上二进制可从 git 复现：本地 `make build` 后比对 sha256。
