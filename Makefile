.PHONY: build deploy

build:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/singbox-panel .

deploy: build
	ssh $(DEPLOY_HOST) 'mkdir -p /opt/singbox-panel/data'
	scp bin/singbox-panel $(DEPLOY_HOST):/opt/singbox-panel/singbox-panel.new
	scp deploy/singbox-panel.service $(DEPLOY_HOST):/etc/systemd/system/singbox-panel.service
	ssh $(DEPLOY_HOST) 'mv /opt/singbox-panel/singbox-panel.new /opt/singbox-panel/singbox-panel && systemctl daemon-reload && systemctl restart singbox-panel && systemctl enable singbox-panel'
	@echo "Deployed successfully"
