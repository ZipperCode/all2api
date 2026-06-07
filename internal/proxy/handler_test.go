package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-football-gateway/internal/auth"
	"api-football-gateway/internal/keypool"
)

func newTestPool() *keypool.Pool {
	return keypool.New([]keypool.KeyEntry{
		{Label: "key-1", Key: "secret-aaaa1"},
		{Label: "key-2", Key: "secret-bbbb2"},
	}, keypool.Options{
		SwitchThreshold:   1,
		RateLimitCooldown: 60 * time.Second,
		ErrorCooldown:     30 * time.Second,
		MaxErrorCount:     3,
	})
}

func newTestHandler(t *testing.T, upstreamURL string, pool *keypool.Pool) *Handler {
	t.Helper()
	return NewHandler(Config{
		UpstreamBaseURL: upstreamURL,
		Timeout:         5 * time.Second,
		MaxRetries:      3,
	}, pool, auth.New(false, nil))
}

func TestServeHTTP_SuccessForwards(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-requests-limit", "100")
		w.Header().Set("x-ratelimit-requests-remaining", "50")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	defer upstream.Close()

	pool := newTestPool()
	h := newTestHandler(t, upstream.URL, pool)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/fixtures?live=all", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != `{"response":[]}` {
		t.Errorf("body = %q", rec.Body.String())
	}
	for _, s := range pool.Snapshot() {
		if s.Label == "key-1" && s.DailyRemaining != 50 {
			t.Errorf("key-1 DailyRemaining = %d, want 50", s.DailyRemaining)
		}
	}
}

// stagedUpstream 按收到的 x-apisports-key 返回不同响应，模拟多 key 行为。
func stagedUpstream(t *testing.T, byKey map[string]func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-apisports-key")
		if fn, ok := byKey[key]; ok {
			fn(w)
			return
		}
		w.WriteHeader(500)
	}))
}

func TestServeHTTP_ExhaustedKeySwitchesToNext(t *testing.T) {
	upstream := stagedUpstream(t, map[string]func(w http.ResponseWriter){
		"secret-aaaa1": func(w http.ResponseWriter) {
			w.Header().Set("x-ratelimit-requests-limit", "100")
			w.Header().Set("x-ratelimit-requests-remaining", "0")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"from":"key-1"}`))
		},
		"secret-bbbb2": func(w http.ResponseWriter) {
			w.Header().Set("x-ratelimit-requests-limit", "100")
			w.Header().Set("x-ratelimit-requests-remaining", "80")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"from":"key-2"}`))
		},
	})
	defer upstream.Close()

	pool := newTestPool()
	h := newTestHandler(t, upstream.URL, pool)

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/fixtures", nil))
	if rec1.Body.String() != `{"from":"key-1"}` {
		t.Errorf("req1 body = %q, want from key-1", rec1.Body.String())
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/fixtures", nil))
	if rec2.Body.String() != `{"from":"key-2"}` {
		t.Errorf("req2 body = %q, want from key-2 (key-1 exhausted)", rec2.Body.String())
	}
}

func TestServeHTTP_RateLimitedRetriesTransparently(t *testing.T) {
	upstream := stagedUpstream(t, map[string]func(w http.ResponseWriter){
		"secret-aaaa1": func(w http.ResponseWriter) {
			w.WriteHeader(429)
		},
		"secret-bbbb2": func(w http.ResponseWriter) {
			w.Header().Set("x-ratelimit-requests-remaining", "70")
			w.Header().Set("x-ratelimit-requests-limit", "100")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"from":"key-2"}`))
		},
	})
	defer upstream.Close()

	pool := newTestPool()
	h := newTestHandler(t, upstream.URL, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fixtures", nil))

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 (429 must not leak to downstream)", rec.Code)
	}
	if rec.Body.String() != `{"from":"key-2"}` {
		t.Errorf("body = %q, want from key-2", rec.Body.String())
	}
}

func TestServeHTTP_AllKeysExhaustedReturns503(t *testing.T) {
	upstream := stagedUpstream(t, map[string]func(w http.ResponseWriter){
		"secret-aaaa1": func(w http.ResponseWriter) { w.WriteHeader(429) },
		"secret-bbbb2": func(w http.ResponseWriter) { w.WriteHeader(429) },
	})
	defer upstream.Close()

	pool := newTestPool()
	h := newTestHandler(t, upstream.URL, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fixtures", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header on 503")
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal 503 body: %v", err)
	}
	if body.Error != "all_keys_unavailable" {
		t.Errorf("error code = %q, want all_keys_unavailable", body.Error)
	}
	if len(body.KeysStatus) != 2 {
		t.Errorf("keys_status len = %d, want 2", len(body.KeysStatus))
	}
}

func TestServeHTTP_ClientError4xxNotRetried(t *testing.T) {
	var key1Calls, key2Calls int
	upstream := stagedUpstream(t, map[string]func(w http.ResponseWriter){
		"secret-aaaa1": func(w http.ResponseWriter) {
			key1Calls++
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		},
		"secret-bbbb2": func(w http.ResponseWriter) {
			key2Calls++
			w.WriteHeader(200)
		},
	})
	defer upstream.Close()

	pool := newTestPool()
	h := newTestHandler(t, upstream.URL, pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nonexistent", nil))

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404 (passed through)", rec.Code)
	}
	if key1Calls != 1 {
		t.Errorf("key-1 calls = %d, want 1", key1Calls)
	}
	if key2Calls != 0 {
		t.Errorf("key-2 calls = %d, want 0 (4xx must not trigger retry)", key2Calls)
	}
}

func TestServeHTTP_StripsAuthorizationEndToEnd(t *testing.T) {
	var sawAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	pool := newTestPool()
	h := newTestHandler(t, upstream.URL, pool)

	req := httptest.NewRequest(http.MethodGet, "/fixtures", nil)
	req.Header.Set("Authorization", "Bearer downstream-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if sawAuth != "" {
		t.Errorf("upstream saw Authorization = %q, want empty", sawAuth)
	}
}

// TestServeHTTP_NetworkErrorReturns503 验证上游网络不可达时，handler 走 serverError
// 路径换 key 重试，最终所有尝试失败 → 503（下游不感知中间网络错误）。
func TestServeHTTP_NetworkErrorReturns503(t *testing.T) {
	// 指向无人监听的端口，触发连接失败（网络层错误）。
	h := newTestHandler(t, "http://127.0.0.1:1", newTestPool())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 on all-network-error", rec.Code)
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal 503 body: %v", err)
	}
	if body.Error != "all_keys_unavailable" {
		t.Errorf("error = %q, want all_keys_unavailable", body.Error)
	}
}

// TestServeHTTP_PostBodyResentOnRetry 验证非 GET 请求体在换 key 重试时被完整重发——
// 下游无感知对带 body 请求的核心保证。
func TestServeHTTP_PostBodyResentOnRetry(t *testing.T) {
	var gotBodies []string
	// key-1 返回 429 触发换 key；每次都记录收到的 body，验证重试时 body 完整重发。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, string(b))
		if r.Header.Get("x-apisports-key") == "secret-aaaa1" {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	h := newTestHandler(t, upstream.URL, newTestPool())
	req := httptest.NewRequest(http.MethodPost, "/odds", strings.NewReader("payload-123"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 after retry", rec.Code)
	}
	if len(gotBodies) != 2 {
		t.Fatalf("upstream received %d requests, want 2 (original + retry)", len(gotBodies))
	}
	for i, b := range gotBodies {
		if b != "payload-123" {
			t.Errorf("attempt %d body = %q, want payload-123 (body must be re-sent intact)", i+1, b)
		}
	}
}

// TestServeHTTP_ClientDisconnectDoesNotMarkKeyError 验证客户端断连（context 取消）时，
// handler 中止处理且不把健康 key 误标为错误。
func TestServeHTTP_ClientDisconnectDoesNotMarkKeyError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 上游正常，但客户端 context 已取消，transport 会返回 context.Canceled。
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	pool := newTestPool()
	h := newTestHandler(t, upstream.URL, pool)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消，模拟客户端断连
	req := httptest.NewRequest(http.MethodGet, "/fixtures", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// key 不应被标记为 error（断连非 key 的错）——所有 key 仍应可用。
	for _, s := range pool.Snapshot() {
		if s.Status == "error" {
			t.Errorf("key %s marked error on client disconnect, want not-error", s.Label)
		}
	}
}
