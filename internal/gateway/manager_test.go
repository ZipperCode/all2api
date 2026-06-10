package gateway

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"all2api/internal/config"
	"all2api/internal/logstore"
)

func testConfig(logFile string) *config.Config {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Platforms: map[string]config.PlatformConfig{
			"api-sports": {
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
		Management: config.ManagementConfig{AdminKey: "admin", LogFile: logFile, LogReadLimit: 50},
		Logging:    config.LoggingConfig{Level: "info"},
	}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func TestManagerReplaceConfigWritesAndReloads(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	logPath := filepath.Join(dir, "gateway.jsonl")
	cfg := testConfig(logPath)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := logstore.New(logPath)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	m, err := New(configPath, cfg, slog.New(slog.NewTextHandler(os.Stdout, nil)), events)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	next := m.Config()
	next.Platforms["weather"] = config.PlatformConfig{
		Type:           "generic_http",
		BaseURL:        "https://weather.example.test",
		TimeoutSeconds: 10,
		CredentialPool: "weather",
		Auth:           config.AuthConfig{Header: "Authorization", Prefix: "Bearer"},
	}
	next.CredentialPools["weather"] = config.CredentialPoolConfig{Keys: []config.KeyConfig{{Label: "primary", Key: "weather-secret"}}}
	if err := m.ReplaceConfig(next); err != nil {
		t.Fatalf("ReplaceConfig: %v", err)
	}

	if got := m.Overview().PlatformCount; got != 2 {
		t.Errorf("PlatformCount = %d, want 2", got)
	}
	reloaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load saved config: %v", err)
	}
	if _, ok := reloaded.Platforms["weather"]; !ok {
		t.Fatalf("saved config missing weather platform: %+v", reloaded.Platforms)
	}
}

func TestManagerAPIDocsIncludesEndpointDetails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	logPath := filepath.Join(dir, "gateway.jsonl")
	cfg := testConfig(logPath)
	cfg.Platforms["api-basketball"] = config.PlatformConfig{
		Type:           "api_sports",
		BaseURL:        "https://v1.basketball.api-sports.io",
		TimeoutSeconds: 30,
		CredentialPool: "sports",
	}
	cfg.Platforms["weather"] = config.PlatformConfig{
		Type:           "generic_http",
		BaseURL:        "https://weather.example.test",
		TimeoutSeconds: 10,
		CredentialPool: "weather",
		Auth:           config.AuthConfig{Header: "Authorization", Prefix: "Bearer"},
	}
	cfg.CredentialPools["weather"] = config.CredentialPoolConfig{Keys: []config.KeyConfig{{Label: "primary", Key: "weather-secret"}}}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := logstore.New(logPath)
	if err != nil {
		t.Fatalf("logstore.New: %v", err)
	}
	m, err := New(configPath, cfg, slog.New(slog.NewTextHandler(os.Stdout, nil)), events)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	docs := m.APIDocs()
	football := findPlatformDoc(t, docs, "api-sports")
	if football.AuthHeader != "x-apisports-key" {
		t.Fatalf("football auth header = %q, want x-apisports-key", football.AuthHeader)
	}
	fixtures := findDocEndpoint(t, football, "/api-sports/fixtures")
	if !strings.Contains(fixtures.Summary, "比赛") || len(fixtures.Parameters) == 0 || len(fixtures.ResponseFields) == 0 || fixtures.ResponseSample == nil {
		t.Fatalf("fixtures docs are incomplete: %+v", fixtures)
	}
	if !hasDocParameter(fixtures, "league") || !hasDocField(fixtures, "response[].fixture.id") {
		t.Fatalf("fixtures docs missing league parameter or fixture id field: %+v", fixtures)
	}

	basketball := findPlatformDoc(t, docs, "api-basketball")
	games := findDocEndpoint(t, basketball, "/api-basketball/games")
	if !strings.Contains(games.Description, "各节比分") || !hasDocParameter(games, "season") || !hasDocField(games, "response[].scores.home") {
		t.Fatalf("games docs are incomplete: %+v", games)
	}

	weather := findPlatformDoc(t, docs, "weather")
	if weather.AuthHeader != "Authorization" || weather.AuthValue != "Bearer <client-key>" {
		t.Fatalf("weather auth docs = %q %q", weather.AuthHeader, weather.AuthValue)
	}
	genericGet := findDocEndpoint(t, weather, "/weather/<path>")
	if !hasDocParameter(genericGet, "<path>") || !hasDocField(genericGet, "<upstream response>") {
		t.Fatalf("generic docs missing passthrough details: %+v", genericGet)
	}
}

func findPlatformDoc(t *testing.T, docs APIDocs, name string) PlatformDoc {
	t.Helper()
	for _, platform := range docs.Platforms {
		if platform.Name == name {
			return platform
		}
	}
	t.Fatalf("platform %q not found in docs: %+v", name, docs.Platforms)
	return PlatformDoc{}
}

func findDocEndpoint(t *testing.T, platform PlatformDoc, path string) DocEndpoint {
	t.Helper()
	for _, endpoint := range platform.Endpoints {
		if endpoint.Path == path {
			return endpoint
		}
	}
	t.Fatalf("endpoint %q not found in platform %q docs: %+v", path, platform.Name, platform.Endpoints)
	return DocEndpoint{}
}

func hasDocParameter(endpoint DocEndpoint, name string) bool {
	for _, param := range endpoint.Parameters {
		if param.Name == name {
			return true
		}
	}
	return false
}

func hasDocField(endpoint DocEndpoint, name string) bool {
	for _, field := range endpoint.ResponseFields {
		if field.Name == name {
			return true
		}
	}
	return false
}
