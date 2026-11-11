package model

import "math"

// TrafficMultiplier 将用户代理流量折算成一收一发的成本，不代表网卡实测值。
// 数据库始终保留原始字节；历史、当前用量和配额统一在读取时折算。
const TrafficMultiplier int64 = 2

// AccountedBytes 只用于原始计数的输出边界，不可将结果再写回原始计数器。
func AccountedBytes(raw int64) int64 {
	if raw > math.MaxInt64/TrafficMultiplier {
		return math.MaxInt64
	}
	return raw * TrafficMultiplier
}

// trafficAvailableSQL 与 quotaExhausted 使用同一个倍率；先除上限，避免乘法溢出。
// 占位参数必须传 TrafficMultiplier，且上限 0 仍表示不限量。
const trafficAvailableSQL = `(u.traffic_limit_bytes = 0 OR u.traffic_used_bytes <= (u.traffic_limit_bytes - 1) / ?)`

func quotaExhausted(raw, limit int64) bool {
	return limit > 0 && raw > (limit-1)/TrafficMultiplier
}
