package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"api-football-gateway/internal/auth"
	"api-football-gateway/internal/keypool"
)

// Config 配置中转 handler。
type Config struct {
	UpstreamBaseURL string
	Timeout         time.Duration
	MaxRetries      int
}

// Handler 是网关的核心中转处理器。
type Handler struct {
	pool       *keypool.Pool
	auth       *auth.Authenticator
	transport  *transport
	maxRetries int
	logger     *slog.Logger
}

// NewHandler 构造中转 handler。
func NewHandler(cfg Config, pool *keypool.Pool, a *auth.Authenticator) *Handler {
	maxRetries := cfg.MaxRetries
	if maxRetries < 1 {
		// 防御误配：MaxRetries<1 会导致每个请求直接 503。至少尝试一次。
		maxRetries = 1
	}
	return &Handler{
		pool:       pool,
		auth:       a,
		transport:  newTransport(cfg.UpstreamBaseURL, cfg.Timeout),
		maxRetries: maxRetries,
		logger:     slog.Default(),
	}
}

// WithLogger 注入自定义 logger。
func (h *Handler) WithLogger(l *slog.Logger) *Handler {
	h.logger = l
	return h
}

// ServeHTTP 执行中转管线：认证 → 缓冲请求体 → 换 key 重试 → 回传。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.Authorize(r); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "client authorization failed", nil, 0)
		return
	}

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

		res := h.transport.do(attemptReq, handle.Key())

		switch res.kind {
		case outcomeSuccess:
			h.pool.UpdateFromUpstream(handle.Label(), res.snapshot)
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
