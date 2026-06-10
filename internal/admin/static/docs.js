const root = document.querySelector("#docs-root");

const node = (tag, props = {}, ...children) => {
  const element = document.createElement(tag);
  Object.entries(props).forEach(([key, value]) => {
    if (key === "class") element.className = value;
    else if (key === "open") {
      if (value) element.setAttribute("open", "");
    }
    else if (value !== undefined && value !== null) element.setAttribute(key, value);
  });
  children.flat().forEach((child) => {
    if (child === null || child === undefined) return;
    element.append(child instanceof Node ? child : document.createTextNode(String(child)));
  });
  return element;
};

const text = (value, fallback = "-") => value === null || value === undefined || value === "" ? fallback : String(value);

async function loadDocs() {
  const response = await fetch("/__gateway/docs");
  const docs = await response.json();
  if (!response.ok) throw new Error(docs.message || docs.error || `HTTP ${response.status}`);
  renderDocs(docs);
}

function renderDocs(docs) {
  root.replaceChildren(
    section("站点", "路径格式为 /{namespace}/{official-path}。认证方式、查询参数和响应结构保持官方 API 语义。",
      node("div", { class: "doc-stack" }, (docs.platforms || []).map(platformDoc)),
    ),
  );
}

function platformDoc(doc) {
  return node("article", { class: "doc-card doc-card-wide" },
    node("div", { class: "doc-card-header" },
      node("div", {},
        node("h3", {}, doc.title || doc.name),
        node("p", { class: "muted" }, `${doc.type} · ${doc.gateway_base_path}`),
      ),
      node("span", { class: "pill active" }, doc.auth_header),
    ),
    node("div", { class: "kv-grid docs-meta" },
      kv("Gateway Base", text(doc.gateway_base_url)),
      kv("Official Base", text(doc.upstream_base_url)),
      kv("Auth", `${doc.auth_header}: ${doc.auth_value}`),
      kv("Credential Pool", text(doc.credential_pool)),
    ),
    node("div", { class: "endpoint-list" }, (doc.endpoints || []).map((endpoint, index) => endpointDoc(doc, endpoint, index === 0))),
  );
}

function endpointDoc(platform, endpoint, initiallyOpen) {
  return node("details", { class: "endpoint-doc", open: initiallyOpen },
    node("summary", { class: "endpoint-summary" },
      node("span", { class: "method-badge" }, endpoint.method),
      node("span", { class: "endpoint-path" }, endpoint.path),
      node("span", { class: "endpoint-title" }, endpoint.summary || endpoint.description),
    ),
    node("div", { class: "endpoint-body" },
      node("p", { class: "endpoint-description" }, endpoint.description),
      node("div", { class: "endpoint-tools" },
        compactKv("Request URL", exampleURL(endpoint)),
        compactKv("Curl", exampleCurl(platform, endpoint)),
      ),
      subsection("请求参数", parametersTable(endpoint.parameters || [])),
      subsection("返回结构", fieldsTable(endpoint.response_fields || [])),
      endpoint.response_sample ? subsection("响应示例", codeBlock(endpoint.response_sample)) : null,
    ),
  );
}

function parametersTable(parameters) {
  if (!parameters.length) return node("div", { class: "empty-inline" }, "无请求参数");
  return node("div", { class: "table-wrap doc-table-wrap" },
    node("table", { class: "docs-table params-table" },
      node("thead", {}, node("tr", {}, ["Name", "In", "Required", "Type", "Example", "Description"].map((heading) => node("th", {}, heading)))),
      node("tbody", {}, parameters.map((param) => node("tr", {},
        node("td", {}, node("code", {}, param.name)),
        node("td", {}, param.in),
        node("td", {}, requiredPill(param.required)),
        node("td", {}, node("code", {}, param.type)),
        node("td", {}, param.example ? node("code", {}, param.example) : "-"),
        node("td", {}, param.description),
      ))),
    ),
  );
}

function fieldsTable(fields) {
  if (!fields.length) return node("div", { class: "empty-inline" }, "按上游原始响应返回");
  return node("div", { class: "table-wrap doc-table-wrap" },
    node("table", { class: "docs-table fields-table" },
      node("thead", {}, node("tr", {}, ["Field", "Type", "Description"].map((heading) => node("th", {}, heading)))),
      node("tbody", {}, fields.map((field) => node("tr", {},
        node("td", {}, node("code", {}, field.name)),
        node("td", {}, node("code", {}, field.type)),
        node("td", {}, field.description),
      ))),
    ),
  );
}

function subsection(title, child) {
  return node("div", { class: "doc-subsection" },
    node("h4", {}, title),
    child,
  );
}

function requiredPill(required) {
  return node("span", { class: required ? "required-pill required" : "required-pill optional" }, required ? "required" : "optional");
}

function codeBlock(value) {
  return node("pre", { class: "code-sample" }, typeof value === "string" ? value : JSON.stringify(value, null, 2));
}

function exampleURL(endpoint) {
  const params = (endpoint.parameters || []).filter((param) => param.in === "query" && param.example);
  if (!params.length) return `http://<gateway>${endpoint.path}`;
  const query = params.slice(0, 3).map((param) => `${encodeURIComponent(param.name)}=${encodeURIComponent(param.example)}`).join("&");
  return `http://<gateway>${endpoint.path}?${query}`;
}

function exampleCurl(platform, endpoint) {
  return `curl -H "${platform.auth_header}: ${platform.auth_value}" "${exampleURL(endpoint)}"`;
}

function section(title, subtitle, ...children) {
  return node("section", { class: "section" },
    node("div", { class: "section-header" },
      node("div", {}, node("h3", {}, title), subtitle ? node("p", {}, subtitle) : null),
    ),
    children,
  );
}

function kv(label, value) {
  return node("div", { class: "kv" }, node("span", {}, label), node("strong", { title: text(value) }, text(value)));
}

function compactKv(label, value) {
  return node("div", { class: "compact-kv" }, node("span", {}, label), node("code", {}, value));
}

loadDocs().catch((error) => {
  root.replaceChildren(node("div", { class: "empty-state" }, error.message));
});
