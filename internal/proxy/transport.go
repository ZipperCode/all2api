package proxy

import (
	"io"
	"net/http"
	"strings"
	"time"

	"api-football-gateway/internal/keypool"
)

// outcomeKind 是上游响应的归类结果。
type outcomeKind int

const (
	outcomeSuccess     outcomeKind = iota // 2xx
	outcomeRateLimited                    // 429 → 换 key
	outcomeKeyInvalid                     // 401/403 → key 失效，换 key
	outcomeServerError                    // 5xx / 网络错误 → 换 key
	outcomeClientError                    // 其余 4xx（非额度类）→ 不重试，原样回传
)

// upstreamResult 是一次上游请求的完整结果（已缓冲响应体）。
type upstreamResult struct {
	kind       outcomeKind
	statusCode int
	header     http.Header
	body       []byte
	snapshot   keypool.RateLimitSnapshot
	err        error // 网络层错误（非 HTTP 状态错误）
}

// transport 封装对上游的单次 HTTP 调用。
type transport struct {
	baseURL string
	client  *http.Client
}

// newTransport 构造 transport。
func newTransport(baseURL string, timeout time.Duration) *transport {
	return &transport{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// do 用给定真实 key 向上游发起一次请求，返回已缓冲的结果。
// 注入 x-apisports-key；不透传下游 Authorization（构造全新请求头）。
func (t *transport) do(clientReq *http.Request, apiKey string) upstreamResult {
	url := t.baseURL + clientReq.URL.Path
	if clientReq.URL.RawQuery != "" {
		url += "?" + clientReq.URL.RawQuery
	}

	var bodyReader io.Reader
	if clientReq.Body != nil {
		bodyReader = clientReq.Body
	}

	upReq, err := http.NewRequestWithContext(clientReq.Context(), clientReq.Method, url, bodyReader)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}
	copySafeRequestHeaders(clientReq.Header, upReq.Header)
	upReq.Header.Set("x-apisports-key", apiKey)

	resp, err := t.client.Do(upReq)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}

	snap, _ := keypool.ParseRateLimit(resp.Header)
	return upstreamResult{
		kind:       classifyStatus(resp.StatusCode),
		statusCode: resp.StatusCode,
		header:     resp.Header,
		body:       body,
		snapshot:   snap,
	}
}

// classifyStatus 把 HTTP 状态码归类。
func classifyStatus(code int) outcomeKind {
	switch {
	case code >= 200 && code < 300:
		return outcomeSuccess
	case code == http.StatusTooManyRequests: // 429
		return outcomeRateLimited
	case code == http.StatusUnauthorized || code == http.StatusForbidden: // 401/403
		return outcomeKeyInvalid
	case code >= 500:
		return outcomeServerError
	default: // 其余 4xx
		return outcomeClientError
	}
}

// copySafeRequestHeaders 复制对上游安全且有意义的请求头。
// 显式排除 Authorization（下游凭证）与逐跳头。
func copySafeRequestHeaders(src, dst http.Header) {
	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
		if _, blocked := blockedRequestHeaders[canonical]; blocked {
			continue
		}
		for _, v := range values {
			dst.Add(canonical, v)
		}
	}
}

var blockedRequestHeaders = map[string]struct{}{
	"Authorization":     {}, // 下游凭证，不透传
	"Connection":        {},
	"Proxy-Connection":  {},
	"Keep-Alive":        {},
	"Transfer-Encoding": {},
	"Upgrade":           {},
	"Host":              {},
	"X-Apisports-Key":   {}, // 防止下游伪造上游密钥头
}
