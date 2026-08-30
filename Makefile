.PHONY: web build build-stamped test deploy verify-deploy

# 前端产物 web/dist 由 go:embed 打进二进制，任何 go build 之前必须先跑 web。
# pnpm 经 corepack 调用，无需全局安装（版本锁在 web/package.json 的 packageManager）。
web:
	cd web && corepack pnpm install --frozen-lockfile && corepack pnpm build
	@touch web/dist/.gitkeep  # 入库的占位文件，让未构建前端的克隆也能通过 go build / go test

# Stamped into the binary so `GET /api/version` can answer "which build is
# live?" over HTTP, instead of someone ssh-ing to the host to run sha256sum.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
# Build date comes from the commit, not from `date`: a wall-clock stamp makes
# every rebuild a different binary and destroys the reproducibility check.
BUILD_DATE ?= $(shell git show -s --format=%cI HEAD 2>/dev/null)
LDFLAGS = -s -w \
	-X github.com/briqt/singbox-panel/buildinfo.version=$(VERSION) \
	-X github.com/briqt/singbox-panel/buildinfo.commit=$(COMMIT) \
	-X github.com/briqt/singbox-panel/buildinfo.date=$(BUILD_DATE)

build: web
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/singbox-panel .

# Same stamp, caller-chosen output and target platform. CI uses this so a
# release artifact reports the same version a local `make build` would.
OUT ?= bin/singbox-panel
build-stamped:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(OUT) .

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
	@$(MAKE) --no-print-directory verify-deploy DEPLOY_HOST=$(DEPLOY_HOST)

# "scp exited 0" is not evidence the new build is serving: systemd may have
# restarted the old binary, or the new one may have died on startup and left
# the previous process answering. Ask the running service which commit it is.
PANEL_URL ?= http://127.0.0.1:2082
verify-deploy:
	@echo "verifying $(COMMIT) is live on $(DEPLOY_HOST)..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		live=$$(ssh $(DEPLOY_HOST) "curl -fsS --max-time 5 $(PANEL_URL)/api/version" 2>/dev/null \
			| sed -n 's/.*"commit":"\([^"]*\)".*/\1/p'); \
		if [ "$$live" = "$(COMMIT)" ]; then \
			echo "Deployed successfully: commit $$live is serving"; exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "DEPLOY VERIFICATION FAILED: expected commit $(COMMIT), service reports '$$live'" >&2; \
	echo "the old binary may still be serving, or the new one failed to start:" >&2; \
	ssh $(DEPLOY_HOST) 'systemctl is-active singbox-panel; journalctl -u singbox-panel -n 20 --no-pager' >&2; \
	exit 1
