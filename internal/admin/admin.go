// Package admin 提供网关管理端点：/__gateway/status 与 /__gateway/health。
package admin

import (
	"encoding/json"
	"net/http"

	"api-football-gateway/internal/keypool"
)

// StatusResponse 是 /__gateway/status 的响应体。
type StatusResponse struct {
	Keys []keypool.KeyStateView `json:"keys"`
}

// Handler 处理 /__gateway/* 管理请求。
type Handler struct {
	pool *keypool.Pool
}

// New 构造 admin handler。
func New(pool *keypool.Pool) *Handler {
	return &Handler{pool: pool}
}

// ServeHTTP 路由管理端点。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/__gateway/status":
		h.handleStatus(w, r)
	case "/__gateway/health":
		h.handleHealth(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(StatusResponse{Keys: h.pool.Snapshot()})
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
