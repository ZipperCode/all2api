# API-Football 多 Key 请求网关 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一个局域网部署的 Go 请求网关，对客户端屏蔽真实 API-Football 密钥，在多个 key 间顺序耗尽式轮换，实时监控各 key 当日额度，上游错误时自动换 key 重试且对下游透明。

**Architecture:** 显式分层中转（非透明代理）。客户端用 `Authorization: Bearer` 认证（初版直通），网关剥离该头、按顺序耗尽策略从内存 key 池选一个 Active key、注入 `x-apisports-key` 转发到 `https://v3.football.api-sports.io`，读取响应 `x-ratelimit-*` header 更新额度。上游失败（429/耗尽/5xx/网络）内部换 key 重试，仅最终成功响应回传客户端。

**Tech Stack:** Go 1.24（标准库 `net/http`、`net/http/httptest`、`sync`、`context`），`gopkg.in/yaml.v3` 解析配置。

**设计文档:** `docs/superpowers/specs/2026-06-07-api-football-gateway-design.md`

---

## File Structure

| 文件 | 职责 |
|------|------|
| `cmd/gateway/main.go` | 入口：加载配置 → 构建 keypool/auth/proxy/admin → 启 HTTP server + 优雅关闭 |
| `internal/config/config.go` | 配置结构体 + YAML 加载 + 环境变量覆盖 + 校验 |
| `internal/config/config_test.go` | config 单元测试 |
| `internal/keypool/ratelimit.go` | 解析上游 `x-ratelimit-*` header → `RateLimitSnapshot` |
| `internal/keypool/ratelimit_test.go` | ratelimit 单元测试 |
| `internal/keypool/keypool.go` | `KeyState`/`KeyStatus` + 池 + 顺序耗尽选择 + 状态更新 + 加锁 |
| `internal/keypool/keypool_test.go` | keypool 单元测试（含 `-race`） |
| `internal/auth/auth.go` | 客户端 `Authorization` 校验钩子（初版直通） |
| `internal/auth/auth_test.go` | auth 单元测试 |
| `internal/proxy/transport.go` | `http.Client` 封装 + 单次上游请求 + 响应分类 |
| `internal/proxy/transport_test.go` | transport 单元测试 |
| `internal/proxy/handler.go` | 中转管线：认证→选 key→构造上游请求→换 key 重试→回传（下游无感知） |
| `internal/proxy/handler_test.go` | handler 集成测试（`httptest` 假上游） |
| `internal/admin/admin.go` | `GET /__gateway/status`（脱敏额度 JSON）+ `GET /__gateway/health` |
| `internal/admin/admin_test.go` | admin 单元测试 |
| `config.example.yaml` | 示例配置（占位 key，可提交） |
| `.gitignore` | 忽略 `config.yaml`、二进制 |
| `README.md` | 用法说明 |

**包依赖方向**（无环）：`config` ← `keypool`,`auth`,`admin`,`proxy`；`proxy` ← `keypool`,`auth`,`config`；`admin` ← `keypool`；`main` ← 全部。

---

## Task 1: 项目脚手架与 go module

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `config.example.yaml`

- [ ] **Step 1: 初始化 go module**

在 `~/Projects/api-football-gateway/` 下运行：

```bash
cd ~/Projects/api-football-gateway
go mod init api-football-gateway
go get gopkg.in/yaml.v3@v3.0.1
```

Expected: 生成 `go.mod`（module `api-football-gateway`，go 1.24）和 `go.sum`，含 `gopkg.in/yaml.v3`。

- [ ] **Step 2: 创建 `.gitignore`**

```gitignore
# 真实配置（含密钥），不提交
config.yaml

# 编译产物
/gateway
/bin/
*.exe

# 测试与覆盖率
*.out
coverage.html

# 编辑器
.idea/
.vscode/
*.swp
```

- [ ] **Step 3: 创建 `config.example.yaml`**

```yaml
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
    key: "your_real_api_key_1"
  - label: "key-2"
    key: "your_real_api_key_2"

client_auth:
  enabled: false
  # tokens: ["future-token-1"]

logging:
  level: "info"
```

- [ ] **Step 4: 验证 module 可编译（空）**

Run: `go build ./...`
Expected: 无输出、退出码 0（当前无 .go 文件，构建空集成功）。

- [ ] **Step 5: Commit**

```bash
git init
git add go.mod go.sum .gitignore config.example.yaml
git commit -m "chore: init go module and project scaffold"
```

---

## Task 2: config 包 — 结构体与 YAML 加载

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试 — 加载合法 YAML**

`internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
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
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/config/ -run TestLoad_ValidConfig -v`
Expected: FAIL（编译错误，`Load` / 类型未定义）。

- [ ] **Step 3: 写最小实现**

`internal/config/config.go`:

```go
// Package config 负责加载、校验网关配置，并支持环境变量覆盖敏感字段。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是网关的完整配置。
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Upstream   UpstreamConfig   `yaml:"upstream"`
	Scheduler  SchedulerConfig  `yaml:"scheduler"`
	Keys       []KeyConfig      `yaml:"keys"`
	ClientAuth ClientAuthConfig `yaml:"client_auth"`
	Logging    LoggingConfig    `yaml:"logging"`
}

// ServerConfig 控制 HTTP 监听地址。
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// UpstreamConfig 控制上游 API-Football 连接。
type UpstreamConfig struct {
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
	applyEnvOverrides(&cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
```

> 注：`applyEnvOverrides` 与 `validate` 在 Task 3、4 实现。本步骤先加占位以通过编译：在文件末尾追加临时占位（Task 3/4 会替换为真实实现）。

临时占位（同文件追加）：

```go
func applyEnvOverrides(cfg *Config) {}

func (c *Config) validate() error { return nil }
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/config/ -run TestLoad_ValidConfig -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add config struct and YAML loading"
```

---

## Task 3: config 包 — 环境变量覆盖

**Files:**
- Modify: `internal/config/config.go`（替换 `applyEnvOverrides` 占位）
- Test: `internal/config/config_test.go`（追加测试）

环境变量覆盖规则：`GW_SERVER_PORT` 覆盖端口；`GW_KEY_<label大写、非字母数字转下划线>` 覆盖对应 label 的 key 值。例如 label `key-1` → 环境变量 `GW_KEY_KEY_1`。

- [ ] **Step 1: 写失败测试**

`internal/config/config_test.go` 追加：

```go
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
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/config/ -run TestLoad_EnvOverrides -v`
Expected: FAIL（端口仍 8080，key 未覆盖）。

- [ ] **Step 3: 实现 `applyEnvOverrides`**

替换 `internal/config/config.go` 中的占位 `func applyEnvOverrides(cfg *Config) {}`，并补充 import：

```go
import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)
```

```go
// applyEnvOverrides 用环境变量覆盖敏感/可变字段。
// GW_SERVER_PORT 覆盖监听端口；GW_KEY_<LABEL> 覆盖对应 label 的真实 key。
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GW_SERVER_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}
	for i := range cfg.Keys {
		envName := "GW_KEY_" + sanitizeEnvLabel(cfg.Keys[i].Label)
		if v := os.Getenv(envName); v != "" {
			cfg.Keys[i].Key = v
		}
	}
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
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/config/ -run TestLoad_EnvOverrides -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): support env var overrides for port and keys"
```

---

## Task 4: config 包 — 校验

**Files:**
- Modify: `internal/config/config.go`（替换 `validate` 占位）
- Test: `internal/config/config_test.go`（追加测试）

- [ ] **Step 1: 写失败测试**

`internal/config/config_test.go` 追加：

```go
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
```

需要一个 `mustParse` 辅助（追加到测试文件，复用 yaml）：

```go
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
```

并在测试文件 import 增加 `"strings"` 与 `"gopkg.in/yaml.v3"`。

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: FAIL（`validate` 占位返回 nil，不校验也不填默认值）。

- [ ] **Step 3: 实现 `validate`**

替换占位 `func (c *Config) validate() error { return nil }`：

```go
// validate 校验必填字段并为可选字段填默认值。
func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("config invalid: server.port must be 1-65535, got %d", c.Server.Port)
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Upstream.BaseURL == "" {
		return fmt.Errorf("config invalid: upstream.base_url is required")
	}
	if len(c.Keys) == 0 {
		return fmt.Errorf("config invalid: at least one key is required")
	}
	for i, k := range c.Keys {
		if k.Key == "" {
			return fmt.Errorf("config invalid: keys[%d] (%s) has empty key value", i, k.Label)
		}
		if k.Label == "" {
			c.Keys[i].Label = fmt.Sprintf("key-%d", i+1)
		}
	}
	// 默认值
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = 30
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
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/config/ -v`
Expected: 所有 config 测试 PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add validation and defaults"
```

---

## Task 5: keypool 包 — 额度 header 解析

**Files:**
- Create: `internal/keypool/ratelimit.go`
- Test: `internal/keypool/ratelimit_test.go`

- [ ] **Step 1: 写失败测试**

`internal/keypool/ratelimit_test.go`:

```go
package keypool

import (
	"net/http"
	"testing"
)

func TestParseRateLimit(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-requests-limit", "7500")
	h.Set("x-ratelimit-requests-remaining", "7499")
	h.Set("X-RateLimit-Limit", "10")
	h.Set("X-RateLimit-Remaining", "9")

	snap, ok := ParseRateLimit(h)
	if !ok {
		t.Fatal("ParseRateLimit ok = false, want true")
	}
	if snap.DailyLimit != 7500 || snap.DailyRemaining != 7499 {
		t.Errorf("daily = %d/%d, want 7499/7500", snap.DailyRemaining, snap.DailyLimit)
	}
	if snap.MinuteLimit != 10 || snap.MinuteRemaining != 9 {
		t.Errorf("minute = %d/%d, want 9/10", snap.MinuteRemaining, snap.MinuteLimit)
	}
}

func TestParseRateLimit_MissingHeaders(t *testing.T) {
	h := http.Header{}
	_, ok := ParseRateLimit(h)
	if ok {
		t.Error("ParseRateLimit ok = true on empty headers, want false")
	}
}

func TestParseRateLimit_PartialAndMalformed(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-requests-remaining", "not-a-number")
	h.Set("x-ratelimit-requests-limit", "100")
	snap, ok := ParseRateLimit(h)
	// limit 可解析 → ok=true；remaining 非法 → 该字段 HasDaily 视为不可用
	if !ok {
		t.Fatal("ok = false, want true (limit parseable)")
	}
	if snap.HasDaily {
		t.Error("HasDaily = true, want false (remaining malformed)")
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/keypool/ -run TestParseRateLimit -v`
Expected: FAIL（`ParseRateLimit` / `RateLimitSnapshot` 未定义）。

- [ ] **Step 3: 写实现**

`internal/keypool/ratelimit.go`:

```go
// Package keypool 维护上游真实 key 的运行时状态、额度快照与顺序耗尽调度。
package keypool

import (
	"net/http"
	"strconv"
)

// RateLimitSnapshot 是从上游响应 header 解析出的额度快照。
type RateLimitSnapshot struct {
	DailyLimit      int
	DailyRemaining  int
	MinuteLimit     int
	MinuteRemaining int
	HasDaily        bool // 当日 limit 与 remaining 均成功解析
	HasMinute       bool // 每分钟 limit 与 remaining 均成功解析
}

// ParseRateLimit 解析 API-Football 的额度 header。
// 返回的 ok 表示是否至少解析到一组有效数值（用于判断响应是否携带额度信息）。
func ParseRateLimit(h http.Header) (RateLimitSnapshot, bool) {
	var snap RateLimitSnapshot

	dl, dlOK := atoiHeader(h, "x-ratelimit-requests-limit")
	dr, drOK := atoiHeader(h, "x-ratelimit-requests-remaining")
	if dlOK {
		snap.DailyLimit = dl
	}
	if drOK {
		snap.DailyRemaining = dr
	}
	snap.HasDaily = dlOK && drOK

	ml, mlOK := atoiHeader(h, "X-RateLimit-Limit")
	mr, mrOK := atoiHeader(h, "X-RateLimit-Remaining")
	if mlOK {
		snap.MinuteLimit = ml
	}
	if mrOK {
		snap.MinuteRemaining = mr
	}
	snap.HasMinute = mlOK && mrOK

	ok := dlOK || drOK || mlOK || mrOK
	return snap, ok
}

// atoiHeader 读取并解析某 header 为 int，缺失或非法返回 (0,false)。
func atoiHeader(h http.Header, name string) (int, bool) {
	v := h.Get(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/keypool/ -run TestParseRateLimit -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/keypool/ratelimit.go internal/keypool/ratelimit_test.go
git commit -m "feat(keypool): parse upstream x-ratelimit headers"
```

---

## Task 6: keypool 包 — KeyState、状态机与池构造

**Files:**
- Create: `internal/keypool/keypool.go`
- Test: `internal/keypool/keypool_test.go`

本任务引入一个可注入的时钟以便测试时间相关逻辑。

- [ ] **Step 1: 写失败测试 — 池构造与脱敏**

`internal/keypool/keypool_test.go`:

```go
package keypool

import (
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	p := New([]KeyEntry{
		{Label: "key-1", Key: "secret-abcd1"},
		{Label: "key-2", Key: "secret-wxyz9"},
	}, Options{
		SwitchThreshold:     1,
		RateLimitCooldown:   60 * time.Second,
		ErrorCooldown:       30 * time.Second,
		MaxErrorCount:       3,
	})
	return p
}

func TestNew_InitialStateActive(t *testing.T) {
	p := newTestPool(t)
	states := p.Snapshot()
	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}
	for _, s := range states {
		if s.Status != StatusActive {
			t.Errorf("key %s initial status = %v, want Active", s.Label, s.Status)
		}
	}
}

func TestMaskedKey(t *testing.T) {
	got := maskKey("secret-abcd1")
	if got != "****bcd1" {
		t.Errorf("maskKey = %q, want ****bcd1", got)
	}
	if maskKey("ab") != "****" {
		t.Errorf("maskKey short = %q, want ****", maskKey("ab"))
	}
}

func TestSnapshot_DoesNotLeakFullKey(t *testing.T) {
	p := newTestPool(t)
	for _, s := range p.Snapshot() {
		if s.MaskedKey == "secret-abcd1" || s.MaskedKey == "secret-wxyz9" {
			t.Errorf("Snapshot leaked full key: %q", s.MaskedKey)
		}
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/keypool/ -run 'TestNew_InitialStateActive|TestMaskedKey|TestSnapshot' -v`
Expected: FAIL（`Pool`/`New`/`KeyEntry`/`Options`/`StatusActive`/`maskKey`/`Snapshot` 未定义）。

- [ ] **Step 3: 写实现**

`internal/keypool/keypool.go`:

```go
package keypool

import (
	"sync"
	"time"
)

// KeyStatus 是单个 key 的调度状态。
type KeyStatus int

const (
	StatusActive      KeyStatus = iota // 可用
	StatusExhausted                    // 当日额度耗尽
	StatusRateLimited                  // 触发每分钟限速
	StatusError                        // 连续异常
)

func (s KeyStatus) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusExhausted:
		return "exhausted"
	case StatusRateLimited:
		return "rate_limited"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// KeyEntry 是构造池时传入的单个 key 定义。
type KeyEntry struct {
	Label string
	Key   string
}

// Options 控制调度与退避。
type Options struct {
	SwitchThreshold   int
	RateLimitCooldown time.Duration
	ErrorCooldown     time.Duration
	MaxErrorCount     int
}

// keyState 是池内部维护的单个 key 完整状态（不导出，含真实 key）。
type keyState struct {
	label           string
	key             string
	dailyLimit      int
	dailyRemaining  int
	minuteLimit     int
	minuteRemaining int
	lastUpdated     time.Time
	status          KeyStatus
	cooldownUntil   time.Time
	errorCount      int
	lastError       string
}

// KeyStateView 是对外暴露的脱敏快照（用于 /__gateway/status）。
type KeyStateView struct {
	Label           string    `json:"label"`
	MaskedKey       string    `json:"masked_key"`
	Status          string    `json:"status"`
	DailyLimit      int       `json:"daily_limit"`
	DailyRemaining  int       `json:"daily_remaining"`
	MinuteLimit     int       `json:"minute_limit"`
	MinuteRemaining int       `json:"minute_remaining"`
	LastUpdated     time.Time `json:"last_updated"`
	CooldownUntil   time.Time `json:"cooldown_until,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

// Pool 是并发安全的 key 池。
type Pool struct {
	mu    sync.RWMutex
	keys  []*keyState
	opts  Options
	now   func() time.Time // 可注入时钟，便于测试
}

// New 构造一个 key 池，所有 key 初始为 Active。
func New(entries []KeyEntry, opts Options) *Pool {
	keys := make([]*keyState, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, &keyState{
			label:  e.Label,
			key:    e.Key,
			status: StatusActive,
		})
	}
	return &Pool{keys: keys, opts: opts, now: time.Now}
}

// maskKey 返回脱敏后的 key：****+尾4位；长度不足返回 ****。
func maskKey(k string) string {
	if len(k) < 4 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

// Snapshot 返回所有 key 的脱敏视图，用于监控端点。
func (p *Pool) Snapshot() []KeyStateView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	views := make([]KeyStateView, 0, len(p.keys))
	for _, k := range p.keys {
		views = append(views, KeyStateView{
			Label:           k.label,
			MaskedKey:       maskKey(k.key),
			Status:          k.status.String(),
			DailyLimit:      k.dailyLimit,
			DailyRemaining:  k.dailyRemaining,
			MinuteLimit:     k.minuteLimit,
			MinuteRemaining: k.minuteRemaining,
			LastUpdated:     k.lastUpdated,
			CooldownUntil:   k.cooldownUntil,
			LastError:       k.lastError,
		})
	}
	return views
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/keypool/ -run 'TestNew_InitialStateActive|TestMaskedKey|TestSnapshot' -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/keypool/keypool.go internal/keypool/keypool_test.go
git commit -m "feat(keypool): add KeyState, status enum, pool construction and masking"
```

---

## Task 7: keypool 包 — 顺序耗尽选择与跨天重置

**Files:**
- Modify: `internal/keypool/keypool.go`（追加 `Acquire` 等方法）
- Test: `internal/keypool/keypool_test.go`（追加测试）

- [ ] **Step 1: 写失败测试**

`internal/keypool/keypool_test.go` 追加：

```go
func TestAcquire_SequentialExhaustion(t *testing.T) {
	p := newTestPool(t)
	// 初始应返回第一个 key
	h, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Label() != "key-1" {
		t.Errorf("first Acquire = %s, want key-1", h.Label())
	}
	// 标记 key-1 耗尽 → 下次应返回 key-2
	p.MarkExhausted("key-1")
	h2, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire after exhaust: %v", err)
	}
	if h2.Label() != "key-2" {
		t.Errorf("after key-1 exhausted, Acquire = %s, want key-2", h2.Label())
	}
}

func TestAcquire_AllUnavailable(t *testing.T) {
	p := newTestPool(t)
	p.MarkExhausted("key-1")
	p.MarkExhausted("key-2")
	_, err := p.Acquire()
	if err == nil {
		t.Fatal("Acquire returned nil error when all keys unavailable")
	}
}

func TestAcquire_SkipsRateLimitedUntilCooldown(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	p := newTestPool(t)
	p.now = fixedClock(now)

	p.MarkRateLimited("key-1") // cooldown 60s
	h, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Label() != "key-2" {
		t.Errorf("Acquire = %s, want key-2 (key-1 rate limited)", h.Label())
	}

	// 推进时钟越过冷却 → key-1 恢复
	p.now = fixedClock(now.Add(61 * time.Second))
	h2, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire after cooldown: %v", err)
	}
	if h2.Label() != "key-1" {
		t.Errorf("after cooldown Acquire = %s, want key-1", h2.Label())
	}
}

func TestAcquire_CrossDayResetsExhausted(t *testing.T) {
	day1 := time.Date(2026, 6, 7, 23, 0, 0, 0, time.UTC)
	p := newTestPool(t)
	p.now = fixedClock(day1)
	// key-1 在 day1 耗尽并记录 lastUpdated
	p.UpdateFromUpstream("key-1", RateLimitSnapshot{DailyLimit: 100, DailyRemaining: 0, HasDaily: true})
	if got := statusOf(t, p, "key-1"); got != "exhausted" {
		t.Fatalf("key-1 status = %s, want exhausted", got)
	}
	// 次日：选 key 时应乐观复位 key-1
	day2 := time.Date(2026, 6, 8, 1, 0, 0, 0, time.UTC)
	p.now = fixedClock(day2)
	h, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire day2: %v", err)
	}
	if h.Label() != "key-1" {
		t.Errorf("day2 Acquire = %s, want key-1 (cross-day reset)", h.Label())
	}
}

func statusOf(t *testing.T, p *Pool, label string) string {
	t.Helper()
	for _, s := range p.Snapshot() {
		if s.Label == label {
			return s.Status
		}
	}
	t.Fatalf("label %s not found", label)
	return ""
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/keypool/ -run TestAcquire -v`
Expected: FAIL（`Acquire`/`Handle`/`MarkExhausted`/`MarkRateLimited`/`UpdateFromUpstream` 未定义）。

- [ ] **Step 3: 写实现**

在 `internal/keypool/keypool.go` 追加（注意 import 增加 `"errors"`）：

```go
// ErrNoKeyAvailable 表示当前没有任何可用 key。
var ErrNoKeyAvailable = errors.New("no key available")

// Handle 是一次 Acquire 返回的句柄，标识被选中的 key。
type Handle struct {
	pool  *Pool
	state *keyState
}

// Label 返回句柄对应 key 的标签。
func (h *Handle) Label() string { return h.state.label }

// Key 返回句柄对应的真实 key（仅供注入上游请求，不得记录）。
func (h *Handle) Key() string { return h.state.key }

// Acquire 按顺序耗尽策略返回第一个可用 key 的句柄。
// 选取前会做跨天重置与冷却到期检查。无可用 key 返回 ErrNoKeyAvailable。
func (p *Pool) Acquire() (*Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, k := range p.keys {
		p.refreshLocked(k, now)
		if k.status == StatusActive {
			return &Handle{pool: p, state: k}, nil
		}
	}
	return nil, ErrNoKeyAvailable
}

// refreshLocked 在持锁状态下根据当前时间复位可恢复的 key。
func (p *Pool) refreshLocked(k *keyState, now time.Time) {
	switch k.status {
	case StatusRateLimited, StatusError:
		if !k.cooldownUntil.IsZero() && now.After(k.cooldownUntil) {
			k.status = StatusActive
			k.errorCount = 0
		}
	case StatusExhausted:
		// 跨 UTC 日界 → 乐观复位，真实额度由下次响应修正
		if !k.lastUpdated.IsZero() && !sameUTCDate(k.lastUpdated, now) {
			k.status = StatusActive
		}
	}
}

func sameUTCDate(a, b time.Time) bool {
	au, bu := a.UTC(), b.UTC()
	return au.Year() == bu.Year() && au.YearDay() == bu.YearDay()
}

// findLocked 按 label 查 keyState，未找到返回 nil。
func (p *Pool) findLocked(label string) *keyState {
	for _, k := range p.keys {
		if k.label == label {
			return k
		}
	}
	return nil
}

// MarkExhausted 将指定 key 标记为当日耗尽。
func (p *Pool) MarkExhausted(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.status = StatusExhausted
		if k.lastUpdated.IsZero() {
			k.lastUpdated = p.now()
		}
	}
}

// MarkRateLimited 将指定 key 标记为限速并设置冷却。
func (p *Pool) MarkRateLimited(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.status = StatusRateLimited
		k.cooldownUntil = p.now().Add(p.opts.RateLimitCooldown)
	}
}

// MarkError 累计错误计数，达到上限则标记 Error 并设置冷却。
func (p *Pool) MarkError(label, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.errorCount++
		k.lastError = reason
		if k.errorCount >= p.opts.MaxErrorCount {
			k.status = StatusError
			k.cooldownUntil = p.now().Add(p.opts.ErrorCooldown)
		}
	}
}

// MarkKeyInvalid 立即将 key 标记为 Error（用于 401/403 等明确失效）。
func (p *Pool) MarkKeyInvalid(label, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.status = StatusError
		k.lastError = reason
		k.cooldownUntil = p.now().Add(p.opts.ErrorCooldown)
	}
}

// UpdateFromUpstream 用上游响应额度快照更新 key 状态。
// 若当日剩余 <= SwitchThreshold，标记为 Exhausted（不影响本次已完成的请求）。
func (p *Pool) UpdateFromUpstream(label string, snap RateLimitSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := p.findLocked(label)
	if k == nil {
		return
	}
	now := p.now()
	if snap.HasDaily {
		k.dailyLimit = snap.DailyLimit
		k.dailyRemaining = snap.DailyRemaining
	}
	if snap.HasMinute {
		k.minuteLimit = snap.MinuteLimit
		k.minuteRemaining = snap.MinuteRemaining
	}
	k.lastUpdated = now
	// 成功响应重置错误计数
	if k.status == StatusError {
		k.errorCount = 0
	}
	k.status = StatusActive
	if snap.HasDaily && snap.DailyRemaining <= p.opts.SwitchThreshold {
		k.status = StatusExhausted
	}
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/keypool/ -run TestAcquire -v`
Expected: PASS（4 个子用例全过）。

- [ ] **Step 5: 全 keypool 测试 + 竞态**

Run: `go test -race ./internal/keypool/ -v`
Expected: 全部 PASS，无 race 报告。

- [ ] **Step 6: Commit**

```bash
git add internal/keypool/keypool.go internal/keypool/keypool_test.go
git commit -m "feat(keypool): sequential exhaustion selection, status transitions, cross-day reset"
```

---

## Task 8: keypool 包 — 全部 key 不可用时的状态汇总

**Files:**
- Modify: `internal/keypool/keypool.go`（追加 `EarliestRecoverySeconds`）
- Test: `internal/keypool/keypool_test.go`（追加测试）

503 响应需要 `Retry-After` = 最早恢复 key 的冷却剩余秒数。

- [ ] **Step 1: 写失败测试**

追加：

```go
func TestEarliestRecoverySeconds(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	p := newTestPool(t)
	p.now = fixedClock(now)
	p.MarkRateLimited("key-1") // cooldown 60s → 恢复在 +60s
	p.MarkExhausted("key-2")   // 耗尽，当天不恢复 → 视为很大

	secs := p.EarliestRecoverySeconds()
	if secs != 60 {
		t.Errorf("EarliestRecoverySeconds = %d, want 60", secs)
	}
}

func TestEarliestRecoverySeconds_AllExhausted(t *testing.T) {
	p := newTestPool(t)
	p.MarkExhausted("key-1")
	p.MarkExhausted("key-2")
	secs := p.EarliestRecoverySeconds()
	if secs <= 0 {
		t.Errorf("EarliestRecoverySeconds = %d, want > 0 fallback", secs)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/keypool/ -run TestEarliestRecovery -v`
Expected: FAIL（方法未定义）。

- [ ] **Step 3: 写实现**

追加到 `keypool.go`：

```go
// defaultRetryAfterSeconds 是无法精确计算恢复时间时的兜底（如全部耗尽，
// 距 UTC 次日重置可能很久，但返回一个温和的重试间隔避免客户端死等）。
const defaultRetryAfterSeconds = 300

// EarliestRecoverySeconds 返回最早一个 key 恢复可用的秒数，用于 503 的 Retry-After。
// 对 RateLimited/Error 用 cooldownUntil 计算；Exhausted 无明确冷却，用兜底值参与比较。
func (p *Pool) EarliestRecoverySeconds() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := p.now()
	best := -1
	for _, k := range p.keys {
		var secs int
		switch k.status {
		case StatusActive:
			return 0
		case StatusRateLimited, StatusError:
			if k.cooldownUntil.IsZero() {
				secs = defaultRetryAfterSeconds
			} else {
				d := int(k.cooldownUntil.Sub(now).Seconds())
				if d < 1 {
					d = 1
				}
				secs = d
			}
		case StatusExhausted:
			secs = defaultRetryAfterSeconds
		default:
			secs = defaultRetryAfterSeconds
		}
		if best == -1 || secs < best {
			best = secs
		}
	}
	if best < 1 {
		best = defaultRetryAfterSeconds
	}
	return best
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test -race ./internal/keypool/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/keypool/keypool.go internal/keypool/keypool_test.go
git commit -m "feat(keypool): compute earliest recovery seconds for Retry-After"
```

---

## Task 9: auth 包 — 客户端认证钩子

**Files:**
- Create: `internal/auth/auth.go`
- Test: `internal/auth/auth_test.go`

- [ ] **Step 1: 写失败测试**

`internal/auth/auth_test.go`:

```go
package auth

import (
	"net/http"
	"testing"
)

func TestAuthorize_DisabledAllowsAnything(t *testing.T) {
	a := New(false, nil)
	req, _ := http.NewRequest(http.MethodGet, "/fixtures", nil)
	// 无 Authorization 也应通过（初版直通）
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (disabled, no header) = %v, want nil", err)
	}
	req.Header.Set("Authorization", "Bearer anything")
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (disabled, any token) = %v, want nil", err)
	}
}

func TestAuthorize_EnabledChecksToken(t *testing.T) {
	a := New(true, []string{"good-token"})
	req, _ := http.NewRequest(http.MethodGet, "/fixtures", nil)

	req.Header.Set("Authorization", "Bearer good-token")
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (valid token) = %v, want nil", err)
	}

	req.Header.Set("Authorization", "Bearer bad-token")
	if err := a.Authorize(req); err == nil {
		t.Error("Authorize (invalid token) = nil, want error")
	}

	req.Header.Del("Authorization")
	if err := a.Authorize(req); err == nil {
		t.Error("Authorize (missing token when enabled) = nil, want error")
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/auth/ -v`
Expected: FAIL（`New`/`Authorize` 未定义）。

- [ ] **Step 3: 写实现**

`internal/auth/auth.go`:

```go
// Package auth 提供下游客户端认证钩子。初版不启用（直通），
// 启用后校验 Authorization: Bearer <token> 是否在白名单内。
package auth

import (
	"errors"
	"net/http"
	"strings"
)

// ErrUnauthorized 表示客户端凭证缺失或不在白名单内。
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator 校验下游请求的 Authorization 头。
type Authenticator struct {
	enabled bool
	tokens  map[string]struct{}
}

// New 构造认证器。enabled=false 时一律放行。
func New(enabled bool, tokens []string) *Authenticator {
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		set[t] = struct{}{}
	}
	return &Authenticator{enabled: enabled, tokens: set}
}

// Authorize 校验请求；未启用直接返回 nil。
func (a *Authenticator) Authorize(r *http.Request) error {
	if !a.enabled {
		return nil
	}
	token := extractBearer(r.Header.Get("Authorization"))
	if token == "" {
		return ErrUnauthorized
	}
	if _, ok := a.tokens[token]; !ok {
		return ErrUnauthorized
	}
	return nil
}

// extractBearer 从 "Bearer <token>" 提取 token，大小写不敏感于 scheme。
func extractBearer(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/auth/ -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/auth/auth.go internal/auth/auth_test.go
git commit -m "feat(auth): client Authorization hook (pass-through in v1)"
```

---

## Task 10: proxy 包 — transport（单次上游请求与响应分类）

**Files:**
- Create: `internal/proxy/transport.go`
- Test: `internal/proxy/transport_test.go`

transport 负责：根据 base_url + 原始请求构造上游请求、注入 key、发送、读取完整响应体（实现"下游无感知"的缓冲前提）、把上游响应归类为一个 `outcome`。

- [ ] **Step 1: 写失败测试**

`internal/proxy/transport_test.go`:

```go
package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		status int
		want   outcomeKind
	}{
		{200, outcomeSuccess},
		{204, outcomeSuccess},
		{429, outcomeRateLimited},
		{401, outcomeKeyInvalid},
		{403, outcomeKeyInvalid},
		{500, outcomeServerError},
		{502, outcomeServerError},
		{400, outcomeClientError},
		{404, outcomeClientError},
	}
	for _, c := range cases {
		if got := classifyStatus(c.status); got != c.want {
			t.Errorf("classifyStatus(%d) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestDoUpstream_InjectsKeyAndStripsAuthorization(t *testing.T) {
	var gotAPIKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-apisports-key")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("x-ratelimit-requests-limit", "100")
		w.Header().Set("x-ratelimit-requests-remaining", "99")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, 5*time.Second)

	clientReq, _ := http.NewRequest(http.MethodGet, "http://gateway/fixtures?live=all", nil)
	clientReq.Header.Set("Authorization", "Bearer client-token")

	res := tr.do(clientReq, "real-secret")
	if res.err != nil {
		t.Fatalf("do err: %v", res.err)
	}
	if gotAPIKey != "real-secret" {
		t.Errorf("upstream x-apisports-key = %q, want real-secret", gotAPIKey)
	}
	if gotAuth != "" {
		t.Errorf("upstream received Authorization = %q, want empty (stripped)", gotAuth)
	}
	if res.kind != outcomeSuccess {
		t.Errorf("kind = %v, want success", res.kind)
	}
	if !res.snapshot.HasDaily || res.snapshot.DailyRemaining != 99 {
		t.Errorf("snapshot = %+v, want daily 99/100", res.snapshot)
	}
	if string(res.body) != `{"ok":true}` {
		t.Errorf("body = %q", string(res.body))
	}
}

func TestDoUpstream_PreservesQueryAndPath(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, 5*time.Second)
	clientReq, _ := http.NewRequest(http.MethodGet, "http://gateway/players?team=33&season=2024", nil)
	res := tr.do(clientReq, "k")
	if res.err != nil {
		t.Fatalf("do: %v", res.err)
	}
	if gotPath != "/players" {
		t.Errorf("path = %q, want /players", gotPath)
	}
	if gotQuery != "team=33&season=2024" {
		t.Errorf("query = %q, want team=33&season=2024", gotQuery)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/proxy/ -run 'TestClassifyOutcome|TestDoUpstream' -v`
Expected: FAIL（`classifyStatus`/`outcomeKind`/`newTransport`/`transport.do`/`upstreamResult` 未定义）。

- [ ] **Step 3: 写实现**

`internal/proxy/transport.go`:

```go
package proxy

import (
	"io"
	"net/http"
	"strings"
	"time"

	"api-football-gateway/internal/keypool"
)

// outcomeKind 是上游响应的归类结果。
type outcomeKind int

const (
	outcomeSuccess     outcomeKind = iota // 2xx
	outcomeRateLimited                    // 429 → 换 key
	outcomeKeyInvalid                     // 401/403 → key 失效，换 key
	outcomeServerError                    // 5xx / 网络错误 → 换 key
	outcomeClientError                    // 其余 4xx（非额度类）→ 不重试，原样回传
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
	baseURL string
	client  *http.Client
}

// newTransport 构造 transport。
func newTransport(baseURL string, timeout time.Duration) *transport {
	return &transport{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// do 用给定真实 key 向上游发起一次请求，返回已缓冲的结果。
// 注入 x-apisports-key；不透传下游 Authorization（构造全新请求头）。
func (t *transport) do(clientReq *http.Request, apiKey string) upstreamResult {
	url := t.baseURL + clientReq.URL.Path
	if clientReq.URL.RawQuery != "" {
		url += "?" + clientReq.URL.RawQuery
	}

	var bodyReader io.Reader
	if clientReq.Body != nil {
		// 调用方负责保证 Body 可重读（handler 已缓冲）。
		bodyReader = clientReq.Body
	}

	upReq, err := http.NewRequestWithContext(clientReq.Context(), clientReq.Method, url, bodyReader)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}
	// 仅透传安全的内容相关头，绝不透传 Authorization。
	copySafeRequestHeaders(clientReq.Header, upReq.Header)
	upReq.Header.Set("x-apisports-key", apiKey)

	resp, err := t.client.Do(upReq)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return upstreamResult{kind: outcomeServerError, err: err}
	}

	snap, _ := keypool.ParseRateLimit(resp.Header)
	return upstreamResult{
		kind:       classifyStatus(resp.StatusCode),
		statusCode: resp.StatusCode,
		header:     resp.Header,
		body:       body,
		snapshot:   snap,
	}
}

// classifyStatus 把 HTTP 状态码归类。
func classifyStatus(code int) outcomeKind {
	switch {
	case code >= 200 && code < 300:
		return outcomeSuccess
	case code == http.StatusTooManyRequests: // 429
		return outcomeRateLimited
	case code == http.StatusUnauthorized || code == http.StatusForbidden: // 401/403
		return outcomeKeyInvalid
	case code >= 500:
		return outcomeServerError
	default: // 其余 4xx
		return outcomeClientError
	}
}

// copySafeRequestHeaders 复制对上游安全且有意义的请求头。
// 显式排除 Authorization（下游凭证）与逐跳头。
func copySafeRequestHeaders(src, dst http.Header) {
	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
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
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/proxy/ -run 'TestClassifyOutcome|TestDoUpstream' -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/transport.go internal/proxy/transport_test.go
git commit -m "feat(proxy): upstream transport with key injection and response classification"
```

---

## Task 11: proxy 包 — handler（中转管线 + 换 key 重试 + 下游无感知）

**Files:**
- Create: `internal/proxy/handler.go`
- Test: `internal/proxy/handler_test.go`

handler 是核心管线：认证 → 缓冲请求体 → 循环（选 key → transport.do → 按 outcome 更新池/决定是否换 key）→ 仅成功响应回传；全失败回 503。

- [ ] **Step 1: 写失败测试 — 正常转发与额度更新**

`internal/proxy/handler_test.go`:

```go
package proxy

import (
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
	// 额度应更新到 key-1
	for _, s := range pool.Snapshot() {
		if s.Label == "key-1" && s.DailyRemaining != 50 {
			t.Errorf("key-1 DailyRemaining = %d, want 50", s.DailyRemaining)
		}
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/proxy/ -run TestServeHTTP_SuccessForwards -v`
Expected: FAIL（`Handler`/`NewHandler`/`Config`/`ServeHTTP` 未定义）。

- [ ] **Step 3: 写实现**

`internal/proxy/handler.go`:

```go
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"api-football-gateway/internal/auth"
	"api-football-gateway/internal/keypool"
)

// Config 配置中转 handler。
type Config struct {
	UpstreamBaseURL string
	Timeout         time.Duration
	MaxRetries      int
}

// Handler 是网关的核心中转处理器。
type Handler struct {
	pool      *keypool.Pool
	auth      *auth.Authenticator
	transport *transport
	maxRetries int
	logger    *slog.Logger
}

// NewHandler 构造中转 handler。logger 为 nil 时使用默认。
func NewHandler(cfg Config, pool *keypool.Pool, a *auth.Authenticator) *Handler {
	return &Handler{
		pool:       pool,
		auth:       a,
		transport:  newTransport(cfg.UpstreamBaseURL, cfg.Timeout),
		maxRetries: cfg.MaxRetries,
		logger:     slog.Default(),
	}
}

// WithLogger 注入自定义 logger。
func (h *Handler) WithLogger(l *slog.Logger) *Handler {
	h.logger = l
	return h
}

// ServeHTTP 执行中转管线：认证 → 缓冲请求体 → 换 key 重试 → 回传。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 客户端认证（初版直通）
	if err := h.auth.Authorize(r); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "client authorization failed", nil, 0)
		return
	}

	// 2. 缓冲请求体以支持换 key 重发
	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "failed to read request body", nil, 0)
			return
		}
		bodyBytes = b
		_ = r.Body.Close()
	}

	// 3. 换 key 重试循环
	attempts := 0
	for attempts < h.maxRetries {
		handle, err := h.pool.Acquire()
		if err != nil {
			break // 无可用 key
		}
		attempts++

		// 每次尝试用新的 body reader
		attemptReq := r.Clone(r.Context())
		if bodyBytes != nil {
			attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		res := h.transport.do(attemptReq, handle.Key())

		switch res.kind {
		case outcomeSuccess:
			h.pool.UpdateFromUpstream(handle.Label(), res.snapshot)
			h.writeUpstreamResponse(w, res)
			return

		case outcomeRateLimited:
			h.pool.MarkRateLimited(handle.Label())
			h.logger.Warn("key rate limited, switching",
				"key", handle.Label(), "attempt", attempts)
			continue

		case outcomeKeyInvalid:
			h.pool.MarkKeyInvalid(handle.Label(), "upstream returned "+strconv.Itoa(res.statusCode))
			h.logger.Error("key invalid (401/403), switching",
				"key", handle.Label(), "status", res.statusCode)
			continue

		case outcomeServerError:
			reason := "upstream server error"
			if res.err != nil {
				reason = res.err.Error()
			}
			h.pool.MarkError(handle.Label(), reason)
			h.logger.Warn("upstream error, switching",
				"key", handle.Label(), "reason", reason)
			continue

		case outcomeClientError:
			// 非额度类 4xx：请求端自身问题，原样回传，不重试。
			// 仍更新额度（响应可能带 header）。
			h.pool.UpdateFromUpstream(handle.Label(), res.snapshot)
			h.writeUpstreamResponse(w, res)
			return
		}
	}

	// 4. 全部 key 不可用 / 重试耗尽 → 503，下游只见最终失败
	retryAfter := h.pool.EarliestRecoverySeconds()
	h.logger.Error("all keys unavailable", "retry_after_seconds", retryAfter)
	writeJSONError(w, http.StatusServiceUnavailable, "all_keys_unavailable",
		"All upstream API keys are exhausted or rate-limited", h.pool.Snapshot(), retryAfter)
}

// writeUpstreamResponse 把上游响应原样回传给下游（白名单响应头 + 状态码 + body）。
func (h *Handler) writeUpstreamResponse(w http.ResponseWriter, res upstreamResult) {
	copySafeResponseHeaders(res.header, w.Header())
	w.WriteHeader(res.statusCode)
	_, _ = w.Write(res.body)
}

// errorResponse 是网关自身错误的 JSON 结构。
type errorResponse struct {
	Error             string                  `json:"error"`
	Message           string                  `json:"message"`
	KeysStatus        []keypool.KeyStateView  `json:"keys_status,omitempty"`
	RetryAfterSeconds int                     `json:"retry_after_seconds,omitempty"`
}

// writeJSONError 写网关错误响应。
func writeJSONError(w http.ResponseWriter, status int, code, msg string, keys []keypool.KeyStateView, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:             code,
		Message:           msg,
		KeysStatus:        keys,
		RetryAfterSeconds: retryAfter,
	})
}

// copySafeResponseHeaders 复制上游响应头到下游，排除逐跳头。
func copySafeResponseHeaders(src, dst http.Header) {
	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
		if _, blocked := blockedResponseHeaders[canonical]; blocked {
			continue
		}
		for _, v := range values {
			dst.Add(canonical, v)
		}
	}
}

var blockedResponseHeaders = map[string]struct{}{
	"Connection":        {},
	"Keep-Alive":        {},
	"Transfer-Encoding": {},
	"Upgrade":           {},
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/proxy/ -run TestServeHTTP_SuccessForwards -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/handler.go internal/proxy/handler_test.go
git commit -m "feat(proxy): core relay pipeline with key rotation and downstream transparency"
```

---

## Task 12: proxy 包 — 换 key 重试与下游无感知集成测试

**Files:**
- Modify: `internal/proxy/handler_test.go`（追加场景测试）

- [ ] **Step 1: 写失败测试 — 覆盖换 key/503/4xx 不重试/下游无感知**

先在 `internal/proxy/handler_test.go` 的 import 块加入 `"encoding/json"`（下方 503 测试用 `json.Unmarshal`）。更新后的 import 块：

```go
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"api-football-gateway/internal/auth"
	"api-football-gateway/internal/keypool"
)
```

然后在 `internal/proxy/handler_test.go` 追加以下测试函数：

```go
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
			// key-1 当日耗尽（remaining 0）但仍返回 200 → 应更新为 Exhausted 且本次成功
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

	// 第一次请求：key-1 返回 200（remaining 0）→ 成功回传，但 key-1 标记 Exhausted
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/fixtures", nil))
	if rec1.Body.String() != `{"from":"key-1"}` {
		t.Errorf("req1 body = %q, want from key-1", rec1.Body.String())
	}

	// 第二次请求：key-1 已 Exhausted → 自动用 key-2
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/fixtures", nil))
	if rec2.Body.String() != `{"from":"key-2"}` {
		t.Errorf("req2 body = %q, want from key-2 (key-1 exhausted)", rec2.Body.String())
	}
}

func TestServeHTTP_RateLimitedRetriesTransparently(t *testing.T) {
	upstream := stagedUpstream(t, map[string]func(w http.ResponseWriter){
		"secret-aaaa1": func(w http.ResponseWriter) {
			w.WriteHeader(429) // key-1 限速
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

	// 下游无感知：不应看到 429，只看到 key-2 的 200
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
			w.WriteHeader(404) // 非额度类 4xx
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
```

- [ ] **Step 2: 运行测试验证（应通过，因 handler 已实现）**

Run: `go test ./internal/proxy/ -v`
Expected: 所有 proxy 测试 PASS。若有 FAIL，按失败信息修正 `handler.go`/`transport.go` 后重跑，直至全绿。

- [ ] **Step 3: 竞态检测**

Run: `go test -race ./internal/proxy/ -v`
Expected: 无 race 报告。

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/handler_test.go
git commit -m "test(proxy): cover key rotation, 503, 4xx pass-through, downstream transparency"
```

---

## Task 13: admin 包 — status 与 health 端点

**Files:**
- Create: `internal/admin/admin.go`
- Test: `internal/admin/admin_test.go`

- [ ] **Step 1: 写失败测试**

`internal/admin/admin_test.go`:

```go
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
	// 脱敏断言
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
}

func TestUnknownAdminPath404(t *testing.T) {
	h := New(newPool())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__gateway/unknown", nil))
	if rec.Code != 404 {
		t.Errorf("unknown admin path status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/admin/ -v`
Expected: FAIL（`New`/`StatusResponse`/`ServeHTTP` 未定义）。

- [ ] **Step 3: 写实现**

`internal/admin/admin.go`:

```go
// Package admin 提供网关管理端点：/__gateway/status 与 /__gateway/health。
package admin

import (
	"encoding/json"
	"net/http"

	"api-football-gateway/internal/keypool"
)

// StatusResponse 是 /__gateway/status 的响应体。
type StatusResponse struct {
	Keys []keypool.KeyStateView `json:"keys"`
}

// Handler 处理 /__gateway/* 管理请求。
type Handler struct {
	pool *keypool.Pool
}

// New 构造 admin handler。
func New(pool *keypool.Pool) *Handler {
	return &Handler{pool: pool}
}

// ServeHTTP 路由管理端点。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/__gateway/status":
		h.handleStatus(w, r)
	case "/__gateway/health":
		h.handleHealth(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(StatusResponse{Keys: h.pool.Snapshot()})
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/admin/ -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/admin/admin.go internal/admin/admin_test.go
git commit -m "feat(admin): status and health endpoints with key masking"
```

---

## Task 14: main 装配 — 路由、服务器与优雅关闭

**Files:**
- Create: `cmd/gateway/main.go`

main 把各组件装配起来：管理路径走 admin，其余走 proxy；按 logging.level 设 slog；优雅关闭。

- [ ] **Step 1: 写实现**

`cmd/gateway/main.go`:

```go
// Command gateway 启动 API-Football 多 key 请求网关。
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"api-football-gateway/internal/admin"
	"api-football-gateway/internal/auth"
	"api-football-gateway/internal/config"
	"api-football-gateway/internal/keypool"
	"api-football-gateway/internal/proxy"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Logging.Level)
	slog.SetDefault(logger)

	// 构造 key 池
	entries := make([]keypool.KeyEntry, 0, len(cfg.Keys))
	for _, k := range cfg.Keys {
		entries = append(entries, keypool.KeyEntry{Label: k.Label, Key: k.Key})
	}
	pool := keypool.New(entries, keypool.Options{
		SwitchThreshold:   cfg.Scheduler.SwitchThreshold,
		RateLimitCooldown: time.Duration(cfg.Scheduler.RateLimitCooldownSeconds) * time.Second,
		ErrorCooldown:     time.Duration(cfg.Scheduler.ErrorCooldownSeconds) * time.Second,
		MaxErrorCount:     cfg.Scheduler.MaxErrorCount,
	})

	authenticator := auth.New(cfg.ClientAuth.Enabled, cfg.ClientAuth.Tokens)

	proxyHandler := proxy.NewHandler(proxy.Config{
		UpstreamBaseURL: cfg.Upstream.BaseURL,
		Timeout:         time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second,
		MaxRetries:      cfg.Scheduler.MaxRetries,
	}, pool, authenticator).WithLogger(logger)

	adminHandler := admin.New(pool)

	// 路由：/__gateway/* → admin，其余 → proxy
	mux := http.NewServeMux()
	mux.Handle("/__gateway/", adminHandler)
	mux.Handle("/", proxyHandler)

	addr := cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// 优雅关闭
	go func() {
		logger.Info("gateway listening",
			"addr", addr,
			"upstream", cfg.Upstream.BaseURL,
			"keys", len(cfg.Keys),
			"client_auth", cfg.ClientAuth.Enabled)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}

// newLogger 按级别构造 slog logger。
func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
```

- [ ] **Step 2: 编译验证**

Run: `go build ./...`
Expected: 编译成功，无错误。

- [ ] **Step 3: go vet 静态检查**

Run: `go vet ./...`
Expected: 无警告。

- [ ] **Step 4: 启动冒烟测试（用 example 配置的占位 key，预期上游 401 但网关能起）**

```bash
cp config.example.yaml config.local.test.yaml
go run ./cmd/gateway -config config.local.test.yaml &
GW_PID=$!
sleep 1
curl -s http://127.0.0.1:8080/__gateway/health
echo ""
curl -s http://127.0.0.1:8080/__gateway/status
echo ""
kill $GW_PID
rm -f config.local.test.yaml
```

Expected: health 返回 `{"status":"ok"}`；status 返回含 `key-1`/`key-2` 的脱敏 JSON。

- [ ] **Step 5: Commit**

```bash
git add cmd/gateway/main.go
git commit -m "feat(cmd): wire up gateway server with routing and graceful shutdown"
```

---

## Task 15: 全量验证、覆盖率与 README

**Files:**
- Create: `README.md`

- [ ] **Step 1: 全量测试 + 竞态**

Run: `go test -race ./...`
Expected: 所有包 PASS，无 race。

- [ ] **Step 2: 覆盖率验证（目标 >= 80%）**

Run: `go test -cover ./...`
Expected: 各业务包（config/keypool/auth/proxy/admin）覆盖率 >= 80%。若某包不足，补充对应测试用例（针对未覆盖分支，如 config 非法值、keypool 错误计数累积、transport 网络错误路径）再重跑。

生成详细报告（可选）：

```bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -20
```

- [ ] **Step 3: 编写 README**

`README.md`:

```markdown
# API-Football Gateway

局域网内的 API-Football 多 key 请求网关。对客户端屏蔽真实密钥，在多个 key 间顺序耗尽式轮换，实时监控各 key 当日额度，上游错误自动换 key 重试且对下游透明。

## 快速开始

1. 复制并填写配置：

   ```bash
   cp config.example.yaml config.yaml
   # 编辑 config.yaml，填入真实 API key
   ```

2. 运行：

   ```bash
   go run ./cmd/gateway -config config.yaml
   # 或编译后运行
   go build -o gateway ./cmd/gateway && ./gateway -config config.yaml
   ```

3. 请求端改造：把 baseUrl 指向网关，Authorization 任意填（初版不校验）：

   ```bash
   # 原本: https://v3.football.api-sports.io/fixtures?live=all
   curl -H "Authorization: Bearer anything" \
        "http://<网关LAN_IP>:8080/fixtures?live=all"
   ```

## 配置说明

见 `config.example.yaml`。关键项：

- `keys`: 真实 key 列表，按顺序耗尽使用。
- `scheduler.switch_threshold`: 当日剩余 <= 此值即切换（默认 1）。
- `scheduler.max_retries`: 单请求最多换几次 key（默认 3）。
- `client_auth.enabled`: 初版 false（接受任意 Bearer token）。

真实 key 也可用环境变量覆盖（避免明文落盘）：

- `GW_SERVER_PORT` 覆盖端口。
- `GW_KEY_<LABEL>` 覆盖对应 label 的 key（label 大写、非字母数字转下划线）。例如 `key-1` → `GW_KEY_KEY_1`。

## 监控

- `GET /__gateway/status`: 各 key 脱敏额度状态 JSON。
- `GET /__gateway/health`: 健康检查。

## 上游错误处理（下游无感知）

| 上游 | 网关行为 | 换 key |
|------|---------|--------|
| 2xx | 更新额度 → 回传 | 否 |
| 429 | 标记限速 + 冷却 → 换 key | 是 |
| 当日耗尽 | 标记耗尽 → 换 key | 是 |
| 401/403 | 标记 key 失效 → 换 key | 是 |
| 5xx/网络错误 | 计错误 → 换 key | 是 |
| 其余 4xx | 原样回传 | 否 |

全部 key 不可用时返回 503 + `Retry-After`。

## 测试

```bash
go test -race ./...
go test -cover ./...
```
```

- [ ] **Step 4: 最终全量验证**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: 全部成功、无警告、无 race。

- [ ] **Step 5: Commit**

```bash
git add README.md coverage.out
git commit -m "docs: add README and verify full test suite with coverage"
```

---

## 完成标准

- [ ] `go build ./...` 成功
- [ ] `go vet ./...` 无警告
- [ ] `go test -race ./...` 全部 PASS，无 race
- [ ] `go test -cover ./...` 各业务包 >= 80%
- [ ] 启动冒烟：`/__gateway/health` 与 `/__gateway/status` 正常，status 不泄露完整 key
- [ ] 设计文档全部要求均有对应实现（见下方 Self-Review 映射）
