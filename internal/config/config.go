// Package config 负责加载、校验网关配置，并支持环境变量覆盖敏感字段。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是网关的完整配置。
type Config struct {
	Server     ServerConfig               `yaml:"server"`
	Workspaces map[string]WorkspaceConfig `yaml:"workspaces"`
	Scheduler  SchedulerConfig            `yaml:"scheduler"`
	Keys       []KeyConfig                `yaml:"keys"`
	ClientAuth ClientAuthConfig           `yaml:"client_auth"`
	Logging    LoggingConfig              `yaml:"logging"`
}

// ServerConfig 控制 HTTP 监听地址。
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// WorkspaceConfig 是单个 workspace（对应一个上游体育 API 子站）的配置。
type WorkspaceConfig struct {
	BaseURL        string `yaml:"base_url"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// SchedulerConfig 控制 key 调度与退避行为。
type SchedulerConfig struct {
	SwitchThreshold          int `yaml:"switch_threshold"`
	RateLimitCooldownSeconds int `yaml:"rate_limit_cooldown_seconds"`
	ErrorCooldownSeconds     int `yaml:"error_cooldown_seconds"`
	MaxErrorCount            int `yaml:"max_error_count"`
	MaxRetries               int `yaml:"max_retries"`
}

// KeyConfig 是单个上游真实密钥。
type KeyConfig struct {
	Label string `yaml:"label"`
	Key   string `yaml:"key"`
}

// ClientAuthConfig 控制下游客户端认证（初版不启用）。
type ClientAuthConfig struct {
	Enabled bool     `yaml:"enabled"`
	Tokens  []string `yaml:"tokens"`
}

// LoggingConfig 控制日志级别。
type LoggingConfig struct {
	Level string `yaml:"level"`
}

// Load 从指定路径读取 YAML 配置并校验。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := applyEnvOverrides(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnvOverrides 用环境变量覆盖敏感/可变字段。
// GW_SERVER_PORT 覆盖监听端口；GW_KEY_<LABEL> 覆盖对应 label 的真实 key。
// 显式提供但非法的值会返回错误（fail-fast），避免静默回退到 YAML 值。
func applyEnvOverrides(cfg *Config) error {
	if v := os.Getenv("GW_SERVER_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("GW_SERVER_PORT=%q is not a valid port number: %w", v, err)
		}
		cfg.Server.Port = port
	}
	for i := range cfg.Keys {
		envName := "GW_KEY_" + sanitizeEnvLabel(cfg.Keys[i].Label)
		if v := os.Getenv(envName); v != "" {
			cfg.Keys[i].Key = v
		}
	}
	return nil
}

// sanitizeEnvLabel 将 label 转为环境变量后缀：大写，非字母数字转下划线。
func sanitizeEnvLabel(label string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(label) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// validate 校验必填字段并为可选字段填默认值。
func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("config invalid: server.port must be 1-65535, got %d", c.Server.Port)
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if len(c.Workspaces) == 0 {
		return fmt.Errorf("config invalid: at least one workspace is required")
	}
	for name, ws := range c.Workspaces {
		if name == "" {
			return fmt.Errorf("config invalid: workspace name must not be empty")
		}
		if strings.Contains(name, "/") {
			return fmt.Errorf("config invalid: workspace name %q must not contain '/'", name)
		}
		if ws.BaseURL == "" {
			return fmt.Errorf("config invalid: workspace %q has empty base_url", name)
		}
		if ws.TimeoutSeconds <= 0 {
			ws.TimeoutSeconds = 30
			c.Workspaces[name] = ws
		}
	}
	if len(c.Keys) == 0 {
		return fmt.Errorf("config invalid: at least one key is required")
	}
	for i, k := range c.Keys {
		if k.Label == "" {
			c.Keys[i].Label = fmt.Sprintf("key-%d", i+1)
		}
		if k.Key == "" {
			return fmt.Errorf("config invalid: keys[%d] (%s) has empty key value", i, c.Keys[i].Label)
		}
	}
	if c.Scheduler.SwitchThreshold < 0 {
		c.Scheduler.SwitchThreshold = 1
	}
	if c.Scheduler.RateLimitCooldownSeconds <= 0 {
		c.Scheduler.RateLimitCooldownSeconds = 60
	}
	if c.Scheduler.ErrorCooldownSeconds <= 0 {
		c.Scheduler.ErrorCooldownSeconds = 30
	}
	if c.Scheduler.MaxErrorCount <= 0 {
		c.Scheduler.MaxErrorCount = 3
	}
	if c.Scheduler.MaxRetries <= 0 {
		c.Scheduler.MaxRetries = 3
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	return nil
}
