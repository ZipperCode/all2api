package proxy

import (
	"io"
	"net/http"
	"strings"
	"time"

	"all2api/internal/keypool"
	"all2api/internal/provider"
)

// outcomeKind 是上游响应的归类结果。
type outcomeKind = provider.Outcome

const (
	outcomeSuccess     = provider.OutcomeSuccess
	outcomeRateLimited = provider.OutcomeRateLimited
	outcomeKeyInvalid  = provider.OutcomeKeyInvalid
	outcomeServerError = provider.OutcomeServerError
	outcomeClientError = provider.OutcomeClientError
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
	baseURL  string
	client   *http.Client
	provider provider.Provider
}

// newTransport 构造 transport。
func newTransport(baseURL string, timeout time.Duration, p provider.Provider) *transport {
	if p == nil {
		p = mustAPISportsProvider()
	}
	return &transport{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: timeout},
		provider: p,
	}
}

// do 用给定真实 key 向上游发起一次请求，返回已缓冲的结果。
// 凭证注入由 provider 决定；不透传下游 Authorization（构造全新请求头）。
func (t *transport) do(clientReq *http.Request, apiKey string) upstreamResult {
	rawURL := t.baseURL + clientReq.URL.Path
	if clientReq.URL.RawQuery != "" {
		rawURL += "?" + clientReq.URL.RawQuery
	}

	var bodyReader io.Reader
	if clientReq.Body != nil {
		bodyReader = clientReq.Body
	}

	upReq, err := http.NewRequestWithContext(clientReq.Context(), clientReq.Method, rawURL, bodyReader)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}
	copySafeRequestHeaders(clientReq.Header, upReq.Header, t.provider.CredentialHeader())
	t.provider.InjectCredential(upReq.Header, apiKey)

	resp, err := t.client.Do(upReq)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}

	snap := t.provider.ParseRateLimit(resp.Header)
	return upstreamResult{
		kind:       t.provider.ClassifyStatus(resp.StatusCode),
		statusCode: resp.StatusCode,
		header:     resp.Header,
		body:       body,
		snapshot:   snap,
	}
}

// classifyStatus 把 HTTP 状态码归类。
func classifyStatus(code int) outcomeKind {
	return mustAPISportsProvider().ClassifyStatus(code)
}

// copySafeRequestHeaders 复制对上游安全且有意义的请求头。
// 显式排除 Authorization（下游凭证）与逐跳头。
func copySafeRequestHeaders(src, dst http.Header, credentialHeader string) {
	credentialHeader = http.CanonicalHeaderKey(credentialHeader)
	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
		if canonical == credentialHeader {
			continue
		}
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
