package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"all2api/internal/config"
	"all2api/internal/gateway"
	"all2api/internal/keypool"
	"all2api/internal/logstore"
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
	if len(payload.CredentialPools) != 1 || payload.CredentialPools[0].Name != "default" {
		t.Errorf("credential_pools = %+v", payload.CredentialPools)
	}
	if strings.Contains(rec.Body.String(), "supersecret-1234") {
		t.Error("status response leaked full key")
	}
}

func TestStatusEndpoint_MultiplePools(t *testing.T) {
	h := NewPools(map[string]*keypool.Pool{
		"sports":  newPool(),
		"weather": newPool(),
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__gateway/status", nil))

	var payload StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.CredentialPools) != 2 {
		t.Fatalf("credential_pools len = %d, want 2", len(payload.CredentialPools))
	}
	if payload.CredentialPools[0].Name != "sports" || payload.CredentialPools[1].Name != "weather" {
		t.Errorf("credential_pools order/names = %+v", payload.CredentialPools)
	}
	if len(payload.Keys) != 0 {
		t.Errorf("legacy keys should be omitted for multiple pools, got %+v", payload.Keys)
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

func newConsoleHandler(t *testing.T) (*Handler, *gateway.Manager, *logstore.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Platforms: map[string]config.PlatformConfig{
			"football": {
				Type:           "api_sports",
				BaseURL:        "https://v3.football.api-sports.io",
				TimeoutSeconds: 30,
				CredentialPool: "sports",
			},
		},
		CredentialPools: map[string]config.CredentialPoolConfig{
			"sports": {Keys: []config.KeyConfig{{Label: "key-1", Key: "secret"}}},
		},
		Scheduler:  config.SchedulerConfig{MaxRetries: 1},
		Management: config.ManagementConfig{AdminKey: "admin-secret", LogFile: filepath.Join(dir, "gateway.jsonl"), LogReadLimit: 100},
		Logging:    config.LoggingConfig{Level: "info"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := logstore.New(cfg.Management.LogFile)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	manager, err := gateway.New(configPath, cfg, slog.New(slog.NewTextHandler(os.Stdout, nil)), events)
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	return NewConsole(manager, events), manager, events
}

func TestAdminSessionAndProtectedOverview(t *testing.T) {
	h, _, _ := newConsoleHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/__gateway/admin/session", strings.NewReader(`{"key":"bad"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/__gateway/admin/session", strings.NewReader(`{"key":"admin-secret"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", rec.Code)
	}
	var session map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("unmarshal session: %v", err)
	}
	if session["token"] != "admin-secret" {
		t.Errorf("token = %q, want admin-secret", session["token"])
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__gateway/admin/overview", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("overview without auth = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/__gateway/admin/overview", nil)
	req.Header.Set("Authorization", "Bearer admin-secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminConfigUpdateAndDocs(t *testing.T) {
	h, manager, _ := newConsoleHandler(t)
	next := manager.Config()
	next.Platforms["weather"] = config.PlatformConfig{
		Type:           "generic_http",
		BaseURL:        "https://weather.example.test",
		TimeoutSeconds: 20,
		CredentialPool: "weather",
		Auth:           config.AuthConfig{Header: "Authorization", Prefix: "Bearer"},
	}
	next.CredentialPools["weather"] = config.CredentialPoolConfig{Keys: []config.KeyConfig{{Label: "primary", Key: "weather-secret"}}}
	raw, _ := json.Marshal(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/__gateway/admin/config", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer admin-secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config update status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := manager.Config().Platforms["weather"]; !ok {
		t.Fatal("manager config missing weather after update")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/__gateway/admin/docs", nil)
	req.Header.Set("Authorization", "Bearer admin-secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("docs status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "weather") {
		t.Errorf("docs body missing weather: %s", rec.Body.String())
	}
}

func TestAdminLogsReadAndClear(t *testing.T) {
	h, _, events := newConsoleHandler(t)
	events.Record(logstore.Event{Kind: "proxy", Level: "info", Platform: "football", Message: "ok"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/__gateway/admin/logs?kind=proxy", nil)
	req.Header.Set("Authorization", "Bearer admin-secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logs status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "football") {
		t.Errorf("logs body missing football: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/__gateway/admin/logs", nil)
	req.Header.Set("Authorization", "Bearer admin-secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear logs status = %d, want 200", rec.Code)
	}
}
