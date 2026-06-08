// Package admin provides gateway status endpoints and the web admin console.
package admin

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"all2api/internal/config"
	"all2api/internal/gateway"
	"all2api/internal/keypool"
	"all2api/internal/logstore"
)

//go:embed static/*
var staticFiles embed.FS

// StatusResponse 是 /__gateway/status 的响应体。
type StatusResponse struct {
	CredentialPools []CredentialPoolStatus `json:"credential_pools"`
	Keys            []keypool.KeyStateView `json:"keys,omitempty"`
}

// CredentialPoolStatus is one named upstream credential pool.
type CredentialPoolStatus struct {
	Name string                 `json:"name"`
	Keys []keypool.KeyStateView `json:"keys"`
}

// Handler 处理 /__gateway/* 管理请求。
type Handler struct {
	pools   map[string]*keypool.Pool
	manager *gateway.Manager
	events  *logstore.Store
	ui      http.Handler
}

// New 构造 admin handler。
func New(pool *keypool.Pool) *Handler {
	return NewPools(map[string]*keypool.Pool{"default": pool})
}

// NewPools constructs an admin handler for multiple named credential pools.
func NewPools(pools map[string]*keypool.Pool) *Handler {
	copied := make(map[string]*keypool.Pool, len(pools))
	for name, pool := range pools {
		copied[name] = pool
	}
	return &Handler{pools: copied, ui: staticHandler()}
}

// NewConsole constructs the full web admin handler.
func NewConsole(manager *gateway.Manager, events *logstore.Store) *Handler {
	return &Handler{manager: manager, events: events, ui: staticHandler()}
}

// ServeHTTP 路由管理端点。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/__admin") {
		h.serveUI(w, r)
		return
	}
	switch r.URL.Path {
	case "/__gateway/status":
		h.handleStatus(w, r)
	case "/__gateway/health":
		h.handleHealth(w, r)
	case "/__gateway/admin/session":
		h.handleSession(w, r)
	case "/__gateway/admin/overview":
		h.requireAdmin(w, r, h.handleOverview)
	case "/__gateway/admin/config":
		h.requireAdmin(w, r, h.handleConfig)
	case "/__gateway/admin/logs":
		h.requireAdmin(w, r, h.handleLogs)
	case "/__gateway/admin/docs":
		h.requireAdmin(w, r, h.handleDocs)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := StatusResponse{CredentialPools: snapshotPools(h.currentPools())}
	if len(response.CredentialPools) == 1 && response.CredentialPools[0].Name == "default" {
		response.Keys = response.CredentialPools[0].Keys
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		// 响应头已发送，无法回退状态码；仅记录便于运营排查。
		slog.Error("admin: failed to encode status response", "error", err)
	}
}

func (h *Handler) currentPools() map[string]*keypool.Pool {
	if h.manager != nil {
		return h.manager.Pools()
	}
	return h.pools
}

func snapshotPools(pools map[string]*keypool.Pool) []CredentialPoolStatus {
	names := make([]string, 0, len(pools))
	for name := range pools {
		names = append(names, name)
	}
	sort.Strings(names)

	status := make([]CredentialPoolStatus, 0, len(names))
	for _, name := range names {
		status = append(status, CredentialPoolStatus{
			Name: name,
			Keys: pools[name].Snapshot(),
		})
	}
	return status
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/__admin" {
		http.Redirect(w, r, "/__admin/", http.StatusFound)
		return
	}
	h.ui.ServeHTTP(w, r)
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if !h.validAdminKey(body.Key) {
		h.recordAdmin("warn", "login failed", "login_failed", r)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	h.recordAdmin("info", "login succeeded", "login", r)
	writeJSON(w, http.StatusOK, map[string]any{"token": body.Key})
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	if !h.validAdminKey(extractAdminToken(r)) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	next(w, r)
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if h.manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "manager_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, h.manager.Overview())
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.manager.Config())
	case http.MethodPut:
		var next config.Config
		if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "message": err.Error()})
			return
		}
		if err := h.manager.ReplaceConfig(&next); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "config_invalid", "message": err.Error()})
			return
		}
		h.recordAdmin("info", "config updated and runtime reloaded", "config_update", r)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "logs_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = h.logReadLimit()
		}
		if maxLimit := h.logReadLimit(); limit > maxLimit {
			limit = maxLimit
		}
		events, err := h.events.Read(logstore.Query{
			Limit:    limit,
			Kind:     r.URL.Query().Get("kind"),
			Level:    r.URL.Query().Get("level"),
			Platform: r.URL.Query().Get("platform"),
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read_logs_failed", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "path": h.events.Path()})
	case http.MethodDelete:
		if err := h.events.Clear(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "clear_logs_failed", "message": err.Error()})
			return
		}
		h.recordAdmin("warn", "logs cleared", "logs_clear", r)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (h *Handler) handleDocs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if h.manager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "manager_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, h.manager.APIDocs())
}

func (h *Handler) logReadLimit() int {
	if h.manager == nil {
		return 500
	}
	limit := h.manager.Config().Management.LogReadLimit
	if limit <= 0 {
		return 500
	}
	return limit
}

func (h *Handler) validAdminKey(got string) bool {
	if h.manager == nil {
		return false
	}
	want := h.manager.Config().Management.AdminKey
	if want == "" || got == "" {
		return false
	}
	if len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func extractAdminToken(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Admin-Key")); t != "" {
		return t
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func (h *Handler) recordAdmin(level, message, action string, r *http.Request) {
	if h.events == nil {
		return
	}
	h.events.Record(logstore.Event{
		Time:       time.Now().UTC(),
		Kind:       "admin",
		Level:      level,
		Message:    message,
		Action:     action,
		Method:     r.Method,
		Path:       r.URL.Path,
		RemoteAddr: r.RemoteAddr,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/__admin/", http.FileServer(http.FS(sub)))
}
