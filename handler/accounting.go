package handler

import "github.com/briqt/singbox-panel/model"

// accountedUser 是只用于响应的投影，不修改参与配额、同步和回滚的原始 User。
// 同名 JSON 字段覆盖嵌入 User 的原始计数；额度上限及其他字段原样保留。
type accountedUser struct {
	model.User
	TrafficUsedBytes int64 `json:"traffic_used_bytes"`
	TrafficUpBytes   int64 `json:"traffic_up_bytes"`
	TrafficDownBytes int64 `json:"traffic_down_bytes"`
}

func accountUser(user model.User) accountedUser {
	return accountedUser{
		User:             user,
		TrafficUsedBytes: model.AccountedBytes(user.TrafficUsedBytes),
		TrafficUpBytes:   model.AccountedBytes(user.TrafficUpBytes),
		TrafficDownBytes: model.AccountedBytes(user.TrafficDownBytes),
	}
}

// Usage 的查询结果由本次请求独占；只折算响应，不改变历史原始记录。
func accountUsage(rows []model.UsageRow) []model.UsageRow {
	for i := range rows {
		rows[i].Up = model.AccountedBytes(rows[i].Up)
		rows[i].Down = model.AccountedBytes(rows[i].Down)
	}
	return rows
}
