package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-football-gateway/internal/keypool"
)

func newPool() *keypool.Pool {
	return keypool.New([]keypool.KeyEntry{
		{Label: "key-1", Key: "supersecret-1234"},
	}, keypool.Options{SwitchThreshold: 1, RateLimitCooldown: time.Minute, ErrorCooldown: time.Minute, MaxErrorCount: 3})
}

func TestStatusEndpoint(t *testing.T) {
	pool := newPool()
	h := New(pool)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__gateway/status", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var payload StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Keys) != 1 || payload.Keys[0].Label != "key-1" {
		t.Errorf("keys = %+v", payload.Keys)
	}
	if strings.Contains(rec.Body.String(), "supersecret-1234") {
		t.Error("status response leaked full key")
	}
}

func TestHealthEndpoint(t *testing.T) {
	h := New(newPool())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__gateway/health", nil))
	if rec.Code != 200 {
		t.Errorf("health status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("health Content-Type = %q, want application/json", ct)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"status":"ok"}` {
		t.Errorf("health body = %q, want {\"status\":\"ok\"}", body)
	}
}

func TestUnknownAdminPath404(t *testing.T) {
	h := New(newPool())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__gateway/unknown", nil))
	if rec.Code != 404 {
		t.Errorf("unknown admin path status = %d, want 404", rec.Code)
	}
}
