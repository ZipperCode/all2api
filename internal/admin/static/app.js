const state = {
  token: sessionStorage.getItem("all2api_admin_token") || "",
  view: "dashboard",
  overview: null,
  config: null,
  logs: [],
  loading: false,
  error: "",
  saving: false,
  logsLoading: false,
  logsError: "",
  dirty: false,
  selectedProvider: "",
  drawer: null,
  toast: null,
  toastTimer: null,
  logFilterTimer: null,
  visibleKeys: new Set(),
  selectedRows: { keys: new Set() },
  logFilters: { kind: "", level: "", platform: "" },
  sort: {
    platforms: { key: "name", direction: "asc" },
    keys: { key: "status", direction: "desc" },
    pools: { key: "status", direction: "desc" },
    logs: { key: "time", direction: "desc" },
  },
  pagination: {
    logs: { page: 1, pageSize: 50 },
  },
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
  state.loading = true;
  state.error = "";
  render();
  setStatus("加载中");
  try {
    const [overview, config, logs] = await Promise.all([
      api("/__gateway/admin/overview"),
      api("/__gateway/admin/config"),
      api("/__gateway/admin/logs?limit=160"),
    ]);
    state.overview = overview;
    state.config = config;
    state.logs = logs.events || [];
    state.dirty = false;
    state.logsError = "";
    setStatus("");
  } catch (error) {
    state.error = error.message;
    setStatus(error.message);
    showToast(`加载失败：${error.message}`, "error");
    throw error;
  } finally {
    state.loading = false;
    render();
  }
}

function render() {
  if (!views[state.view]) state.view = "dashboard";
  const [kicker, title] = views[state.view];
  $("#view-kicker").textContent = kicker;
  $("#view-title").textContent = title;
  syncSaveButton();
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
  renderDrawer();
  renderToast();
}

function syncSaveButton() {
  const saveButton = $("#save-config");
  saveButton.classList.toggle("hidden", state.view !== "keys");
  saveButton.disabled = !state.dirty || state.saving || !state.config;
  saveButton.textContent = state.saving ? "保存中" : state.dirty ? "保存配置" : "已保存";
  saveButton.classList.toggle("dirty", state.dirty);
}

function renderDashboard() {
  const root = $("#dashboard-view");
  if (state.loading && !state.overview) {
    root.replaceChildren(dashboardSkeleton());
    return;
  }
  if (state.error && !state.overview) {
    root.replaceChildren(errorState("控制台加载失败", state.error, () => loadAll().catch(() => {})));
    return;
  }
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
    metricTile("可用密钥", overview.active_key_count, "success"),
    metricTile("异常密钥", overview.problem_key_count, overview.problem_key_count ? "danger" : ""),
    metricTile("总密钥", overview.key_count),
    metricTile("命名空间", overview.platform_count),
    metricTile("密钥池", overview.credential_pool_count),
  );
}

function metricTile(label, value, tone = "") {
  return node("div", { class: `metric-tile${tone ? ` ${tone}` : ""}` }, node("span", {}, label), node("strong", {}, value));
}

function platformOverviewTable(platforms) {
  if (!platforms.length) return emptyState("暂无命名空间", "在 config.yaml 添加 platforms 后，这里会显示路由前缀、认证头和上游地址。");
  const rows = sortTableRows(platforms.map((platform) => ({
    id: platform.name,
    raw: platform,
    sortValues: {
      name: platform.name,
      type: platform.type,
      base_url: platform.base_url,
      credential_pool: platform.credential_pool,
      timeout: platform.timeout_seconds || 30,
      auth_header: platform.auth_header || platform.auth?.header || "x-apisports-key",
    },
    cells: [
      strongText(platform.name),
      statusPill(platform.type),
      truncate(platform.base_url),
      platform.credential_pool || "-",
      `${platform.timeout_seconds || 30}s`,
      platform.auth_header || platform.auth?.header || "x-apisports-key",
    ],
  })), "platforms");
  return table(
    [
      column("命名空间", "name"),
      column("类型", "type"),
      column("Base URL", "base_url"),
      column("密钥池", "credential_pool"),
      column("超时", "timeout"),
      column("认证头", "auth_header"),
    ],
    rows,
    "platform-table",
    {
      sortId: "platforms",
      emptyTitle: "暂无命名空间",
      onRowClick: (row) => openDrawer("platform", row.raw),
    },
  );
}

function platformEntries() {
  return Object.entries(state.config?.platforms || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, config]) => ({ name, ...config }));
}

function renderKeys() {
  const root = $("#keys-view");
  if (state.loading && !state.config) {
    root.replaceChildren(keysSkeleton());
    return;
  }
  if (state.error && !state.config) {
    root.replaceChildren(errorState("配置加载失败", state.error, () => loadAll().catch(() => {})));
    return;
  }
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
      state.selectedRows.keys.clear();
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
  const rows = sortTableRows(keys.map((key, index) => {
    const runtime = runtimeKey(poolName, key, index);
    const visible = state.visibleKeys.has(keyVisibilityId(poolName, index));
    return {
      id: keySelectionId(poolName, index),
      raw: { poolName, key, runtime, index },
      sortValues: {
        label: key.label,
        key: key.key,
        status: keyRuntimeStatus(key, runtime),
        masked: runtime?.masked_key || maskSecret(key.key),
        daily: runtime?.daily_remaining ?? -1,
        minute: runtime?.minute_remaining ?? -1,
        error: runtime?.last_error || "",
      },
      cells: [
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
      ],
    };
  }), "keys");

  return table(
    [
      column("Label", "label"),
      column("密钥值", "key", { sortable: false }),
      column("状态", "status"),
      column("Masked", "masked"),
      column("Daily", "daily"),
      column("Minute", "minute"),
      column("错误", "error"),
      column("操作", "", { sortable: false }),
    ],
    rows,
    "credential-table",
    {
      sortId: "keys",
      selectable: true,
      selectedSet: state.selectedRows.keys,
      bulkActions: [
        button("删除选中", "text danger", () => deleteSelectedKeys(poolName)),
        button("全部隐藏", "secondary", () => hideSelectedKeys(poolName)),
      ],
      onSelectionChange: renderKeys,
      onRowClick: (row) => openDrawer("key", row.raw),
    },
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

function keyRuntimeStatus(key, runtime) {
  if (!key.key) return "pending_value";
  return runtime?.status || "pending_save";
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
  const activeFilters = logFilterChips();
  root.replaceChildren(
    stack(
      section("事件日志", "按类型、级别和命名空间过滤",
        node("div", { class: "toolbar log-toolbar" },
          node("div", { class: "filters" },
            select(state.logFilters.kind, ["", "proxy", "admin"], (value) => updateLogFilter("kind", value), "类型"),
            select(state.logFilters.level, ["", "info", "warn", "error"], (value) => updateLogFilter("level", value), "级别"),
            input(state.logFilters.platform, (value) => updateLogFilter("platform", value), "table-input filter-input", "text", "命名空间", "input", { "aria-label": "命名空间过滤" }),
            activeFilters.length ? node("div", { class: "filter-chips", "aria-label": "当前过滤条件" }, activeFilters) : null,
          ),
          node("div", { class: "inline-actions" },
            button("刷新日志", "secondary", refreshLogs),
            button("清空日志", "text danger", clearLogs),
          ),
        ),
        activeFilters.length ? node("button", { class: "button text reset-filters", type: "button", onClick: resetLogFilters }, "重置过滤") : null,
        eventTable(state.logs),
      ),
    ),
  );
}

function eventTable(events) {
  if (state.logsError) return errorState("日志加载失败", state.logsError, () => refreshLogs().catch(() => {}));
  if (state.logsLoading || (state.loading && !events.length)) {
    return table(
      [column("时间"), column("类型"), column("级别"), column("命名空间"), column("消息"), column("状态码")],
      [],
      "log-table",
      { loading: true, loadingRows: 7 },
    );
  }
  if (!events.length) return emptyState("暂无日志", "触发一次网关请求后，这里会显示代理、管理操作和错误事件。", button("刷新日志", "secondary", refreshLogs));
  const rows = sortTableRows(events.map((event, index) => ({
    id: event.id || `${event.time || "event"}:${index}`,
    raw: event,
    sortValues: {
      time: event.time ? new Date(event.time).getTime() : 0,
      kind: event.kind || "",
      level: event.level || "info",
      platform: event.platform || "",
      message: event.message || event.action || "",
      status_code: event.status_code || 0,
    },
    cells: [
      formatTime(event.time),
      statusPill(event.kind || "event"),
      levelPill(event.level || "info"),
      event.platform || "-",
      node("span", { class: "event-message", title: event.message || event.action || "" }, event.message || event.action || "-"),
      event.status_code || "-",
    ],
  })), "logs");
  return table(
    [
      column("时间", "time"),
      column("类型", "kind"),
      column("级别", "level"),
      column("命名空间", "platform"),
      column("消息", "message"),
      column("状态码", "status_code"),
    ],
    rows,
    "log-table",
    {
      sortId: "logs",
      pagination: state.pagination.logs,
      onPageChange: (page) => {
        state.pagination.logs.page = page;
        renderLogs();
      },
      onRowClick: (row) => openDrawer("log", row.raw),
    },
  );
}

function poolStatusTable(pools) {
  const rows = [];
  pools.forEach((pool) => {
    (pool.keys || []).forEach((key) => {
      rows.push({
        id: `${pool.name}:${key.label}`,
        raw: { pool, key },
        sortValues: {
          pool: pool.name,
          label: key.label,
          masked: key.masked_key,
          status: key.status,
          daily: key.daily_remaining || 0,
          minute: key.minute_remaining || 0,
          error: key.last_error || "",
        },
        cells: [
          pool.name,
          key.label,
          truncate(key.masked_key),
          statusPill(key.status),
          key.daily_remaining || 0,
          key.minute_remaining || 0,
          truncate(key.last_error || "-"),
        ],
      });
    });
  });
  if (!rows.length) return emptyState("暂无密钥状态", "添加并保存密钥后，这里会显示运行态健康度和额度。");
  return table(
    [
      column("密钥池", "pool"),
      column("Label", "label"),
      column("Masked key", "masked"),
      column("状态", "status"),
      column("Daily", "daily"),
      column("Minute", "minute"),
      column("Last error", "error"),
    ],
    sortTableRows(rows, "pools"),
    "pool-table",
    {
      sortId: "pools",
      onRowClick: (row) => openDrawer("pool-key", row.raw),
    },
  );
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

function column(label, key = "", options = {}) {
  return { label, key, sortable: Boolean(key) && options.sortable !== false };
}

// DataTable 统一处理后台表格的排序、选择、分页和加载态，避免每个视图重复实现交互细节。
function table(headings, rows, className = "", options = {}) {
  const columns = headings.map((heading) => typeof heading === "string" ? column(heading) : heading);
  const normalizedRows = rows.map((row, index) => normalizeRow(row, index));
  const pageRows = paginateRows(normalizedRows, options.pagination);
  const visibleRows = options.loading ? skeletonRows(columns.length, options.loadingRows || 6) : pageRows;
  const selectable = Boolean(options.selectable);
  const selectedSet = options.selectedSet || new Set();
  const visibleIds = pageRows.map((row) => row.id);
  const allSelected = visibleIds.length > 0 && visibleIds.every((id) => selectedSet.has(id));
  const selectedCount = selectedSet.size;

  return node("div", { class: "data-table-shell" },
    selectable && selectedCount ? bulkBar(selectedCount, options.bulkActions || []) : null,
    node("div", { class: `table-wrap${options.loading ? " loading" : ""}` },
      node("table", { class: className },
        node("thead", {},
          node("tr", {},
            selectable ? node("th", { class: "select-col" },
              checkbox(allSelected, "选择当前页", (checked) => toggleVisibleRows(visibleIds, selectedSet, checked, options.onSelectionChange)),
            ) : null,
            columns.map((item) => node("th", {}, sortHeader(item, options.sortId))),
          ),
        ),
        node("tbody", {},
          visibleRows.map((row) => tableRow(row, columns, {
            selectable,
            selectedSet,
            onSelectionChange: options.onSelectionChange,
            onRowClick: options.onRowClick,
          })),
        ),
      ),
    ),
    !options.loading && !normalizedRows.length ? emptyState(options.emptyTitle || "暂无数据", options.emptyMessage || "当前视图没有可显示的数据。") : null,
    options.pagination && normalizedRows.length ? paginationControls(normalizedRows.length, options.pagination, options.onPageChange) : null,
  );
}

function normalizeRow(row, index) {
  if (Array.isArray(row)) return { id: String(index), cells: row, sortValues: {}, raw: row };
  return { id: String(row.id || index), cells: row.cells || [], sortValues: row.sortValues || {}, raw: row.raw ?? row };
}

function skeletonRows(columnCount, count) {
  return Array.from({ length: count }, (_, index) => ({
    id: `skeleton-${index}`,
    cells: Array.from({ length: columnCount }, () => node("span", { class: "skeleton-line" })),
    sortValues: {},
  }));
}

function tableRow(row, columns, options) {
  const selected = options.selectedSet?.has(row.id);
  const clickProps = options.onRowClick && !row.id.startsWith("skeleton-") ? {
    tabIndex: "0",
    onClick: (event) => {
      if (event.target.closest("button,input,select,a,label")) return;
      options.onRowClick(row);
    },
    onKeydown: (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      options.onRowClick(row);
    },
  } : {};

  return node("tr", { class: `${options.onRowClick ? "clickable" : ""}${selected ? " selected" : ""}`, ...clickProps },
    options.selectable ? node("td", { class: "select-col" },
      checkbox(selected, "选择行", (checked) => {
        if (checked) options.selectedSet.add(row.id);
        else options.selectedSet.delete(row.id);
        options.onSelectionChange?.();
      }),
    ) : null,
    columns.map((_, index) => cell(row.cells[index] ?? "")),
  );
}

function sortHeader(item, sortId) {
  if (!item.sortable || !sortId) return item.label;
  const current = state.sort[sortId] || {};
  const active = current.key === item.key;
  const direction = active ? current.direction : "none";
  return node("button", {
    class: `table-sort${active ? " active" : ""}`,
    type: "button",
    "aria-sort": active ? (direction === "asc" ? "ascending" : "descending") : "none",
    onClick: () => {
      toggleSort(sortId, item.key);
      render();
    },
  }, item.label, node("span", { "aria-hidden": "true" }, active ? (direction === "asc" ? "↑" : "↓") : "↕"));
}

function toggleSort(sortId, key) {
  const current = state.sort[sortId] || {};
  state.sort[sortId] = {
    key,
    direction: current.key === key && current.direction === "asc" ? "desc" : "asc",
  };
}

function sortTableRows(rows, sortId) {
  const current = state.sort[sortId];
  if (!current?.key) return rows;
  const direction = current.direction === "desc" ? -1 : 1;
  return [...rows].sort((a, b) => compareSortValues(a.sortValues?.[current.key], b.sortValues?.[current.key]) * direction);
}

function compareSortValues(a, b) {
  const left = a === null || a === undefined ? "" : a;
  const right = b === null || b === undefined ? "" : b;
  if (typeof left === "number" && typeof right === "number") return left - right;
  return String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: "base" });
}

function paginateRows(rows, pagination) {
  if (!pagination) return rows;
  const totalPages = Math.max(1, Math.ceil(rows.length / pagination.pageSize));
  pagination.page = Math.min(Math.max(1, pagination.page), totalPages);
  const start = (pagination.page - 1) * pagination.pageSize;
  return rows.slice(start, start + pagination.pageSize);
}

function paginationControls(total, pagination, onPageChange) {
  const totalPages = Math.max(1, Math.ceil(total / pagination.pageSize));
  const start = total ? (pagination.page - 1) * pagination.pageSize + 1 : 0;
  const end = Math.min(total, pagination.page * pagination.pageSize);
  return node("div", { class: "table-pagination" },
    node("span", {}, `${start}-${end} / ${total}`),
    node("div", { class: "inline-actions" },
      node("button", { class: "button secondary", type: "button", disabled: pagination.page <= 1 ? "disabled" : null, onClick: () => onPageChange?.(pagination.page - 1) }, "上一页"),
      node("button", { class: "button secondary", type: "button", disabled: pagination.page >= totalPages ? "disabled" : null, onClick: () => onPageChange?.(pagination.page + 1) }, "下一页"),
    ),
  );
}

function bulkBar(count, actions) {
  return node("div", { class: "bulk-bar" },
    node("strong", {}, `已选择 ${count} 项`),
    node("div", { class: "inline-actions" }, actions),
  );
}

function checkbox(checked, label, onChange) {
  return node("label", { class: "table-checkbox" },
    node("input", { type: "checkbox", checked: checked ? "checked" : null, "aria-label": label, onChange: (event) => onChange(event.target.checked) }),
    node("span", { class: "sr-only" }, label),
  );
}

function toggleVisibleRows(ids, selectedSet, checked, onSelectionChange) {
  ids.forEach((id) => {
    if (checked) selectedSet.add(id);
    else selectedSet.delete(id);
  });
  onSelectionChange?.();
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
  return node("button", { class: `button ${variant}`, type: "button", onClick: () => handleAction(onClick) }, label);
}

function iconButton(label, title, onClick, icon) {
  return node("button", { class: "icon-button", type: "button", title, "aria-label": label, onClick: () => handleAction(onClick) },
    node("span", { class: `icon ${icon}`, "aria-hidden": "true" }),
  );
}

function handleAction(action) {
  const result = action?.();
  if (result?.catch) result.catch((error) => setStatus(error.message));
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
  return emptyState("暂无数据", "当前视图没有可显示的数据。");
}

function emptyState(title, message, action = null) {
  return node("div", { class: "empty-state" },
    node("div", { class: "state-icon", "aria-hidden": "true" }),
    node("strong", {}, title),
    message ? node("p", {}, message) : null,
    action ? node("div", { class: "empty-actions" }, action) : null,
  );
}

function errorState(title, message, retry) {
  return node("div", { class: "empty-state error-state" },
    node("div", { class: "state-icon", "aria-hidden": "true" }),
    node("strong", {}, title),
    node("p", {}, message || "请求失败，请稍后重试。"),
    retry ? node("div", { class: "empty-actions" }, button("重试", "secondary", retry)) : null,
  );
}

function dashboardSkeleton() {
  return stack(
    node("div", { class: "overview-grid" },
      node("section", { class: "card" }, node("div", { class: "metric-grid" },
        Array.from({ length: 5 }, () => node("div", { class: "metric-tile skeleton-tile" }, node("span", { class: "skeleton-line" }), node("strong", { class: "skeleton-line" }))),
      )),
      node("section", { class: "card dark" }, node("div", { class: "runtime-list" },
        Array.from({ length: 3 }, () => node("div", { class: "runtime-row" }, node("span", { class: "skeleton-line" }), node("strong", { class: "skeleton-line" }))),
      )),
    ),
    section("命名空间状态", "当前站点前缀与上游配置", table([column("命名空间"), column("类型"), column("Base URL"), column("密钥池"), column("超时"), column("认证头")], [], "platform-table", { loading: true })),
    section("密钥状态", "运行态密钥健康度", table([column("密钥池"), column("Label"), column("Masked key"), column("状态"), column("Daily"), column("Minute"), column("Last error")], [], "pool-table", { loading: true })),
  );
}

function keysSkeleton() {
  return stack(
    section("提供商密钥", "选择当前配置里的提供商，再维护它绑定的上游密钥",
      node("div", { class: "provider-layout" },
        node("aside", { class: "provider-rail skeleton-panel" }, Array.from({ length: 4 }, () => node("div", { class: "provider-item" }, node("span", { class: "skeleton-line" }), node("span", { class: "skeleton-line" })))),
        node("article", { class: "credential-panel" },
          node("div", { class: "credential-hero" }, node("div", {}, node("span", { class: "skeleton-line" }), node("span", { class: "skeleton-line" }))),
          node("div", { class: "key-management-body" }, table([column("Label"), column("密钥值"), column("状态"), column("Masked"), column("Daily"), column("Minute"), column("错误"), column("操作")], [], "credential-table", { loading: true })),
        ),
      ),
    ),
  );
}

function formatTime(value) {
  if (!value) return "-";
  return new Date(value).toLocaleString();
}

function logFilterChips() {
  return Object.entries(state.logFilters)
    .filter(([, value]) => value)
    .map(([key, value]) => node("button", { class: "filter-chip", type: "button", onClick: () => updateLogFilter(key, "") }, `${logFilterLabel(key)}: ${value}`));
}

function logFilterLabel(key) {
  if (key === "kind") return "类型";
  if (key === "level") return "级别";
  return "命名空间";
}

function updateLogFilter(key, value) {
  state.logFilters[key] = value;
  state.pagination.logs.page = 1;
  if (key !== "platform") renderLogs();
  queueRefreshLogs();
}

function queueRefreshLogs() {
  clearTimeout(state.logFilterTimer);
  state.logFilterTimer = setTimeout(() => {
    refreshLogs().catch(() => {});
  }, 250);
}

function resetLogFilters() {
  state.logFilters = { kind: "", level: "", platform: "" };
  state.pagination.logs.page = 1;
  refreshLogs().catch(() => {});
}

function openDrawer(type, data) {
  state.drawer = { type, data };
  renderDrawer();
}

function closeDrawer() {
  state.drawer = null;
  renderDrawer();
}

function renderDrawer() {
  const root = $("#drawer-root");
  if (!root) return;
  if (!state.drawer) {
    root.replaceChildren();
    root.classList.remove("open");
    return;
  }
  root.classList.add("open");
  const content = drawerContent(state.drawer);
  root.replaceChildren(
    node("div", { class: "drawer-backdrop", onClick: closeDrawer }),
    node("aside", { class: "detail-drawer", role: "dialog", "aria-modal": "true", "aria-labelledby": "drawer-title" },
      node("header", { class: "drawer-header" },
        node("div", {}, node("p", { class: "eyebrow" }, content.kicker), node("h3", { id: "drawer-title" }, content.title), content.subtitle ? node("p", {}, content.subtitle) : null),
        iconButton("关闭详情", "Close detail", closeDrawer, "close"),
      ),
      node("div", { class: "drawer-body" }, content.body),
      content.actions ? node("footer", { class: "drawer-actions" }, content.actions) : null,
    ),
  );
}

function drawerContent(drawer) {
  if (drawer.type === "log") return logDrawer(drawer.data);
  if (drawer.type === "platform") return platformDrawer(drawer.data);
  if (drawer.type === "key") return keyDrawer(drawer.data);
  if (drawer.type === "pool-key") return poolKeyDrawer(drawer.data);
  return { kicker: "Detail", title: "详情", body: emptyState("暂无详情", "当前行没有可展示的详情。") };
}

function logDrawer(event) {
  return {
    kicker: "Event",
    title: event.message || event.action || "日志详情",
    subtitle: formatTime(event.time),
    body: node("div", { class: "drawer-stack" },
      kvGrid([
        ["类型", event.kind || "-"],
        ["级别", event.level || "-"],
        ["命名空间", event.platform || "-"],
        ["状态码", event.status_code || "-"],
        ["耗时", event.duration_ms ? `${event.duration_ms}ms` : "-"],
        ["尝试次数", event.attempts || "-"],
        ["客户端", event.remote_addr || "-"],
        ["错误", event.error || "-"],
      ]),
      node("div", { class: "drawer-section" },
        node("h4", {}, "原始事件"),
        node("pre", {}, JSON.stringify(event, null, 2)),
      ),
    ),
  };
}

function platformDrawer(platform) {
  return {
    kicker: "Namespace",
    title: `/${platform.name}`,
    subtitle: platform.base_url || "",
    body: node("div", { class: "drawer-stack" },
      kvGrid([
        ["类型", platform.type || "-"],
        ["密钥池", platform.credential_pool || "-"],
        ["超时", `${platform.timeout_seconds || 30}s`],
        ["认证头", platform.auth_header || platform.auth?.header || "x-apisports-key"],
        ["认证前缀", platform.auth_prefix || platform.auth?.prefix || "-"],
      ]),
    ),
  };
}

function keyDrawer(detail) {
  return {
    kicker: "Credential",
    title: detail.key.label || `key-${detail.index + 1}`,
    subtitle: detail.poolName,
    body: node("div", { class: "drawer-stack" },
      kvGrid([
        ["状态", keyRuntimeStatus(detail.key, detail.runtime)],
        ["Masked key", detail.runtime?.masked_key || maskSecret(detail.key.key)],
        ["Daily", detail.runtime?.daily_remaining ?? "-"],
        ["Minute", detail.runtime?.minute_remaining ?? "-"],
        ["Last error", detail.runtime?.last_error || "-"],
      ]),
      node("div", { class: "drawer-section danger-zone" },
        node("h4", {}, "危险操作"),
        node("p", {}, "删除后需要保存配置才会热重载。"),
      ),
    ),
    actions: node("div", { class: "inline-actions" }, button("删除密钥", "text danger", () => deleteKey(detail.poolName, detail.index))),
  };
}

function poolKeyDrawer(detail) {
  return {
    kicker: "Runtime key",
    title: detail.key.label || detail.pool.name,
    subtitle: detail.pool.name,
    body: kvGrid([
      ["状态", detail.key.status || "-"],
      ["Masked key", detail.key.masked_key || "-"],
      ["Daily", detail.key.daily_remaining ?? "-"],
      ["Minute", detail.key.minute_remaining ?? "-"],
      ["Cooldown until", detail.key.cooldown_until || "-"],
      ["Last error", detail.key.last_error || "-"],
    ]),
  };
}

function kvGrid(items) {
  return node("div", { class: "kv-grid" }, items.map(([label, value]) => kv(label, value)));
}

function showToast(message, tone = "info") {
  clearTimeout(state.toastTimer);
  state.toast = { message, tone };
  renderToast();
  state.toastTimer = setTimeout(() => {
    state.toast = null;
    renderToast();
  }, 2600);
}

function renderToast() {
  const root = $("#toast-root");
  if (!root) return;
  if (!state.toast) {
    root.replaceChildren();
    return;
  }
  root.replaceChildren(node("div", { class: `toast ${state.toast.tone}` }, state.toast.message));
}

function markDirty() {
  state.dirty = true;
  setStatus("未保存");
  syncSaveButton();
}

function keyVisibilityId(poolName, index) {
  return `${poolName}:${index}`;
}

function keySelectionId(poolName, index) {
  return `key:${poolName}:${index}`;
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
  showToast("已新增密钥，保存后生效", "success");
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
  if (!window.confirm("确认删除这条密钥？保存配置后会立即热重载。")) return;
  state.config.credential_pools[poolName].keys.splice(index, 1);
  state.visibleKeys.delete(keyVisibilityId(poolName, index));
  state.selectedRows.keys.clear();
  closeDrawer();
  markDirty();
  showToast("密钥已删除，保存后生效", "success");
  renderKeys();
}

function deleteSelectedKeys(poolName) {
  const indexes = selectedKeyIndexes(poolName);
  if (!indexes.length) return;
  if (!window.confirm(`确认删除选中的 ${indexes.length} 条密钥？保存配置后会立即热重载。`)) return;
  const pool = ensureConfigPool(poolName);
  indexes.sort((a, b) => b - a).forEach((index) => {
    pool.keys.splice(index, 1);
    state.visibleKeys.delete(keyVisibilityId(poolName, index));
  });
  state.selectedRows.keys.clear();
  markDirty();
  showToast(`已删除 ${indexes.length} 条密钥，保存后生效`, "success");
  renderKeys();
}

function hideSelectedKeys(poolName) {
  selectedKeyIndexes(poolName).forEach((index) => state.visibleKeys.delete(keyVisibilityId(poolName, index)));
  state.selectedRows.keys.clear();
  renderKeys();
}

function selectedKeyIndexes(poolName) {
  return [...state.selectedRows.keys]
    .map((id) => id.match(new RegExp(`^key:${escapeRegExp(poolName)}:(\\d+)$`)))
    .filter(Boolean)
    .map((match) => Number(match[1]));
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
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
  state.saving = true;
  syncSaveButton();
  setStatus("保存中");
  try {
    await api("/__gateway/admin/config", { method: "PUT", body: JSON.stringify(state.config) });
    state.dirty = false;
    showToast("配置已保存并热重载", "success");
    await loadAll();
    setStatus("已保存");
    setTimeout(() => setStatus(""), 1200);
  } catch (error) {
    setStatus(error.message);
    showToast(`保存失败：${error.message}`, "error");
    throw error;
  } finally {
    state.saving = false;
    syncSaveButton();
  }
}

async function refreshLogs() {
  state.logsLoading = true;
  state.logsError = "";
  renderLogs();
  const query = new URLSearchParams({ limit: "160" });
  if (state.logFilters.kind) query.set("kind", state.logFilters.kind);
  if (state.logFilters.level) query.set("level", state.logFilters.level);
  if (state.logFilters.platform) query.set("platform", state.logFilters.platform);
  try {
    const response = await api(`/__gateway/admin/logs?${query.toString()}`);
    state.logs = response.events || [];
    state.logsError = "";
  } catch (error) {
    state.logsError = error.message;
    showToast(`日志加载失败：${error.message}`, "error");
    throw error;
  } finally {
    state.logsLoading = false;
    renderLogs();
  }
}

async function clearLogs() {
  if (!window.confirm("确认清空全部日志？")) return;
  setStatus("清空中");
  try {
    await api("/__gateway/admin/logs", { method: "DELETE" });
    await refreshLogs();
    setStatus("已清空");
    showToast("日志已清空", "success");
    setTimeout(() => setStatus(""), 1200);
  } catch (error) {
    setStatus(error.message);
    showToast(`清空失败：${error.message}`, "error");
    throw error;
  }
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

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && state.drawer) closeDrawer();
});

window.addEventListener("beforeunload", (event) => {
  if (!state.dirty) return;
  event.preventDefault();
  event.returnValue = "";
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
