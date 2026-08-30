# singbox-panel

个人 sing-box 节点管理面板。一个仓、一个二进制、一个 SQLite 库，前端 SPA 由 `go:embed` 打进二进制，发布物是**单文件**。

全生命周期：建节点 → 注入 SSH 公钥 → 装 sing-box → 自动配协议 → 推配置 → 管用户 → 发订阅。

功能与部署说明见 [README.md](README.md)，操作手册见 [skill/SKILL.md](skill/SKILL.md)。本文是工程规范。

## 快速开始

```bash
make web      # 前端产物；go build / go test 之前必须先跑（go:embed 需要 web/dist 存在）
make test     # 质量闸：go test ./... + 前端 oxlint
make build    # 交叉编译 linux/amd64 到 bin/singbox-panel，版本戳写进二进制
make deploy DEPLOY_HOST=<ssh别名>   # 构建 + 上传 + 重启 + 回读 /api/version 确认新构建在服务
```

发布走 tag：`git tag vX.Y.Z && git push origin vX.Y.Z`，CI 交叉编译 linux 的 amd64/arm64 并发 Release。

前端单独开发：`cd web && corepack pnpm dev`（`/api`、`/sub` 代理到 `127.0.0.1:2082`）。

| 地址 | 是什么 |
|------|--------|
| `GET /api/health` | 存活探针。**只报进程活着**，不查任何依赖——依赖抖动不该触发重启 |
| `GET /api/ready` | 就绪探针。查数据库可达性，不可达返 **503** |
| `POST /api/nodes/{id}/tune` | 对存量节点重新应用内核基线并回读校验 |
| `POST /api/nodes/{id}/cert-renew` | 重签 TLS 入站的证书并回读校验，不动端口与 Reality 密钥（`{"force":true}` 可无视有效期强制重签） |
| `POST /api/batch/reprovision` | 对所有启用节点重跑 auto-setup，逐个串行；部分成功 207、全失败 502，`{"dry_run":true}` 先看会动谁 |
| `GET /api/version` | 当前运行的构建（version/commit/dirty）。免鉴权，`make deploy` 用它确认新二进制真的在服务 |

## 仓库结构

```
.
├── AGENTS.md / CLAUDE.md   # 本文件，工程规范（两份独立文件，内容必须一致，改一处要同步另一处）
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
│   ├── cert.go             #   TLS 证书签发/续期/到期回读（单一权威）
│   ├── nodeops.go          #   装/升级/状态/tune/cert-renew
│   ├── setup.go            #   一键纳管
│   └── reprovision.go      #   批量重跑 auto-setup（全网协议升级走这里）
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
- **文件存在不等于内容有效。** 证书曾用 `[ -f cert ]` 判断"已就绪"，于是一张过期两个月的证书照样让 auto-setup 报成功、让节点在面板上显示健康，而所有 hysteria2 客户端都握手失败。判据要落在被检查的**性质**上（`openssl -checkend`），不是承载它的文件上。同理监听着的端口不代表入站可用，`healthState` 因此把证书过期排在监听检查之前。
- **凭据/CA 这类"记在别处"的状态，改默认值不会改已有记录。** `acme.sh --set-default-ca` 只影响新域名；已有域名记着自己的 `Le_API`，在别的 CA 上建的记录会一直回那个 CA 续期，若其账户凭据从未存过（ZeroSSL 要 EAB）就永久失败。因此 `--issue` 必须显式带 `--server`，`TestRenewScriptPinsCAOnIssue` 守这条。
- **静默的定时任务等于没有。** acme.sh 不设 `LOG_FILE` 就不写日志，`--install-cronjob` 装的 cron 又把 stdout 丢进 `/dev/null`，续期失败五周无人知晓直到证书过期。装计划任务时一并把日志落地。
- **新增的检查/门禁，当轮做证伪测试。** 故意把它该抓的东西弄坏，确认真的报警。绿灯只证明被测的量在范围内，不证明要防的事没发生。`TestHostKeyRejectsMismatch` 是范例。
- **证伪要证伪到"值"，`strings.Contains` 会放水。** `obfs-password=<pw>` 是 `obfs-password=<pw>x` 的前缀，所以把客户端密码改错、用 `Contains` 断言的测试照样绿。第一版 `TestHysteria2ObfsPasswordReachesEveryClientFormat` 就是这么骗过证伪的。断言配置项一律解析出**值**再比（URI 走 `url.Parse` 取 query，YAML 走整行匹配），别拿子串糊弄。
- **测得快不等于伪装得像。** `selectRealitySNI` 原本只按 `time_appconnect` 排序挑最快的候选，于是 CDN 边缘必胜——三个直连节点全选中 `updates.cdn-apple.com`，而它答 HTTP/1.1 加 403，跟它冒充的站行为对不上。REALITY 会把探测流量回落到这个目标，所以判据得落在**行为可信度**（h2 + 非错误状态码）上，延迟只配在合格者之间做排序。
- **"已存在就跳过"会把配置永久冻在第一次的取值上。** `auto-setup` 曾对已有的 Reality 入站无条件 `skipped`，结果任何后续改进都够不着最需要它的那批节点。幂等应当是"重新求值，结果相同才跳过"，不是"存在就不看"。重配时要区分**可换的**（端口、握手目标）与**换了就废掉已发订阅的**（密钥对、short_id、UUID），后者必须原样保留。
- **"部署命令退出码 0"不等于新版本在服务。** systemd 可能重启的还是旧二进制，新的也可能启动即死、留着旧进程继续应答。`make deploy` 因此以 `verify-deploy` 收尾：轮询 `/api/version`，直到服务自报的 commit 与本次构建一致才算成功，否则打印 journal 并以非零码退出。判据落在"谁在应答"，不落在"文件传过去了"。
- **`pnpm build` 会删掉入库的 `web/dist/.gitkeep`，让工作区变脏。** 本地看不出来是因为 `make web` 末尾自己 `touch` 补了回去；CI 直接调 pnpm，于是 v0.5.0 的 release 产物全部自报 `dirty=true`——发布物无法从 git 复现，而这恰恰是发布前要检查的那条性质。凡是 CI 会独立调用的构建目标，都要自己保证这个不变量，不能依赖另一个目标的副作用。`TestBuildStampedRestoresGitkeep` 守这条。
- **版本戳不能用 `date`。** 用 wall-clock 时间会让每次重建都产出不同二进制，直接废掉"线上二进制可从 git 复现"这条发布前检查。取 commit 时间（`git show -s --format=%cI`）。
- **要对每个节点做的事，必须是面板的一条代码路径，不能是当场敲的 ssh 循环。** 2026-08-30 的协议加固就是反例：改动本身走了仓库和发版，可"把新配置铺到 4 个节点"却是 ssh 上机、heredoc 拼 curl + 内联 python 干的——只存在于那一个终端会话，谁也没法重跑，还为了少登录几次把 admin JWT 落到了节点的 `/tmp`。`POST /api/batch/reprovision` 是补上的正确形态：逻辑在代码里、有测试、随版本发布、留下可复现的调用。**判断标准是"能不能原样再来一次"，不是"这次成没成功"。**
- **批量操作的状态码要能区分"全成"和"半成"。** 只看状态码的调用方会把 200 读成"整个机群都好了"。`reprovision` 因此全失败返 502、部分成功返 207，且每个节点各自带 `status` 与失败原因。
- **阈值只调到仍能捕捉回归的位置，不调到消音。** 见 `web/vite.config.ts` 的 `chunkSizeWarningLimit`。
- **前端分包是否生效，看 `dist/index.html` 的 `script` 与 `modulepreload` 列表，不看 chunk 体积表。** 手动命名一个 chunk 会把它拉进入口的预加载列表，体积表上"拆开了"、浏览器首屏照样下载。同理 `manualChunks` 必须按 `node_modules/` 之后的完整包名段匹配——`recharts` 的路径里含 `react`，子串匹配会把图表库塞进首屏。

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
