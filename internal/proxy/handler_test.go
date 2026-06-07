package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
