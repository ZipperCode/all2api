# API-Football Gateway

局域网内的 API-Football 多 key 请求网关。对客户端屏蔽真实密钥，在多个 key 间**顺序耗尽式轮换**，实时监控各 key 当日额度，上游错误时**自动换 key 重试且对下游透明**。

## 特性

- **多 key 顺序耗尽**：固定用 key-1 直到当日额度耗尽，再自动切到 key-2……把多个 key 当成一个大额度池。
- **下游无感知**：上游返回 429 / 额度耗尽 / 401 / 403 / 5xx / 网络错误时，网关内部静默换 key 重试，客户端只看到最终成功响应或全部失败的 503。
- **实时额度监控**：额度数据完全来自上游响应 header（`x-ratelimit-*`），网关不自行计数，重启自愈。
- **凭证隔离**：客户端用 `Authorization: Bearer <token>`（初版不校验，预留钩子），与上游真实 `x-apisports-key` 彻底分离。
- **内存状态**：无数据库依赖，单二进制部署。

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

3. 请求端改造：把 baseUrl 指向网关，`Authorization` 任意填（初版不校验）：

   ```bash
   # 原本: https://v3.football.api-sports.io/fixtures?live=all
   curl -H "Authorization: Bearer anything" \
        "http://<网关LAN_IP>:8080/fixtures?live=all"
   ```

   网关会剥离 `Authorization`、注入真实 `x-apisports-key`、转发到上游，并原样回传响应。

## 配置说明

见 `config.example.yaml`。关键项：

| 配置 | 说明 |
|------|------|
| `server.host` / `server.port` | 监听地址（局域网部署用 `0.0.0.0`） |
| `upstream.base_url` | 上游地址，官方直连为 `https://v3.football.api-sports.io` |
| `upstream.timeout_seconds` | 单次上游请求超时（默认 30） |
| `keys` | 真实 key 列表，按顺序耗尽使用 |
| `scheduler.switch_threshold` | 当日剩余 <= 此值即视为耗尽并切换（默认 1） |
| `scheduler.rate_limit_cooldown_seconds` | 429 后该 key 退避时长（默认 60） |
| `scheduler.error_cooldown_seconds` | 连续错误后退避时长（默认 30） |
| `scheduler.max_error_count` | 连续错误几次进入 Error 冷却（默认 3） |
| `scheduler.max_retries` | 单请求最多换几次 key（默认 3） |
| `client_auth.enabled` | 初版 `false`（接受任意 Bearer token） |
| `logging.level` | `debug` / `info` / `warn` / `error` |

### 环境变量覆盖

真实 key 可用环境变量覆盖（避免明文落盘）：

- `GW_SERVER_PORT` 覆盖端口。
- `GW_KEY_<LABEL>` 覆盖对应 label 的 key（label 大写、非字母数字转下划线）。例如 label `key-1` → `GW_KEY_KEY_1`。

非法的 `GW_SERVER_PORT` 会在启动时报错（fail-fast），不会静默回退。

## 监控

| 端点 | 说明 |
|------|------|
| `GET /__gateway/status` | 各 key 脱敏额度状态 JSON（key 仅显示 `****` + 尾 4 位） |
| `GET /__gateway/health` | 健康检查，返回 `{"status":"ok"}` |

`/__gateway/` 为管理命名空间，其余所有路径一律转发上游（天然支持上游全部端点）。

示例：

```bash
curl http://127.0.0.1:8080/__gateway/status
# {"keys":[{"label":"key-1","masked_key":"****a3f9","status":"active","daily_limit":7500,"daily_remaining":7499,...}]}
```

## 上游错误处理（下游无感知）

| 上游情况 | 网关行为 | 是否换 key |
|---------|---------|-----------|
| 2xx 正常 | 更新额度 → 原样回传 | 否 |
| 429 限速 | 标记限速 + 冷却 → 换 key 重试 | 是 |
| 当日额度耗尽 | 标记耗尽 → 换 key 重试 | 是 |
| 401 / 403 key 失效 | 标记 key 失效 + 告警 → 换 key 重试 | 是 |
| 5xx / 网络错误 | 累计错误 → 换 key 重试 | 是 |
| 其余 4xx（参数错误等） | 原样回传，不重试 | 否 |

全部 key 不可用时返回 `503` + `Retry-After` header + JSON 错误体（含各 key 状态）。

## 项目结构

```
cmd/gateway/main.go      入口：加载配置、装配组件、启服务、优雅关闭
internal/config/         配置加载、环境变量覆盖、校验
internal/keypool/        key 状态机、顺序耗尽调度、额度 header 解析
internal/auth/           客户端 Authorization 校验钩子（初版直通）
internal/proxy/          中转管线：换 key 重试、下游无感知
internal/admin/          管理端点（status / health）
```

## 测试

```bash
go test -race ./...      # 全量测试 + 竞态检测
go test -cover ./...     # 覆盖率（各业务包 >= 80%）
```

## 设计与计划文档

- 设计：`docs/superpowers/specs/2026-06-07-api-football-gateway-design.md`
- 实现计划：`docs/superpowers/plans/2026-06-07-api-football-gateway.md`
