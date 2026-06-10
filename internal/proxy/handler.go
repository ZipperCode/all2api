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

	"all2api/internal/auth"
	"all2api/internal/keypool"
	"all2api/internal/logstore"
	"all2api/internal/provider"
)

// splitPlatform separates the public namespace prefix from the upstream path.
//
//	"/api-sports/fixtures?..." → ("api-sports", "/fixtures")
//	"/api-sports"              → ("api-sports", "/")
//	"/"  或  ""                → ("", "/")
//
// 剩余路径总是以 "/" 开头，便于直接拼接上游 base_url。
func splitPlatform(path string) (platform, rest string) {
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

// PlatformUpstream describes one public namespace prefix.
type PlatformUpstream struct {
	BaseURL          string
	Timeout          time.Duration
	Provider         provider.Provider
	Pool             *keypool.Pool
	PoolName         string
	ClientAuthHeader string
}

// Config 配置中转 handler。
type Config struct {
	// Platforms maps /<namespace>/<rest> to its upstream runtime.
	Platforms map[string]PlatformUpstream

	// Workspaces is the legacy API-Sports-only runtime config.
	Workspaces map[string]WorkspaceUpstream
	MaxRetries int
}

type platformRuntime struct {
	transport        *transport
	pool             *keypool.Pool
	poolName         string
	clientAuthHeader string
}

// Handler 是网关的核心中转处理器。
type Handler struct {
	auth       *auth.Authenticator
	platforms  map[string]platformRuntime
	maxRetries int
	logger     *slog.Logger
	eventSink  EventSink
}

// EventSink records proxy events.
type EventSink interface {
	Record(logstore.Event)
}

// NewHandler 构造中转 handler，为每个 platform 预构建一个 transport（复用连接池）。
func NewHandler(cfg Config, defaultPool *keypool.Pool, a *auth.Authenticator) *Handler {
	maxRetries := cfg.MaxRetries
	if maxRetries < 1 {
		// 防御误配：MaxRetries<1 会导致每个请求直接 503。至少尝试一次。
		maxRetries = 1
	}
	platforms := normalizePlatforms(cfg, defaultPool)
	runtimes := make(map[string]platformRuntime, len(platforms))
	for name, upstream := range platforms {
		runtimes[name] = platformRuntime{
			transport:        newTransport(upstream.BaseURL, upstream.Timeout, upstream.Provider),
			pool:             upstream.Pool,
			poolName:         upstream.PoolName,
			clientAuthHeader: upstream.ClientAuthHeader,
		}
	}
	return &Handler{
		auth:       a,
		platforms:  runtimes,
		maxRetries: maxRetries,
		logger:     slog.Default(),
	}
}

func normalizePlatforms(cfg Config, defaultPool *keypool.Pool) map[string]PlatformUpstream {
	if len(cfg.Platforms) > 0 {
		out := make(map[string]PlatformUpstream, len(cfg.Platforms))
		for name, upstream := range cfg.Platforms {
			if upstream.Pool == nil {
				upstream.Pool = defaultPool
			}
			if upstream.PoolName == "" {
				upstream.PoolName = "default"
			}
			if upstream.Provider == nil {
				upstream.Provider = mustAPISportsProvider()
			}
			if upstream.ClientAuthHeader == "" {
				upstream.ClientAuthHeader = upstream.Provider.CredentialHeader()
			}
			out[name] = upstream
		}
		return out
	}

	out := make(map[string]PlatformUpstream, len(cfg.Workspaces))
	apiSports := mustAPISportsProvider()
	for name, ws := range cfg.Workspaces {
		out[name] = PlatformUpstream{
			BaseURL:          ws.BaseURL,
			Timeout:          ws.Timeout,
			Provider:         apiSports,
			Pool:             defaultPool,
			PoolName:         "default",
			ClientAuthHeader: apiSports.CredentialHeader(),
		}
	}
	return out
}

func mustAPISportsProvider() provider.Provider {
	p, err := provider.New(provider.Config{Type: provider.TypeAPISports})
	if err != nil {
		panic(err)
	}
	return p
}

// WithLogger 注入自定义 logger。
func (h *Handler) WithLogger(l *slog.Logger) *Handler {
	h.logger = l
	return h
}

// WithEventSink injects a structured event sink.
func (h *Handler) WithEventSink(sink EventSink) *Handler {
	h.eventSink = sink
	return h
}

// ServeHTTP 执行中转管线：认证 → 解析 namespace → 缓冲请求体 → 换 key 重试 → 回传。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	originalPath := r.URL.Path

	// 解析路径前缀 /<namespace>/<rest>，选定对应上游 runtime，并把剩余路径设回请求。
	platform, rest := splitPlatform(r.URL.Path)
	runtime, ok := h.platforms[platform]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown_platform",
			"unknown namespace in path; expected /<namespace>/<path>", nil, 0)
		h.recordProxyEvent(logstore.Event{
			Level:      "warn",
			Message:    "unknown namespace",
			Platform:   platform,
			Method:     r.Method,
			Path:       originalPath,
			StatusCode: http.StatusNotFound,
			DurationMS: time.Since(start).Milliseconds(),
			RemoteAddr: r.RemoteAddr,
		})
		return
	}
	if err := h.authorize(runtime, r); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "client authorization failed", nil, 0)
		h.recordProxyEvent(logstore.Event{
			Level:      "warn",
			Message:    "client authorization failed",
			Platform:   platform,
			Method:     r.Method,
			Path:       originalPath,
			StatusCode: http.StatusUnauthorized,
			DurationMS: time.Since(start).Milliseconds(),
			Error:      err.Error(),
			RemoteAddr: r.RemoteAddr,
		})
		return
	}
	if runtime.pool == nil {
		writeJSONError(w, http.StatusInternalServerError, "platform_misconfigured",
			"platform has no credential pool", nil, 0)
		h.recordProxyEvent(logstore.Event{
			Level:      "error",
			Message:    "platform has no credential pool",
			Platform:   platform,
			Method:     r.Method,
			Path:       originalPath,
			StatusCode: http.StatusInternalServerError,
			DurationMS: time.Since(start).Milliseconds(),
			RemoteAddr: r.RemoteAddr,
		})
		return
	}
	r.URL.Path = rest

	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "failed to read request body", nil, 0)
			h.recordProxyEvent(logstore.Event{
				Level:      "warn",
				Message:    "failed to read request body",
				Platform:   platform,
				Method:     r.Method,
				Path:       originalPath,
				StatusCode: http.StatusBadRequest,
				DurationMS: time.Since(start).Milliseconds(),
				Error:      err.Error(),
				RemoteAddr: r.RemoteAddr,
			})
			return
		}
		bodyBytes = b
		_ = r.Body.Close()
	}

	attempts := 0
	for attempts < h.maxRetries {
		handle, err := runtime.pool.Acquire()
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

		res := runtime.transport.do(attemptReq, handle.Key())

		switch res.kind {
		case outcomeSuccess:
			justExhausted := runtime.pool.UpdateFromUpstream(handle.Label(), res.snapshot)
			// 若本次成功后该 key 当日额度恰好耗尽，记 INFO 便于运营感知后续切换。
			if justExhausted {
				h.logger.Info("key exhausted after this response, will switch on next request",
					"platform", platform, "pool", runtime.poolName, "key", handle.Label(),
					"daily_remaining", res.snapshot.DailyRemaining)
			}
			h.writeUpstreamResponse(w, res)
			h.recordProxyEvent(logstore.Event{
				Level:      "info",
				Message:    "proxy request completed",
				Platform:   platform,
				Pool:       runtime.poolName,
				KeyLabel:   handle.Label(),
				Method:     r.Method,
				Path:       originalPath,
				StatusCode: res.statusCode,
				DurationMS: time.Since(start).Milliseconds(),
				Attempts:   attempts,
				RemoteAddr: r.RemoteAddr,
			})
			return

		case outcomeRateLimited:
			runtime.pool.MarkRateLimited(handle.Label())
			h.logger.Warn("key rate limited, switching",
				"platform", platform, "pool", runtime.poolName, "key", handle.Label(), "attempt", attempts)
			continue

		case outcomeKeyInvalid:
			runtime.pool.MarkKeyInvalid(handle.Label(), "upstream returned "+strconv.Itoa(res.statusCode))
			h.logger.Error("key invalid (401/403), switching",
				"platform", platform, "pool", runtime.poolName, "key", handle.Label(), "status", res.statusCode)
			continue

		case outcomeServerError:
			// 客户端断连导致的 context 取消不计为 key 错误，且重试无意义（无人接收响应）。
			if r.Context().Err() != nil {
				h.logger.Warn("client disconnected, aborting", "key", handle.Label())
				h.recordProxyEvent(logstore.Event{
					Level:      "warn",
					Message:    "client disconnected",
					Platform:   platform,
					Pool:       runtime.poolName,
					KeyLabel:   handle.Label(),
					Method:     r.Method,
					Path:       originalPath,
					DurationMS: time.Since(start).Milliseconds(),
					Attempts:   attempts,
					Error:      r.Context().Err().Error(),
					RemoteAddr: r.RemoteAddr,
				})
				return
			}
			reason := "upstream server error"
			if res.err != nil {
				reason = res.err.Error()
			}
			runtime.pool.MarkError(handle.Label(), reason)
			h.logger.Warn("upstream error, switching",
				"platform", platform, "pool", runtime.poolName, "key", handle.Label(), "reason", reason)
			continue

		case outcomeClientError:
			runtime.pool.UpdateFromUpstream(handle.Label(), res.snapshot)
			h.writeUpstreamResponse(w, res)
			h.recordProxyEvent(logstore.Event{
				Level:      "info",
				Message:    "proxy client error passed through",
				Platform:   platform,
				Pool:       runtime.poolName,
				KeyLabel:   handle.Label(),
				Method:     r.Method,
				Path:       originalPath,
				StatusCode: res.statusCode,
				DurationMS: time.Since(start).Milliseconds(),
				Attempts:   attempts,
				RemoteAddr: r.RemoteAddr,
			})
			return
		}
	}

	retryAfter := runtime.pool.EarliestRecoverySeconds()
	h.logger.Error("all keys unavailable", "platform", platform, "pool", runtime.poolName,
		"retry_after_seconds", retryAfter)
	writeJSONError(w, http.StatusServiceUnavailable, "all_keys_unavailable",
		"All upstream API keys are exhausted or rate-limited", runtime.pool.Snapshot(), retryAfter)
	h.recordProxyEvent(logstore.Event{
		Level:      "error",
		Message:    "all keys unavailable",
		Platform:   platform,
		Pool:       runtime.poolName,
		Method:     r.Method,
		Path:       originalPath,
		StatusCode: http.StatusServiceUnavailable,
		DurationMS: time.Since(start).Milliseconds(),
		Attempts:   attempts,
		RemoteAddr: r.RemoteAddr,
	})
}

func (h *Handler) recordProxyEvent(e logstore.Event) {
	if h.eventSink == nil {
		return
	}
	e.Kind = "proxy"
	h.eventSink.Record(e)
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

func (h *Handler) authorize(runtime platformRuntime, r *http.Request) error {
	if h.auth == nil {
		return nil
	}
	if runtime.clientAuthHeader == "" {
		return h.auth.Authorize(r)
	}
	return auth.NewWithHeader(h.auth.Enabled(), h.auth.Tokens(), runtime.clientAuthHeader).Authorize(r)
}

var blockedResponseHeaders = map[string]struct{}{
	"Connection":        {},
	"Keep-Alive":        {},
	"Transfer-Encoding": {},
	"Upgrade":           {},
}
