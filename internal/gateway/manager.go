// Package gateway owns the hot-reloadable runtime state.
package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"all2api/internal/auth"
	"all2api/internal/config"
	"all2api/internal/keypool"
	"all2api/internal/logstore"
	"all2api/internal/provider"
	"all2api/internal/proxy"
)

// Manager swaps proxy runtime state after config updates.
type Manager struct {
	mu         sync.RWMutex
	configPath string
	cfg        *config.Config
	proxy      http.Handler
	pools      map[string]*keypool.Pool
	logger     *slog.Logger
	events     *logstore.Store
}

// New builds a manager from loaded config.
func New(configPath string, cfg *config.Config, logger *slog.Logger, events *logstore.Store) (*Manager, error) {
	m := &Manager{
		configPath: configPath,
		logger:     logger,
		events:     events,
	}
	proxyHandler, pools, err := buildProxy(cfg, logger, events)
	if err != nil {
		return nil, err
	}
	m.cfg = cfg.Clone()
	m.proxy = proxyHandler
	m.pools = pools
	return m, nil
}

// ServeHTTP forwards through the current proxy runtime.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	h := m.proxy
	m.mu.RUnlock()
	h.ServeHTTP(w, r)
}

// Config returns a deep copy of the active config.
func (m *Manager) Config() *config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Clone()
}

// ConfigPath returns the writable config path.
func (m *Manager) ConfigPath() string { return m.configPath }

// Pools returns a shallow copy of active pools.
func (m *Manager) Pools() map[string]*keypool.Pool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*keypool.Pool, len(m.pools))
	for name, pool := range m.pools {
		out[name] = pool
	}
	return out
}

// ReplaceConfig validates, saves, and hot-reloads a new config.
func (m *Manager) ReplaceConfig(next *config.Config) error {
	next = next.Clone()
	if err := next.Validate(); err != nil {
		return err
	}
	proxyHandler, pools, err := buildProxy(next, m.logger, m.events)
	if err != nil {
		return err
	}
	if err := config.Save(m.configPath, next); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = next.Clone()
	m.proxy = proxyHandler
	m.pools = pools
	m.mu.Unlock()
	return nil
}

// Overview is dashboard-level runtime data.
type Overview struct {
	PlatformCount       int                     `json:"platform_count"`
	CredentialPoolCount int                     `json:"credential_pool_count"`
	KeyCount            int                     `json:"key_count"`
	ActiveKeyCount      int                     `json:"active_key_count"`
	ProblemKeyCount     int                     `json:"problem_key_count"`
	Platforms           []PlatformOverview      `json:"platforms"`
	CredentialPools     []CredentialPoolRuntime `json:"credential_pools"`
	LogFile             string                  `json:"log_file"`
	ConfigPath          string                  `json:"config_path"`
}

// PlatformOverview is one configured platform.
type PlatformOverview struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	BaseURL        string `json:"base_url"`
	CredentialPool string `json:"credential_pool"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	AuthHeader     string `json:"auth_header"`
	AuthPrefix     string `json:"auth_prefix"`
}

// CredentialPoolRuntime is one runtime pool snapshot.
type CredentialPoolRuntime struct {
	Name string                 `json:"name"`
	Keys []keypool.KeyStateView `json:"keys"`
}

// Overview returns dashboard data from config plus runtime key state.
func (m *Manager) Overview() Overview {
	cfg := m.Config()
	pools := m.Pools()
	out := Overview{
		PlatformCount:       len(cfg.Platforms),
		CredentialPoolCount: len(cfg.CredentialPools),
		LogFile:             cfg.Management.LogFile,
		ConfigPath:          m.configPath,
	}
	platformNames := make([]string, 0, len(cfg.Platforms))
	for name := range cfg.Platforms {
		platformNames = append(platformNames, name)
	}
	sort.Strings(platformNames)
	for _, name := range platformNames {
		p := cfg.Platforms[name]
		authHeader := "x-apisports-key"
		authPrefix := ""
		if p.Type == provider.TypeGenericHTTP {
			authHeader = p.Auth.Header
			authPrefix = p.Auth.Prefix
		}
		out.Platforms = append(out.Platforms, PlatformOverview{
			Name:           name,
			Type:           p.Type,
			BaseURL:        p.BaseURL,
			CredentialPool: p.CredentialPool,
			TimeoutSeconds: p.TimeoutSeconds,
			AuthHeader:     authHeader,
			AuthPrefix:     authPrefix,
		})
	}

	poolNames := make([]string, 0, len(pools))
	for name := range pools {
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)
	for _, name := range poolNames {
		keys := pools[name].Snapshot()
		out.CredentialPools = append(out.CredentialPools, CredentialPoolRuntime{Name: name, Keys: keys})
		for _, key := range keys {
			out.KeyCount++
			if key.Status == "active" {
				out.ActiveKeyCount++
			} else {
				out.ProblemKeyCount++
			}
		}
	}
	return out
}

// APIDocs returns dynamic docs for configured platforms.
func (m *Manager) APIDocs() APIDocs {
	cfg := m.Config()
	names := make([]string, 0, len(cfg.Platforms))
	for name := range cfg.Platforms {
		names = append(names, name)
	}
	sort.Strings(names)
	docs := APIDocs{
		AdminEndpoints: []DocEndpoint{
			{Method: "POST", Path: "/__gateway/admin/session", Description: "Validate admin key and return a bearer token."},
			{Method: "GET", Path: "/__gateway/admin/overview", Description: "Dashboard summary and key runtime status."},
			{Method: "GET", Path: "/__gateway/admin/config", Description: "Current writable gateway config."},
			{Method: "PUT", Path: "/__gateway/admin/config", Description: "Replace config.yaml and hot-reload runtime state."},
			{Method: "GET", Path: "/__gateway/admin/logs", Description: "Read structured JSONL gateway logs."},
			{Method: "DELETE", Path: "/__gateway/admin/logs", Description: "Clear the configured log file."},
			{Method: "GET", Path: "/__gateway/admin/docs", Description: "Dynamic platform and admin API documentation."},
		},
	}
	for _, name := range names {
		p := cfg.Platforms[name]
		authHeader := "x-apisports-key"
		authValue := "<gateway-injected-key>"
		if p.Type == provider.TypeGenericHTTP {
			authHeader = p.Auth.Header
			if p.Auth.Prefix != "" {
				authValue = p.Auth.Prefix + " <gateway-injected-key>"
			}
		}
		docs.Platforms = append(docs.Platforms, PlatformDoc{
			Name:            name,
			Type:            p.Type,
			GatewayBasePath: "/" + name,
			UpstreamBaseURL: p.BaseURL,
			CredentialPool:  p.CredentialPool,
			AuthHeader:      authHeader,
			AuthValue:       authValue,
			ExampleCurl:     fmt.Sprintf("curl -H \"Authorization: Bearer <client-token>\" \"http://<gateway>/%s/<path>\"", name),
		})
	}
	return docs
}

// APIDocs is the API docs payload.
type APIDocs struct {
	Platforms      []PlatformDoc `json:"platforms"`
	AdminEndpoints []DocEndpoint `json:"admin_endpoints"`
}

// PlatformDoc describes one forwarding platform.
type PlatformDoc struct {
	Name            string `json:"name"`
	Type            string `json:"type"`
	GatewayBasePath string `json:"gateway_base_path"`
	UpstreamBaseURL string `json:"upstream_base_url"`
	CredentialPool  string `json:"credential_pool"`
	AuthHeader      string `json:"auth_header"`
	AuthValue       string `json:"auth_value"`
	ExampleCurl     string `json:"example_curl"`
}

// DocEndpoint describes one admin API.
type DocEndpoint struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

func buildProxy(cfg *config.Config, logger *slog.Logger, events *logstore.Store) (*proxy.Handler, map[string]*keypool.Pool, error) {
	poolOptions := keypool.Options{
		SwitchThreshold:   cfg.Scheduler.SwitchThreshold,
		RateLimitCooldown: time.Duration(cfg.Scheduler.RateLimitCooldownSeconds) * time.Second,
		ErrorCooldown:     time.Duration(cfg.Scheduler.ErrorCooldownSeconds) * time.Second,
		MaxErrorCount:     cfg.Scheduler.MaxErrorCount,
	}
	pools := make(map[string]*keypool.Pool, len(cfg.CredentialPools))
	for poolName, poolCfg := range cfg.CredentialPools {
		entries := make([]keypool.KeyEntry, 0, len(poolCfg.Keys))
		for _, k := range poolCfg.Keys {
			entries = append(entries, keypool.KeyEntry{Label: k.Label, Key: k.Key})
		}
		pools[poolName] = keypool.New(entries, poolOptions)
	}

	platformNames := make([]string, 0, len(cfg.Platforms))
	for name := range cfg.Platforms {
		platformNames = append(platformNames, name)
	}
	slices.Sort(platformNames)
	platforms := make(map[string]proxy.PlatformUpstream, len(cfg.Platforms))
	for _, name := range platformNames {
		platformCfg := cfg.Platforms[name]
		platformProvider, err := provider.New(provider.Config{
			Type: platformCfg.Type,
			Auth: provider.HeaderAuthConfig{
				Header: platformCfg.Auth.Header,
				Prefix: platformCfg.Auth.Prefix,
			},
			RateLimit: provider.RateLimitConfig{
				DailyLimitHeader:      platformCfg.RateLimit.DailyLimitHeader,
				DailyRemainingHeader:  platformCfg.RateLimit.DailyRemainingHeader,
				MinuteLimitHeader:     platformCfg.RateLimit.MinuteLimitHeader,
				MinuteRemainingHeader: platformCfg.RateLimit.MinuteRemainingHeader,
			},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("platform %q provider: %w", name, err)
		}
		platforms[name] = proxy.PlatformUpstream{
			BaseURL:  platformCfg.BaseURL,
			Timeout:  time.Duration(platformCfg.TimeoutSeconds) * time.Second,
			Provider: platformProvider,
			Pool:     pools[platformCfg.CredentialPool],
			PoolName: platformCfg.CredentialPool,
		}
	}
	proxyHandler := proxy.NewHandler(proxy.Config{
		Platforms:  platforms,
		MaxRetries: cfg.Scheduler.MaxRetries,
	}, nil, auth.New(cfg.ClientAuth.Enabled, cfg.ClientAuth.Tokens)).WithLogger(logger).WithEventSink(events)
	return proxyHandler, pools, nil
}
