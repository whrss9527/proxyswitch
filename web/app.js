"use strict";

// 设置页的主程序：导航、与程序同步状态、处理按钮和设置项的操作。

const app = {
  state: null,
  config: null,
  page: "proxies",
  busy: false,
  saving: false,
  latency: {},
  diagnostics: null,
  log: "",
  update: null,
  navigateSerial: 0,
};

const pollInterval = 2000;
let pollTimer = 0;
let saveQueue = Promise.resolve();

// ---------- 状态同步 ----------

function configJson(config) {
  return JSON.stringify(config);
}

// receiveState 接收程序返回的最新状态。options.force 为 true 时一定重绘页面。
function receiveState(state, options = {}) {
  const previous = app.state;
  const configChanged = !previous || configJson(state.config) !== configJson(previous.config);
  app.state = state;
  if (configChanged) {
    app.config = clone(state.config);
  }
  applyAppearance();
  // 程序请求切换页面（例如点击了托盘通知）。序号只增不减：比这次请求更早发出的请求可能更晚返回，不能再切一次。
  const navigate = state.navigate || { page: "", serial: 0 };
  const requested = navigate.serial > app.navigateSerial && pageRenderers[navigate.page];
  app.navigateSerial = Math.max(app.navigateSerial, navigate.serial);
  if (requested) {
    goto(navigate.page);
    return;
  }
  renderNav();
  const stateChanged = !previous || JSON.stringify(previous) !== JSON.stringify(state);
  if (options.force || stateChanged) {
    renderPage({ fromPoll: Boolean(options.fromPoll) });
  }
}

async function refreshState(fromPoll = false) {
  const state = await api("GET", "/api/state");
  receiveState(state, { fromPoll });
}

function schedulePoll() {
  clearTimeout(pollTimer);
  pollTimer = setTimeout(async () => {
    if (!document.hidden) {
      try {
        await refreshState(true);
      } catch (error) {
        // 连接问题由 connectionLost 处理。
      }
    }
    schedulePoll();
  }, pollInterval);
}

function applyAppearance() {
  const theme = currentTheme(app.config ? app.config.theme : "system");
  if (document.documentElement.dataset.theme !== theme) {
    document.documentElement.dataset.theme = theme;
  }
  applyAccent(app.state.accent, theme);
  const status = app.state.status;
  const profile = findProfile(status.profile);
  if (status.state === "on" && profile) {
    setFavicon(profile.color, true);
  } else if (status.state === "external") {
    setFavicon(amberTrack, true);
  } else {
    setFavicon(grayTrack, false);
  }
}

darkQuery.addEventListener("change", () => {
  if (app.state) {
    applyAppearance();
  }
});

// ---------- 渲染 ----------

function renderShell() {
  setHtml(document.getElementById("app"), html`
    <div class="shell">
      <nav class="nav" id="nav" aria-label="设置分类"></nav>
      <main class="main" id="main"><div class="page" id="page"></div></main>
    </div>`);
}

function navStatus() {
  const status = app.state.status;
  const profile = findProfile(status.profile);
  let color = "var(--text-3)";
  let title = "代理已关闭";
  let detail = profile ? `打开后使用「${profile.name}」` : "还没有代理配置";
  if (status.state === "on" && profile) {
    color = profile.color;
    title = "代理已开启";
    detail = profile.name;
    if (status.health === "down") {
      color = "var(--danger)";
      detail = `${profile.name} · 连不上`;
    }
  } else if (status.state === "external") {
    color = amberTrack;
    title = "其他程序的代理";
    detail = status.external;
  }
  return html`
    <div class="nav-status" title="${title} · ${detail}">
      <span class="dot ring" style="background:${raw(escapeHtml(color))};color:${raw(escapeHtml(color))}"></span>
      <div class="nav-status-text"><div class="nav-status-title">${title}</div><div class="nav-status-detail">${detail}</div></div>
      ${switchButton({ checked: status.state !== "off", action: "toggle", label: "开关代理", disabled: app.busy })}
    </div>`;
}

function renderNav() {
  const nav = document.getElementById("nav");
  if (!nav) {
    return;
  }
  const badges = {
    proxies: Boolean(app.state.config_error),
    about: Boolean(app.update && app.update.newer),
  };
  setHtml(nav, html`
    <div class="brand">
      ${logoSvg(app.state.status.state === "off" ? grayTrack : (findProfile(app.state.status.profile) || {}).color || amberTrack, app.state.status.state !== "off")}
      <div class="brand-text"><div class="brand-name">ProxySwitch</div><div class="brand-version">版本 ${app.state.version}</div></div>
    </div>
    ${pages.map((page) => html`
      <button class="nav-item" data-action="goto" data-page="${page.id}" ${app.page === page.id ? raw('aria-current="page"') : ""} title="${page.label}">
        ${icon(page.icon)}<span>${page.label}</span>${badges[page.id] ? html`<span class="nav-badge"></span>` : ""}
      </button>`)}
    <div class="nav-spacer"></div>
    ${navStatus()}`);
}

function isEditing() {
  const active = document.activeElement;
  return active && active.closest(".page") && active.matches("input, textarea, select");
}

// renderPage 重绘当前页面。定时刷新时如果正在输入，就先不重绘，避免打断输入。
function renderPage({ fromPoll = false, animate = false } = {}) {
  if (fromPoll && isEditing()) {
    return;
  }
  const main = document.getElementById("main");
  let page = document.getElementById("page");
  if (!main || !page) {
    return;
  }
  if (animate) {
    const fresh = document.createElement("div");
    fresh.className = "page";
    fresh.id = "page";
    page.replaceWith(fresh);
    page = fresh;
    main.scrollTop = 0;
  }
  const active = document.activeElement;
  const focusId = active && page.contains(active) ? active.id || null : null;
  const focusAction = active && page.contains(active) && !focusId ? active.dataset.action + "|" + (active.dataset.id || active.dataset.name || active.dataset.page || active.dataset.index || "") : null;
  setHtml(page, pageRenderers[app.page]());
  if (focusId) {
    const target = document.getElementById(focusId);
    if (target) {
      target.focus();
    }
  } else if (focusAction) {
    const [action, key] = focusAction.split("|");
    const target = [...page.querySelectorAll(`[data-action="${action}"]`)].find((element) => (element.dataset.id || element.dataset.name || element.dataset.page || element.dataset.index || "") === key);
    if (target) {
      target.focus();
    }
  }
}

function goto(pageId) {
  if (!pageRenderers[pageId]) {
    return;
  }
  cancelHotkeyRecording();
  app.page = pageId;
  renderNav();
  renderPage({ animate: true });
  if (pageId === "diagnostics") {
    loadDiagnostics();
  }
  if (location.hash !== `#${pageId}`) {
    history.replaceState(null, "", `/#${pageId}`);
  }
}

// ---------- 操作 ----------

// runOperation 执行开关、切换等操作，完成后刷新状态；部分失败时程序返回 409 和最新状态。
async function runOperation(path, body, successMessage) {
  app.busy = true;
  renderNav();
  renderPage();
  try {
    const state = await api("POST", path, body);
    app.busy = false;
    receiveState(state, { force: true });
    if (successMessage) {
      toast(successMessage);
    }
    return true;
  } catch (error) {
    app.busy = false;
    if (error.state) {
      receiveState(error.state, { force: true });
      toast(error.message, "warning", "部分设置没有成功");
    } else {
      renderNav();
      renderPage();
      if (error.status !== 0 && error.status !== 403) {
        toast(error.message, "danger", "操作失败");
      }
    }
    return false;
  }
}

// saveConfig 修改配置并立即保存；多次修改按顺序保存。
function saveConfig(mutate, successMessage = "") {
  const next = clone(app.config);
  mutate(next);
  app.config = next;
  app.saving = true;
  renderPage();
  saveQueue = saveQueue.then(async () => {
    try {
      const state = await api("PUT", "/api/config", next);
      app.saving = false;
      receiveState(state, { force: true });
      if (successMessage) {
        toast(successMessage);
      }
    } catch (error) {
      app.saving = false;
      app.config = clone(app.state.config);
      renderPage();
      if (error.status !== 0 && error.status !== 403) {
        toast(error.message, "danger", "没有保存");
      }
    }
  });
  return saveQueue;
}

function setPath(object, path, value) {
  const keys = path.split(".");
  let target = object;
  for (const key of keys.slice(0, -1)) {
    target = target[key];
  }
  target[keys[keys.length - 1]] = value;
}

function getPath(object, path) {
  return path.split(".").reduce((value, key) => (value == null ? value : value[key]), object);
}

async function testProfile(profile) {
  app.latency[profile.id] = { running: true };
  renderPage();
  try {
    const result = await api("POST", "/api/test", { profile: profile.name });
    app.latency[profile.id] = result;
  } catch (error) {
    app.latency[profile.id] = { ok: false, message: error.message };
  }
  renderPage();
  return app.latency[profile.id];
}

async function testAllProfiles() {
  const results = await Promise.all(app.config.profiles.map((profile) => testProfile(profile)));
  const passed = results.filter((result) => result.ok).length;
  toast(`${passed} 个可用，${results.length - passed} 个失败`, passed === results.length ? "success" : "warning", "测速完成");
}

async function loadDiagnostics() {
  try {
    const [diagnostics, log] = await Promise.all([api("GET", "/api/diagnostics"), api("GET", "/api/log?lines=300")]);
    app.diagnostics = diagnostics;
    app.log = log.text;
  } catch (error) {
    return;
  }
  if (app.page === "diagnostics") {
    renderPage();
    const logElement = document.getElementById("log");
    if (logElement) {
      logElement.scrollTop = logElement.scrollHeight;
    }
  }
}

function diagnosticsText() {
  const diagnostics = app.diagnostics;
  const state = app.state;
  const lines = [
    `ProxySwitch ${state.version}（${state.platform}）`,
    `状态：${state.status.state}${state.status.profile ? ` · ${state.status.profile}` : ""}${state.status.health ? ` · 健康：${state.status.health}` : ""}`,
    `系统代理：${JSON.stringify(diagnostics.system)}（${diagnostics.system_source}）`,
    `连接：${diagnostics.connections.join("、")}；组策略统一设置：${diagnostics.machine_policy ? "是" : "否"}`,
    `环境变量：${JSON.stringify(diagnostics.env)}`,
    `git：${diagnostics.git.available ? `http.proxy=${diagnostics.git.http_proxy || "-"} https.proxy=${diagnostics.git.https_proxy || "-"}` : "未安装"}`,
    `npm：${JSON.stringify(diagnostics.npm)}`,
    `网络：${state.auto_switch.network}；自动切换：${app.config && app.config.auto_switch.enabled ? "开" : "关"}`,
    "",
    "最近日志：",
    (app.log || "").split("\n").slice(-40).join("\n"),
  ];
  return lines.join("\n");
}

function profileById(id) {
  return app.config.profiles.find((profile) => profile.id === id);
}

function moveItem(list, index, offset) {
  const target = index + offset;
  if (target < 0 || target >= list.length) {
    return;
  }
  const [item] = list.splice(index, 1);
  list.splice(target, 0, item);
}

function openProfileMenu(anchor, profile) {
  const index = app.config.profiles.indexOf(profile);
  const active = app.state.status.state === "on" && app.state.status.profile === profile.name;
  openMenu(anchor, [
    { label: "测速", icon: "gauge", action: () => testProfile(profile) },
    { label: "复制一份", icon: "copy", action: () => openProfileEditor({ ...profile, id: "", name: `${profile.name} 副本`, color: nextColor() }) },
    { separator: true },
    { label: "上移", icon: "up", disabled: index === 0, action: () => saveConfig((config) => moveItem(config.profiles, index, -1)) },
    { label: "下移", icon: "down", disabled: index === app.config.profiles.length - 1, action: () => saveConfig((config) => moveItem(config.profiles, index, 1)) },
    { separator: true },
    { label: "删除", icon: "trash", danger: true, action: () => deleteProfile(profile, active) },
  ]);
}

async function deleteProfile(profile, active) {
  const usedByRules = app.config.auto_switch.rules.filter((rule) => rule.action === "use" && rule.profile === profile.name).length;
  const usedByDefault = app.config.auto_switch.default_action === "use" && app.config.auto_switch.default_profile === profile.name;
  const notes = [];
  if (active) {
    notes.push("它正在使用，删除后会关闭代理。");
  }
  if (usedByRules || usedByDefault) {
    notes.push("自动切换里用到它的规则也会一起删除。");
  }
  const confirmed = await confirmDialog({ title: `删除「${profile.name}」？`, message: notes.join("\n") || "删除后无法恢复。", confirmText: "删除", danger: true });
  if (!confirmed) {
    return;
  }
  saveConfig((config) => {
    config.profiles = config.profiles.filter((item) => item.id !== profile.id);
    config.auto_switch.rules = config.auto_switch.rules.filter((rule) => !(rule.action === "use" && rule.profile === profile.name));
    if (usedByDefault) {
      config.auto_switch.default_action = "keep";
      config.auto_switch.default_profile = "";
    }
  }, `已删除「${profile.name}」`);
}

// 重命名配置时，自动切换里引用它的地方跟着改名。
function renameReferences(config, oldName, newName) {
  if (!oldName || oldName === newName) {
    return;
  }
  for (const rule of config.auto_switch.rules) {
    if (rule.profile === oldName) {
      rule.profile = newName;
    }
  }
  if (config.auto_switch.default_profile === oldName) {
    config.auto_switch.default_profile = newName;
  }
}

async function importConfig() {
  const file = await pickFile(".json,.jsonc,application/json");
  if (!file) {
    return;
  }
  const text = await file.text();
  try {
    await api("POST", "/api/validate", text);
  } catch (error) {
    toast(error.message, "danger", "这个文件不能导入");
    return;
  }
  const confirmed = await confirmDialog({ title: "导入设置？", message: `会用「${file.name}」里的设置和代理配置替换当前的全部内容。`, confirmText: "导入" });
  if (!confirmed) {
    return;
  }
  try {
    const state = await api("PUT", "/api/config", text);
    receiveState(state, { force: true });
    toast("设置和代理配置已替换", "success", "导入成功");
  } catch (error) {
    toast(error.message, "danger", "导入失败");
  }
}

// ---------- 快捷键录制 ----------

function startHotkeyRecording(setting) {
  recordingHotkey = { setting };
  renderPage();
}

function cancelHotkeyRecording() {
  if (recordingHotkey) {
    recordingHotkey = null;
    if (app.state) {
      renderPage();
    }
  }
}

document.addEventListener("keydown", (event) => {
  if (!recordingHotkey) {
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  if (event.key === "Escape") {
    cancelHotkeyRecording();
    return;
  }
  const combination = hotkeyFromEvent(event);
  if (!combination) {
    return;
  }
  if (!event.ctrlKey && !event.altKey && !event.shiftKey && !event.metaKey) {
    toast("需要同时按住 Ctrl、Alt、Shift 或 Win 中的至少一个", "warning", "快捷键需要修饰键");
    return;
  }
  const setting = recordingHotkey.setting;
  recordingHotkey = null;
  saveConfig((config) => setPath(config, setting, combination), `快捷键已改为 ${combination}`);
}, true);

// ---------- 事件 ----------

const actions = {
  goto: (element) => goto(element.dataset.page),
  toggle: () => {
    const status = app.state.status;
    if (status.state === "off" && app.config && app.config.profiles.length === 0) {
      goto("proxies");
      openDetectDialog();
      return;
    }
    runOperation(status.state === "off" ? "/api/on" : "/api/off");
  },
  use: (element) => runOperation("/api/use", { name: element.dataset.name }),
  add: () => openProfileEditor(),
  "add-pac": () => openProfileEditor({}, { kind: "pac" }),
  "save-external": () => {
    const external = app.state.status.external_profile;
    if (!external) {
      return;
    }
    const names = new Set(app.config.profiles.map((profile) => profile.name.toLowerCase()));
    let name = "原有代理";
    for (let suffix = 2; names.has(name.toLowerCase()); suffix++) {
      name = `原有代理 ${suffix}`;
    }
    openProfileEditor({ ...external, name });
  },
  detect: () => openDetectDialog(),
  edit: (element) => {
    const profile = profileById(element.dataset.id);
    if (profile) {
      openProfileEditor(profile);
    }
  },
  "profile-menu": (element) => {
    const profile = profileById(element.dataset.id);
    if (profile) {
      openProfileMenu(element, profile);
    }
  },
  "test-profile": (element) => {
    const profile = profileById(element.dataset.id);
    if (profile) {
      testProfile(profile);
    }
  },
  "test-all": () => testAllProfiles(),
  "terminal-menu": (element) => openMenu(element, (app.state.status.terminal || []).map((command) => ({
    label: command.label,
    icon: "copy",
    title: command.command,
    action: async () => {
      if (await copyText(command.command)) {
        toast("粘贴到终端里回车，这个终端窗口就会使用代理", "success", `已复制 ${command.label} 命令`);
      }
    },
  }))),
  autostart: () => runOperation("/api/autostart", { enabled: !app.state.autostart }),
  "record-hotkey": (element) => {
    if (recordingHotkey) {
      cancelHotkeyRecording();
    } else {
      startHotkeyRecording(element.dataset.settingName);
    }
  },
  "clear-hotkey": () => saveConfig((config) => (config.hotkey = ""), "已清除快捷键"),
  "set-theme": (element) => saveConfig((config) => (config.theme = element.dataset.value)),
  "reset-test-url": () => saveConfig((config) => (config.test_url = app.state.defaults.test_url)),
  "open-config-dir": () => api("POST", "/api/open/config-dir").catch((error) => toast(error.message, "danger", "无法打开")),
  "open-config-file": () => api("POST", "/api/open/config-file").then(() => toast("保存后几秒内自动生效", "info", "已用编辑器打开配置文件")).catch((error) => toast(error.message, "danger", "无法打开")),
  "open-log": () => api("POST", "/api/open/log").catch((error) => toast(error.message, "danger", "无法打开")),
  "open-url": (element) => api("POST", "/api/open-url", { url: element.dataset.url }).catch((error) => toast(error.message, "danger", "无法打开")),
  "open-location-settings": () => api("POST", "/api/open-url", { url: "ms-settings:privacy-location" }).catch((error) => toast(error.message, "danger", "无法打开")),
  "export-config": () => {
    downloadText("proxyswitch-config.json", JSON.stringify(app.config, null, 2) + "\n");
    toast("已导出，保存在浏览器的下载文件夹", "success", "导出完成");
  },
  "import-config": () => importConfig(),
  "reset-config": async () => {
    const confirmed = await confirmDialog({ title: "恢复为默认配置？", message: "配置文件会被覆盖成默认内容，里面的代理配置会丢失。", confirmText: "恢复默认", danger: true });
    if (confirmed) {
      try {
        receiveState(await api("PUT", "/api/config", "{}"), { force: true });
        toast("配置文件已恢复为默认内容");
      } catch (error) {
        toast(error.message, "danger", "恢复失败");
      }
    }
  },
  "reset-settings": async () => {
    const confirmed = await confirmDialog({ title: "恢复默认设置？", message: "「常规」页的设置会恢复为默认值，代理配置和自动切换规则会保留。", confirmText: "恢复默认" });
    if (confirmed) {
      const kept = { profiles: app.config.profiles, auto_switch: app.config.auto_switch };
      try {
        receiveState(await api("PUT", "/api/config", kept), { force: true });
        toast("已恢复默认设置");
      } catch (error) {
        toast(error.message, "danger", "恢复失败");
      }
    }
  },
  "add-rule": () => openRuleDialog(),
  "quick-rule": (element) => openRuleDialog({ match: element.dataset.match, value: element.dataset.value }),
  "rule-up": (element) => saveConfig((config) => moveItem(config.auto_switch.rules, Number(element.dataset.index), -1)),
  "rule-down": (element) => saveConfig((config) => moveItem(config.auto_switch.rules, Number(element.dataset.index), 1)),
  "rule-delete": (element) => saveConfig((config) => config.auto_switch.rules.splice(Number(element.dataset.index), 1), "已删除规则"),
  "apply-auto-switch": () => runOperation("/api/auto-switch/apply", undefined, "已按当前网络应用规则"),
  "refresh-diagnostics": () => {
    app.diagnostics = null;
    renderPage();
    loadDiagnostics();
  },
  "refresh-log": () => loadDiagnostics(),
  "copy-diagnostics": async () => {
    if (await copyText(diagnosticsText())) {
      toast("可以粘贴到问题反馈里", "success", "已复制诊断信息");
    }
  },
  "clear-all": async () => {
    const confirmed = await confirmDialog({
      title: "清除所有代理设置？",
      message: "会把 Windows 系统代理改为直接连接，并清除环境变量、git 和 npm 里的代理设置。遇到上不了网又不知道原因时可以用它恢复。",
      confirmText: "全部清除",
      danger: true,
    });
    if (confirmed) {
      await runOperation("/api/clear-all", undefined, "所有代理设置已清除");
      loadDiagnostics();
    }
  },
  "check-update": async () => {
    app.update = { checking: true };
    renderPage();
    try {
      app.update = await api("GET", "/api/update");
    } catch (error) {
      app.update = { error: error.message };
    }
    renderNav();
    renderPage();
  },
};

document.addEventListener("click", (event) => {
  const element = event.target.closest("[data-action], [data-setting]");
  if (!element || element.disabled || element.closest(".dialog")) {
    return;
  }
  if (element.dataset.action && actions[element.dataset.action]) {
    event.preventDefault();
    actions[element.dataset.action](element);
    return;
  }
  // 开关类设置：点击即切换并保存。
  if (element.classList.contains("switch") && element.dataset.setting) {
    const path = element.dataset.setting;
    saveConfig((config) => setPath(config, path, !getPath(config, path)));
  }
});

document.addEventListener("change", (event) => {
  const element = event.target;
  if (element.closest(".dialog")) {
    return;
  }
  if (element.matches("select[data-setting]")) {
    const path = element.dataset.setting;
    const value = element.value;
    saveConfig((config) => {
      if (path === "auto_switch.default") {
        config.auto_switch.default_action = value.startsWith("use:") ? "use" : value;
        config.auto_switch.default_profile = value.startsWith("use:") ? value.slice(4) : "";
      } else if (typeof getPath(config, path) === "number") {
        setPath(config, path, Number(value));
      } else {
        setPath(config, path, value);
      }
    });
    return;
  }
  if (element.matches("input[data-setting-text]")) {
    const path = element.dataset.settingText;
    const value = element.value.trim();
    if (value !== getPath(app.config, path)) {
      saveConfig((config) => setPath(config, path, value));
    }
    return;
  }
  if (element.dataset.rule !== undefined) {
    const index = Number(element.dataset.rule);
    const fieldName = element.dataset.ruleField;
    const value = element.value;
    saveConfig((config) => {
      const rule = config.auto_switch.rules[index];
      if (fieldName === "action") {
        rule.action = value === "off" ? "off" : "use";
        rule.profile = value === "off" ? "" : value.slice(4);
      } else {
        rule[fieldName] = fieldName === "value" ? value.trim() : value;
      }
    });
  }
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && event.target.matches(".page input.input")) {
    event.target.blur();
  }
});

window.addEventListener("hashchange", () => {
  const pageId = location.hash.slice(1);
  if (pageId && pageId !== app.page) {
    goto(pageId);
  }
});

document.addEventListener("visibilitychange", () => {
  if (!document.hidden && app.state) {
    refreshState(true).catch(() => {});
  }
});

// ---------- 连接断开 ----------

connectionLost = (reason) => {
  if (document.getElementById("blocker")) {
    return;
  }
  clearTimeout(pollTimer);
  document.title = "已失效 · ProxySwitch 设置";
  const blocker = document.createElement("div");
  blocker.className = "blocker";
  blocker.id = "blocker";
  const title = reason === "expired" ? "这个设置页面已经失效" : "ProxySwitch 已经退出";
  const message = reason === "expired"
    ? "ProxySwitch 重新启动过。请关闭这个窗口，再从托盘菜单打开「设置」。"
    : "托盘程序没有在运行。重新打开 ProxySwitch 后，从托盘菜单打开「设置」。";
  setHtml(blocker, html`<div class="card">${logoSvg(grayTrack, false, "empty-logo")}<h2>${title}</h2><p class="muted">${message}</p><button class="button accent" data-close-window>关闭窗口</button></div>`);
  blocker.querySelector("[data-close-window]").addEventListener("click", () => window.close());
  document.body.appendChild(blocker);
};

// ---------- 启动 ----------

async function start() {
  if (!pageToken) {
    connectionLost("expired");
    return;
  }
  try {
    const state = await api("GET", "/api/state");
    const initialPage = location.hash.slice(1);
    if (pageRenderers[initialPage]) {
      app.page = initialPage;
    }
    app.state = state;
    app.config = clone(state.config);
    app.navigateSerial = state.navigate ? state.navigate.serial : 0;
    applyAppearance();
    renderShell();
    renderNav();
    renderPage({ animate: true });
    if (app.page === "diagnostics") {
      loadDiagnostics();
    }
    schedulePoll();
  } catch (error) {
    // 连接问题由 connectionLost 显示。
  }
}

start();
