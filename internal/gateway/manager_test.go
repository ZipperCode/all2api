package gateway

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"all2api/internal/config"
	"all2api/internal/logstore"
)

func testConfig(logFile string) *config.Config {
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
