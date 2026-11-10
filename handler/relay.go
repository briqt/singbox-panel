package handler

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/briqt/singbox-panel/model"
)

// 落地入站（protocol "shadowsocks"）只供其他节点作为上游连接，不发给用户：
// 不进订阅、不按用户授权启停，凭来源白名单与 SS2022 密钥双重限制。

// newRelaySettings 补齐方法与密钥。调用方必须给出 allowed_sources——
// 面板不替操作者猜测哪些机器有权把这台机器当出口。
func newRelaySettings(raw json.RawMessage) (json.RawMessage, error) {
	settings := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
			return nil, fmt.Errorf("settings must be a JSON object")
		}
	}
	method, _ := settings["method"].(string)
	if method == "" {
		method = model.DefaultSS2022Method
		settings["method"] = method
	}
	size, ok := model.SS2022Methods[method]
	if !ok {
		return nil, fmt.Errorf("shadowsocks method must be one of the 2022-blake3 methods")
	}
	if password, _ := settings["password"].(string); password == "" {
		key := make([]byte, size)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate shadowsocks key: %w", err)
		}
		settings["password"] = base64.StdEncoding.EncodeToString(key)
	}
	return json.Marshal(settings)
}

func validateRelaySettings(settings map[string]any) error {
	method, _ := settings["method"].(string)
	password, _ := settings["password"].(string)
	if err := model.ValidateSS2022Key(method, password); err != nil {
		return err
	}
	_, err := model.RelayAllowedSources(settings)
	return err
}

// newHysteria2Settings 让自定义 hysteria2 线路沿用节点默认 hysteria2 的域名、
// 证书与混淆参数。证书由默认线路的续期流程维护，同一份文件对两条线路都有效。
func newHysteria2Settings(inbounds []model.NodeInbound) (json.RawMessage, error) {
	for _, inbound := range inbounds {
		if inbound.Custom || inbound.Protocol != "hysteria2" {
			continue
		}
		var settings map[string]any
		if err := json.Unmarshal(inbound.Settings, &settings); err != nil || settings == nil {
			return nil, fmt.Errorf("default hysteria2 inbound has invalid settings")
		}
		return json.Marshal(settings)
	}
	return nil, fmt.Errorf("node has no default hysteria2 inbound to copy domain and certificate from; pass settings explicitly")
}
