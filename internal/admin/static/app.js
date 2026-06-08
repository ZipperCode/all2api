const state = {
  token: sessionStorage.getItem("all2api_admin_token") || "",
  view: "dashboard",
  overview: null,
  config: null,
  logs: [],
  docs: null,
  dirty: false,
  logFilters: { kind: "", level: "", platform: "" },
};

const views = {
  dashboard: ["Runtime", "控制台"],
  platforms: ["Routing", "平台"],
  keys: ["Credentials", "密钥池"],
  logs: ["Audit", "日志"],
  docs: ["Reference", "文档"],
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
  const [overview, config, logs, docs] = await Promise.all([
    api("/__gateway/admin/overview"),
    api("/__gateway/admin/config"),
    api("/__gateway/admin/logs?limit=160"),
    api("/__gateway/admin/docs"),
  ]);
  state.overview = overview;
  state.config = config;
  state.logs = logs.events || [];
  state.docs = docs;
  state.dirty = false;
  render();
  setStatus("");
}

function render() {
  const [kicker, title] = views[state.view];
  $("#view-kicker").textContent = kicker;
  $("#view-title").textContent = title;
  $("#save-config").classList.toggle("hidden", !["platforms", "keys"].includes(state.view));
  document.querySelectorAll(".nav-tab").forEach((tab) => {
    const active = tab.dataset.view === state.view;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-selected", active ? "true" : "false");
  });
  document.querySelectorAll(".view").forEach((view) => view.classList.remove("active"));
  $(`#${state.view}-view`).classList.add("active");

  renderDashboard();
  renderPlatforms();
  renderKeys();
  renderLogs();
  renderDocs();
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
      section("平台状态", "当前请求前缀与上游配置", platformOverviewTable(state.overview.platforms || [])),
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
    metricTile("平台", overview.platform_count),
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
    ["平台", "类型", "Base URL", "密钥池", "超时", "认证头"],
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

function renderPlatforms() {
  const root = $("#platforms-view");
  if (!state.config) return;
  const platforms = platformEntries();
  root.replaceChildren(
    stack(
      section("平台配置", "修改后点击右上角保存配置",
        node("div", { class: "toolbar" },
          node("div", { class: "tool-cluster" }, button("新增平台", "primary", addPlatform)),
        ),
        platformEditor(platforms),
      ),
    ),
  );
}

function platformEntries() {
  return Object.entries(state.config?.platforms || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, config]) => ({ name, ...config }));
}

function platformEditor(platforms) {
  if (!platforms.length) return empty();
  return node("div", { class: "table-wrap" },
    node("table", {},
      node("thead", {}, node("tr", {}, ["名称", "类型", "Base URL", "密钥池", "超时", "Auth Header", "Auth Prefix", "操作"].map((heading) => node("th", {}, heading)))),
      node("tbody", {}, platforms.map((platform) => node("tr", {},
        cell(input(platform.name, (value) => renamePlatform(platform.name, value), "table-input", "text", "", "change", { "aria-label": "平台名称" })),
        cell(select(platform.type, ["api_sports", "generic_http"], (value) => updatePlatform(platform.name, "type", value), "平台类型")),
        cell(input(platform.base_url || "", (value) => updatePlatform(platform.name, "base_url", value), "table-input", "text", "", "input", { "aria-label": "Base URL" })),
        cell(input(platform.credential_pool || "", (value) => updatePlatform(platform.name, "credential_pool", value), "table-input", "text", "", "input", { "aria-label": "密钥池" })),
        cell(input(platform.timeout_seconds || 30, (value) => updatePlatform(platform.name, "timeout_seconds", Number(value)), "table-input", "number", "", "input", { "aria-label": "超时秒数" })),
        cell(input(platform.auth?.header || "", (value) => updatePlatformAuth(platform.name, "header", value), "table-input", "text", "", "input", { "aria-label": "认证头" })),
        cell(input(platform.auth?.prefix || "", (value) => updatePlatformAuth(platform.name, "prefix", value), "table-input", "text", "", "input", { "aria-label": "认证前缀" })),
        cell(button("删除", "text danger", () => deletePlatform(platform.name))),
      ))),
    ),
  );
}

function renderKeys() {
  const root = $("#keys-view");
  if (!state.config) return;
  const pools = Object.entries(state.config.credential_pools || {}).sort(([a], [b]) => a.localeCompare(b));
  root.replaceChildren(
    stack(
      section("密钥池", "修改后点击右上角保存配置",
        node("div", { class: "toolbar" },
          node("div", { class: "tool-cluster" }, button("新增密钥池", "primary", addPool)),
        ),
        node("div", { class: "pool-stack" }, pools.length ? pools.map(([name, pool]) => poolCard(name, pool)) : empty()),
      ),
    ),
  );
}

function poolCard(name, pool) {
  const keys = pool.keys || [];
  return node("article", { class: "pool-card" },
    node("div", { class: "pool-card-header" },
      node("div", {},
        node("h3", {}, name),
        node("p", { class: "muted" }, `${keys.length} keys`),
      ),
      node("div", { class: "inline-actions" },
        button("新增密钥", "secondary", () => addKey(name)),
        button("删除池", "text danger", () => deletePool(name)),
      ),
    ),
    node("div", { class: "key-list" }, keys.length ? keys.map((key, index) => keyRow(name, key, index)) : empty()),
  );
}

function keyRow(poolName, key, index) {
  return node("div", { class: "key-row" },
    field("Label", input(key.label || "", (value) => updateKey(poolName, index, "label", value), "table-input", "text", "", "input", { "aria-label": "密钥标签" })),
    field("Key", input(key.key || "", (value) => updateKey(poolName, index, "key", value), "table-input", "password", "", "input", { "aria-label": "密钥值" })),
    node("div", { class: "inline-actions" }, button("删除", "text danger", () => deleteKey(poolName, index))),
  );
}

function renderLogs() {
  const root = $("#logs-view");
  root.replaceChildren(
    stack(
      section("事件日志", "按类型、级别和平台过滤",
        node("div", { class: "toolbar" },
          node("div", { class: "filters" },
            select(state.logFilters.kind, ["", "proxy", "admin"], (value) => { state.logFilters.kind = value; refreshLogs(); }, "类型"),
            select(state.logFilters.level, ["", "info", "warn", "error"], (value) => { state.logFilters.level = value; refreshLogs(); }, "级别"),
            input(state.logFilters.platform, (value) => { state.logFilters.platform = value; refreshLogs(); }, "table-input", "text", "平台", "input", { "aria-label": "平台过滤" }),
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
    ["时间", "类型", "级别", "平台", "消息", "状态码"],
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

function renderDocs() {
  const root = $("#docs-view");
  if (!state.docs) return;
  root.replaceChildren(
    stack(
      section("平台文档", "按当前运行配置生成",
        node("div", { class: "doc-grid" }, (state.docs.platforms || []).map(platformDoc)),
      ),
      section("管理 API", "后台页面使用的鉴权接口",
        docsTable(state.docs.admin_endpoints || []),
      ),
    ),
  );
}

function platformDoc(doc) {
  return node("article", { class: "doc-card" },
    node("div", { class: "doc-card-header" },
      node("div", {},
        node("h3", {}, doc.name),
        node("p", { class: "muted" }, `${doc.type} · ${doc.gateway_base_path}`),
      ),
      statusPill(doc.credential_pool),
    ),
    node("div", { class: "kv-grid" },
      kv("Upstream", doc.upstream_base_url),
      kv("Injected auth", `${doc.auth_header}: ${doc.auth_value}`),
    ),
    node("pre", {}, doc.example_curl),
  );
}

function docsTable(endpoints) {
  if (!endpoints.length) return empty();
  return table(
    ["Method", "Path", "Description"],
    endpoints.map((endpoint) => [endpoint.method, endpoint.path, endpoint.description]),
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

function updatePlatform(name, key, value) {
  state.config.platforms[name][key] = value;
  markDirty();
}

function updatePlatformAuth(name, key, value) {
  state.config.platforms[name].auth ||= {};
  state.config.platforms[name].auth[key] = value;
  markDirty();
}

function renamePlatform(oldName, newName) {
  const nextName = newName.trim();
  if (!nextName || oldName === nextName || state.config.platforms[nextName]) return;
  state.config.platforms[nextName] = state.config.platforms[oldName];
  delete state.config.platforms[oldName];
  markDirty();
  renderPlatforms();
}

function addPlatform() {
  state.config.platforms ||= {};
  let name = "new-platform";
  let index = 1;
  while (state.config.platforms[name]) name = `new-platform-${index++}`;
  const poolName = Object.keys(state.config.credential_pools || {})[0] || "default";
  state.config.platforms[name] = {
    type: "generic_http",
    base_url: "https://api.example.com",
    timeout_seconds: 30,
    credential_pool: poolName,
    auth: { header: "Authorization", prefix: "Bearer" },
    rate_limit: {},
  };
  markDirty();
  renderPlatforms();
}

function deletePlatform(name) {
  delete state.config.platforms[name];
  markDirty();
  renderPlatforms();
}

function addPool() {
  state.config.credential_pools ||= {};
  let name = "new-pool";
  let index = 1;
  while (state.config.credential_pools[name]) name = `new-pool-${index++}`;
  state.config.credential_pools[name] = { keys: [] };
  markDirty();
  renderKeys();
}

function deletePool(name) {
  delete state.config.credential_pools[name];
  markDirty();
  renderKeys();
}

function addKey(poolName) {
  state.config.credential_pools[poolName].keys ||= [];
  const next = state.config.credential_pools[poolName].keys.length + 1;
  state.config.credential_pools[poolName].keys.push({ label: `key-${next}`, key: "" });
  markDirty();
  renderKeys();
}

function updateKey(poolName, index, key, value) {
  state.config.credential_pools[poolName].keys[index][key] = value;
  markDirty();
}

function deleteKey(poolName, index) {
  state.config.credential_pools[poolName].keys.splice(index, 1);
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
