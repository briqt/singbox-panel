package model

import (
	"fmt"
	"net/netip"
)

// RelayAllowedSources 把落地入站的来源白名单规范成前缀。空名单直接拒绝：
// 它在 sing-box 路由里会退化成"不限来源"，与这个字段的本意相反。
func RelayAllowedSources(settings map[string]any) ([]string, error) {
	var rawList []any
	switch list := settings["allowed_sources"].(type) {
	case []any:
		rawList = list
	case []string:
		for _, item := range list {
			rawList = append(rawList, item)
		}
	}
	if len(rawList) == 0 {
		return nil, fmt.Errorf("shadowsocks relay requires a non-empty allowed_sources list")
	}
	prefixes := make([]string, 0, len(rawList))
	for _, item := range rawList {
		text, _ := item.(string)
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			addr, addrErr := netip.ParseAddr(text)
			if addrErr != nil {
				return nil, fmt.Errorf("allowed_sources entry %q is not an IP address or CIDR", text)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("allowed_sources entry %q would admit every address", text)
		}
		prefixes = append(prefixes, prefix.Masked().String())
	}
	return prefixes, nil
}
