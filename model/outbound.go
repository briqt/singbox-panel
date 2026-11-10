package model

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"
)

// OutboundConfig 仅保存在服务端，不得写入客户端订阅。
// 支持 SOCKS5 TCP 与 Shadowsocks 2022 两种上游；不支持的业务显式失败，不回退直连。
type OutboundConfig struct {
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	// Method 仅用于 shadowsocks，取值见 SS2022Methods。
	Method string `json:"method,omitempty"`
	// Network 为空表示 TCP 与 UDP 都走上游；"tcp" 表示只转发 TCP，UDP 直接失败。
	Network string `json:"network,omitempty"`
}

// SS2022Methods 映射 Shadowsocks 2022 方法到其 PSK 的字节长度。
// 只开放 2022 系列：旧式 AEAD 没有重放保护，也不校验时间戳。
var SS2022Methods = map[string]int{
	"2022-blake3-aes-128-gcm":       16,
	"2022-blake3-aes-256-gcm":       32,
	"2022-blake3-chacha20-poly1305": 32,
}

// DefaultSS2022Method 是新建落地入站的默认方法。
const DefaultSS2022Method = "2022-blake3-aes-128-gcm"

// ValidateSS2022Key 检查 PSK 是否为该方法要求长度的标准 base64。
// 长度不对的密钥会被 sing-box 在启动时拒绝，提前在面板挡住才能回滚得干净。
func ValidateSS2022Key(method, key string) error {
	size, ok := SS2022Methods[method]
	if !ok {
		return fmt.Errorf("shadowsocks method must be one of the 2022-blake3 methods")
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != size {
		return fmt.Errorf("shadowsocks password must be a base64 key of %d bytes for %s", size, method)
	}
	return nil
}

// CarriesUDP 报告该线路的 UDP 是否有路可走。nil 表示直出。
func (o *OutboundConfig) CarriesUDP() bool {
	if o == nil {
		return true
	}
	switch o.Type {
	case "shadowsocks":
		return o.Network != "tcp"
	default:
		return false
	}
}

func ValidateOutbound(outbound *OutboundConfig) error {
	if outbound == nil {
		return nil
	}
	if outbound.Server == "" || strings.TrimSpace(outbound.Server) != outbound.Server || strings.ContainsAny(outbound.Server, "/\\\r\n\t @") {
		return fmt.Errorf("outbound server must be a hostname or IP address")
	}
	if strings.Contains(outbound.Server, ":") && net.ParseIP(outbound.Server) == nil {
		return fmt.Errorf("outbound server must not include a port")
	}
	if outbound.ServerPort < 1 || outbound.ServerPort > 65535 {
		return fmt.Errorf("outbound server_port must be between 1 and 65535")
	}
	switch outbound.Type {
	case "socks":
		if outbound.Network != "" && outbound.Network != "tcp" {
			return fmt.Errorf("only TCP upstream forwarding is supported")
		}
		if outbound.Method != "" {
			return fmt.Errorf("method is only valid for shadowsocks outbounds")
		}
		if len(outbound.Username) > 255 || len(outbound.Password) > 255 {
			return fmt.Errorf("SOCKS5 credentials must not exceed 255 bytes")
		}
		if (outbound.Username == "") != (outbound.Password == "") {
			return fmt.Errorf("outbound username and password must be provided together")
		}
	case "shadowsocks":
		if outbound.Network != "" && outbound.Network != "tcp" {
			return fmt.Errorf("shadowsocks network must be empty (tcp+udp) or tcp")
		}
		if outbound.Username != "" {
			return fmt.Errorf("shadowsocks outbounds do not take a username")
		}
		if err := ValidateSS2022Key(outbound.Method, outbound.Password); err != nil {
			return err
		}
	default:
		return fmt.Errorf("outbound type must be socks or shadowsocks")
	}
	return nil
}
