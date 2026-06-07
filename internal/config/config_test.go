package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

const validYAML = `
server:
  host: "0.0.0.0"
  port: 8080
upstream:
  base_url: "https://v3.football.api-sports.io"
  timeout_seconds: 30
scheduler:
  switch_threshold: 1
  rate_limit_cooldown_seconds: 60
  error_cooldown_seconds: 30
  max_error_count: 3
  max_retries: 3
keys:
  - label: "key-1"
    key: "real-1"
  - label: "key-2"
    key: "real-2"
client_auth:
  enabled: false
logging:
  level: "info"
`

func mustParse(t *testing.T, content string) *Config {
	t.Helper()
	p := writeTempConfig(t, content)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &cfg
}

func TestLoad_ValidConfig(t *testing.T) {
	p := writeTempConfig(t, validYAML)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Upstream.BaseURL != "https://v3.football.api-sports.io" {
		t.Errorf("Upstream.BaseURL = %q", cfg.Upstream.BaseURL)
	}
	if cfg.Scheduler.SwitchThreshold != 1 {
		t.Errorf("SwitchThreshold = %d, want 1", cfg.Scheduler.SwitchThreshold)
	}
	if len(cfg.Keys) != 2 || cfg.Keys[0].Label != "key-1" || cfg.Keys[0].Key != "real-1" {
		t.Errorf("Keys parsed incorrectly: %+v", cfg.Keys)
	}
	if cfg.ClientAuth.Enabled {
		t.Errorf("ClientAuth.Enabled = true, want false")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	p := writeTempConfig(t, validYAML)
	t.Setenv("GW_SERVER_PORT", "9090")
	t.Setenv("GW_KEY_KEY_1", "env-secret-1")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090 (env override)", cfg.Server.Port)
	}
	if cfg.Keys[0].Key != "env-secret-1" {
		t.Errorf("Keys[0].Key = %q, want env-secret-1", cfg.Keys[0].Key)
	}
	if cfg.Keys[1].Key != "real-2" {
		t.Errorf("Keys[1].Key = %q, want real-2 (unchanged)", cfg.Keys[1].Key)
	}
}

func TestValidate_Errors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"no keys", func(c *Config) { c.Keys = nil }, "at least one key"},
		{"empty key value", func(c *Config) { c.Keys[0].Key = "" }, "empty key"},
		{"bad port", func(c *Config) { c.Server.Port = 0 }, "server.port"},
		{"empty base_url", func(c *Config) { c.Upstream.BaseURL = "" }, "upstream.base_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustParse(t, validYAML)
			tc.mutate(cfg)
			err := cfg.validate()
			if err == nil {
				t.Fatalf("validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("validate() error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidate_AppliesDefaults(t *testing.T) {
	cfg := mustParse(t, validYAML)
	cfg.Scheduler.MaxRetries = 0
	cfg.Upstream.TimeoutSeconds = 0
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Scheduler.MaxRetries != 3 {
		t.Errorf("MaxRetries default = %d, want 3", cfg.Scheduler.MaxRetries)
	}
	if cfg.Upstream.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds default = %d, want 30", cfg.Upstream.TimeoutSeconds)
	}
}

// TestLoad_DefaultValues 走完整 Load 路径，验证最小配置经 Load 后正确填充默认值。
func TestLoad_DefaultValues(t *testing.T) {
	minYAML := `
server:
  port: 8080
upstream:
  base_url: "https://example.com"
keys:
  - key: "k1"
`
	p := writeTempConfig(t, minYAML)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Host default = %q, want 0.0.0.0", cfg.Server.Host)
	}
	if cfg.Upstream.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds default = %d, want 30", cfg.Upstream.TimeoutSeconds)
	}
	if cfg.Scheduler.MaxRetries != 3 {
		t.Errorf("MaxRetries default = %d, want 3", cfg.Scheduler.MaxRetries)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level default = %q, want info", cfg.Logging.Level)
	}
	if cfg.Keys[0].Label != "key-1" {
		t.Errorf("empty label default = %q, want key-1", cfg.Keys[0].Label)
	}
}

// TestLoad_InvalidEnvPort 验证非法 GW_SERVER_PORT 触发 fail-fast 错误（不静默回退）。
func TestLoad_InvalidEnvPort(t *testing.T) {
	p := writeTempConfig(t, validYAML)
	t.Setenv("GW_SERVER_PORT", "not-a-number")
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load with invalid GW_SERVER_PORT = nil error, want error")
	}
	if !strings.Contains(err.Error(), "GW_SERVER_PORT") {
		t.Errorf("error = %q, want substring GW_SERVER_PORT", err.Error())
	}
}
