# Generic Platform MVP

日期：2026-06-08

## 共识边界

- 路由模型：`/<namespace>/<path>`。第一段是站点命名空间，剥离后把剩余路径转发给该 namespace 绑定的上游。
- 凭证模型：命名 `credential_pools`。多个 namespace 可以共享同一池，也可以独立使用不同池。
- 协议范围：第一版纯转发，不做请求/响应协议转换。
- 内置 provider：
  - `api_sports`：兼容现有 API-Sports 行为，注入 `x-apisports-key`，解析 API-Sports 额度 header。
  - `generic_http`：通用 Header 凭证注入，支持 `X-API-Key`、`Authorization: Bearer <key>` 等。

## 配置模型

```yaml
platforms:
  api-sports:
    type: api_sports
    base_url: "https://v3.football.api-sports.io"
    credential_pool: api-sports

  weather:
    type: generic_http
    base_url: "https://api.weather.example"
    credential_pool: weather
    auth:
      header: Authorization
      prefix: Bearer

credential_pools:
  api-sports:
    keys:
      - label: key-1
        key: real-api-sports-key
  weather:
    keys:
      - label: primary
        key: real-weather-key
```

## 运行时模型

`proxy.Handler` 按 namespace 选择 runtime：

- `transport`：上游 base URL 与连接池。
- `provider`：凭证注入、限额解析、状态码分类。
- `keypool.Pool`：该 namespace 引用的命名凭证池。

请求生命周期：

```text
client request
  -> client auth
  -> split /<namespace>/<path>
  -> acquire key from namespace credential pool
  -> provider injects upstream credential
  -> upstream request
  -> provider classifies response and parses rate limit
  -> keypool updates state
  -> retry or return final response
```

## 兼容策略

旧版 `workspaces + keys` 配置仍可加载：

- `workspaces.<name>` 自动转换为 `platforms.<name>.type = api_sports`，其中 `<name>` 仍作为对外 namespace。
- 顶层 `keys` 自动转换为 `credential_pools.default.keys`。

旧请求路径保持不变，例如 `/football/fixtures`。新配置建议用站点命名空间，例如 `/api-sports/fixtures`。
