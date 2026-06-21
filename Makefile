.PHONY: build deploy

build:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/singbox-panel .

deploy: build
	ssh tokyo 'mkdir -p /opt/singbox-panel/data'
	scp bin/singbox-panel tokyo:/opt/singbox-panel/singbox-panel.new
	scp deploy/singbox-panel.service tokyo:/etc/systemd/system/singbox-panel.service
	ssh tokyo 'mv /opt/singbox-panel/singbox-panel.new /opt/singbox-panel/singbox-panel && systemctl daemon-reload && systemctl restart singbox-panel && systemctl enable singbox-panel'
	@echo "Deployed successfully"
