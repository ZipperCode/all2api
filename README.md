# all2api Gateway

局域网内的通用 API 转发平台。对客户端屏蔽真实上游凭证，按 `/<platform>/<原路径>` 路由到不同上游，并在命名 `credential_pools` 中做多 key 顺序耗尽、自动重试和状态监控。

第一版是**纯转发 MVP**：不做请求/响应协议转换，只做路由、客户端认证、上游凭证注入、限额解析、错误分类和换 key 重试。

## 特性

- **多 platform 路由**：`/<platform>/<path>` 选择上游平台，路径前缀会被剥离后转发。
- **命名凭证池**：多个 platform 可以共享同一个 `credential_pool`，也可以各自独立。
- **平台 provider**：
  - `api_sports`：注入 `x-apisports-key`，解析 API-Sports 额度 header。
  - `generic_http`：可配置任意 Header 凭证，例如 `X-API-Key` 或 `Authorization: Bearer <key>`。
- **顺序耗尽式轮换**：固定用 key-1 直到额度耗尽，再切 key-2。
- **下游无感知重试**：429、401/403、5xx、网络错误会内部换 key 重试；全部失败才返回 503。
- **凭证隔离**：客户端凭证不会透传到上游，上游真实凭证也不会泄露给客户端。
- **内存状态**：无数据库依赖，额度来自上游响应 header，重启后由响应自愈。

## 快速开始

一键本地运行：

```bash
./scripts/run.sh
```

首次运行会自动从 `config.example.yaml` 创建 `config.yaml`，生成 `management.admin_key`，并创建 `logs/`。已有 `config.yaml` 不会被覆盖。

手动运行：

```bash
cp config.example.yaml config.yaml
# 编辑 config.yaml，填入真实 key 和 management.admin_key

go run ./cmd/gateway -config config.yaml
# 或
go build -o gateway ./cmd/gateway && ./gateway -config config.yaml
```

管理界面：

```text
http://127.0.0.1:8080/__admin/
```

登录只需要输入 `management.admin_key`（或 `GW_ADMIN_KEY`）。

## Docker Compose 部署

一键启动：

```bash
./scripts/docker-up.sh
```

手动启动：

```bash
cp config.example.yaml config.yaml
# 编辑 config.yaml，填入真实 key 和 management.admin_key
mkdir -p logs
docker compose up -d --build
```

默认映射到宿主机 `8080`：

```text
http://127.0.0.1:8080/__admin/
```

常用命令：

```bash
docker compose logs -f all2api
docker compose restart all2api
docker compose down
```

可用 `ALL2API_PORT` 改宿主机端口：

```bash
ALL2API_PORT=18080 docker compose up -d --build
```

请求示例：

```bash
# API-Sports platform
curl -H "Authorization: Bearer client-token" \
  "http://127.0.0.1:8080/football/fixtures?live=all"

# Generic Header platform，配置中会把 weather pool 的 key 注入为 Authorization: Bearer <key>
curl -H "Authorization: Bearer client-token" \
  "http://127.0.0.1:8080/weather/forecast?city=shanghai"
```

网关处理流程：

```text
/<platform>/<path>
  -> 校验客户端凭证（可关闭）
  -> 剥离 platform 前缀
  -> 按 platform 选择 provider + credential_pool
  -> 选择可用 key
  -> provider 注入上游凭证
  -> 转发到 base_url + /<path>
  -> 根据 provider 解析额度与错误
```

## 配置说明

核心结构见 `config.example.yaml`。

| 配置 | 说明 |
|------|------|
| `platforms.<name>.type` | `api_sports` 或 `generic_http` |
| `platforms.<name>.base_url` | 上游基础地址 |
| `platforms.<name>.credential_pool` | 引用的命名凭证池 |
| `platforms.<name>.auth.header` | `generic_http` 注入凭证的 Header 名 |
| `platforms.<name>.auth.prefix` | 可选前缀，如 `Bearer`，最终为 `Bearer <key>` |
| `credential_pools.<name>.keys` | 真实上游 key 列表 |
| `scheduler.max_retries` | 单请求最多换 key 尝试次数 |
| `management.admin_key` | 管理后台登录 key |
| `management.log_file` | 结构化 JSONL 日志文件 |
| `management.log_read_limit` | 日志页最大读取条数 |
| `client_auth.enabled` | 是否校验下游客户端 token |

### API-Sports 共享 key

```yaml
platforms:
  football:
    type: api_sports
    base_url: "https://v3.football.api-sports.io"
    credential_pool: api-sports
  basketball:
    type: api_sports
    base_url: "https://v1.basketball.api-sports.io"
    credential_pool: api-sports

credential_pools:
  api-sports:
    keys:
      - label: key-1
        key: real-key-1
      - label: key-2
        key: real-key-2
```

### Generic Header API

```yaml
platforms:
  weather:
    type: generic_http
    base_url: "https://api.weather.example"
    credential_pool: weather
    auth:
      header: Authorization
      prefix: Bearer
    rate_limit:
      daily_limit_header: X-Daily-Limit
      daily_remaining_header: X-Daily-Remaining

credential_pools:
  weather:
    keys:
      - label: primary
        key: real-weather-key
```

## 环境变量覆盖

- `GW_SERVER_PORT` 覆盖监听端口。
- `GW_ADMIN_KEY` 覆盖管理后台登录 key。
- `GW_LOG_FILE` 覆盖结构化日志文件路径。
- 新配置：`GW_KEY_<POOL>_<LABEL>` 覆盖指定池内 key。
  - `api-sports` + `key-1` → `GW_KEY_API_SPORTS_KEY_1`
  - `weather` + `primary` → `GW_KEY_WEATHER_PRIMARY`
- 兼容旧配置：`GW_KEY_<LABEL>` 仍可覆盖旧顶层 `keys`。

非法的 `GW_SERVER_PORT` 会在启动时报错，不会静默回退。

## 管理端点

| 端点 | 说明 |
|------|------|
| `GET /__gateway/status` | 所有 credential pool 的脱敏 key 状态 |
| `GET /__gateway/health` | 健康检查 |
| `POST /__gateway/admin/session` | 管理后台登录 |
| `GET /__gateway/admin/overview` | dashboard 数据 |
| `GET / PUT /__gateway/admin/config` | 读取/写回配置并热重载 |
| `GET / DELETE /__gateway/admin/logs` | 查询/清空文件日志 |
| `GET /__gateway/admin/docs` | 动态接口文档 |

示例：

```bash
curl http://127.0.0.1:8080/__gateway/status
```

返回结构按池分组：

```json
{
  "credential_pools": [
    {
      "name": "api-sports",
      "keys": [
        {
          "label": "key-1",
          "masked_key": "****1234",
          "status": "active",
          "daily_limit": 100,
          "daily_remaining": 80
        }
      ]
    }
  ]
}
```

## 旧配置兼容

旧版 `workspaces + keys` 配置仍可加载。启动时会自动转换为：

- 每个 `workspaces.<name>` → 一个 `api_sports` platform。
- 顶层 `keys` → `credential_pools.default.keys`。

旧请求路径如 `/football/fixtures` 保持可用。

## 测试

```bash
go test ./...
go test -race ./...
```
