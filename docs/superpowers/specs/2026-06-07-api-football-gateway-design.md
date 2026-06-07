# API-Football 多 Key 请求网关 — 设计文档

- **日期**：2026-06-07
- **状态**：待用户审查
- **项目路径**：`~/Projects/api-football-gateway/`

## 1. 背景与目标

用户持有同一站点（API-Football，官方直连 api-sports）的**多个 API key**，每个 key 有独立的**当日额度**。单个 key 额度不足以覆盖使用量，手动在多个 key 之间切换繁琐。

**目标**：构建一个部署在**局域网**内的请求网关（中转层），实现：

1. 统一入口转发请求到上游 API-Football，客户端无需感知真实密钥。
2. 在多个真实 key 之间**顺序耗尽式轮换**——把多个 key 当成"一个大额度池"使用。
3. **实时监控**每个 key 的当日额度使用情况，并在切换/告警时输出日志。
4. 上游错误时**自动换 key 重试，对下游完全透明**。

### 非目标（YAGNI，明确不做）

- 不做多租户 / 完整鉴权体系（初版客户端认证位仅占位，不校验）。
- 不做额度历史持久化（SQLite/数据库）——额度每天重置且上游响应 header 自带实时值。
- 不做 Prometheus 指标 / Web 可视化面板——本地/局域网自用，状态端点 + 日志足够。
- 不做请求缓存。
- 不手动维护上游业务端点——非管理路径一律透传，天然支持上游全部端点。

## 2. 核心决策摘要

| 维度 | 决策 |
|------|------|
| 部署形态 | 局域网单机部署，监听 `0.0.0.0:<port>` |
| 技术栈 | Go（标准库 `net/http`，手写中转管线） |
| 上游接入 | 官方直连：base `https://v3.football.api-sports.io`，认证 `x-apisports-key` |
| 调度策略 | 顺序耗尽（固定用 key1 直到耗尽再切 key2…） |
| 额度数据来源 | **唯一来源 = 上游响应 header**，网关不自行累加计数 |
| 状态存储 | 内存（重启后由首次响应 header 自愈校准） |
| 客户端认证位 | `Authorization: Bearer <token>`，初版接受任意值不校验，预留校验钩子 |
| 监控形式 | 状态端点 `GET /__gateway/status` + 分级日志 |
| 错误透明性 | 上游错误内部换 key 重试，下游只见最终结果（成功响应或全失败 503） |

## 3. 架构与请求生命周期

### 3.1 分层模型

```
[局域网请求端]                      [中转层 / 网关]                        [上游 API]

baseUrl = http://<网关LAN_IP>:8080   ① 客户端认证钩子(初版直通)
Authorization: Bearer <任意值>  ──►  ② 剥离 Authorization,不透传上游
例: GET .../fixtures?live=all        ③ 选 key (顺序耗尽)
                                     ④ 构造上游请求 + 注入             ──► https://v3.football.api-sports.io/...
                                        x-apisports-key: <真实key>
                                     ⑤ http.Client 发送
                                     ⑥ 读响应 x-ratelimit-* → 更新额度 ◄── 响应(带额度 header)
                                     ⑦ 失败(429/耗尽/5xx)→ 换 key 重试
                              ◄────  ⑧ 仅最终成功响应原样回传下游
```

### 3.2 路由划分

- `/__gateway/*` — 管理端点命名空间（本地直接响应，不走上游）：
  - `GET /__gateway/status` — 各 key 脱敏额度监控 JSON
  - `GET /__gateway/health` — 健康检查
- **其余所有路径** — 一律视为转发给上游。既保持显式分层中转，又天然支持上游全部端点（含未来新增），无需逐个维护。

### 3.3 一级设计原则：上游错误对下游透明

- 网关在**内部**完成所有换 key 重试，客户端连接全程等待，**只看到最终结果**。
- 中间任何一次失败的状态码（429/额度耗尽/5xx）**绝不透传**给客户端。
- 技术约束：网关**先完整缓冲并判定上游响应是否成功，成功才写给客户端**；失败则静默换 key 重试（实现于 `transport.go`）。
- 代价：客户端延迟 = 最坏情况下的重试累计时间。对 GET + 局域网可接受。

## 4. 密钥池与额度状态模型

### 4.1 KeyState（内存）

```go
type KeyState struct {
    Key             string    // 真实 API key (脱敏后才进日志/状态端点)
    Label           string    // 可读标签, 如 "key-1"

    // 来自上游响应 header 的实时额度
    DailyLimit      int       // x-ratelimit-requests-limit
    DailyRemaining  int       // x-ratelimit-requests-remaining
    MinuteLimit     int       // X-RateLimit-Limit
    MinuteRemaining int       // X-RateLimit-Remaining
    LastUpdated     time.Time // 最后一次从响应更新额度的时间

    // 网关维护的调度状态
    Status          KeyStatus // Active / Exhausted / RateLimited / Error
    CooldownUntil   time.Time // 429/Error 退避到期时间
    LastError       string    // 最近一次错误信息 (脱敏)
}
```

### 4.2 KeyStatus 状态机

| 状态 | 含义 | 进入条件 | 退出条件 |
|------|------|---------|---------|
| Active | 可用 | 初始 / 冷却结束 / 跨天重置 | — |
| Exhausted | 当日额度耗尽 | `DailyRemaining <= switch_threshold` 或上游额度耗尽 | 次日 UTC 重置（`LastUpdated` 跨日界 → 乐观复位 Active） |
| RateLimited | 触发每分钟限速 | 上游返回 429 | `CooldownUntil` 到期 → Active |
| Error | 连续异常 | 网络错误/5xx 连续达 `max_error_count`，或 401/403 | 冷却后 → Active |

### 4.3 顺序耗尽调度（KeySelector）

```
按配置顺序遍历 key 池:
  跳过 Exhausted
  跳过 RateLimited 且 CooldownUntil 未到
  跳过 Error 冷却中
  → 返回第一个 Active 的 key
无可用 key → 触发"全部 key 不可用"路径 (见 6.1)
```

### 4.4 关键设计点

1. **额度来源唯一**：以上游响应 header 为准，网关不自累加，避免与真实值漂移；重启自愈。
2. **跨天重置自检**：选 key 时若 `Exhausted` 的 key `LastUpdated` 已是"昨天(UTC)"，乐观复位 Active 试一次，真额度由该次响应修正。
3. **切换阈值**：`switch_threshold` 默认 `1`（留 1 个额度缓冲，避免边界请求失败）。
4. **并发安全**：key 池用 `sync.RWMutex` 保护（局域网多客户端并发）。
5. **脱敏**：状态端点/日志仅显示 `Label + ****尾4位`（如 `key-1 (****a3f9)`），绝不输出完整密钥。

## 5. 配置与项目结构

### 5.1 config.yaml

```yaml
server:
  host: "0.0.0.0"
  port: 8080

upstream:
  base_url: "https://v3.football.api-sports.io"
  timeout_seconds: 30

scheduler:
  switch_threshold: 1                # 当日剩余 <= 此值视为耗尽并切换
  rate_limit_cooldown_seconds: 60    # 429 后该 key 退避时长
  error_cooldown_seconds: 30         # 连续错误后退避时长
  max_error_count: 3                 # 连续错误几次进入 Error 冷却
  max_retries: 3                     # 单请求最多换几次 key

keys:
  - label: "key-1"
    key: "your_real_api_key_1"
  - label: "key-2"
    key: "your_real_api_key_2"

client_auth:
  enabled: false                     # 初版 false = 接受任意 Bearer token
  # tokens: ["future-token-1"]       # 未来启用时填白名单

logging:
  level: "info"                      # debug / info / warn / error
```

**环境变量覆盖**：真实 key 支持环境变量覆盖（避免明文落盘），具体覆盖键命名规则在实现计划中确定。`config.yaml` 中真实 key 加入 `.gitignore`，仓库提交 `config.example.yaml`（占位 key）。

### 5.2 项目结构（many small files）

```
api-football-gateway/
├── cmd/gateway/main.go              # 入口: 加载配置→建组件→启 HTTP server
├── internal/
│   ├── config/
│   │   ├── config.go                # 配置结构体 + YAML 加载 + 环境变量覆盖
│   │   └── config_test.go
│   ├── keypool/
│   │   ├── keypool.go               # KeyState/KeyStatus + 池 + 顺序耗尽选择 + 加锁
│   │   ├── keypool_test.go
│   │   ├── ratelimit.go             # 解析上游 x-ratelimit-* header
│   │   └── ratelimit_test.go
│   ├── proxy/
│   │   ├── handler.go               # 中转管线: 认证钩子→选key→构造上游请求→回传
│   │   ├── handler_test.go
│   │   ├── transport.go             # http.Client 封装 + 换key重试 + 下游无感知
│   │   └── transport_test.go
│   ├── admin/
│   │   ├── status.go                # GET /__gateway/status (脱敏额度 JSON)
│   │   ├── health.go                # GET /__gateway/health
│   │   └── status_test.go
│   └── auth/
│       └── auth.go                  # 客户端 Authorization 校验钩子 (初版直通)
├── config.yaml                      # 真实配置 (.gitignore)
├── config.example.yaml              # 示例配置 (可提交)
├── go.mod
├── go.sum
└── README.md
```

### 5.3 模块职责

| 模块 | 职责 | 依赖 |
|------|------|------|
| config | 加载/校验配置 | — |
| keypool | 维护 key 状态、选 key、解析额度 header | config |
| proxy | HTTP 中转管线、换 key 重试、下游无感知 | keypool, auth |
| admin | 管理端点（status/health） | keypool |
| auth | 客户端认证钩子 | config |

## 6. 错误处理、重试与边界场景

### 6.1 上游响应分类处理

| 上游情况 | 网关行为 | 换 key 重试 |
|---------|---------|------------|
| 2xx 正常 | 读额度 header → 更新状态；若读后 `DailyRemaining <= switch_threshold` 则标记 `Exhausted`（不影响本次已成功的响应） → 回传 | 否 |
| 429 Too Many Requests | 标记 `RateLimited` + 设 CooldownUntil → 换 key | 是 |
| 当日额度耗尽（上游直接返回额度耗尽错误） | 标记 `Exhausted` → 换 key | 是 |
| 401/403（key 失效/无权限） | 标记 `Error` + ERROR 日志 → 换 key | 是 |
| 5xx 上游故障 | 计 error count，达上限标记 `Error` → 换 key | 是 |
| 网络超时/连接失败 | 同 5xx，计 error count → 换 key | 是 |
| 4xx 非额度类（400/404 等） | **请求端自身问题，原样回传，不重试、不换 key** | 否 |

### 6.2 重试控制（防雪崩）

```
单请求重试上限 = min(可用 key 数, max_retries)
每次重试换一个不同 key
全部尝试失败 → "全部 key 不可用"路径
重试仅针对幂等安全场景: API-Football 全 GET, 天然幂等
```

### 6.3 边界场景

**① 全部 key 不可用** → 返回 503 + JSON 错误体 + `Retry-After` header：

```json
{
  "error": "all_keys_unavailable",
  "message": "All upstream API keys are exhausted or rate-limited",
  "keys_status": [
    {"label": "key-1", "status": "exhausted", "daily_remaining": 0},
    {"label": "key-2", "status": "rate_limited", "cooldown_until": "..."}
  ],
  "retry_after_seconds": 42
}
```

`Retry-After` = 最早恢复的 key 的冷却剩余秒数。同时 ERROR 级日志告警。

**② 请求体处理**：API-Football 全 GET 无 body，但管线仍读入缓冲以支持换 key 重发，保证健壮性。

**③ 上游 header 缺失**：某响应意外无 `x-ratelimit-*`，保留该 key 上次已知额度，不误判耗尽，记 debug 日志。

**④ 客户端断连**：上游请求进行中客户端断开 → 用 `context` 取消上游请求，不浪费额度。

### 6.4 日志策略

| 级别 | 事件 |
|------|------|
| INFO | 启动/配置加载、key 切换（`key-1 → key-2, 原因: exhausted`） |
| WARN | 单 key 进入 RateLimited / 接近耗尽（剩余 < 5%） |
| ERROR | key 失效（401/403）、全部 key 不可用、上游持续故障 |
| DEBUG | 每请求选中的 key + 额度快照（默认关闭） |

日志统一脱敏 `label + ****尾4位`，绝不输出完整 key。

## 7. 测试策略

遵循 TDD 与 80% 覆盖率；执行上按"先最相关最小测试 → 模块级 → 全量 + 竞态"分层。

### 7.1 单元测试

| 模块 | 测试重点 |
|------|---------|
| config | YAML 解析、环境变量覆盖、缺失字段校验、非法值拒绝 |
| keypool | **顺序耗尽选择**、状态机转换、跨天重置自检、并发安全(`-race`) |
| ratelimit | 解析各种 `x-ratelimit-*` header 组合、缺失/非法值容错 |
| auth | 初版直通、未来校验钩子（预留测试桩） |

### 7.2 集成测试（`httptest` 假上游）

| 场景 | 验证点 |
|------|--------|
| 正常转发 | 注入正确 key → 上游收到 `x-apisports-key` → 响应回传 |
| 凭证剥离 | 上游收不到 `Authorization`，只收到 `x-apisports-key` |
| 额度耗尽换 key | key-1 耗尽 → 自动用 key-2 重试成功 |
| 429 退避换 key | key-1 返 429 → RateLimited → 换 key-2 |
| 全部不可用 | 所有 key 耗尽 → 503 + 正确 JSON + Retry-After |
| 4xx 不重试 | 上游返 404 → 原样回传，不消耗其他 key |
| 下游无感知 | 中间失败状态码不透传，客户端只见最终结果 |
| 额度监控更新 | 响应 header remaining → 正确反映到 `/__gateway/status` |

### 7.3 管理端点测试

- `/__gateway/status` 返回脱敏 JSON（断言不含完整 key）。
- `/__gateway/health` 健康检查。

### 7.4 执行顺序

```
1. go test ./internal/keypool/    # 核心调度逻辑
2. go test ./internal/...         # 模块级
3. go test -race ./...            # 全量 + 竞态
4. go test -cover ./...           # 覆盖率 >= 80%
```

### 7.5 测试原则

- 假上游用 `httptest.Server`，不碰真实 API、不耗真实额度，可精确编排响应。
- 并发测试用 `-race` 验证 key 池无数据竞争。
- 每个输出路径（日志/status/错误体）断言不泄露完整 key。
- 表驱动测试覆盖 header 解析与状态机的多种组合。

## 8. 已验证的上游事实（API-Football v3 官方直连）

- 认证：HTTP header `x-apisports-key: <KEY>`（官方直连）。
- Base URL：`https://v3.football.api-sports.io`。
- 额度 header（每个响应返回）：
  - `x-ratelimit-requests-limit`（当日总额度）
  - `x-ratelimit-requests-remaining`（当日剩余）
  - `X-RateLimit-Limit` / `X-RateLimit-Remaining`（每分钟速率）
- 额度按天（UTC）重置；典型计划 Free 100/天、Pro 7500/天、Ultra 75000/天、Mega 150000/天。
- 超限返回 429。
