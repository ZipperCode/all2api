// Package keypool 维护上游真实 key 的运行时状态、额度快照与顺序耗尽调度。
package keypool

import (
	"net/http"
	"strconv"
)

// RateLimitSnapshot 是从上游响应 header 解析出的额度快照。
type RateLimitSnapshot struct {
	DailyLimit      int
	DailyRemaining  int
	MinuteLimit     int
	MinuteRemaining int
	HasDaily        bool // 当日 limit 与 remaining 均成功解析
	HasMinute       bool // 每分钟 limit 与 remaining 均成功解析
}

// ParseRateLimit 解析 API-Football 的额度 header。
// 返回的 ok 表示是否至少解析到一组有效数值（用于判断响应是否携带额度信息）。
func ParseRateLimit(h http.Header) (RateLimitSnapshot, bool) {
	var snap RateLimitSnapshot

	dl, dlOK := atoiHeader(h, "x-ratelimit-requests-limit")
	dr, drOK := atoiHeader(h, "x-ratelimit-requests-remaining")
	if dlOK {
		snap.DailyLimit = dl
	}
	if drOK {
		snap.DailyRemaining = dr
	}
	snap.HasDaily = dlOK && drOK

	ml, mlOK := atoiHeader(h, "X-RateLimit-Limit")
	mr, mrOK := atoiHeader(h, "X-RateLimit-Remaining")
	if mlOK {
		snap.MinuteLimit = ml
	}
	if mrOK {
		snap.MinuteRemaining = mr
	}
	snap.HasMinute = mlOK && mrOK

	ok := dlOK || drOK || mlOK || mrOK
	return snap, ok
}

// atoiHeader 读取并解析某 header 为 int，缺失或非法返回 (0,false)。
func atoiHeader(h http.Header, name string) (int, bool) {
	v := h.Get(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
