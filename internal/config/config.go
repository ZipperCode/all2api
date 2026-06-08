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
	Server          ServerConfig                    `yaml:"server" json:"server"`
	Platforms       map[string]PlatformConfig       `yaml:"platforms" json:"platforms"`
	CredentialPools map[string]CredentialPoolConfig `yaml:"credential_pools" json:"credential_pools"`
	Scheduler       SchedulerConfig                 `yaml:"scheduler" json:"scheduler"`
	Management      ManagementConfig                `yaml:"management" json:"management"`
	ClientAuth      ClientAuthConfig                `yaml:"client_auth" json:"client_auth"`
	Logging         LoggingConfig                   `yaml:"logging" json:"logging"`

	// Legacy API-Sports config. validate converts it into Platforms and
	// CredentialPools when the new generic fields are absent.
	Workspaces map[string]WorkspaceConfig `yaml:"workspaces" json:"workspaces,omitempty"`
	Keys       []KeyConfig                `yaml:"keys" json:"keys,omitempty"`
}

// ServerConfig 控制 HTTP 监听地址。
type ServerConfig struct {
	Host string `yaml:"host" json:"host"`
	Port int    `yaml:"port" json:"port"`
}

// WorkspaceConfig 是单个 workspace（对应一个上游体育 API 子站）的配置。
type WorkspaceConfig struct {
	BaseURL        string `yaml:"base_url" json:"base_url"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

// PlatformConfig is one public routing prefix and its upstream behavior.
type PlatformConfig struct {
	Type           string          `yaml:"type" json:"type"`
	BaseURL        string          `yaml:"base_url" json:"base_url"`
	TimeoutSeconds int             `yaml:"timeout_seconds" json:"timeout_seconds"`
	CredentialPool string          `yaml:"credential_pool" json:"credential_pool"`
	Auth           AuthConfig      `yaml:"auth" json:"auth"`
	RateLimit      RateLimitConfig `yaml:"rate_limit" json:"rate_limit"`
}

// AuthConfig configures generic header credential injection.
type AuthConfig struct {
	Header string `yaml:"header" json:"header"`
	Prefix string `yaml:"prefix" json:"prefix"`
}

// RateLimitConfig configures generic response headers used for quota tracking.
type RateLimitConfig struct {
	DailyLimitHeader      string `yaml:"daily_limit_header" json:"daily_limit_header"`
	DailyRemainingHeader  string `yaml:"daily_remaining_header" json:"daily_remaining_header"`
	MinuteLimitHeader     string `yaml:"minute_limit_header" json:"minute_limit_header"`
	MinuteRemainingHeader string `yaml:"minute_remaining_header" json:"minute_remaining_header"`
}

// CredentialPoolConfig groups upstream credentials that can be shared by
// multiple platforms.
type CredentialPoolConfig struct {
	Keys []KeyConfig `yaml:"keys" json:"keys"`
}

// SchedulerConfig 控制 key 调度与退避行为。
type SchedulerConfig struct {
	SwitchThreshold          int `yaml:"switch_threshold" json:"switch_threshold"`
	RateLimitCooldownSeconds int `yaml:"rate_limit_cooldown_seconds" json:"rate_limit_cooldown_seconds"`
	ErrorCooldownSeconds     int `yaml:"error_cooldown_seconds" json:"error_cooldown_seconds"`
	MaxErrorCount            int `yaml:"max_error_count" json:"max_error_count"`
	MaxRetries               int `yaml:"max_retries" json:"max_retries"`
}

// KeyConfig 是单个上游真实密钥。
type KeyConfig struct {
	Label string `yaml:"label" json:"label"`
	Key   string `yaml:"key" json:"key"`
}

// ClientAuthConfig 控制下游客户端认证（初版不启用）。
type ClientAuthConfig struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Tokens  []string `yaml:"tokens" json:"tokens"`
}

// ManagementConfig controls the built-in web admin console.
type ManagementConfig struct {
	AdminKey     string `yaml:"admin_key" json:"admin_key"`
	LogFile      string `yaml:"log_file" json:"log_file"`
	LogReadLimit int    `yaml:"log_read_limit" json:"log_read_limit"`
}

// LoggingConfig 控制日志级别。
type LoggingConfig struct {
	Level string `yaml:"level" json:"level"`
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

// Save validates and writes config as YAML.
func Save(path string, cfg *Config) error {
	next := cfg.Clone()
	if err := next.Validate(); err != nil {
		return err
	}
	raw, err := yaml.Marshal(next)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Clone returns a deep copy through YAML so map/slice mutations are isolated.
func (c *Config) Clone() *Config {
	raw, err := yaml.Marshal(c)
	if err != nil {
		panic(err)
	}
	var out Config
	if err := yaml.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return &out
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
	for poolName, pool := range cfg.CredentialPools {
		for i := range pool.Keys {
			envName := "GW_KEY_" + sanitizeEnvLabel(poolName) + "_" + sanitizeEnvLabel(pool.Keys[i].Label)
			if v := os.Getenv(envName); v != "" {
				pool.Keys[i].Key = v
				continue
			}
			if poolName == defaultCredentialPool {
				legacyEnvName := "GW_KEY_" + sanitizeEnvLabel(pool.Keys[i].Label)
				if v := os.Getenv(legacyEnvName); v != "" {
					pool.Keys[i].Key = v
				}
			}
		}
		cfg.CredentialPools[poolName] = pool
	}
	if v := os.Getenv("GW_ADMIN_KEY"); v != "" {
		cfg.Management.AdminKey = v
	}
	if v := os.Getenv("GW_LOG_FILE"); v != "" {
		cfg.Management.LogFile = v
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
const (
	defaultCredentialPool = "default"
	providerAPISports     = "api_sports"
	providerGenericHTTP   = "generic_http"
)

func (c *Config) validate() error {
	return c.Validate()
}

// Validate 校验必填字段并为可选字段填默认值。
func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("config invalid: server.port must be 1-65535, got %d", c.Server.Port)
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	c.normalizeLegacyConfig()
	if len(c.Platforms) == 0 {
		return fmt.Errorf("config invalid: at least one platform is required")
	}
	if len(c.CredentialPools) == 0 {
		return fmt.Errorf("config invalid: at least one credential pool is required")
	}
	if err := c.validateCredentialPools(); err != nil {
		return err
	}
	if err := c.validatePlatforms(); err != nil {
		return err
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
	if c.Management.LogFile == "" {
		c.Management.LogFile = "logs/gateway.jsonl"
	}
	if c.Management.LogReadLimit <= 0 {
		c.Management.LogReadLimit = 500
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	return nil
}

func (c *Config) normalizeLegacyConfig() {
	for i, k := range c.Keys {
		if k.Label == "" {
			c.Keys[i].Label = fmt.Sprintf("key-%d", i+1)
		}
	}
	if len(c.CredentialPools) == 0 && len(c.Keys) > 0 {
		c.CredentialPools = map[string]CredentialPoolConfig{
			defaultCredentialPool: {Keys: append([]KeyConfig(nil), c.Keys...)},
		}
	}
	if len(c.Platforms) == 0 && len(c.Workspaces) > 0 {
		c.Platforms = make(map[string]PlatformConfig, len(c.Workspaces))
		for name, ws := range c.Workspaces {
			c.Platforms[name] = PlatformConfig{
				Type:           providerAPISports,
				BaseURL:        ws.BaseURL,
				TimeoutSeconds: ws.TimeoutSeconds,
				CredentialPool: defaultCredentialPool,
			}
		}
	}
}

func (c *Config) validateCredentialPools() error {
	for poolName, pool := range c.CredentialPools {
		if poolName == "" {
			return fmt.Errorf("config invalid: credential pool name must not be empty")
		}
		if strings.Contains(poolName, "/") {
			return fmt.Errorf("config invalid: credential pool name %q must not contain '/'", poolName)
		}
		if len(pool.Keys) == 0 {
			return fmt.Errorf("config invalid: credential pool %q must contain at least one key", poolName)
		}
		for i, k := range pool.Keys {
			if k.Label == "" {
				pool.Keys[i].Label = fmt.Sprintf("key-%d", i+1)
			}
			if k.Key == "" {
				return fmt.Errorf("config invalid: credential pool %q key %q has empty key value", poolName, pool.Keys[i].Label)
			}
		}
		c.CredentialPools[poolName] = pool
		if poolName == defaultCredentialPool {
			c.Keys = append([]KeyConfig(nil), pool.Keys...)
		}
	}
	return nil
}

func (c *Config) validatePlatforms() error {
	for name, platform := range c.Platforms {
		if name == "" {
			return fmt.Errorf("config invalid: platform name must not be empty")
		}
		if strings.Contains(name, "/") {
			return fmt.Errorf("config invalid: platform name %q must not contain '/'", name)
		}
		if platform.Type == "" {
			return fmt.Errorf("config invalid: platform %q has empty type", name)
		}
		switch platform.Type {
		case providerAPISports, providerGenericHTTP:
		default:
			return fmt.Errorf("config invalid: platform %q has unknown type %q", name, platform.Type)
		}
		if platform.BaseURL == "" {
			return fmt.Errorf("config invalid: platform %q has empty base_url", name)
		}
		if platform.TimeoutSeconds <= 0 {
			platform.TimeoutSeconds = 30
		}
		if platform.CredentialPool == "" {
			platform.CredentialPool = defaultCredentialPool
		}
		if _, ok := c.CredentialPools[platform.CredentialPool]; !ok {
			return fmt.Errorf("config invalid: platform %q references unknown credential_pool %q", name, platform.CredentialPool)
		}
		if platform.Type == providerGenericHTTP && strings.TrimSpace(platform.Auth.Header) == "" {
			return fmt.Errorf("config invalid: platform %q generic_http requires auth.header", name)
		}
		c.Platforms[name] = platform
		if ws, ok := c.Workspaces[name]; ok && ws.TimeoutSeconds <= 0 {
			ws.TimeoutSeconds = platform.TimeoutSeconds
			c.Workspaces[name] = ws
		}
	}
	return nil
}
