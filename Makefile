.PHONY: web build test deploy

# 前端产物 web/dist 由 go:embed 打进二进制，任何 go build 之前必须先跑 web。
# pnpm 经 corepack 调用，无需全局安装（版本锁在 web/package.json 的 packageManager）。
web:
	cd web && corepack pnpm install --frozen-lockfile && corepack pnpm build
	@touch web/dist/.gitkeep  # 入库的占位文件，让未构建前端的克隆也能通过 go build / go test

build: web
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/singbox-panel .

test:
	go test ./...
	cd web && corepack pnpm lint

# 目标面板机：ssh 主机别名或 user@host，按需覆盖 `make deploy DEPLOY_HOST=myhost`
DEPLOY_HOST ?= singbox-panel-host

deploy: build
	ssh $(DEPLOY_HOST) 'mkdir -p /opt/singbox-panel/data'
	scp bin/singbox-panel $(DEPLOY_HOST):/opt/singbox-panel/singbox-panel.new
	scp deploy/singbox-panel.service $(DEPLOY_HOST):/etc/systemd/system/singbox-panel.service
	ssh $(DEPLOY_HOST) 'mv /opt/singbox-panel/singbox-panel.new /opt/singbox-panel/singbox-panel && systemctl daemon-reload && systemctl restart singbox-panel && systemctl enable singbox-panel'
	@echo "Deployed successfully"
