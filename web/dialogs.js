"use strict";

// 对话框：编辑代理配置、检测本机代理、添加自动切换规则。

const kindLabels = { http: "HTTP", socks: "SOCKS5", pac: "PAC 脚本", subscription: "订阅", custom: "按协议指定" };

const kindHints = {
  http: "最常见的类型。代理软件的设置里一般写着「HTTP 端口」或「混合端口」。",
  socks: "SOCKS5 端口。部分程序不支持 SOCKS5，如果代理软件有 HTTP 或混合端口，优先用那个。",
  pac: "由自动配置脚本决定哪些网站走代理，常见于公司和学校网络。",
  subscription: "填机场提供的订阅地址，ProxySwitch 用内置的代理内核连接订阅里的节点，不需要另外安装代理软件。",
  custom: "给不同协议分别指定代理，写法：http=主机:端口;https=主机:端口;socks=主机:端口",
};

const editorKinds = ["http", "socks", "pac", "subscription", "custom"];

const modeOptions = [["rule", "大陆直连"], ["global", "全部走代理"]];

const modeHints = {
  rule: "国内的网站直接访问，其余经过节点。推荐",
  global: "所有网站都经过节点",
};

// splitServer 把 “协议://主机:端口” 拆开，支持 [IPv6]:端口。
function splitServer(server) {
  let rest = String(server || "").trim();
  let scheme = "";
  const match = /^([a-z][a-z0-9+.-]*):\/\//i.exec(rest);
  if (match) {
    scheme = match[1].toLowerCase();
    rest = rest.slice(match[0].length);
  }
  rest = rest.replace(/\/+$/, "");
  const ipv6 = /^\[([^\]]+)\](?::(\d*))?$/.exec(rest);
  if (ipv6) {
    return { scheme, host: ipv6[1], port: ipv6[2] || "" };
  }
  const index = rest.lastIndexOf(":");
  if (index > 0 && rest.indexOf(":") === index) {
    return { scheme, host: rest.slice(0, index), port: rest.slice(index + 1) };
  }
  return { scheme, host: rest, port: "" };
}

function joinHostPort(host, port) {
  let cleanHost = String(host).trim();
  if (cleanHost.includes(":") && !cleanHost.startsWith("[")) {
    cleanHost = `[${cleanHost}]`;
  }
  return `${cleanHost}:${String(port).trim()}`;
}

function profileKind(profile) {
  if (profile.subscription) {
    return "subscription";
  }
  if (profile.pac) {
    return "pac";
  }
  const server = profile.server || "";
  if (server.includes("=")) {
    return "custom";
  }
  const scheme = splitServer(server).scheme;
  return scheme === "socks5" || scheme === "socks" || scheme === "socks5h" ? "socks" : "http";
}

// describeServer 是列表里显示的地址说明。
function describeServer(profile) {
  const kind = profileKind(profile);
  if (kind === "subscription") {
    return `订阅 · ${profile.node || "自动选择"}`;
  }
  if (kind === "pac") {
    return profile.server ? `PAC · ${profile.pac} · ${profile.server}` : `PAC · ${profile.pac}`;
  }
  if (kind === "custom") {
    return profile.server;
  }
  const parts = splitServer(profile.server);
  const address = parts.port ? joinHostPort(parts.host, parts.port) : parts.host;
  return `${kindLabels[kind]} · ${address}`;
}

function nextColor() {
  const used = new Set((app.config ? app.config.profiles : []).map((profile) => profile.color));
  const palette = app.state.palette;
  return palette.find((color) => !used.has(color)) || palette[(app.config ? app.config.profiles.length : 0) % palette.length];
}

function bypassToLines(bypass) {
  return String(bypass || "").split(";").map((entry) => entry.trim()).filter(Boolean).join("\n");
}

function linesToBypass(text) {
  return String(text || "").split(/[\n;]/).map((entry) => entry.trim()).filter(Boolean).join(";");
}

function draftFromProfile(profile) {
  const defaults = app.state.defaults;
  const draft = {
    id: profile.id || "",
    name: profile.name || "",
    color: profile.color || nextColor(),
    kind: "http",
    host: "",
    port: "",
    raw: "",
    pac: profile.pac || "",
    pacServer: "",
    applyTo: [...(profile.apply_to && profile.apply_to.length ? profile.apply_to : ["system"])],
    bypassText: bypassToLines(profile.bypass || defaults.bypass),
    noProxy: profile.no_proxy || defaults.no_proxy,
    touched: {},
    submitted: false,
    saving: false,
    serverError: "",
    test: null,
    preview: [],
    advancedOpen: false,
    useAfterSave: false,
    subscription: profile.subscription || "",
    mode: profile.mode || "rule",
    node: profile.node || "",
    savingText: "",
  };
  const server = (profile.server || "").trim();
  if (draft.subscription) {
    draft.kind = "subscription";
  } else if (draft.pac) {
    draft.kind = "pac";
    draft.pacServer = server;
  } else if (server.includes("=")) {
    draft.kind = "custom";
    draft.raw = server;
  } else if (server) {
    const parts = splitServer(server);
    if (parts.scheme === "" || parts.scheme === "http") {
      draft.kind = "http";
    } else if (parts.scheme === "socks5" || parts.scheme === "socks") {
      draft.kind = "socks";
    } else {
      draft.kind = "custom";
      draft.raw = server;
    }
    draft.host = parts.host;
    draft.port = parts.port;
  }
  const bypassChanged = linesToBypass(draft.bypassText) !== defaults.bypass;
  const noProxyChanged = draft.noProxy !== defaults.no_proxy;
  draft.advancedOpen = bypassChanged || noProxyChanged;
  return draft;
}

function profileFromDraft(draft) {
  let server = "";
  let pac = "";
  switch (draft.kind) {
    case "http":
      server = draft.host.trim() ? joinHostPort(draft.host, draft.port) : "";
      break;
    case "socks":
      server = draft.host.trim() ? "socks5://" + joinHostPort(draft.host, draft.port) : "";
      break;
    case "custom":
      server = draft.raw.trim().replace(/\s+/g, "");
      break;
    case "pac":
      pac = draft.pac.trim();
      server = draft.pacServer.trim();
      break;
    case "subscription":
      // 订阅由内核代理，地址是内核的本地端口（保存时程序也会这样填），这里填上是为了预览将要做的修改。
      server = `127.0.0.1:${app.config ? app.config.core.port : 17890}`;
      break;
  }
  const profile = {
    id: draft.id,
    name: draft.name.trim(),
    color: draft.color,
    server,
    pac,
    bypass: linesToBypass(draft.bypassText),
    no_proxy: draft.noProxy.trim(),
    apply_to: draft.applyTo,
  };
  if (draft.kind === "subscription") {
    profile.subscription = draft.subscription.trim();
    profile.mode = draft.mode;
    profile.node = draft.node;
  }
  return profile;
}

const hostPattern = /^[^\s/?#@]+$/;

function validatePort(port) {
  const text = String(port).trim();
  if (!text) {
    return "请填写端口";
  }
  if (!/^\d+$/.test(text) || Number(text) < 1 || Number(text) > 65535) {
    return "端口需要是 1~65535 之间的数字";
  }
  return "";
}

function validateHostPortText(text) {
  const parts = splitServer(text);
  if (parts.scheme && !["http", "https", "socks", "socks5", "socks5h"].includes(parts.scheme)) {
    return `不支持 ${parts.scheme}:// 开头的地址`;
  }
  if (!parts.host || !hostPattern.test(parts.host)) {
    return "应写成 主机:端口，例如 127.0.0.1:7890";
  }
  return validatePort(parts.port);
}

function validateDraft(draft) {
  const errors = {};
  const name = draft.name.trim();
  if (!name) {
    errors.name = "请给这个配置起个名字";
  } else if ([...name].length > 40) {
    errors.name = "名字最多 40 个字";
  } else if ((app.config ? app.config.profiles : []).some((profile) => profile.id !== draft.id && profile.name.toLowerCase() === name.toLowerCase())) {
    errors.name = "已经有同名的配置了";
  }
  let hasServer = true;
  switch (draft.kind) {
    case "http":
    case "socks":
      if (!draft.host.trim()) {
        errors.host = "请填写代理服务器的地址";
      } else if (!hostPattern.test(draft.host.trim())) {
        errors.host = "地址里不能有空格或 / ? # @";
      }
      if (validatePort(draft.port)) {
        errors.port = validatePort(draft.port);
      }
      break;
    case "custom": {
      const entries = draft.raw.split(";").map((entry) => entry.trim()).filter(Boolean);
      if (entries.length === 0) {
        errors.raw = "请填写，例如 http=127.0.0.1:7890;https=127.0.0.1:7890";
      }
      for (const entry of entries) {
        const [protocol, value] = entry.split("=");
        if (value === undefined || !["http", "https", "ftp", "socks"].includes(protocol.trim().toLowerCase())) {
          errors.raw = `「${entry}」应写成 协议=主机:端口，协议可以是 http / https / ftp / socks`;
          break;
        }
        const problem = validateHostPortText(value);
        if (problem) {
          errors.raw = `「${entry}」${problem}`;
          break;
        }
      }
      break;
    }
    case "subscription":
      if (!draft.subscription.trim()) {
        errors.subscription = "请填写订阅地址";
      } else if (!/^https?:\/\/\S+$/i.test(draft.subscription.trim())) {
        errors.subscription = "订阅地址应以 http:// 或 https:// 开头";
      }
      break;
    case "pac":
      if (!draft.pac.trim()) {
        errors.pac = "请填写 PAC 脚本的地址";
      } else if (!/^(https?|file):\/\/\S+$/i.test(draft.pac.trim())) {
        errors.pac = "PAC 地址应以 http:// 或 https:// 开头";
      }
      hasServer = Boolean(draft.pacServer.trim());
      if (hasServer && validateHostPortText(draft.pacServer)) {
        errors.pacServer = validateHostPortText(draft.pacServer);
      }
      break;
  }
  if (draft.applyTo.length === 0) {
    errors.targets = "至少选择一项";
  } else if (!hasServer && draft.applyTo.some((target) => target !== "system")) {
    errors.targets = "环境变量、git、npm 不能使用 PAC，需要在上面填写代理服务器地址";
  } else if (draft.kind === "socks" && draft.applyTo.includes("npm")) {
    errors.targets = "npm 不支持 SOCKS5 代理，请取消勾选 npm";
  }
  return errors;
}

// ---------- 编辑代理配置 ----------

function openProfileEditor(profile = {}, options = {}) {
  const draft = draftFromProfile(profile);
  if (options.kind) {
    draft.kind = options.kind;
  }
  const editing = Boolean(profile.id);
  draft.useAfterSave = !editing && app.state.status.state !== "on";

  const refreshPreview = debounce(async () => {
    try {
      const result = await api("POST", "/api/preview", profileFromDraft(draft));
      draft.preview = result.lines || [];
    } catch (error) {
      draft.preview = [];
    }
    const container = dialog.element.querySelector("[data-preview]");
    if (container) {
      setHtml(container, renderPreview(draft));
    }
  }, 250);

  function errorsToShow() {
    const errors = validateDraft(draft);
    if (draft.submitted) {
      return errors;
    }
    const shown = {};
    for (const key of Object.keys(errors)) {
      if (draft.touched[key]) {
        shown[key] = errors[key];
      }
    }
    return shown;
  }

  // 输入时只更新错误提示和预览，不重建输入框，避免打断中文输入法。
  function updateFeedback() {
    const errors = errorsToShow();
    for (const element of dialog.element.querySelectorAll("[data-error]")) {
      element.textContent = errors[element.dataset.error] || "";
    }
    for (const element of dialog.element.querySelectorAll("[data-field]")) {
      element.classList.toggle("invalid", Boolean(errors[element.dataset.field]));
    }
    refreshPreview();
  }

  const dialog = openDialog({
    className: "",
    render: () => renderProfileEditor(draft, editing, errorsToShow()),
    onMount: (dialogInstance) => {
      const element = dialogInstance.element;
      element.addEventListener("input", (event) => {
        const field = event.target.dataset.field;
        if (!field || event.target.type === "checkbox") {
          return;
        }
        draft[field] = event.target.value;
        // 只在输入时标记为“已填写”：失去焦点时显示错误会让下面的内容下移，导致点击落空。
        draft.touched[field] = true;
        if (field === "host") {
          absorbPastedAddress(event.target);
        }
        draft.test = null;
        for (const result of element.querySelectorAll("[data-test-result], [data-check-result]")) {
          result.textContent = "";
        }
        updateFeedback();
      });
      element.addEventListener("change", (event) => {
        const target = event.target;
        if (target.dataset.target) {
          const set = new Set(draft.applyTo);
          if (target.checked) {
            set.add(target.dataset.target);
          } else {
            set.delete(target.dataset.target);
          }
          draft.applyTo = ["system", "env", "git", "npm"].filter((item) => set.has(item));
          draft.touched.targets = true;
          dialogInstance.render();
          refreshPreview();
        } else if (target.dataset.field === "useAfterSave") {
          draft.useAfterSave = target.checked;
        } else if (target.dataset.customColor !== undefined) {
          draft.color = target.value.toLowerCase();
          dialogInstance.render();
        }
      });
      element.addEventListener("click", (event) => {
        const button = event.target.closest("[data-action], [data-kind], [data-color], [data-mode]");
        if (!button) {
          return;
        }
        if (button.dataset.kind) {
          switchKind(button.dataset.kind);
          dialogInstance.render();
          refreshPreview();
          const firstField = { pac: "pac", custom: "raw", subscription: "subscription" }[draft.kind] || "host";
          const first = element.querySelector(`[data-field="${firstField}"]`);
          if (first) {
            first.focus();
          }
          return;
        }
        if (button.dataset.mode) {
          draft.mode = button.dataset.mode;
          dialogInstance.render();
          return;
        }
        if (button.dataset.color) {
          draft.color = button.dataset.color;
          dialogInstance.render();
          return;
        }
        switch (button.dataset.action) {
          case "toggle-advanced":
            draft.advancedOpen = !draft.advancedOpen;
            dialogInstance.render();
            break;
          case "reset-bypass":
            draft.bypassText = bypassToLines(app.state.defaults.bypass);
            draft.noProxy = app.state.defaults.no_proxy;
            dialogInstance.render();
            refreshPreview();
            break;
          case "dialog-test":
            testDraft();
            break;
          case "install-core":
            installCore(() => {
              if (!dialog.closed) {
                dialog.render();
              }
            });
            break;
          case "dialog-save":
            saveDraft();
            break;
          case "dialog-cancel":
            dialogInstance.close(false);
            break;
        }
      });
      refreshPreview();
    },
  });

  // 在“主机”里粘贴完整地址（例如 http://127.0.0.1:7890）时自动拆成主机和端口。
  function absorbPastedAddress(input) {
    const value = input.value.trim();
    if (!/:\d+$/.test(value) && !value.includes("://")) {
      return;
    }
    const parts = splitServer(value);
    if (!parts.host || (parts.port && !/^\d+$/.test(parts.port))) {
      return;
    }
    draft.host = parts.host;
    if (parts.port) {
      draft.port = parts.port;
    }
    if (parts.scheme.startsWith("socks")) {
      draft.kind = "socks";
    } else if (parts.scheme === "http") {
      draft.kind = "http";
    }
    dialog.render();
    const hostInput = dialog.element.querySelector('[data-field="host"]');
    if (hostInput) {
      hostInput.focus();
      hostInput.setSelectionRange(hostInput.value.length, hostInput.value.length);
    }
  }

  function switchKind(kind) {
    if (kind === draft.kind) {
      return;
    }
    const current = profileFromDraft(draft);
    if (kind === "custom" && !draft.raw && current.server) {
      const address = splitServer(current.server);
      const target = joinHostPort(address.host, address.port);
      draft.raw = draft.kind === "socks" ? `socks=${target}` : `http=${target};https=${target}`;
    }
    if (kind === "pac" && !draft.pacServer && current.server && draft.kind !== "custom") {
      draft.pacServer = current.server;
    }
    if ((kind === "http" || kind === "socks") && !draft.host && draft.kind === "pac" && draft.pacServer) {
      const parts = splitServer(draft.pacServer);
      draft.host = parts.host;
      draft.port = parts.port;
    }
    draft.kind = kind;
    draft.test = null;
  }

  async function testDraft() {
    if (draft.kind === "subscription") {
      checkSubscription();
      return;
    }
    const profileValue = profileFromDraft(draft);
    const body = profileValue.server ? { server: profileValue.server } : { pac: profileValue.pac };
    if (!profileValue.server && !profileValue.pac) {
      draft.submitted = true;
      updateFeedback();
      return;
    }
    draft.test = { running: true };
    dialog.render();
    try {
      draft.test = await api("POST", "/api/test", body);
    } catch (error) {
      draft.test = { ok: false, message: error.message };
    }
    if (!dialog.closed) {
      dialog.render();
    }
  }

  // checkSubscription 下载订阅检查地址是否可用，显示节点数和流量；还没填名字时用机场给的名字。
  async function checkSubscription() {
    if (validateDraft(draft).subscription) {
      draft.submitted = true;
      updateFeedback();
      return;
    }
    draft.test = { running: true };
    dialog.render();
    try {
      const check = await api("POST", "/api/subscriptions/check", { url: draft.subscription.trim() });
      draft.test = { ok: true, check };
      if (!draft.name.trim() && check.name) {
        draft.name = check.name;
      }
    } catch (error) {
      draft.test = { ok: false, message: error.message };
    }
    if (!dialog.closed) {
      dialog.render();
    }
  }

  async function saveDraft() {
    draft.submitted = true;
    const errors = validateDraft(draft);
    if (Object.keys(errors).length > 0) {
      dialog.render();
      const firstInvalid = dialog.element.querySelector(".invalid, .field-error:not(:empty)");
      if (firstInvalid) {
        firstInvalid.scrollIntoView({ block: "center" });
      }
      return;
    }
    const profileValue = profileFromDraft(draft);
    const config = clone(app.config);
    const index = draft.id ? config.profiles.findIndex((item) => item.id === draft.id) : -1;
    if (index >= 0) {
      renameReferences(config, config.profiles[index].name, profileValue.name);
      config.profiles[index] = profileValue;
    } else {
      config.profiles.push(profileValue);
    }
    draft.saving = true;
    draft.serverError = "";
    dialog.render();
    try {
      const state = await api("PUT", "/api/config", config);
      receiveState(state, { force: true });
      // 新的订阅（或改了地址的订阅）保存后立即下载，下载完才能开启。
      const saved = app.config.profiles.find((item) => item.name === profileValue.name);
      if (saved && saved.subscription && (!editing || profile.subscription !== saved.subscription)) {
        draft.savingText = "正在下载订阅…";
        dialog.render();
        try {
          receiveState(await api("POST", `/api/subscriptions/${saved.id}/update`), { force: true });
        } catch (error) {
          if (error.state) {
            receiveState(error.state, { force: true });
          }
          dialog.close(true);
          toast(error.message, "warning", `已保存「${profileValue.name}」，但订阅没有下载成功`);
          return;
        }
      }
      dialog.close(true);
      if (draft.useAfterSave) {
        await runOperation("/api/use", { name: profileValue.name }, `已开启「${profileValue.name}」`);
      } else {
        const active = app.state.status.state === "on" && app.state.status.profile === profileValue.name;
        toast(active ? "修改已立即生效" : "可以在列表里点「使用」开启", "success", `已保存「${profileValue.name}」`);
      }
    } catch (error) {
      draft.saving = false;
      draft.serverError = error.message;
      if (!dialog.closed) {
        dialog.render();
      }
    }
  }

  return dialog;
}

function renderPreview(draft) {
  if (!draft.preview || draft.preview.length === 0) {
    return html``;
  }
  return html`<div class="preview"><strong>开启后会做这些修改</strong><ul>${draft.preview.map((line) => html`<li>${line}</li>`)}</ul></div>`;
}

// routeText 描述 PAC 为测速地址选择的去向。
function routeText(result) {
  if (!result || !result.route) {
    return "";
  }
  return result.route === "DIRECT" ? "PAC 选择直连" : `PAC 选择 ${result.route}`;
}

function renderTestResult(test) {
  if (!test) {
    return html``;
  }
  if (test.running) {
    return html`<span class="spinner"></span><span class="muted">正在测试…</span>`;
  }
  if (test.ok && test.millis) {
    return html`${icon("success")}<span style="color:var(--success)">连接正常，<span class="numeric">${test.millis} ms</span></span>${test.route ? html`<span class="muted">${routeText(test)}</span>` : ""}`;
  }
  if (test.ok) {
    return html`${icon("success")}<span style="color:var(--success)">${test.message}</span>`;
  }
  return html`${icon("warning")}<span style="color:var(--danger)">${test.message}</span>`;
}

// renderCheckResult 是「检查订阅」的结果：节点数、流量和到期时间，或下载失败的原因。
function renderCheckResult(test) {
  if (!test) {
    return html``;
  }
  if (test.running) {
    return html`<div class="preview" style="display:flex;gap:8px;align-items:center"><span class="spinner"></span>正在下载订阅…</div>`;
  }
  if (!test.ok) {
    return html`<div class="infobar danger">${icon("error")}<div class="infobar-body"><div class="infobar-title">订阅不能用</div>${test.message}</div></div>`;
  }
  const usage = usageText(test.check);
  return html`<div class="infobar success">${icon("success")}<div class="infobar-body"><div class="infobar-title">找到 ${test.check.nodes} 个节点</div>${usage || "机场没有提供流量和到期时间"}</div></div>`;
}

function field({ label, name, value, placeholder = "", error = "", hint = "", className = "", type = "text", mono = false, focus = name }) {
  return html`
    <div class="field ${className}">
      <label class="field-label" for="field-${name}">${label}</label>
      <input class="input ${mono ? "mono" : ""} ${error ? "invalid" : ""}" id="field-${name}" type="${type}" data-field="${name}" data-focus="${focus}" value="${value}" placeholder="${placeholder}" spellcheck="false" autocomplete="off">
      <div class="field-error" data-error="${name}">${error}</div>
      ${hint ? html`<div class="field-hint">${hint}</div>` : ""}
    </div>`;
}

function renderProfileEditor(draft, editing, errors) {
  const targets = app.state.targets || [];
  const palette = app.state.palette || [];
  const customColor = !palette.includes(draft.color);
  let kindFields;
  switch (draft.kind) {
    case "pac":
      kindFields = html`
        ${field({ label: "PAC 脚本地址", name: "pac", value: draft.pac, placeholder: "http://example.com/proxy.pac", error: errors.pac, mono: true })}
        ${field({ label: "代理服务器（可选）", name: "pacServer", value: draft.pacServer, placeholder: "例如 127.0.0.1:7890", error: errors.pacServer, hint: "只在勾选环境变量、git 或 npm 时需要：它们不支持 PAC，会改用这个地址。", mono: true })}`;
      break;
    case "custom":
      kindFields = field({ label: "代理设置", name: "raw", value: draft.raw, placeholder: "http=127.0.0.1:7890;https=127.0.0.1:7890", error: errors.raw, mono: true });
      break;
    case "subscription":
      kindFields = html`
        ${coreNotice()}
        ${field({ label: "订阅地址", name: "subscription", value: draft.subscription, placeholder: "https://…", error: errors.subscription, mono: true, hint: "机场提供的订阅链接，Clash 和 V2Ray 格式都可以。点下面的「检查订阅」可以先看看有多少个节点" })}
        <div data-check-result>${renderCheckResult(draft.test)}</div>
        <div class="field">
          <span class="field-label">分流</span>
          <div class="segmented" role="group" aria-label="分流">
            ${modeOptions.map(([value, label]) => html`<button type="button" data-mode="${value}" aria-pressed="${draft.mode === value}">${label}</button>`)}
          </div>
          <div class="field-hint">${modeHints[draft.mode]}</div>
        </div>`;
      break;
    default:
      kindFields = html`
        <div class="field-row">
          ${field({ label: "代理服务器地址", name: "host", value: draft.host, placeholder: "127.0.0.1", error: errors.host, mono: true, hint: "本机运行的代理软件一般填 127.0.0.1" })}
          ${field({ label: "端口", name: "port", value: draft.port, placeholder: draft.kind === "socks" ? "1080" : "7890", error: errors.port, className: "port", mono: true, type: "text" })}
        </div>`;
  }
  const showNoProxy = draft.applyTo.includes("env") || draft.applyTo.includes("npm");
  return html`
    <div class="dialog-body">
      <h2 class="dialog-title">${editing ? "编辑代理配置" : "添加代理配置"}</h2>
      ${draft.serverError ? html`<div class="infobar danger">${icon("error")}<div class="infobar-body"><div class="infobar-title">没有保存</div>${draft.serverError}</div></div>` : ""}
      ${field({ label: "名称", name: "name", value: draft.name, placeholder: "例如：公司代理", error: errors.name })}
      <div class="field">
        <span class="field-label">颜色</span>
        <div class="swatches">
          ${palette.map((color) => html`<button class="swatch" style="background:${color}" data-color="${color}" aria-pressed="${draft.color === color}" aria-label="颜色 ${color}"></button>`)}
          <label class="swatch-custom" title="自定义颜色" style="${customColor ? raw(`background:${escapeHtml(draft.color)};border-style:solid;border-color:var(--text)`) : ""}">
            ${customColor ? "" : icon("plus")}
            <input type="color" value="${draft.color}" data-custom-color aria-label="自定义颜色">
          </label>
          <span class="caption muted">托盘图标会用这个颜色显示</span>
        </div>
      </div>
      <div class="field">
        <span class="field-label">类型</span>
        <div class="segmented" role="group" aria-label="代理类型">
          ${editorKinds.map((kind) => html`<button type="button" data-kind="${kind}" aria-pressed="${draft.kind === kind}">${kindLabels[kind]}</button>`)}
        </div>
        <div class="field-hint">${kindHints[draft.kind]}</div>
      </div>
      ${kindFields}
      <div class="field">
        <span class="field-label">在哪里生效</span>
        <div class="targets">
          ${targets.map((target) => html`
            <label class="checkbox ${target.available ? "" : "disabled"}">
              <input type="checkbox" data-target="${target.id}" ${draft.applyTo.includes(target.id) ? raw("checked") : ""} ${target.available || draft.applyTo.includes(target.id) ? "" : raw("disabled")}>
              <span class="checkbox-text">${target.label}${target.id === "system" ? html` <span class="badge accent">推荐</span>` : ""}<small>${target.note || target.description}</small></span>
            </label>`)}
        </div>
        <div class="field-error" data-error="targets">${errors.targets}</div>
      </div>
      <button class="disclosure" type="button" data-action="toggle-advanced" aria-expanded="${draft.advancedOpen}">${icon("chevron")}高级设置</button>
      ${draft.advancedOpen ? html`
        <div class="field">
          <label class="field-label" for="field-bypass">不走代理的地址（系统代理）</label>
          <textarea class="textarea mono" id="field-bypass" rows="4" data-field="bypassText" data-focus="bypass" spellcheck="false">${draft.bypassText}</textarea>
          <div class="field-hint">每行一个，* 是通配符，&lt;local&gt; 表示不含点的内网名称。默认已包含本机和局域网地址。<a href="#" data-action="reset-bypass">恢复默认</a></div>
        </div>
        ${showNoProxy ? field({ label: "NO_PROXY（环境变量和 npm）", name: "noProxy", value: draft.noProxy, placeholder: app.state.defaults.no_proxy, mono: true, hint: "逗号分隔，这些地址不走代理" }) : ""}` : ""}
      <div data-preview>${renderPreview(draft)}</div>
      ${editing ? "" : html`<label class="checkbox" style="margin:0 -10px 8px"><input type="checkbox" data-field="useAfterSave" ${draft.useAfterSave ? raw("checked") : ""}><span class="checkbox-text">保存后立即使用这个配置</span></label>`}
    </div>
    <div class="dialog-footer">
      <div class="left">
        <button class="button" type="button" data-action="dialog-test" ${draft.test && draft.test.running ? raw("disabled") : ""}>${draft.kind === "subscription" ? html`${icon("refresh")}检查订阅` : html`${icon("gauge")}测试连接`}</button>
        <span class="test-result" data-test-result>${draft.kind === "subscription" ? "" : renderTestResult(draft.test)}</span>
      </div>
      <button class="button accent" type="button" data-action="dialog-save" ${draft.saving ? raw("disabled") : ""}>${draft.saving ? html`<span class="spinner"></span>` : ""}${draft.savingText || "保存"}</button>
      <button class="button" type="button" data-action="dialog-cancel">取消</button>
    </div>`;
}

// ---------- 检测本机代理 ----------

function suggestProfileName(proxy) {
  let base = (proxy.process || "").replace(/\.exe$/i, "").trim();
  if (!base) {
    base = "本机代理";
  }
  let name = `${base} ${proxy.port}`;
  const names = new Set((app.config ? app.config.profiles : []).map((profile) => profile.name.toLowerCase()));
  for (let suffix = 2; names.has(name.toLowerCase()); suffix++) {
    name = `${base} ${proxy.port} (${suffix})`;
  }
  return name;
}

function existingProfileFor(proxy) {
  const address = `${proxy.host}:${proxy.port}`;
  return (app.config ? app.config.profiles : []).find((profile) => {
    const parts = splitServer(profile.server);
    return parts.port === String(proxy.port) && (parts.host === proxy.host || (["127.0.0.1", "localhost"].includes(parts.host) && proxy.host === "127.0.0.1")) || profile.server === address;
  });
}

function openDetectDialog() {
  const view = { loading: true, proxies: [], error: "" };
  const dialog = openDialog({
    className: "wide",
    render: () => renderDetect(view),
    onMount: (dialogInstance) => {
      dialogInstance.element.addEventListener("click", (event) => {
        const button = event.target.closest("[data-action]");
        if (!button) {
          return;
        }
        switch (button.dataset.action) {
          case "detect-add": {
            const proxy = view.proxies[Number(button.dataset.index)];
            const socksOnly = proxy.socks && !proxy.http;
            dialogInstance.close(true);
            openProfileEditor({
              name: suggestProfileName(proxy),
              server: socksOnly ? `socks5://${proxy.host}:${proxy.port}` : `${proxy.host}:${proxy.port}`,
              apply_to: ["system"],
            });
            break;
          }
          case "detect-rescan":
            scan();
            break;
          case "detect-manual":
            dialogInstance.close(true);
            openProfileEditor();
            break;
          case "dialog-cancel":
            dialogInstance.close(false);
            break;
        }
      });
    },
  });

  async function scan() {
    view.loading = true;
    view.error = "";
    dialog.render();
    try {
      const result = await api("GET", "/api/detect");
      view.proxies = result.proxies || [];
    } catch (error) {
      view.error = error.message;
    }
    view.loading = false;
    if (!dialog.closed) {
      dialog.render();
    }
  }
  scan();
}

function renderDetect(view) {
  let content;
  if (view.loading) {
    content = html`<div class="card" style="padding:28px;display:flex;align-items:center;gap:14px"><span class="spinner"></span><div><strong>正在查找本机正在运行的代理软件…</strong><div class="caption muted">会逐个确认端口能否转发连接，大约需要两秒</div></div></div>`;
  } else if (view.error) {
    content = html`<div class="infobar danger">${icon("error")}<div class="infobar-body"><div class="infobar-title">检测失败</div>${view.error}</div></div>`;
  } else if (view.proxies.length === 0) {
    content = html`<div class="infobar info">${icon("info")}<div class="infobar-body"><div class="infobar-title">没有找到正在运行的代理软件</div>请先打开代理软件，再点「重新检测」。也可以手动填写代理地址。</div></div>`;
  } else {
    content = html`
      <p class="muted" style="margin:0 0 12px">找到 ${view.proxies.length} 个可以使用的代理端口：</p>
      <div class="stack">
        ${view.proxies.map((proxy, index) => {
          const existing = existingProfileFor(proxy);
          return html`
            <div class="card setting">
              ${icon("monitor", "large")}
              <div>
                <div class="setting-title"><strong>${proxy.process ? proxy.process.replace(/\.exe$/i, "") : "未知程序"}</strong> <span class="mono muted">${proxy.host}:${proxy.port}</span></div>
                <div class="chips" style="margin-top:4px">
                  ${proxy.http ? html`<span class="badge accent">HTTP</span>` : ""}
                  ${proxy.socks ? html`<span class="badge accent">SOCKS5</span>` : ""}
                  ${proxy.http_auth || proxy.socks_auth ? html`<span class="badge warning">需要用户名和密码</span>` : ""}
                </div>
              </div>
              <div class="setting-control">
                ${existing ? html`<span class="badge success">${icon("check")}已添加为「${existing.name}」</span>` : html`<button class="button accent" data-action="detect-add" data-index="${index}">${icon("plus")}添加</button>`}
              </div>
            </div>`;
        })}
      </div>`;
  }
  return html`
    <div class="dialog-body">
      <h2 class="dialog-title">检测本机代理</h2>
      ${content}
    </div>
    <div class="dialog-footer">
      <div class="left"><button class="button" data-action="detect-rescan" ${view.loading ? raw("disabled") : ""}>${icon("refresh")}重新检测</button></div>
      <button class="button" data-action="detect-manual">手动填写</button>
      <button class="button" data-action="dialog-cancel">关闭</button>
    </div>`;
}

// ---------- 订阅的节点 ----------

// delayBadge 是节点最近一次测得的延迟：没测过显示横线，测不通显示超时。
function delayBadge(node) {
  if (!node.tested) {
    return html`<span class="badge">—</span>`;
  }
  if (!node.alive) {
    return html`<span class="badge danger">超时</span>`;
  }
  const millis = Math.max(1, node.delay);
  const level = millis < 300 ? "success" : millis < 1000 ? "warning" : "danger";
  return html`<span class="badge ${level} numeric">${millis} ms</span>`;
}

// sortedNodes 按搜索词过滤；按延迟排序时能用的节点在前，测不通和没测过的在后。
function sortedNodes(view) {
  const query = view.query.trim().toLowerCase();
  const nodes = view.list.nodes.filter((node) => !query || node.name.toLowerCase().includes(query));
  if (!view.sortByDelay) {
    return nodes;
  }
  const rank = (node) => (node.tested && node.alive ? node.delay : Number.MAX_SAFE_INTEGER);
  return [...nodes].sort((first, second) => rank(first) - rank(second));
}

function openNodesDialog(profileId) {
  const view = { loading: true, busy: "", list: null, error: "", query: "", sortByDelay: false };
  const dialog = openDialog({
    className: "wide",
    render: () => renderNodes(profileById(profileId), view),
    onMount: (dialogInstance) => {
      const element = dialogInstance.element;
      // 搜索时只重绘列表，不重建输入框，避免打断中文输入法。
      element.addEventListener("input", (event) => {
        if (event.target.dataset.focus === "node-search") {
          view.query = event.target.value;
          const container = element.querySelector("[data-node-list]");
          if (container) {
            setHtml(container, renderNodeList(profileById(profileId), view));
          }
        }
      });
      element.addEventListener("click", (event) => {
        const button = event.target.closest("[data-action]");
        if (!button || button.disabled) {
          return;
        }
        switch (button.dataset.action) {
          case "node-select":
            run("select", () => api("POST", `/api/subscriptions/${profileId}/select`, { node: button.dataset.node }));
            break;
          case "nodes-test":
            run("test", () => api("POST", `/api/subscriptions/${profileId}/test`));
            break;
          case "nodes-sort":
            view.sortByDelay = !view.sortByDelay;
            dialog.render();
            break;
          case "nodes-update":
            update();
            break;
          case "nodes-use": {
            const profile = profileById(profileId);
            dialogInstance.close(true);
            if (profile) {
              runOperation("/api/use", { name: profile.name }, `已开启「${profile.name}」`);
            }
            break;
          }
          case "install-core":
            installCore(() => {
              if (!dialog.closed) {
                dialog.render();
              }
            }).then(load);
            break;
          case "dialog-cancel":
            dialogInstance.close(false);
            break;
        }
      });
    },
  });

  // run 执行一个返回节点列表的操作，期间禁用按钮。
  async function run(busy, request) {
    view.busy = busy;
    dialog.render();
    try {
      view.list = await request();
      view.error = "";
    } catch (error) {
      toast(error.message, "danger", busy === "test" ? "测速失败" : "没有切换成功");
    }
    view.busy = "";
    if (!dialog.closed) {
      dialog.render();
    }
    refreshState().catch(() => {});
  }

  async function load() {
    view.loading = true;
    dialog.render();
    try {
      view.list = await api("GET", `/api/subscriptions/${profileId}/nodes`);
      view.error = "";
    } catch (error) {
      view.error = error.message;
    }
    view.loading = false;
    if (!dialog.closed) {
      dialog.render();
    }
  }

  async function update() {
    view.busy = "update";
    dialog.render();
    try {
      receiveState(await api("POST", `/api/subscriptions/${profileId}/update`), { force: true });
      toast(`共 ${(app.state.subscriptions[profileId] || {}).nodes || 0} 个节点`, "success", "订阅已更新");
    } catch (error) {
      if (error.state) {
        receiveState(error.state, { force: true });
      }
      toast(error.message, "danger", "订阅没有更新成功");
    }
    view.busy = "";
    await load();
  }

  load();
  return dialog;
}

function renderNodeList(profile, view) {
  const list = view.list;
  const nodes = sortedNodes(view);
  const auto = !list.selected;
  const disabled = view.busy ? raw("disabled") : "";
  const rows = nodes.map((node) => html`
    <button class="node" data-action="node-select" data-node="${node.name}" aria-pressed="${list.selected === node.name}" ${disabled}>
      <span class="node-check">${list.selected === node.name ? icon("check") : ""}</span>
      <span class="node-name" title="${node.name}">${node.name}</span>
      <span class="node-type">${node.type}</span>
      ${delayBadge(node)}
    </button>`);
  return html`
    ${view.query ? "" : html`
      <button class="node" data-action="node-select" data-node="" aria-pressed="${auto}" ${disabled}>
        <span class="node-check">${auto ? icon("check") : ""}</span>
        <span class="node-name"><strong>自动选择</strong><small>${auto && list.current ? `延迟最低，当前：${list.current}` : "每隔一段时间测一次延迟，用延迟最低的节点"}</small></span>
        <span></span><span></span>
      </button>`}
    ${rows.length ? rows : html`<p class="muted" style="margin:12px 10px">${view.query ? "没有名字里包含这些字的节点" : "订阅里没有节点"}</p>`}`;
}

function renderNodes(profile, view) {
  if (!profile) {
    return html`<div class="dialog-body"><h2 class="dialog-title">这个配置已被删除</h2></div><div class="dialog-footer"><button class="button" data-action="dialog-cancel">关闭</button></div>`;
  }
  const info = app.state.subscriptions[profile.id] || {};
  const active = app.state.status.state === "on" && app.state.status.profile === profile.name;
  const busy = view.busy ? raw("disabled") : "";
  let content;
  if (view.loading && !view.list) {
    content = html`<div class="card" style="padding:28px;display:flex;align-items:center;gap:14px"><span class="spinner"></span><strong>正在读取节点…</strong></div>`;
  } else if (view.error) {
    content = html`${coreNotice()}<div class="infobar warning">${icon("warning")}<div class="infobar-body"><div class="infobar-title">读不到节点</div>${info.updated ? view.error : info.error ? `订阅没有下载成功：${info.error}` : "订阅还在下载，请稍后再试"}</div></div>`;
  } else {
    content = html`
      <div class="nodes-toolbar">
        <input class="input" type="search" data-focus="node-search" value="${view.query}" placeholder="搜索节点，例如 香港" aria-label="搜索节点" spellcheck="false" autocomplete="off">
        <button class="button" data-action="nodes-sort" aria-pressed="${view.sortByDelay}" title="按延迟排序">${icon("swap")}${view.sortByDelay ? "按延迟" : "按顺序"}</button>
        <button class="button" data-action="nodes-test" ${busy}>${view.busy === "test" ? html`<span class="spinner"></span>` : icon("gauge")}全部测速</button>
      </div>
      <div class="node-list" data-node-list>${renderNodeList(profile, view)}</div>`;
  }
  const usage = usageText(info);
  return html`
    <div class="dialog-body">
      <h2 class="dialog-title">选择节点 · ${profile.name}</h2>
      <p class="caption muted" style="margin:-8px 0 14px">${view.list ? `${view.list.nodes.length} 个节点` : ""}${usage ? ` · ${usage}` : ""}${info.updated ? ` · ${relativeTime(info.updated)}更新` : ""}</p>
      ${content}
    </div>
    <div class="dialog-footer">
      <div class="left"><button class="button" data-action="nodes-update" ${busy}>${view.busy === "update" ? html`<span class="spinner"></span>` : icon("refresh")}更新订阅</button></div>
      ${active ? "" : html`<button class="button accent" data-action="nodes-use" ${busy}>使用这个配置</button>`}
      <button class="button" data-action="dialog-cancel">${active ? "完成" : "关闭"}</button>
    </div>`;
}

// ---------- 添加自动切换规则 ----------

const matchLabels = { ssid: "Wi-Fi 名称", dns_suffix: "DNS 后缀", gateway: "网关" };

const matchHints = {
  ssid: "连接的 Wi-Fi 名称，不区分大小写",
  dns_suffix: "网络分配的 DNS 后缀，例如 corp.example.com，公司网络常见，有线网络也适用",
  gateway: "路由器的 MAC 地址（更可靠）或 IP 地址，有线网络也适用",
};

function networkSuggestions(match) {
  const network = app.state.network || { ssids: [], adapters: [] };
  const values = new Set();
  if (match === "ssid") {
    network.ssids.forEach((ssid) => values.add(ssid));
  } else if (match === "dns_suffix") {
    network.adapters.forEach((adapter) => adapter.dns_suffix && values.add(adapter.dns_suffix));
  } else {
    network.adapters.forEach((adapter) => {
      if (adapter.gateway_mac) {
        values.add(adapter.gateway_mac);
      }
      if (adapter.gateway) {
        values.add(adapter.gateway);
      }
    });
  }
  return [...values];
}

function describeAction(action) {
  return action === "off" ? "关闭代理" : `使用「${action.slice(4)}」`;
}

function openRuleDialog(prefill = {}) {
  const profiles = app.config.profiles;
  const draft = {
    match: prefill.match || "ssid",
    value: prefill.value !== undefined ? prefill.value : networkSuggestions(prefill.match || "ssid")[0] || "",
    action: prefill.action || (profiles.length ? `use:${profiles[0].name}` : "off"),
    error: "",
    saving: false,
  };
  openDialog({
    className: "small",
    render: () => html`
      <div class="dialog-body">
        <h2 class="dialog-title">添加自动切换规则</h2>
        ${draft.error ? html`<div class="infobar danger">${icon("error")}<div class="infobar-body">${draft.error}</div></div>` : ""}
        <div class="field">
          <span class="field-label">按什么判断网络</span>
          <div class="segmented" role="group">${Object.keys(matchLabels).map((match) => html`<button type="button" data-match="${match}" aria-pressed="${draft.match === match}">${matchLabels[match]}</button>`)}</div>
          <div class="field-hint">${matchHints[draft.match]}</div>
        </div>
        <div class="field">
          <label class="field-label" for="rule-value">${matchLabels[draft.match]}</label>
          <input class="input mono" id="rule-value" data-focus="rule-value" list="rule-suggestions" value="${draft.value}" placeholder="${draft.match === "ssid" ? "例如 Office-WiFi" : draft.match === "dns_suffix" ? "例如 corp.example.com" : "例如 a4-91-b1-0c-22-9e"}" spellcheck="false" autocomplete="off">
          <datalist id="rule-suggestions">${networkSuggestions(draft.match).map((value) => html`<option value="${value}">`)}</datalist>
          ${networkSuggestions(draft.match).length ? html`<div class="field-hint">当前网络：${networkSuggestions(draft.match).join("、")}</div>` : ""}
        </div>
        <div class="field">
          <label class="field-label" for="rule-action">连上这个网络时</label>
          <select class="select" id="rule-action" data-focus="rule-action">
            ${profiles.map((profile) => html`<option value="use:${profile.name}" ${draft.action === `use:${profile.name}` ? raw("selected") : ""}>使用「${profile.name}」</option>`)}
            <option value="off" ${draft.action === "off" ? raw("selected") : ""}>关闭代理</option>
          </select>
        </div>
      </div>
      <div class="dialog-footer">
        <button class="button accent" data-action="rule-save" ${draft.saving ? raw("disabled") : ""}>添加</button>
        <button class="button" data-action="dialog-cancel">取消</button>
      </div>`,
    onMount: (dialog) => {
      dialog.element.addEventListener("input", (event) => {
        if (event.target.id === "rule-value") {
          draft.value = event.target.value;
        }
      });
      dialog.element.addEventListener("change", (event) => {
        if (event.target.id === "rule-action") {
          draft.action = event.target.value;
        }
      });
      dialog.element.addEventListener("keydown", (event) => {
        if (event.key === "Enter" && event.target.id === "rule-value") {
          dialog.element.querySelector('[data-action="rule-save"]').click();
        }
      });
      dialog.element.addEventListener("click", async (event) => {
        const matchButton = event.target.closest("[data-match]");
        if (matchButton) {
          draft.match = matchButton.dataset.match;
          draft.value = networkSuggestions(draft.match)[0] || "";
          dialog.render();
          return;
        }
        const button = event.target.closest("[data-action]");
        if (!button) {
          return;
        }
        if (button.dataset.action === "dialog-cancel") {
          dialog.close(false);
          return;
        }
        if (button.dataset.action !== "rule-save") {
          return;
        }
        if (!draft.value.trim()) {
          draft.error = `请填写${matchLabels[draft.match]}`;
          dialog.render();
          return;
        }
        const config = clone(app.config);
        const firstRule = config.auto_switch.rules.length === 0;
        config.auto_switch.rules.push({
          match: draft.match,
          value: draft.value.trim(),
          action: draft.action === "off" ? "off" : "use",
          profile: draft.action === "off" ? "" : draft.action.slice(4),
        });
        if (firstRule) {
          config.auto_switch.enabled = true;
        }
        draft.saving = true;
        dialog.render();
        try {
          const state = await api("PUT", "/api/config", config);
          receiveState(state, { force: true });
          dialog.close(true);
          toast(firstRule ? "自动切换已开启，网络变化时生效" : "网络变化时生效，也可以点「立即应用」", "success", "已添加规则");
        } catch (error) {
          draft.saving = false;
          draft.error = error.message;
          dialog.render();
        }
      });
    },
  });
}
