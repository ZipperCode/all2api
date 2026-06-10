const state = {
  token: sessionStorage.getItem("all2api_admin_token") || "",
  view: "dashboard",
  overview: null,
  config: null,
  logs: [],
  dirty: false,
  selectedProvider: "",
  visibleKeys: new Set(),
  logFilters: { kind: "", level: "", platform: "" },
};

const views = {
  dashboard: ["Runtime", "控制台"],
  keys: ["Credentials", "密钥管理"],
  logs: ["Audit", "日志"],
};

const $ = (selector) => document.querySelector(selector);

function node(tag, props = {}, ...children) {
  const element = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (key === "class") element.className = value;
    else if (key === "dataset") Object.assign(element.dataset, value);
    else if (key.startsWith("on")) element.addEventListener(key.slice(2).toLowerCase(), value);
    else if (value !== undefined && value !== null) element.setAttribute(key, value);
  }
  children.forEach((child) => appendChild(element, child));
  return element;
}

function appendChild(element, child) {
  if (Array.isArray(child)) {
    child.forEach((item) => appendChild(element, item));
    return;
  }
  if (child === null || child === undefined) return;
  element.append(child instanceof Node ? child : document.createTextNode(String(child)));
}

function text(value, fallback = "") {
  if (value === null || value === undefined || value === "") return fallback;
  return String(value);
}

function setStatus(message = "") {
  $("#save-state").textContent = message;
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Content-Type", "application/json");
  if (state.token) headers.set("Authorization", `Bearer ${state.token}`);
  const response = await fetch(path, { ...options, headers });
  const raw = await response.text();
  const body = raw ? JSON.parse(raw) : null;
  if (!response.ok) {
    throw new Error(body?.message || body?.error || `HTTP ${response.status}`);
  }
  return body;
}

async function login(key) {
  const session = await api("/__gateway/admin/session", {
    method: "POST",
    body: JSON.stringify({ key }),
  });
  state.token = session.token;
  sessionStorage.setItem("all2api_admin_token", state.token);
  $("#login").classList.add("hidden");
  $("#app").classList.remove("hidden");
  await loadAll();
}

async function loadAll() {
  setStatus("加载中");
  const [overview, config, logs] = await Promise.all([
    api("/__gateway/admin/overview"),
    api("/__gateway/admin/config"),
    api("/__gateway/admin/logs?limit=160"),
  ]);
  state.overview = overview;
  state.config = config;
  state.logs = logs.events || [];
  state.dirty = false;
  render();
  setStatus("");
}

function render() {
  if (!views[state.view]) state.view = "dashboard";
  const [kicker, title] = views[state.view];
  $("#view-kicker").textContent = kicker;
  $("#view-title").textContent = title;
  $("#save-config").classList.toggle("hidden", state.view !== "keys");
  document.querySelectorAll(".nav-tab").forEach((tab) => {
    const active = tab.dataset.view === state.view;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-selected", active ? "true" : "false");
  });
  document.querySelectorAll(".view").forEach((view) => view.classList.remove("active"));
  $(`#${state.view}-view`).classList.add("active");

  renderDashboard();
  renderKeys();
  renderLogs();
}

function renderDashboard() {
  const root = $("#dashboard-view");
  if (!state.overview) return;

  root.replaceChildren(
    stack(
      node("div", { class: "overview-grid" },
        node("section", { class: "card" }, metricGrid(state.overview)),
        node("section", { class: "card dark" }, runtimeList(state.overview)),
      ),
      section("命名空间状态", "当前站点前缀与上游配置", platformOverviewTable(state.overview.platforms || [])),
      section("密钥状态", "运行态密钥健康度", poolStatusTable(state.overview.credential_pools || [])),
    ),
  );
}

function runtimeList(overview) {
  return node("div", { class: "runtime-list" },
    runtimeRow("Config", overview.config_path || "-"),
    runtimeRow("Log file", overview.log_file || "-"),
    runtimeRow("Active keys", overview.active_key_count),
  );
}

function runtimeRow(label, value) {
  return node("div", { class: "runtime-row" }, node("span", {}, label), node("strong", { title: text(value, "-") }, text(value, "-")));
}

function metricGrid(overview) {
  return node("div", { class: "metric-grid" },
    metricTile("命名空间", overview.platform_count),
    metricTile("密钥池", overview.credential_pool_count),
    metricTile("总密钥", overview.key_count),
    metricTile("可用密钥", overview.active_key_count),
    metricTile("异常密钥", overview.problem_key_count),
  );
}

function metricTile(label, value) {
  return node("div", { class: "metric-tile" }, node("span", {}, label), node("strong", {}, value));
}

function platformOverviewTable(platforms) {
  if (!platforms.length) return empty();
  return table(
    ["命名空间", "类型", "Base URL", "密钥池", "超时", "认证头"],
    platforms.map((platform) => [
      strongText(platform.name),
      statusPill(platform.type),
      truncate(platform.base_url),
      platform.credential_pool || "-",
      `${platform.timeout_seconds || 30}s`,
      platform.auth_header || platform.auth?.header || "x-apisports-key",
    ]),
  );
}

function platformEntries() {
  return Object.entries(state.config?.platforms || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, config]) => ({ name, ...config }));
}

function renderKeys() {
  const root = $("#keys-view");
  if (!state.config) return;
  const providers = platformEntries();
  const selected = ensureSelectedProvider(providers);
  const unboundPools = credentialPoolEntries().filter(([name]) => !providers.some((provider) => provider.credential_pool === name));

  root.replaceChildren(
    stack(
      section("提供商密钥", "选择当前配置里的提供商，再维护它绑定的上游密钥",
        node("div", { class: "provider-layout" },
          providerRail(providers),
          credentialPanel(selected, providers),
        ),
      ),
      section("认证设置", "管理调用网关时需要使用的客户端密钥；请求头保持各站点官方认证方式",
        clientAuthSettings(providers),
      ),
      unboundPools.length ? section("未绑定密钥池", "这些密钥池当前没有提供商引用，需在配置文件绑定后才会参与转发", unboundPoolTable(unboundPools)) : null,
    ),
  );
}

function credentialPoolEntries() {
  return Object.entries(state.config?.credential_pools || {}).sort(([a], [b]) => a.localeCompare(b));
}

function ensureSelectedProvider(providers) {
  if (!providers.length) {
    state.selectedProvider = "";
    return null;
  }
  const selected = providers.find((provider) => provider.name === state.selectedProvider);
  if (selected) return selected;
  state.selectedProvider = providers[0].name;
  return providers[0];
}

function providerRail(providers) {
  return node("aside", { class: "provider-rail", "aria-label": "提供商列表" },
    node("div", { class: "rail-header" },
      node("div", {},
        node("p", { class: "eyebrow" }, "Providers"),
        node("strong", {}, `${providers.length} 个提供商`),
      ),
      statusPill(`${activeProviderCount(providers)} active`),
    ),
    providers.length ? node("div", { class: "provider-list" }, providers.map(providerItem)) : empty(),
  );
}

function providerItem(provider) {
  const active = provider.name === state.selectedProvider;
  const health = keyHealth(provider.credential_pool);
  return node("button", {
    class: `provider-item${active ? " active" : ""}`,
    type: "button",
    "aria-pressed": active ? "true" : "false",
    onClick: () => {
      state.selectedProvider = provider.name;
      renderKeys();
    },
  },
    node("div", { class: "provider-item-main" },
      node("strong", {}, provider.name),
      statusPill(provider.type || "provider"),
    ),
    node("span", { class: "provider-url", title: text(provider.base_url, "-") }, text(provider.base_url, "-")),
    node("div", { class: "provider-foot" },
      node("span", {}, provider.credential_pool || "未绑定密钥池"),
      node("span", {}, `${health.active}/${health.total} 可用`),
    ),
  );
}

function credentialPanel(provider, providers) {
  if (!provider) {
    return node("div", { class: "credential-panel" }, empty());
  }
  const poolName = provider.credential_pool || "";
  const pool = configPool(poolName);
  const linked = providers.filter((item) => item.credential_pool === poolName);
  const keys = pool.keys || [];

  return node("article", { class: "credential-panel" },
    node("div", { class: "credential-hero" },
      node("div", {},
        node("p", { class: "eyebrow" }, "Selected Provider"),
        node("h3", {}, provider.name),
        node("p", { class: "credential-url", title: text(provider.base_url, "-") }, text(provider.base_url, "-")),
      ),
      node("div", { class: "panel-actions" },
        poolName ? button("新增密钥", "primary", () => addKey(poolName)) : node("button", { class: "button primary", type: "button", disabled: "disabled" }, "未绑定密钥池"),
      ),
    ),
    node("div", { class: "provider-details" },
      kv("请求前缀", `/${provider.name}`),
      kv("Provider", provider.type),
      kv("密钥池", poolName || "-"),
      kv("认证头", authLabel(provider)),
    ),
    linkedNamespaces(linked),
    node("div", { class: "key-management-body" },
      keys.length ? keyManagementTable(poolName, keys) : emptyKeys(poolName),
    ),
  );
}

function linkedNamespaces(providers) {
  return node("div", { class: "linked-namespaces" },
    node("span", {}, "共享命名空间"),
    providers.map((provider) => node("span", { class: "chip" }, `/${provider.name}`)),
  );
}

function keyManagementTable(poolName, keys) {
  return table(
    ["Label", "密钥值", "状态", "Masked", "Daily", "Minute", "错误", "操作"],
    keys.map((key, index) => {
      const runtime = runtimeKey(poolName, key, index);
      const visible = state.visibleKeys.has(keyVisibilityId(poolName, index));
      return [
        input(key.label || "", (value) => updateKey(poolName, index, "label", value), "table-input", "text", "", "input", { "aria-label": "密钥标签" }),
        node("div", { class: "secret-field" },
          input(key.key || "", (value) => updateKey(poolName, index, "key", value), key.key ? "table-input" : "table-input input-warning", visible ? "text" : "password", "粘贴上游 API Key", "input", { "aria-label": "密钥值" }),
          iconButton(visible ? "隐藏" : "查看", visible ? "Hide key" : "Show key", () => toggleKeyVisibility(poolName, index), visible ? "eye-off" : "eye"),
        ),
        keyRuntimePill(key, runtime),
        truncate(runtime?.masked_key || maskSecret(key.key)),
        runtime?.daily_remaining ?? "-",
        runtime?.minute_remaining ?? "-",
        truncate(runtime?.last_error || "-"),
        node("div", { class: "inline-actions" }, button("删除", "text danger", () => deleteKey(poolName, index))),
      ];
    }),
    "credential-table",
  );
}

function clientAuthSettings(providers) {
  state.config.client_auth ||= { enabled: false, tokens: [] };
  state.config.client_auth.tokens ||= [];
  return node("div", { class: "settings-panel" },
    node("div", { class: "settings-row" },
      node("div", {},
        node("h3", {}, "客户端认证"),
        node("p", { class: "muted" }, "开启后，下游必须使用这里配置的客户端密钥调用网关。"),
      ),
      node("label", { class: "switch-field" },
        node("input", { type: "checkbox", checked: state.config.client_auth.enabled ? "checked" : null, onChange: (event) => updateClientAuthEnabled(event.target.checked) }),
        node("span", {}, state.config.client_auth.enabled ? "已开启" : "未开启"),
      ),
    ),
    authHeaderTable(providers),
    node("div", { class: "token-list" },
      state.config.client_auth.tokens.length ? state.config.client_auth.tokens.map((token, index) => tokenRow(token, index)) : empty(),
    ),
    node("div", { class: "toolbar" },
      node("div", { class: "muted" }, "保存配置后立即热重载。启用但无 token 时会拒绝所有业务请求。"),
      button("新增客户端密钥", "secondary", addClientToken),
    ),
  );
}

function authHeaderTable(providers) {
  return table(
    ["命名空间", "官方认证头", "客户端示例"],
    providers.map((provider) => [
      strongText(`/${provider.name}`),
      authLabel(provider),
      truncate(`${authHeader(provider)}: ${authValuePlaceholder(provider)}`),
    ]),
    "compact-table",
  );
}

function tokenRow(token, index) {
  return node("div", { class: "token-row" },
    field("客户端密钥", input(token, (value) => updateClientToken(index, value), "table-input", "text", "client token", "input", { "aria-label": "客户端密钥" })),
    node("div", { class: "inline-actions" }, button("删除", "text danger", () => deleteClientToken(index))),
  );
}

function emptyKeys(poolName) {
  return node("div", { class: "empty-state" },
    poolName ? "当前提供商还没有密钥，点击右上角新增密钥。" : "当前提供商没有绑定密钥池，请先在配置文件中绑定 credential_pool。",
  );
}

function unboundPoolTable(pools) {
  return table(
    ["密钥池", "密钥数", "状态"],
    pools.map(([name, pool]) => [strongText(name), (pool.keys || []).length, statusPill("未绑定")]),
    "compact-table",
  );
}

function configPool(poolName) {
  if (!poolName) return { keys: [] };
  return state.config?.credential_pools?.[poolName] || { keys: [] };
}

function ensureConfigPool(poolName) {
  state.config.credential_pools ||= {};
  state.config.credential_pools[poolName] ||= { keys: [] };
  return state.config.credential_pools[poolName];
}

function runtimePool(poolName) {
  return (state.overview?.credential_pools || []).find((pool) => pool.name === poolName) || { keys: [] };
}

function runtimeKey(poolName, key, index) {
  const runtimeKeys = runtimePool(poolName).keys || [];
  return runtimeKeys.find((item) => item.label === key.label) || runtimeKeys[index] || null;
}

function keyRuntimePill(key, runtime) {
  if (!key.key) return statusPill("待填写");
  if (!runtime?.status) return statusPill("待保存");
  return statusPill(runtime.status);
}

function keyHealth(poolName) {
  const configured = configPool(poolName).keys || [];
  const runtimeKeys = runtimePool(poolName).keys || [];
  const active = runtimeKeys.filter((key) => key.status === "active").length;
  return { total: configured.length, active };
}

function activeProviderCount(providers) {
  return providers.filter((provider) => keyHealth(provider.credential_pool).active > 0).length;
}

function authLabel(provider) {
  const header = authHeader(provider);
  const prefix = provider.auth?.prefix || provider.auth_prefix || "";
  return prefix ? `${header}: ${prefix}` : header;
}

function authHeader(provider) {
  return provider.auth?.header || provider.auth_header || "x-apisports-key";
}

function authValuePlaceholder(provider) {
  const prefix = provider.auth?.prefix || provider.auth_prefix || "";
  return prefix ? `${prefix} <client-key>` : "<client-key>";
}

function maskSecret(value) {
  const raw = text(value);
  if (!raw) return "未填写";
  if (raw.length <= 8) return "********";
  return `${raw.slice(0, 4)}...${raw.slice(-4)}`;
}

function renderLogs() {
  const root = $("#logs-view");
  root.replaceChildren(
    stack(
      section("事件日志", "按类型、级别和命名空间过滤",
        node("div", { class: "toolbar" },
          node("div", { class: "filters" },
            select(state.logFilters.kind, ["", "proxy", "admin"], (value) => { state.logFilters.kind = value; refreshLogs(); }, "类型"),
            select(state.logFilters.level, ["", "info", "warn", "error"], (value) => { state.logFilters.level = value; refreshLogs(); }, "级别"),
            input(state.logFilters.platform, (value) => { state.logFilters.platform = value; refreshLogs(); }, "table-input", "text", "命名空间", "input", { "aria-label": "命名空间过滤" }),
          ),
          node("div", { class: "inline-actions" },
            button("刷新日志", "secondary", refreshLogs),
            button("清空日志", "text danger", clearLogs),
          ),
        ),
        eventTable(state.logs),
      ),
    ),
  );
}

function eventTable(events) {
  if (!events.length) return empty();
  return table(
    ["时间", "类型", "级别", "命名空间", "消息", "状态码"],
    events.map((event) => [
      formatTime(event.time),
      statusPill(event.kind || "event"),
      levelPill(event.level || "info"),
      event.platform || "-",
      node("span", { class: "event-message", title: event.message || event.action || "" }, event.message || event.action || "-"),
      event.status_code || "-",
    ]),
    "log-table",
  );
}

function poolStatusTable(pools) {
  const rows = [];
  pools.forEach((pool) => {
    (pool.keys || []).forEach((key) => {
      rows.push([
        pool.name,
        key.label,
        truncate(key.masked_key),
        statusPill(key.status),
        key.daily_remaining || 0,
        key.minute_remaining || 0,
        truncate(key.last_error || "-"),
      ]);
    });
  });
  if (!rows.length) return empty();
  return table(["密钥池", "Label", "Masked key", "状态", "Daily", "Minute", "Last error"], rows);
}

function stack(...children) {
  return node("div", { class: "page-stack" }, children);
}

function section(title, subtitle, ...children) {
  return node("section", { class: "section" },
    node("div", { class: "section-header" },
      node("div", {}, node("h3", {}, title), subtitle ? node("p", {}, subtitle) : null),
    ),
    children,
  );
}

function table(headings, rows, className = "") {
  return node("div", { class: "table-wrap" },
    node("table", { class: className },
      node("thead", {}, node("tr", {}, headings.map((heading) => node("th", {}, heading)))),
      node("tbody", {}, rows.map((row) => node("tr", {}, row.map((content) => cell(content))))),
    ),
  );
}

function field(label, control) {
  return node("label", { class: "field" }, node("span", {}, label), control);
}

function input(value, onInput, className = "", type = "text", placeholder = "", eventName = "input", attrs = {}) {
  const props = { class: className, type, value: text(value), placeholder, ...attrs };
  props[`on${eventName}`] = (event) => onInput(event.target.value);
  return node("input", props);
}

function select(value, options, onChange, label = "") {
  const element = node("select", { class: "table-input", "aria-label": label, onChange: (event) => onChange(event.target.value) },
    options.map((option) => node("option", { value: option }, option || "全部")),
  );
  element.value = value || "";
  return element;
}

function button(label, variant, onClick) {
  return node("button", { class: `button ${variant}`, type: "button", onClick }, label);
}

function iconButton(label, title, onClick, icon) {
  return node("button", { class: "icon-button", type: "button", title, "aria-label": label, onClick },
    node("span", { class: `icon ${icon}`, "aria-hidden": "true" }),
  );
}

function kv(label, value) {
  return node("div", { class: "kv" }, node("span", {}, label), node("strong", { title: text(value, "-") }, text(value, "-")));
}

function strongText(value) {
  return node("strong", {}, text(value, "-"));
}

function truncate(value) {
  return node("span", { class: "truncate", title: text(value, "-") }, text(value, "-"));
}

function cell(content) {
  return node("td", {}, content ?? "");
}

function statusPill(status) {
  const normalized = text(status, "unknown");
  if (normalized === "active") return node("span", { class: "pill active" }, normalized);
  if (["error", "exhausted", "invalid"].includes(normalized)) return node("span", { class: "pill error" }, normalized);
  if (["warn", "rate_limited"].includes(normalized)) return node("span", { class: "pill warn" }, normalized);
  return node("span", { class: "pill" }, normalized);
}

function levelPill(level) {
  if (level === "error") return node("span", { class: "pill error" }, level);
  if (level === "warn") return node("span", { class: "pill warn" }, level);
  return node("span", { class: "pill active" }, level);
}

function empty() {
  return document.querySelector("#empty-template").content.cloneNode(true);
}

function formatTime(value) {
  if (!value) return "-";
  return new Date(value).toLocaleString();
}

function markDirty() {
  state.dirty = true;
  setStatus("未保存");
}

function keyVisibilityId(poolName, index) {
  return `${poolName}:${index}`;
}

function toggleKeyVisibility(poolName, index) {
  const id = keyVisibilityId(poolName, index);
  if (state.visibleKeys.has(id)) {
    state.visibleKeys.delete(id);
  } else {
    state.visibleKeys.add(id);
  }
  renderKeys();
}

function addKey(poolName) {
  if (!poolName) return;
  const pool = ensureConfigPool(poolName);
  pool.keys ||= [];
  pool.keys.push({ label: nextKeyLabel(pool.keys), key: "" });
  markDirty();
  setStatus("已新增密钥，未保存");
  renderKeys();
}

function nextKeyLabel(keys) {
  let index = keys.length + 1;
  let label = `key-${index}`;
  const labels = new Set(keys.map((key) => key.label));
  while (labels.has(label)) label = `key-${++index}`;
  return label;
}

function updateKey(poolName, index, key, value) {
  state.config.credential_pools[poolName].keys[index][key] = value;
  markDirty();
}

function deleteKey(poolName, index) {
  state.config.credential_pools[poolName].keys.splice(index, 1);
  state.visibleKeys.delete(keyVisibilityId(poolName, index));
  markDirty();
  renderKeys();
}

function updateClientAuthEnabled(enabled) {
  state.config.client_auth ||= { enabled: false, tokens: [] };
  state.config.client_auth.enabled = enabled;
  markDirty();
  renderKeys();
}

function addClientToken() {
  state.config.client_auth ||= { enabled: false, tokens: [] };
  state.config.client_auth.tokens ||= [];
  state.config.client_auth.tokens.push("");
  markDirty();
  renderKeys();
}

function updateClientToken(index, value) {
  state.config.client_auth.tokens[index] = value;
  markDirty();
}

function deleteClientToken(index) {
  state.config.client_auth.tokens.splice(index, 1);
  markDirty();
  renderKeys();
}

async function saveConfig() {
  setStatus("保存中");
  await api("/__gateway/admin/config", { method: "PUT", body: JSON.stringify(state.config) });
  await loadAll();
  setStatus("已保存");
  setTimeout(() => setStatus(""), 1200);
}

async function refreshLogs() {
  const query = new URLSearchParams({ limit: "160" });
  if (state.logFilters.kind) query.set("kind", state.logFilters.kind);
  if (state.logFilters.level) query.set("level", state.logFilters.level);
  if (state.logFilters.platform) query.set("platform", state.logFilters.platform);
  const response = await api(`/__gateway/admin/logs?${query.toString()}`);
  state.logs = response.events || [];
  renderLogs();
}

async function clearLogs() {
  if (!window.confirm("确认清空全部日志？")) return;
  setStatus("清空中");
  await api("/__gateway/admin/logs", { method: "DELETE" });
  await refreshLogs();
  setStatus("已清空");
  setTimeout(() => setStatus(""), 1200);
}

document.querySelector("#login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  $("#login-error").textContent = "";
  try {
    await login($("#admin-key").value);
  } catch (error) {
    $("#login-error").textContent = error.message;
  }
});

document.querySelectorAll("[data-view]").forEach((tab) => {
  tab.addEventListener("click", () => {
    state.view = tab.dataset.view;
    render();
  });
});

$("#refresh").addEventListener("click", () => loadAll().catch((error) => setStatus(error.message)));
$("#save-config").addEventListener("click", () => saveConfig().catch((error) => setStatus(error.message)));
$("#logout").addEventListener("click", () => {
  sessionStorage.removeItem("all2api_admin_token");
  state.token = "";
  $("#app").classList.add("hidden");
  $("#login").classList.remove("hidden");
});

if (state.token) {
  $("#login").classList.add("hidden");
  $("#app").classList.remove("hidden");
  loadAll().catch(() => {
    sessionStorage.removeItem("all2api_admin_token");
    state.token = "";
    $("#app").classList.add("hidden");
    $("#login").classList.remove("hidden");
  });
}
