package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"api-football-gateway/internal/auth"
	"api-football-gateway/internal/keypool"
)

// splitWorkspace 从请求路径中分离 workspace 前缀与剩余路径。
//
//	"/football/fixtures?..." → ("football", "/fixtures")
//	"/football"              → ("football", "/")
//	"/"  或  ""              → ("", "/")
//
// 剩余路径总是以 "/" 开头，便于直接拼接上游 base_url。
func splitWorkspace(path string) (workspace, rest string) {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return "", "/"
	}
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i], trimmed[i:]
	}
	return trimmed, "/"
}

// WorkspaceUpstream 描述单个 workspace 的上游连接参数。
type WorkspaceUpstream struct {
	BaseURL string
	Timeout time.Duration
}

// Config 配置中转 handler。
type Config struct {
	// Workspaces 是 workspace 名 → 上游连接参数的映射。
	// 下游请求路径形如 /<workspace>/<rest>，按 workspace 选对应上游。
	Workspaces map[string]WorkspaceUpstream
	MaxRetries int
}

// Handler 是网关的核心中转处理器。
type Handler struct {
	pool       *keypool.Pool
	auth       *auth.Authenticator
	transports map[string]*transport // workspace 名 → 预构建的 transport
	maxRetries int
	logger     *slog.Logger
}

// NewHandler 构造中转 handler，为每个 workspace 预构建一个 transport（复用连接池）。
func NewHandler(cfg Config, pool *keypool.Pool, a *auth.Authenticator) *Handler {
	maxRetries := cfg.MaxRetries
	if maxRetries < 1 {
		// 防御误配：MaxRetries<1 会导致每个请求直接 503。至少尝试一次。
		maxRetries = 1
	}
	transports := make(map[string]*transport, len(cfg.Workspaces))
	for name, ws := range cfg.Workspaces {
		transports[name] = newTransport(ws.BaseURL, ws.Timeout)
	}
	return &Handler{
		pool:       pool,
		auth:       a,
		transports: transports,
		maxRetries: maxRetries,
		logger:     slog.Default(),
	}
}

// WithLogger 注入自定义 logger。
func (h *Handler) WithLogger(l *slog.Logger) *Handler {
	h.logger = l
	return h
}

// ServeHTTP 执行中转管线：认证 → 解析 workspace → 缓冲请求体 → 换 key 重试 → 回传。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.Authorize(r); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "client authorization failed", nil, 0)
		return
	}

	// 解析路径前缀 /<workspace>/<rest>，选定对应上游 transport，并把剩余路径设回请求。
	workspace, rest := splitWorkspace(r.URL.Path)
	tr, ok := h.transports[workspace]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown_workspace",
			"unknown workspace in path; expected /<workspace>/<path>", nil, 0)
		return
	}
	r.URL.Path = rest

	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "failed to read request body", nil, 0)
			return
		}
		bodyBytes = b
		_ = r.Body.Close()
	}

	attempts := 0
	for attempts < h.maxRetries {
		handle, err := h.pool.Acquire()
		if err != nil {
			break
		}
		attempts++

		// 每次尝试用全新的 body reader，保证换 key 重试时请求体可重发。
		// bodyBytes 为 nil 表示原请求无 body；为零长非 nil 表示显式空 body，均正确保留。
		attemptReq := r.Clone(r.Context())
		if bodyBytes != nil {
			attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		res := tr.do(attemptReq, handle.Key())

		switch res.kind {
		case outcomeSuccess:
			justExhausted := h.pool.UpdateFromUpstream(handle.Label(), res.snapshot)
			// 若本次成功后该 key 当日额度恰好耗尽，记 INFO 便于运营感知后续切换。
			if justExhausted {
				h.logger.Info("key exhausted after this response, will switch on next request",
					"key", handle.Label(), "daily_remaining", res.snapshot.DailyRemaining)
			}
			h.writeUpstreamResponse(w, res)
			return

		case outcomeRateLimited:
			h.pool.MarkRateLimited(handle.Label())
			h.logger.Warn("key rate limited, switching",
				"key", handle.Label(), "attempt", attempts)
			continue

		case outcomeKeyInvalid:
			h.pool.MarkKeyInvalid(handle.Label(), "upstream returned "+strconv.Itoa(res.statusCode))
			h.logger.Error("key invalid (401/403), switching",
				"key", handle.Label(), "status", res.statusCode)
			continue

		case outcomeServerError:
			// 客户端断连导致的 context 取消不计为 key 错误，且重试无意义（无人接收响应）。
			if r.Context().Err() != nil {
				h.logger.Warn("client disconnected, aborting", "key", handle.Label())
				return
			}
			reason := "upstream server error"
			if res.err != nil {
				reason = res.err.Error()
			}
			h.pool.MarkError(handle.Label(), reason)
			h.logger.Warn("upstream error, switching",
				"key", handle.Label(), "reason", reason)
			continue

		case outcomeClientError:
			h.pool.UpdateFromUpstream(handle.Label(), res.snapshot)
			h.writeUpstreamResponse(w, res)
			return
		}
	}

	retryAfter := h.pool.EarliestRecoverySeconds()
	h.logger.Error("all keys unavailable", "retry_after_seconds", retryAfter)
	writeJSONError(w, http.StatusServiceUnavailable, "all_keys_unavailable",
		"All upstream API keys are exhausted or rate-limited", h.pool.Snapshot(), retryAfter)
}

// writeUpstreamResponse 把上游响应原样回传给下游（白名单响应头 + 状态码 + body）。
func (h *Handler) writeUpstreamResponse(w http.ResponseWriter, res upstreamResult) {
	copySafeResponseHeaders(res.header, w.Header())
	w.WriteHeader(res.statusCode)
	_, _ = w.Write(res.body)
}

// errorResponse 是网关自身错误的 JSON 结构。
type errorResponse struct {
	Error             string                 `json:"error"`
	Message           string                 `json:"message"`
	KeysStatus        []keypool.KeyStateView `json:"keys_status,omitempty"`
	RetryAfterSeconds int                    `json:"retry_after_seconds,omitempty"`
}

// writeJSONError 写网关错误响应。
func writeJSONError(w http.ResponseWriter, status int, code, msg string, keys []keypool.KeyStateView, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:             code,
		Message:           msg,
		KeysStatus:        keys,
		RetryAfterSeconds: retryAfter,
	})
}

// copySafeResponseHeaders 复制上游响应头到下游，排除逐跳头。
func copySafeResponseHeaders(src, dst http.Header) {
	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
		if _, blocked := blockedResponseHeaders[canonical]; blocked {
			continue
		}
		for _, v := range values {
			dst.Add(canonical, v)
		}
	}
}

var blockedResponseHeaders = map[string]struct{}{
	"Connection":        {},
	"Keep-Alive":        {},
	"Transfer-Encoding": {},
	"Upgrade":           {},
}
