// Package gateway owns the hot-reloadable runtime state.
package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
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
		PublicEndpoint: "/__gateway/docs",
		AdminEndpoints: []DocEndpoint{
			{Method: "POST", Path: "/__gateway/admin/session", Description: "Validate admin key and return a bearer token."},
			{Method: "GET", Path: "/__gateway/admin/overview", Description: "Dashboard summary and key runtime status."},
			{Method: "GET", Path: "/__gateway/admin/config", Description: "Current writable gateway config."},
			{Method: "PUT", Path: "/__gateway/admin/config", Description: "Replace config.yaml and hot-reload runtime state."},
			{Method: "GET", Path: "/__gateway/admin/logs", Description: "Read structured JSONL gateway logs."},
			{Method: "DELETE", Path: "/__gateway/admin/logs", Description: "Clear the configured log file."},
			{Method: "GET", Path: "/__gateway/docs", Description: "Public gateway API documentation."},
		},
	}
	for _, name := range names {
		p := cfg.Platforms[name]
		authHeader := authHeaderForPlatform(p)
		authValue := "<client-key>"
		if p.Type == provider.TypeGenericHTTP {
			if p.Auth.Prefix != "" {
				authValue = p.Auth.Prefix + " <client-key>"
			}
		}
		docs.Platforms = append(docs.Platforms, PlatformDoc{
			Name:            name,
			Title:           docTitleForPlatform(name, p),
			Type:            p.Type,
			GatewayBasePath: "/" + name,
			GatewayBaseURL:  fmt.Sprintf("http://<gateway>/%s", name),
			UpstreamBaseURL: p.BaseURL,
			CredentialPool:  p.CredentialPool,
			AuthHeader:      authHeader,
			AuthValue:       authValue,
			ExampleCurl:     fmt.Sprintf("curl -H %q \"http://<gateway>/%s%s\"", authHeader+": "+authValue, name, firstDocPath(p)),
			Endpoints:       docEndpointsForPlatform(name, p),
		})
	}
	return docs
}

// APIDocs is the API docs payload.
type APIDocs struct {
	PublicEndpoint string        `json:"public_endpoint"`
	Platforms      []PlatformDoc `json:"platforms"`
	AdminEndpoints []DocEndpoint `json:"admin_endpoints"`
}

// PlatformDoc describes one forwarding platform.
type PlatformDoc struct {
	Name            string        `json:"name"`
	Title           string        `json:"title"`
	Type            string        `json:"type"`
	GatewayBasePath string        `json:"gateway_base_path"`
	GatewayBaseURL  string        `json:"gateway_base_url"`
	UpstreamBaseURL string        `json:"upstream_base_url"`
	CredentialPool  string        `json:"credential_pool"`
	AuthHeader      string        `json:"auth_header"`
	AuthValue       string        `json:"auth_value"`
	ExampleCurl     string        `json:"example_curl"`
	Endpoints       []DocEndpoint `json:"endpoints"`
}

// DocEndpoint describes one documented API endpoint.
type DocEndpoint struct {
	Method         string         `json:"method"`
	Path           string         `json:"path"`
	Summary        string         `json:"summary,omitempty"`
	Description    string         `json:"description"`
	Parameters     []DocParameter `json:"parameters,omitempty"`
	ResponseFields []DocField     `json:"response_fields,omitempty"`
	ResponseSample any            `json:"response_sample,omitempty"`
}

// DocParameter describes one request parameter.
type DocParameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required"`
	Type        string `json:"type"`
	Example     string `json:"example,omitempty"`
	Description string `json:"description"`
}

// DocField describes one response field path.
type DocField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

func authHeaderForPlatform(p config.PlatformConfig) string {
	if p.Type == provider.TypeGenericHTTP {
		return p.Auth.Header
	}
	return "x-apisports-key"
}

func docTitleForPlatform(name string, p config.PlatformConfig) string {
	if p.Type == provider.TypeAPISports {
		if strings.Contains(p.BaseURL, "basketball") {
			return "API-Basketball"
		}
		return "API-Football"
	}
	if name != "" {
		return name
	}
	return p.Type
}

func firstDocPath(p config.PlatformConfig) string {
	for _, endpoint := range docEndpointsForPlatform("", p) {
		return endpoint.Path
	}
	return "/<path>"
}

func docEndpointsForPlatform(name string, p config.PlatformConfig) []DocEndpoint {
	prefix := ""
	if name != "" {
		prefix = "/" + name
	}
	switch {
	case p.Type == provider.TypeAPISports && strings.Contains(p.BaseURL, "basketball"):
		return basketballDocEndpoints(prefix)
	case p.Type == provider.TypeAPISports:
		return footballDocEndpoints(prefix)
	default:
		return genericDocEndpoints(prefix)
	}
}

func footballDocEndpoints(prefix string) []DocEndpoint {
	return []DocEndpoint{
		{
			Method:      "GET",
			Path:        prefix + "/status",
			Summary:     "查看 API-Football 账号与配额状态",
			Description: "用于确认当前 API-Football 订阅是否有效，以及当天请求额度的已用量和上限。这个接口通常用于控制台健康检查、配额预警和接入验证。",
			Parameters:  nil,
			ResponseFields: envelopeFields(
				field("response.account.firstname", "string", "账号名。"),
				field("response.account.lastname", "string", "账号姓氏。"),
				field("response.account.email", "string", "账号邮箱。"),
				field("response.subscription.plan", "string", "订阅计划名称。"),
				field("response.subscription.end", "string", "订阅结束时间，ISO 8601 格式。"),
				field("response.subscription.active", "boolean", "订阅是否处于可用状态。"),
				field("response.requests.current", "number", "当天已使用请求数。"),
				field("response.requests.limit_day", "number", "当天请求上限。"),
			),
			ResponseSample: map[string]any{
				"get":        "status",
				"parameters": []any{},
				"errors":     []any{},
				"results":    0,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": map[string]any{
					"account":      map[string]any{"firstname": "John", "lastname": "Doe", "email": "john@example.com"},
					"subscription": map[string]any{"plan": "Free", "end": "2027-06-07T00:00:00+00:00", "active": true},
					"requests":     map[string]any{"current": 12, "limit_day": 100},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/countries",
			Summary:     "查询 API-Football 支持的国家或地区",
			Description: "返回 API-Football 可用于联赛、球队和赛程过滤的国家/地区列表。可按国家名称、国家代码或搜索词缩小范围。",
			Parameters: []DocParameter{
				queryParam("name", false, "string", "England", "按完整国家/地区名称过滤。"),
				queryParam("code", false, "string", "GB-ENG", "按 API-Sports 国家代码过滤。"),
				queryParam("search", false, "string", "eng", "按名称模糊搜索。"),
			},
			ResponseFields: envelopeFields(
				field("response[].name", "string", "国家或地区名称。"),
				field("response[].code", "string", "API-Sports 国家代码。"),
				field("response[].flag", "string", "旗帜图片 URL。"),
			),
			ResponseSample: map[string]any{
				"get":        "countries",
				"parameters": map[string]any{"name": "England"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{"name": "England", "code": "GB-ENG", "flag": "https://media.api-sports.io/flags/gb-eng.svg"},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/leagues",
			Summary:     "查询足球联赛和杯赛",
			Description: "用于获取联赛/杯赛基础信息、所属国家、Logo、赛季列表和赛季覆盖能力。常用于先查 league id，再用 league+season 查询积分榜、赛程、球队或球员。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "39", "按联赛 ID 精确查询。"),
				queryParam("name", false, "string", "Premier League", "按联赛名称精确查询。"),
				queryParam("country", false, "string", "England", "按国家/地区名称过滤。"),
				queryParam("code", false, "string", "GB-ENG", "按国家/地区代码过滤。"),
				queryParam("season", false, "integer", "2023", "按赛季年份过滤。"),
				queryParam("team", false, "integer", "33", "查询指定球队参加过的联赛。"),
				queryParam("type", false, "string", "league", "赛事类型，可为 league 或 cup。"),
				queryParam("current", false, "boolean", "true", "只返回当前赛季。"),
				queryParam("search", false, "string", "premier", "按联赛名称模糊搜索。"),
				queryParam("last", false, "integer", "10", "返回最近 N 个联赛。"),
			},
			ResponseFields: envelopeFields(
				field("response[].league.id", "number", "联赛 ID。"),
				field("response[].league.name", "string", "联赛名称。"),
				field("response[].league.type", "string", "赛事类型。"),
				field("response[].league.logo", "string", "联赛 Logo URL。"),
				field("response[].country.name", "string", "所属国家/地区名称。"),
				field("response[].country.code", "string", "所属国家/地区代码。"),
				field("response[].seasons[].year", "number", "赛季年份。"),
				field("response[].seasons[].current", "boolean", "是否当前赛季。"),
				field("response[].seasons[].coverage", "object", "该赛季支持的赛程、积分榜、球员、赔率等数据覆盖能力。"),
			),
			ResponseSample: map[string]any{
				"get":        "leagues",
				"parameters": map[string]any{"id": "39"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{
						"league":  map[string]any{"id": 39, "name": "Premier League", "type": "League", "logo": "https://media.api-sports.io/football/leagues/39.png"},
						"country": map[string]any{"name": "England", "code": "GB-ENG", "flag": "https://media.api-sports.io/flags/gb-eng.svg"},
						"seasons": []any{map[string]any{"year": 2023, "current": true, "coverage": map[string]any{"fixtures": map[string]any{"events": true}, "standings": true, "players": true}}},
					},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/teams",
			Summary:     "查询足球球队与主场信息",
			Description: "返回球队基础资料和主场球场信息。常用于通过 league+season 获取某赛季参赛球队，或通过 id/name/country 定位球队 ID。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "33", "按球队 ID 精确查询。"),
				queryParam("name", false, "string", "Manchester United", "按球队名称精确查询。"),
				queryParam("league", false, "integer", "39", "按联赛 ID 过滤。通常与 season 一起使用。"),
				queryParam("season", false, "integer", "2023", "按赛季年份过滤。通常与 league 一起使用。"),
				queryParam("country", false, "string", "England", "按国家/地区名称过滤。"),
				queryParam("code", false, "string", "MUN", "按球队代码过滤。"),
				queryParam("venue", false, "integer", "556", "按球场 ID 过滤。"),
				queryParam("search", false, "string", "manchester", "按球队名称模糊搜索。"),
			},
			ResponseFields: envelopeFields(
				field("response[].team.id", "number", "球队 ID。"),
				field("response[].team.name", "string", "球队名称。"),
				field("response[].team.code", "string", "球队代码。"),
				field("response[].team.country", "string", "所属国家/地区。"),
				field("response[].team.founded", "number", "成立年份。"),
				field("response[].team.national", "boolean", "是否国家队。"),
				field("response[].team.logo", "string", "球队 Logo URL。"),
				field("response[].venue.id", "number", "主场球场 ID。"),
				field("response[].venue.name", "string", "主场球场名称。"),
				field("response[].venue.city", "string", "球场所在城市。"),
				field("response[].venue.capacity", "number", "球场容量。"),
			),
			ResponseSample: map[string]any{
				"get":        "teams",
				"parameters": map[string]any{"id": "33"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{
						"team":  map[string]any{"id": 33, "name": "Manchester United", "code": "MUN", "country": "England", "founded": 1878, "national": false, "logo": "https://media.api-sports.io/football/teams/33.png"},
						"venue": map[string]any{"id": 556, "name": "Old Trafford", "city": "Manchester", "capacity": 76212},
					},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/fixtures",
			Summary:     "查询足球比赛、赛程和比分",
			Description: "返回比赛时间、状态、联赛、主客队、进球、半全场比分等信息。官方要求至少提供一个可限定范围的条件，例如 id、date、live、team、league+season、from+to、last 或 next。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "868289", "按单场比赛 ID 查询。"),
				queryParam("ids", false, "string", "868289-868290", "按多个比赛 ID 查询，多个 ID 用连字符分隔。"),
				queryParam("live", false, "string", "all", "查询实时比赛，可为 all 或指定联赛 ID 列表。"),
				queryParam("date", false, "date", "2023-08-12", "按比赛日期查询，格式 YYYY-MM-DD。"),
				queryParam("league", false, "integer", "39", "按联赛 ID 过滤。与 season 搭配查询赛季赛程。"),
				queryParam("season", false, "integer", "2023", "按赛季年份过滤。与 league 搭配查询赛季赛程。"),
				queryParam("team", false, "integer", "33", "按球队 ID 查询该队比赛。"),
				queryParam("last", false, "integer", "5", "查询最近 N 场比赛。"),
				queryParam("next", false, "integer", "5", "查询接下来 N 场比赛。"),
				queryParam("from", false, "date", "2023-08-01", "日期范围起点，格式 YYYY-MM-DD。通常与 to 一起使用。"),
				queryParam("to", false, "date", "2023-08-31", "日期范围终点，格式 YYYY-MM-DD。通常与 from 一起使用。"),
				queryParam("round", false, "string", "Regular Season - 1", "按轮次过滤。"),
				queryParam("status", false, "string", "NS", "按比赛状态过滤，例如 NS、FT、LIVE。"),
				queryParam("venue", false, "integer", "556", "按球场 ID 过滤。"),
				queryParam("timezone", false, "string", "Asia/Shanghai", "返回时间使用的时区。"),
			},
			ResponseFields: envelopeFields(
				field("response[].fixture.id", "number", "比赛 ID。"),
				field("response[].fixture.referee", "string|null", "裁判名称。"),
				field("response[].fixture.timezone", "string", "响应时区。"),
				field("response[].fixture.date", "string", "比赛时间，ISO 8601 格式。"),
				field("response[].fixture.timestamp", "number", "比赛 Unix 时间戳。"),
				field("response[].fixture.status.long", "string", "比赛状态完整名称。"),
				field("response[].fixture.status.short", "string", "比赛状态缩写。"),
				field("response[].league.id", "number", "联赛 ID。"),
				field("response[].league.name", "string", "联赛名称。"),
				field("response[].league.season", "number", "赛季年份。"),
				field("response[].teams.home.id", "number", "主队 ID。"),
				field("response[].teams.home.name", "string", "主队名称。"),
				field("response[].teams.away.id", "number", "客队 ID。"),
				field("response[].teams.away.name", "string", "客队名称。"),
				field("response[].goals.home", "number|null", "主队进球数。"),
				field("response[].goals.away", "number|null", "客队进球数。"),
				field("response[].score", "object", "半场、全场、加时、点球比分。"),
			),
			ResponseSample: map[string]any{
				"get":        "fixtures",
				"parameters": map[string]any{"league": "39", "season": "2023", "next": "1"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{
						"fixture": map[string]any{"id": 868289, "referee": "Michael Oliver", "timezone": "UTC", "date": "2023-08-12T14:00:00+00:00", "timestamp": 1691848800, "status": map[string]any{"long": "Not Started", "short": "NS", "elapsed": nil}},
						"league":  map[string]any{"id": 39, "name": "Premier League", "country": "England", "season": 2023, "round": "Regular Season - 1"},
						"teams":   map[string]any{"home": map[string]any{"id": 33, "name": "Manchester United", "winner": nil}, "away": map[string]any{"id": 34, "name": "Newcastle", "winner": nil}},
						"goals":   map[string]any{"home": nil, "away": nil},
						"score":   map[string]any{"halftime": map[string]any{"home": nil, "away": nil}, "fulltime": map[string]any{"home": nil, "away": nil}},
					},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/standings",
			Summary:     "查询足球联赛积分榜",
			Description: "返回指定联赛和赛季的积分榜，包括排名、球队、积分、胜平负、进失球和主客场统计。官方常规用法要求提供 league 与 season。",
			Parameters: []DocParameter{
				queryParam("league", true, "integer", "39", "联赛 ID。"),
				queryParam("season", true, "integer", "2023", "赛季年份。"),
				queryParam("team", false, "integer", "33", "只返回指定球队的积分榜记录。"),
			},
			ResponseFields: envelopeFields(
				field("response[].league.id", "number", "联赛 ID。"),
				field("response[].league.name", "string", "联赛名称。"),
				field("response[].league.season", "number", "赛季年份。"),
				field("response[].league.standings[][].rank", "number", "球队排名。"),
				field("response[].league.standings[][].team.id", "number", "球队 ID。"),
				field("response[].league.standings[][].team.name", "string", "球队名称。"),
				field("response[].league.standings[][].points", "number", "积分。"),
				field("response[].league.standings[][].goalsDiff", "number", "净胜球。"),
				field("response[].league.standings[][].group", "string", "分组名称。"),
				field("response[].league.standings[][].form", "string", "近期战绩字符串。"),
				field("response[].league.standings[][].all", "object", "总计场次、胜平负、进失球。"),
				field("response[].league.standings[][].home", "object", "主场场次、胜平负、进失球。"),
				field("response[].league.standings[][].away", "object", "客场场次、胜平负、进失球。"),
			),
			ResponseSample: map[string]any{
				"get":        "standings",
				"parameters": map[string]any{"league": "39", "season": "2023"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{"league": map[string]any{"id": 39, "name": "Premier League", "season": 2023, "standings": []any{[]any{map[string]any{"rank": 1, "team": map[string]any{"id": 33, "name": "Manchester United"}, "points": 88, "goalsDiff": 42, "form": "WWDWW", "all": map[string]any{"played": 38, "win": 28, "draw": 4, "lose": 6}}}}}},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/players",
			Summary:     "查询足球球员资料和赛季统计",
			Description: "返回球员基础资料及其在球队/联赛/赛季下的统计数据。常用于按 team+season 或 league+season 获取球员列表，也可按 id 或 search 定位球员。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "276", "按球员 ID 查询。"),
				queryParam("team", false, "integer", "33", "按球队 ID 过滤。"),
				queryParam("league", false, "integer", "39", "按联赛 ID 过滤。"),
				queryParam("season", false, "integer", "2023", "赛季年份。"),
				queryParam("search", false, "string", "messi", "按球员名称搜索，至少 4 个字符。"),
				queryParam("page", false, "integer", "2", "分页页码。"),
			},
			ResponseFields: envelopeFields(
				field("response[].player.id", "number", "球员 ID。"),
				field("response[].player.name", "string", "球员名称。"),
				field("response[].player.firstname", "string", "名。"),
				field("response[].player.lastname", "string", "姓。"),
				field("response[].player.age", "number", "年龄。"),
				field("response[].player.nationality", "string", "国籍。"),
				field("response[].player.height", "string", "身高。"),
				field("response[].player.weight", "string", "体重。"),
				field("response[].player.injured", "boolean", "是否受伤。"),
				field("response[].statistics[].team", "object", "统计所属球队。"),
				field("response[].statistics[].league", "object", "统计所属联赛。"),
				field("response[].statistics[].games", "object", "出场、首发、位置、评分等比赛统计。"),
				field("response[].statistics[].goals", "object", "进球、助攻、失球等进攻统计。"),
				field("response[].statistics[].cards", "object", "黄牌、红牌统计。"),
			),
			ResponseSample: map[string]any{
				"get":        "players",
				"parameters": map[string]any{"team": "33", "season": "2023"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 3},
				"response": []any{
					map[string]any{"player": map[string]any{"id": 276, "name": "Player Name", "age": 28, "nationality": "England", "injured": false}, "statistics": []any{map[string]any{"team": map[string]any{"id": 33, "name": "Manchester United"}, "league": map[string]any{"id": 39, "season": 2023}, "games": map[string]any{"appearences": 30, "position": "Attacker"}, "goals": map[string]any{"total": 12, "assists": 5}}}},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/odds",
			Summary:     "查询足球比赛赔率",
			Description: "返回指定比赛、联赛或日期范围内的博彩公司、投注项和赔率值。赔率覆盖取决于 API-Football 订阅计划、联赛覆盖能力和数据供应情况。",
			Parameters: []DocParameter{
				queryParam("fixture", false, "integer", "868289", "按比赛 ID 查询赔率。"),
				queryParam("league", false, "integer", "39", "按联赛 ID 过滤。"),
				queryParam("season", false, "integer", "2023", "赛季年份。"),
				queryParam("date", false, "date", "2023-08-12", "按日期过滤。"),
				queryParam("timezone", false, "string", "Asia/Shanghai", "返回时间使用的时区。"),
				queryParam("bookmaker", false, "integer", "6", "按博彩公司 ID 过滤。"),
				queryParam("bet", false, "integer", "1", "按投注项 ID 过滤。"),
				queryParam("page", false, "integer", "2", "分页页码。"),
			},
			ResponseFields: envelopeFields(
				field("response[].league", "object", "联赛 ID、名称、国家、赛季等信息。"),
				field("response[].fixture", "object", "比赛 ID、时区、开赛时间等信息。"),
				field("response[].update", "string", "赔率最后更新时间。"),
				field("response[].bookmakers[].id", "number", "博彩公司 ID。"),
				field("response[].bookmakers[].name", "string", "博彩公司名称。"),
				field("response[].bookmakers[].bets[].id", "number", "投注项 ID。"),
				field("response[].bookmakers[].bets[].name", "string", "投注项名称。"),
				field("response[].bookmakers[].bets[].values[].value", "string", "投注选项。"),
				field("response[].bookmakers[].bets[].values[].odd", "string", "赔率值。"),
			),
			ResponseSample: map[string]any{
				"get":        "odds",
				"parameters": map[string]any{"fixture": "868289"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{"league": map[string]any{"id": 39, "name": "Premier League", "season": 2023}, "fixture": map[string]any{"id": 868289, "date": "2023-08-12T14:00:00+00:00"}, "update": "2023-08-12T08:00:00+00:00", "bookmakers": []any{map[string]any{"id": 6, "name": "Bwin", "bets": []any{map[string]any{"id": 1, "name": "Match Winner", "values": []any{map[string]any{"value": "Home", "odd": "1.85"}}}}}}},
				},
			},
		},
	}
}

func basketballDocEndpoints(prefix string) []DocEndpoint {
	return []DocEndpoint{
		{
			Method:      "GET",
			Path:        prefix + "/status",
			Summary:     "查看 API-Basketball 账号与配额状态",
			Description: "用于确认当前 API-Basketball 订阅是否有效，以及当天请求额度的已用量和上限。适合作为篮球数据接入后的首个联通性检查。",
			ResponseFields: envelopeFields(
				field("response.account.firstname", "string", "账号名。"),
				field("response.account.lastname", "string", "账号姓氏。"),
				field("response.account.email", "string", "账号邮箱。"),
				field("response.subscription.plan", "string", "订阅计划名称。"),
				field("response.subscription.end", "string", "订阅结束时间，ISO 8601 格式。"),
				field("response.subscription.active", "boolean", "订阅是否处于可用状态。"),
				field("response.requests.current", "number", "当天已使用请求数。"),
				field("response.requests.limit_day", "number", "当天请求上限。"),
			),
			ResponseSample: map[string]any{
				"get":        "status",
				"parameters": []any{},
				"errors":     []any{},
				"results":    0,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": map[string]any{
					"account":      map[string]any{"firstname": "John", "lastname": "Doe", "email": "john@example.com"},
					"subscription": map[string]any{"plan": "Free", "end": "2027-06-07T00:00:00+00:00", "active": true},
					"requests":     map[string]any{"current": 12, "limit_day": 100},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/countries",
			Summary:     "查询 API-Basketball 支持的国家或地区",
			Description: "返回篮球数据中可用于联赛、球队和比赛过滤的国家/地区列表。可用于构建国家筛选器或定位联赛所属地区。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "5", "按国家/地区 ID 查询。"),
				queryParam("name", false, "string", "USA", "按国家/地区名称查询。"),
				queryParam("code", false, "string", "US", "按国家/地区代码查询。"),
				queryParam("search", false, "string", "uni", "按名称模糊搜索。"),
			},
			ResponseFields: envelopeFields(
				field("response[].id", "number", "国家/地区 ID。"),
				field("response[].name", "string", "国家/地区名称。"),
				field("response[].code", "string", "国家/地区代码。"),
				field("response[].flag", "string", "旗帜图片 URL。"),
			),
			ResponseSample: map[string]any{
				"get":        "countries",
				"parameters": map[string]any{"code": "US"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response":   []any{map[string]any{"id": 5, "name": "USA", "code": "US", "flag": "https://media.api-sports.io/flags/us.svg"}},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/leagues",
			Summary:     "查询篮球联赛",
			Description: "返回篮球联赛基础信息、国家/地区、赛季列表和类型。常用于查找 league id，再配合 season 查询球队、比赛或积分榜。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "12", "按联赛 ID 查询。"),
				queryParam("name", false, "string", "NBA", "按联赛名称查询。"),
				queryParam("country", false, "integer|string", "5", "按国家/地区过滤，可使用上游支持的 ID 或名称。"),
				queryParam("season", false, "string", "2023-2024", "按赛季过滤。"),
				queryParam("type", false, "string", "league", "赛事类型。"),
				queryParam("search", false, "string", "nba", "按联赛名称模糊搜索。"),
			},
			ResponseFields: envelopeFields(
				field("response[].id", "number", "联赛 ID。"),
				field("response[].name", "string", "联赛名称。"),
				field("response[].type", "string", "赛事类型。"),
				field("response[].logo", "string", "联赛 Logo URL。"),
				field("response[].country", "object", "所属国家/地区信息。"),
				field("response[].seasons[]", "string|object", "联赛支持的赛季列表。"),
			),
			ResponseSample: map[string]any{
				"get":        "leagues",
				"parameters": map[string]any{"name": "NBA"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response":   []any{map[string]any{"id": 12, "name": "NBA", "type": "League", "logo": "https://media.api-sports.io/basketball/leagues/12.png", "country": map[string]any{"id": 5, "name": "USA", "code": "US"}, "seasons": []any{"2023-2024"}}},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/teams",
			Summary:     "查询篮球球队",
			Description: "返回篮球球队基础信息、所属国家/地区和 Logo。常用于通过 league+season 获取参赛球队，或通过名称搜索球队 ID。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "132", "按球队 ID 查询。"),
				queryParam("name", false, "string", "Los Angeles Lakers", "按球队名称查询。"),
				queryParam("league", false, "integer", "12", "按联赛 ID 过滤。"),
				queryParam("season", false, "string", "2023-2024", "按赛季过滤。"),
				queryParam("country", false, "integer|string", "5", "按国家/地区过滤，可使用上游支持的 ID 或名称。"),
				queryParam("search", false, "string", "lakers", "按球队名称模糊搜索。"),
			},
			ResponseFields: envelopeFields(
				field("response[].id", "number", "球队 ID。"),
				field("response[].name", "string", "球队名称。"),
				field("response[].logo", "string", "球队 Logo URL。"),
				field("response[].national", "boolean", "是否国家队。"),
				field("response[].country", "object", "所属国家/地区信息。"),
			),
			ResponseSample: map[string]any{
				"get":        "teams",
				"parameters": map[string]any{"id": "132"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response":   []any{map[string]any{"id": 132, "name": "Los Angeles Lakers", "logo": "https://media.api-sports.io/basketball/teams/132.png", "national": false, "country": map[string]any{"id": 5, "name": "USA", "code": "US"}}},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/games",
			Summary:     "查询篮球比赛、赛程和比分",
			Description: "返回篮球比赛时间、状态、联赛、赛季、主客队、各节比分和总分。常用于按 date 查询赛程，或按 league+season/team 获取球队比赛。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "12345", "按比赛 ID 查询。"),
				queryParam("date", false, "date", "2024-01-15", "按比赛日期查询，格式 YYYY-MM-DD。"),
				queryParam("league", false, "integer", "12", "按联赛 ID 过滤。"),
				queryParam("season", false, "string", "2023-2024", "按赛季过滤。"),
				queryParam("team", false, "integer", "132", "按球队 ID 查询该队比赛。"),
				queryParam("timezone", false, "string", "Asia/Shanghai", "返回时间使用的时区。"),
			},
			ResponseFields: envelopeFields(
				field("response[].id", "number", "比赛 ID。"),
				field("response[].date", "string", "比赛时间，ISO 8601 格式。"),
				field("response[].time", "string|null", "比赛时间文本。"),
				field("response[].timestamp", "number", "比赛 Unix 时间戳。"),
				field("response[].timezone", "string", "响应时区。"),
				field("response[].stage", "string|null", "比赛阶段。"),
				field("response[].status.long", "string", "比赛状态完整名称。"),
				field("response[].status.short", "string", "比赛状态缩写。"),
				field("response[].league", "object", "联赛 ID、名称、类型、赛季等信息。"),
				field("response[].teams.home", "object", "主队 ID、名称、Logo。"),
				field("response[].teams.away", "object", "客队 ID、名称、Logo。"),
				field("response[].scores.home", "object", "主队各节和总分。"),
				field("response[].scores.away", "object", "客队各节和总分。"),
			),
			ResponseSample: map[string]any{
				"get":        "games",
				"parameters": map[string]any{"league": "12", "season": "2023-2024", "team": "132"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{"id": 12345, "date": "2024-01-15T03:30:00+00:00", "timestamp": 1705289400, "timezone": "UTC", "status": map[string]any{"long": "Finished", "short": "FT"}, "league": map[string]any{"id": 12, "name": "NBA", "season": "2023-2024"}, "teams": map[string]any{"home": map[string]any{"id": 132, "name": "Los Angeles Lakers"}, "away": map[string]any{"id": 134, "name": "Boston Celtics"}}, "scores": map[string]any{"home": map[string]any{"quarter_1": 28, "quarter_2": 31, "total": 112}, "away": map[string]any{"quarter_1": 25, "quarter_2": 30, "total": 108}}},
				},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/standings",
			Summary:     "查询篮球联赛积分榜",
			Description: "返回指定联赛和赛季的球队排名、胜负场、分组和胜率等信息。常规用法需要提供 league 与 season。",
			Parameters: []DocParameter{
				queryParam("league", true, "integer", "12", "联赛 ID。"),
				queryParam("season", true, "string", "2023-2024", "赛季。"),
				queryParam("team", false, "integer", "132", "只返回指定球队的排名记录。"),
				queryParam("group", false, "string", "Western Conference", "按分组过滤。"),
			},
			ResponseFields: envelopeFields(
				field("response[].position", "number", "排名。"),
				field("response[].stage", "string|null", "阶段。"),
				field("response[].group.name", "string", "分组名称。"),
				field("response[].team", "object", "球队 ID、名称、Logo。"),
				field("response[].league", "object", "联赛 ID、名称、赛季。"),
				field("response[].country", "object", "国家/地区信息。"),
				field("response[].games.played", "number", "已赛场次。"),
				field("response[].games.win", "object", "胜场统计。"),
				field("response[].games.lose", "object", "负场统计。"),
				field("response[].points.for", "number", "总得分。"),
				field("response[].points.against", "number", "总失分。"),
			),
			ResponseSample: map[string]any{
				"get":        "standings",
				"parameters": map[string]any{"league": "12", "season": "2023-2024"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response":   []any{map[string]any{"position": 1, "stage": "Regular Season", "group": map[string]any{"name": "Western Conference"}, "team": map[string]any{"id": 132, "name": "Los Angeles Lakers"}, "league": map[string]any{"id": 12, "season": "2023-2024"}, "games": map[string]any{"played": 82, "win": map[string]any{"total": 52}, "lose": map[string]any{"total": 30}}, "points": map[string]any{"for": 9680, "against": 9340}}},
			},
		},
		{
			Method:      "GET",
			Path:        prefix + "/players",
			Summary:     "查询篮球球员",
			Description: "返回篮球球员基础资料，可按球队、比赛、赛季或名称搜索。适合先定位 player id，再与比赛或统计接口组合使用。",
			Parameters: []DocParameter{
				queryParam("id", false, "integer", "1046", "按球员 ID 查询。"),
				queryParam("team", false, "integer", "132", "按球队 ID 过滤。"),
				queryParam("game", false, "integer", "12345", "按比赛 ID 过滤。"),
				queryParam("season", false, "string", "2023-2024", "按赛季过滤。"),
				queryParam("search", false, "string", "james", "按球员名称搜索。"),
			},
			ResponseFields: envelopeFields(
				field("response[].id", "number", "球员 ID。"),
				field("response[].firstname", "string", "名。"),
				field("response[].lastname", "string", "姓。"),
				field("response[].birth.date", "string|null", "出生日期。"),
				field("response[].birth.country", "string|null", "出生国家/地区。"),
				field("response[].nba.start", "number|null", "NBA 起始年份。"),
				field("response[].nba.pro", "number|null", "职业年限。"),
				field("response[].height", "object", "英制/公制身高。"),
				field("response[].weight", "object", "英制/公制体重。"),
				field("response[].college", "string|null", "大学。"),
				field("response[].affiliation", "string|null", "隶属信息。"),
				field("response[].leagues", "object", "不同联赛下的球衣号、位置和活跃状态。"),
			),
			ResponseSample: map[string]any{
				"get":        "players",
				"parameters": map[string]any{"search": "james"},
				"errors":     []any{},
				"results":    1,
				"paging":     map[string]any{"current": 1, "total": 1},
				"response": []any{
					map[string]any{"id": 1046, "firstname": "LeBron", "lastname": "James", "birth": map[string]any{"date": "1984-12-30", "country": "USA"}, "nba": map[string]any{"start": 2003, "pro": 20}, "height": map[string]any{"meters": "2.06"}, "weight": map[string]any{"kilograms": "113.4"}, "leagues": map[string]any{"standard": map[string]any{"jersey": 23, "active": true, "pos": "F"}}},
				},
			},
		},
	}
}

func genericDocEndpoints(prefix string) []DocEndpoint {
	return []DocEndpoint{
		{
			Method:      "GET",
			Path:        prefix + "/<path>",
			Summary:     "转发 GET 请求到配置的上游站点",
			Description: "将命名空间后的路径和查询参数透明转发到当前平台的 upstream base URL。请求参数、响应结构和错误格式由上游站点决定，网关只负责命名空间路由、客户端认证、上游凭证注入和日志记录。",
			Parameters: []DocParameter{
				{Name: "<path>", In: "path", Required: true, Type: "string", Example: "v1/status", Description: "要转发到上游的相对路径。"},
				{Name: "*", In: "query", Required: false, Type: "string", Description: "透传给上游的查询参数。"},
			},
			ResponseFields: []DocField{
				field("<upstream response>", "any", "网关保持上游响应体，不重写业务字段。"),
			},
			ResponseSample: map[string]any{"upstream": "response"},
		},
		{
			Method:      "POST",
			Path:        prefix + "/<path>",
			Summary:     "转发 POST 请求到配置的上游站点",
			Description: "将请求体、命名空间后的路径和支持的请求头转发到上游。适用于 OpenAPI 风格、REST JSON 或其他 HTTP API 平台。",
			Parameters: []DocParameter{
				{Name: "<path>", In: "path", Required: true, Type: "string", Example: "v1/messages", Description: "要转发到上游的相对路径。"},
				{Name: "body", In: "body", Required: false, Type: "object|string|bytes", Description: "按原样透传给上游的请求体。"},
			},
			ResponseFields: []DocField{
				field("<upstream response>", "any", "网关保持上游响应体，不重写业务字段。"),
			},
			ResponseSample: map[string]any{"upstream": "response"},
		},
	}
}

func queryParam(name string, required bool, typ string, example string, description string) DocParameter {
	return DocParameter{
		Name:        name,
		In:          "query",
		Required:    required,
		Type:        typ,
		Example:     example,
		Description: description,
	}
}

func field(name string, typ string, description string) DocField {
	return DocField{Name: name, Type: typ, Description: description}
}

func envelopeFields(fields ...DocField) []DocField {
	common := []DocField{
		field("get", "string", "上游接口名。"),
		field("parameters", "object|array", "上游回显的请求参数；无参数时可能为空数组。"),
		field("errors", "array|object", "错误信息集合；成功时通常为空。"),
		field("results", "number", "当前响应返回的数据条数。"),
		field("paging.current", "number", "当前分页页码。"),
		field("paging.total", "number", "总页数。"),
		field("response", "object|array", "接口业务数据。"),
	}
	return append(common, fields...)
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
			BaseURL:          platformCfg.BaseURL,
			Timeout:          time.Duration(platformCfg.TimeoutSeconds) * time.Second,
			Provider:         platformProvider,
			Pool:             pools[platformCfg.CredentialPool],
			PoolName:         platformCfg.CredentialPool,
			ClientAuthHeader: platformProvider.CredentialHeader(),
		}
	}
	proxyHandler := proxy.NewHandler(proxy.Config{
		Platforms:  platforms,
		MaxRetries: cfg.Scheduler.MaxRetries,
	}, nil, auth.New(cfg.ClientAuth.Enabled, cfg.ClientAuth.Tokens)).WithLogger(logger).WithEventSink(events)
	return proxyHandler, pools, nil
}
