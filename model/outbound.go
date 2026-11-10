package model

import (
	"fmt"
	"net"
	"strings"
)

// OutboundConfig 仅保存在服务端，不得写入客户端订阅。
// 当前只开放 SOCKS5 TCP 出口；不支持的业务显式失败，不回退直连。
type OutboundConfig struct {
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	Network    string `json:"network,omitempty"`
}

func ValidateOutbound(outbound *OutboundConfig) error {
	if outbound == nil {
		return nil
	}
	if outbound.Type != "socks" {
		return fmt.Errorf("outbound type must be socks")
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
	if outbound.Network != "" && outbound.Network != "tcp" {
		return fmt.Errorf("only TCP upstream forwarding is supported")
	}
	if len(outbound.Username) > 255 || len(outbound.Password) > 255 {
		return fmt.Errorf("SOCKS5 credentials must not exceed 255 bytes")
	}
	if (outbound.Username == "") != (outbound.Password == "") {
		return fmt.Errorf("outbound username and password must be provided together")
	}
	return nil
}
