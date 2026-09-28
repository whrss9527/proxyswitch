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
  installing: false,
  installError: "",
  restarting: false,
  coreInstalling: false,
  navigateSerial: 0,
  // 局域网共享页：正在使用的设备和最近的连接、经共享端口测试的结果、填错的地址或端口。
  shareActivity: null,
  shareTest: null,
  shareInputError: "",
  shareFirewallBusy: false,
  // 代理页里还没添加的自定义规则：页面重绘（例如刚保存的上一条返回了最新状态）时不丢。
  customRuleDraft: { value: "", policy: "proxy", type: "", error: "" },
  // 网址诊断页：输入的网址、视角、最近一次诊断的进度和结果。
  diagnoseUrl: "",
  diagnosePerspective: "pc",
  diagnoseJob: null,
  diagnoseError: "",
};

let diagnoseTimer = 0;

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
    runRequestedAction(navigate.action, navigate.argument);
    return;
  }
  renderNav();
  // WinHTTP 一栏的「当前代理」跟着代理的开关和配置变。
  if (app.page === "system" && previous && app.winhttp && app.winhttp.info && (previous.status.state !== state.status.state || previous.status.profile !== state.status.profile)) {
    refreshWinHttp(true);
  }
  const stateChanged = !previous || JSON.stringify(previous) !== JSON.stringify(state);
  if (options.force || stateChanged) {
    renderPage({ fromPoll: Boolean(options.fromPoll) });
  }
}

// 程序可以请求页面执行的操作，例如托盘菜单的「检查更新」、命令行的「diagnose 网址」（argument 是 JSON：url、perspective）。
const requestableActions = new Set(["check-update"]);

function runRequestedAction(action, argument = "") {
  if (requestableActions.has(action)) {
    actions[action]();
  } else if (action === "install-update") {
    installRequestedUpdate();
  } else if (action === "import-subscription") {
    let request = {};
    try {
      request = JSON.parse(argument || "{}");
    } catch (error) {
      request = {};
    }
    importSubscription(request.url || "", request.name || "");
  } else if (action === "diagnose") {
    let request = {};
    try {
      request = JSON.parse(argument || "{}");
    } catch (error) {
      request = {};
    }
    startDiagnose(request.url || "", request.perspective || "pc");
  }
}

// installRequestedUpdate 是系统通知上的「立即更新」：还不知道新版本时先检查，能安装就直接安装。
async function installRequestedUpdate() {
  if (!knownUpdate()) {
    await actions["check-update"]();
  }
  actions["install-update"]();
}

// importSubscription 处理机场网站的「一键导入」：打开添加订阅的对话框，填好地址并检查；已经添加过时提示是哪个配置。
function importSubscription(url, name) {
  if (!url) {
    return;
  }
  if (!app.config) {
    toast("配置文件有错误，改好后再导入", "warning", "没有导入订阅");
    return;
  }
  const existing = app.config.profiles.find((profile) => profile.subscription === url);
  if (existing) {
    toast(`配置「${existing.name}」用的就是这个订阅`, "info", "已经添加过这个订阅");
    return;
  }
  openProfileEditor({ subscription: url, name }, { kind: "subscription", check: true });
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
        if (app.page === "share") {
          await refreshShareActivity(true);
        }
      } catch (error) {
        // 连接问题由 connectionLost 处理。
      }
    }
    schedulePoll();
  }, pollInterval);
}

// refreshShareActivity 读取经共享入口上网的设备和最近的连接，有变化时重绘共享页。
async function refreshShareActivity(fromPoll = false) {
  const activity = await api("GET", "/api/share/activity");
  const changed = JSON.stringify(activity) !== JSON.stringify(app.shareActivity);
  app.shareActivity = activity;
  if (changed && app.page === "share") {
    renderPage({ fromPoll });
  }
}

function applyAppearance() {
  const theme = currentTheme(app.config ? app.config.theme : "system");
  if (document.documentElement.dataset.theme !== theme) {
    document.documentElement.dataset.theme = theme;
    // 独立窗口的标题栏跟随页面背景色；设置里选了固定的深浅色时两条都改成这个颜色。
    for (const meta of document.querySelectorAll('meta[name="theme-color"]')) {
      meta.setAttribute("content", theme === "dark" ? "#202020" : "#f3f3f3");
    }
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
  const update = knownUpdate();
  const badges = {
    proxies: Boolean(app.state.config_error),
    about: Boolean(update && update.newer),
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
  // 正在输入的文字不能因为重绘丢掉（例如刚保存的另一项返回了最新状态）。
  const typing = focusId && active.tagName === "INPUT" ? { value: active.value, start: active.selectionStart, end: active.selectionEnd } : null;
  setHtml(page, pageRenderers[app.page]());
  if (focusId) {
    const target = document.getElementById(focusId);
    if (target) {
      if (typing && target.tagName === "INPUT" && target.value !== typing.value) {
        target.value = typing.value;
        try {
          target.setSelectionRange(typing.start, typing.end);
        } catch (error) {
          // 有的输入框类型不支持选区。
        }
      }
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
  if (pageId === "share") {
    refreshShareActivity().catch(() => {});
  }
  if (pageId === "system" && (!app.loopback || app.loopback.error)) {
    refreshLoopback();
  }
  if (pageId === "system") {
    refreshWsl();
    refreshWinHttp();
  }
  if (pageId === "diagnose" && !app.diagnoseJob) {
    loadDiagnose();
  }
  if (location.hash !== `#${pageId}`) {
    history.replaceState(null, "", `/#${pageId}`);
  }
}

// ---------- 操作 ----------

// runOperation 执行开关、切换等操作，完成后刷新状态；部分失败时程序返回 409 和最新状态，failureTitle 是这时提示的标题。
async function runOperation(path, body, successMessage, failureTitle = "部分设置没有成功") {
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
      toast(error.message, "warning", failureTitle);
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

// installCore 下载代理内核，下载期间更频繁地同步状态显示进度；onProgress 给对话框重绘用。
async function installCore(onProgress = () => {}) {
  if (app.coreInstalling) {
    return;
  }
  app.coreInstalling = true;
  renderPage();
  onProgress();
  const timer = setInterval(async () => {
    try {
      await refreshState(true);
    } catch (error) {
      // 连接问题由 connectionLost 处理。
    }
    onProgress();
  }, 500);
  try {
    receiveState(await api("POST", "/api/core/install"), { force: true });
    toast("可以使用订阅了", "success", "代理内核已下载");
  } catch (error) {
    if (error.state) {
      receiveState(error.state, { force: true });
    }
    toast(error.message, "danger", "内核没有下载成功");
  } finally {
    clearInterval(timer);
    app.coreInstalling = false;
    renderPage();
    onProgress();
  }
}

async function updateSubscription(profile) {
  try {
    receiveState(await api("POST", `/api/subscriptions/${profile.id}/update`), { force: true });
    toast(`共 ${(app.state.subscriptions[profile.id] || {}).nodes || 0} 个节点`, "success", `「${profile.name}」的订阅已更新`);
  } catch (error) {
    if (error.state) {
      receiveState(error.state, { force: true });
    }
    toast(error.message, "danger", "订阅没有更新成功");
  }
}

async function updateRules(profile) {
  try {
    receiveState(await api("POST", `/api/subscriptions/${profile.id}/rules`), { force: true });
    const info = (app.state.rules || {})[profile.id] || {};
    toast(`共 ${(info.rules || 0).toLocaleString()} 条规则`, "success", `「${profile.name}」的分流规则已更新`);
  } catch (error) {
    if (error.state) {
      receiveState(error.state, { force: true });
    }
    toast(error.message, "danger", "分流规则没有更新成功");
  }
}

async function setMode(profile, mode) {
  try {
    receiveState(await api("POST", `/api/subscriptions/${profile.id}/mode`, { mode }), { force: true });
    toast(mode === "global" ? "所有网站都经过节点" : `分流规则：${rulesName(profile.rules)}`, "success", mode === "global" ? "已切换到全局代理" : "已切换到按规则分流");
  } catch (error) {
    if (error.state) {
      receiveState(error.state, { force: true });
    }
    toast(error.message, "danger", "没有切换成功");
  }
}

// addCustomRule 加一条自定义规则（type 为 program 时按程序分流）；已经有这个域名、IP 或程序时改它的去向并启用。
// 成功时返回空，否则返回问题。
function addCustomRule(text, policy, type = "") {
  const program = type === "program";
  const value = program ? normalizeProgramTarget(text) : normalizeRuleTarget(text);
  const problem = program ? programTargetProblem(value) : ruleTargetProblem(value);
  if (problem) {
    return problem;
  }
  const same = (rule) => (rule.type || "") === type && (program ? rule.value.toLowerCase() === value.toLowerCase() : rule.value === value);
  saveConfig((config) => {
    config.custom_rules = config.custom_rules || [];
    const existing = config.custom_rules.find(same);
    if (existing) {
      existing.policy = policy;
      existing.disabled = false;
    } else {
      config.custom_rules.push(program ? { type, value, policy } : { value, policy });
    }
  }, `${value} ${policyLabels[policy]}`);
  return "";
}

// loadPrograms 读取正在运行的程序，给按程序分流的规则选程序名。
async function loadPrograms() {
  try {
    app.programs = (await api("GET", "/api/programs")).programs || [];
  } catch (error) {
    app.programs = [];
  }
  const list = document.getElementById("running-programs");
  if (list) {
    setHtml(list, html`${app.programs.map((name) => html`<option value="${name}"></option>`)}`);
  }
}

function submitCustomRule() {
  const input = document.querySelector('[data-focus="custom-rule-value"]');
  const policy = document.querySelector('[data-focus="custom-rule-policy"]');
  const error = document.querySelector("[data-custom-rule-error]");
  if (!input || !policy) {
    return;
  }
  const problem = addCustomRule(input.value, policy.value, app.customRuleDraft.type);
  // 提示记在草稿里：页面随状态刷新时重新画出来，不会一闪就没了。
  app.customRuleDraft.error = problem;
  if (error) {
    error.textContent = problem;
  }
  if (!problem) {
    input.value = "";
    app.customRuleDraft.value = "";
  }
}

// ---------- 网址诊断 ----------

// loadDiagnose 读取最近一次诊断（例如刷新页面后），正在进行时继续跟进。
async function loadDiagnose() {
  try {
    const job = await api("GET", "/api/diagnose");
    if (job.serial) {
      receiveDiagnose(job);
    }
  } catch (error) {
    // 连接问题由 connectionLost 处理。
  }
}

function receiveDiagnose(job) {
  const changed = JSON.stringify(job) !== JSON.stringify(app.diagnoseJob);
  app.diagnoseJob = job;
  if (!app.diagnoseUrl) {
    app.diagnoseUrl = job.url;
  }
  if (changed && app.page === "diagnose") {
    renderPage({ fromPoll: true });
  }
  clearTimeout(diagnoseTimer);
  if (job.running) {
    diagnoseTimer = setTimeout(async () => {
      try {
        receiveDiagnose(await api("GET", "/api/diagnose"));
      } catch (error) {
        // 连接问题由 connectionLost 处理。
      }
    }, 400);
  }
}

// startDiagnose 开始诊断网址（为空时用输入框里的），perspective 为空时保持当前的视角。
async function startDiagnose(address = "", perspective = "") {
  const input = document.getElementById("diagnose-url");
  if (address) {
    app.diagnoseUrl = address;
  } else if (input) {
    app.diagnoseUrl = input.value.trim();
  }
  if (perspective) {
    app.diagnosePerspective = perspective;
  }
  if (app.page !== "diagnose") {
    goto("diagnose");
  }
  if (document.activeElement && document.activeElement.blur) {
    // 输入框有焦点时定时刷新不重绘页面，诊断的进度就显示不出来。
    document.activeElement.blur();
  }
  if (!app.diagnoseUrl) {
    app.diagnoseError = "请填写网址或域名，例如 youtube.com";
    renderPage();
    return;
  }
  app.diagnoseError = "";
  try {
    const job = await api("POST", "/api/diagnose", { url: app.diagnoseUrl, perspective: app.diagnosePerspective });
    app.diagnoseUrl = job.url;
    receiveDiagnose(job);
    renderPage();
  } catch (error) {
    if (error.status !== 0 && error.status !== 403) {
      app.diagnoseError = error.message;
      renderPage();
    }
  }
}

async function stopDiagnose() {
  try {
    receiveDiagnose(await api("POST", "/api/diagnose/cancel"));
    renderPage();
  } catch (error) {
    // 连接问题由 connectionLost 处理。
  }
}

// runDiagnoseAction 执行结论里的操作，改动生效后再诊断一次。
async function runDiagnoseAction(action) {
  const job = app.diagnoseJob;
  const again = () => setTimeout(() => startDiagnose(), 1500);
  switch (action.kind) {
    case "turn_on":
      if (await runOperation("/api/use", { name: action.profile }, `已开启「${action.profile}」`)) {
        again();
      }
      break;
    case "pin_to_proxy": {
      const problem = addCustomRule(action.host, "proxy");
      if (problem) {
        toast(problem, "warning", "没有添加");
        return;
      }
      await saveQueue;
      again();
      break;
    }
    case "auto_select":
      try {
        await api("POST", `/api/subscriptions/${encodeURIComponent(job.subscription)}/select`, { node: "" });
        toast("改为自动选择延迟最低的节点", "success", "已切换");
        again();
      } catch (error) {
        toast(error.message, "danger", "没有切换成功");
      }
      break;
    case "test_nodes":
    case "open_nodes":
      if (job.subscription && profileById(job.subscription)) {
        openNodesDialog(job.subscription, { test: action.kind === "test_nodes" });
      } else {
        goto("proxies");
      }
      break;
    case "open_share":
      goto("share");
      break;
    case "open_proxies":
      goto("proxies");
      break;
    case "copy_report":
      if (await copyText(job.report)) {
        toast("可以粘贴到问题反馈里，或者发给帮你排查的人", "success", "已复制诊断报告");
      }
      break;
  }
}

// ---------- 局域网共享 ----------

async function testShare() {
  const port = app.config.share.port;
  app.shareTest = { running: true };
  renderPage();
  try {
    app.shareTest = { result: await api("POST", "/api/test", { server: `127.0.0.1:${port}` }) };
  } catch (error) {
    app.shareTest = { result: { ok: false, message: error.message } };
  }
  renderPage();
}

// refreshLoopback 读取商店应用和它们能不能连接本机的代理（系统集成页）。
async function refreshLoopback() {
  app.loopback = { loading: true };
  renderPage();
  try {
    app.loopback = { info: await api("GET", "/api/loopback") };
  } catch (error) {
    app.loopback = { error: error.message || "读不到应用" };
  }
  renderPage();
}

// refreshWsl 读取 WSL 的情况（系统集成页）。
async function refreshWsl() {
  if (!app.wsl || app.wsl.error) {
    app.wsl = { loading: true };
    renderPage();
  }
  try {
    app.wsl = { info: await api("GET", "/api/wsl") };
  } catch (error) {
    app.wsl = { error: error.message || "读不到 WSL 的情况" };
  }
  renderPage();
}

// refreshWinHttp 读取 WinHTTP 的代理（系统集成页）；quiet 时不显示正在读取，读不到也保留原来的内容。
async function refreshWinHttp(quiet = false) {
  if (!quiet && (!app.winhttp || app.winhttp.error)) {
    app.winhttp = { loading: true };
    renderPage();
  }
  try {
    app.winhttp = { info: await api("GET", "/api/winhttp") };
  } catch (error) {
    if (!quiet) {
      app.winhttp = { error: error.message || "读不到 WinHTTP 的代理" };
    }
  }
  renderPage();
}

// setWinHttp 把 WinHTTP 的代理设为当前代理（useProxy）或改回直连，要管理员确认。
async function setWinHttp(useProxy) {
  app.winhttp = { ...app.winhttp, busy: true };
  renderPage();
  try {
    app.winhttp = { info: await api("POST", "/api/winhttp", { proxy: useProxy }) };
    toast(useProxy ? "Windows 更新等系统服务现在经过这个代理" : "Windows 更新等系统服务现在直接连接", "success", useProxy ? "已设置 WinHTTP 的代理" : "WinHTTP 已改回直连");
  } catch (error) {
    app.winhttp = { ...app.winhttp, busy: false };
    if (error.status !== 0 && error.status !== 403) {
      toast(error.message, "warning", "没有修改");
    }
  }
  renderPage();
}

// runWsl 设置或重启 WSL，完成后显示结果。
async function runWsl(path, successTitle, successText) {
  app.wsl = { ...app.wsl, busy: true };
  renderPage();
  try {
    app.wsl = { info: await api("POST", path) };
    toast(successText, "success", successTitle);
  } catch (error) {
    app.wsl = { ...app.wsl, busy: false };
    if (error.status !== 0 && error.status !== 403) {
      toast(error.message, "warning", "没有成功");
    }
  }
  renderPage();
}

// saveLoopback 让 exempt 这些商店应用可以连接本机的代理（要管理员确认），成功时返回 true。
async function saveLoopback(exempt) {
  app.loopback = { ...app.loopback, saving: true };
  renderPage();
  try {
    app.loopback = { info: await api("POST", "/api/loopback", { exempt }) };
    toast("重新打开这些应用后生效", "success", "已修改商店应用的设置");
    return true;
  } catch (error) {
    app.loopback = { ...app.loopback, saving: false };
    if (error.status !== 0 && error.status !== 403) {
      toast(error.message, "warning", "没有修改");
    }
    return false;
  } finally {
    renderPage();
  }
}

async function allowShareFirewall() {
  app.shareFirewallBusy = true;
  renderPage();
  try {
    await api("POST", "/api/share/firewall");
    toast("局域网里的设备现在可以连到这台电脑的共享端口", "success", "已允许通过 Windows 防火墙");
  } catch (error) {
    if (error.status !== 0 && error.status !== 403) {
      toast(error.message, "warning", "没有放行");
    }
  } finally {
    app.shareFirewallBusy = false;
    renderPage();
  }
}

function openShareRuleMenu(anchor, connection) {
  const host = connection.host;
  const items = hasSubscriptions() ? Object.entries(policyLabels).map(([policy, label]) => ({
    label: `让 ${host} ${label}`,
    icon: policy === "reject" ? "close" : policy === "direct" ? "link" : "globe",
    action: () => {
      const problem = addCustomRule(host, policy);
      if (problem) {
        toast(problem, "warning", "没有添加");
      }
    },
  })) : [];
  if (items.length) {
    items.push({ separator: true });
  }
  const scheme = connection.port === "80" ? "http" : "https";
  const port = connection.port && connection.port !== "80" && connection.port !== "443" ? `:${connection.port}` : "";
  items.push({ label: `诊断 ${host}`, icon: "stethoscope", action: () => startDiagnose(`${scheme}://${host}${port}/`, "device") });
  openMenu(anchor, items);
}

// saveShareInput 保存共享页里改过的允许的设备或端口；填错时在输入框下面说明，不保存。
function saveShareInput(element) {
  const value = element.value.trim();
  const isPort = element.matches("[data-share-port]");
  const problem = isPort ? sharePortProblem(value) : shareClientsProblem(value);
  const changed = problem !== app.shareInputError;
  app.shareInputError = problem;
  if (problem) {
    if (changed) {
      renderPage();
    }
    return;
  }
  const current = isPort ? app.config.share.port : app.config.share.allowed;
  const next = isPort ? Number(value) : value;
  if (next === current) {
    if (changed) {
      renderPage();
    }
    return;
  }
  if (isPort) {
    app.shareTest = null;
  }
  saveConfig((config) => {
    if (isPort) {
      config.share.port = next;
    } else {
      config.share.allowed = next;
    }
  }, isPort ? `共享端口改为 ${next}，设备上也要跟着改` : "");
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
  if (diagnostics.last_crash) {
    lines.push("", `最近一次意外退出（${diagnostics.last_crash_time}）：`, diagnostics.last_crash.split("\n").slice(0, 60).join("\n"));
  }
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
  const subscriptionItems = profile.subscription ? [
    { label: "选择节点", icon: "list", action: () => openNodesDialog(profile.id) },
    profile.mode === "global"
      ? { label: "切换到按规则分流", icon: "swap", action: () => setMode(profile, "rule") }
      : { label: "切换到全局代理", icon: "swap", action: () => setMode(profile, "global") },
    { label: "更新订阅", icon: "refresh", action: () => updateSubscription(profile) },
    ...(profile.rules ? [{ label: "更新分流规则", icon: "refresh", action: () => updateRules(profile) }] : []),
  ] : [];
  openMenu(anchor, [
    ...subscriptionItems,
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
  "add-subscription": () => openProfileEditor({}, { kind: "subscription" }),
  nodes: (element) => openNodesDialog(element.dataset.id),
  "install-core": () => installCore(),
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
  "open-proxy-settings": () => api("POST", "/api/open-url", { url: "ms-settings:network-proxy" }).catch((error) => toast(error.message, "danger", "无法打开")),
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
  "add-custom-rule": () => submitCustomRule(),
  "custom-rule-toggle": (element) => saveConfig((config) => {
    const rule = config.custom_rules[Number(element.dataset.index)];
    rule.disabled = !rule.disabled;
  }),
  "custom-rule-delete": (element) => saveConfig((config) => config.custom_rules.splice(Number(element.dataset.index), 1), "已删除自定义规则"),
  "apply-auto-switch": () => runOperation("/api/auto-switch/apply", undefined, "已按当前网络应用规则"),
  "share-toggle": () => {
    const enabled = !app.state.config.share.enabled;
    app.shareTest = null;
    runOperation("/api/share", { enabled }, enabled ? "局域网共享已开启" : "局域网共享已关闭", enabled ? "局域网共享没有打开" : "局域网共享没有关闭");
  },
  "copy-share-address": async (element) => {
    if (await copyText(element.dataset.address)) {
      toast("在设备的代理服务器设置里填这个地址和端口", "success", `已复制 ${element.dataset.address}`);
    }
  },
  "share-test": () => testShare(),
  "share-firewall": () => allowShareFirewall(),
  "loopback-refresh": () => refreshLoopback(),
  "winhttp-refresh": () => refreshWinHttp(),
  "winhttp-proxy": () => setWinHttp(true),
  "winhttp-direct": () => setWinHttp(false),
  "wsl-refresh": () => refreshWsl(),
  "wsl-setup": () => runWsl("/api/wsl/setup", "已设置 WSL", "重启 WSL 后，WSL 里的程序就会使用本机的代理"),
  "wsl-reset": () => runWsl("/api/wsl/reset", "已撤销", "重启 WSL 后恢复默认的网络设置"),
  "wsl-restart": () => runWsl("/api/wsl/restart", "已重启 WSL", "重新打开 WSL 的终端就会用上新的设置"),
  "copy-wsl-command": async (element) => {
    if (await copyText(element.dataset.command)) {
      toast("打开局域网共享后，在 WSL 里粘贴运行", "success", "已复制");
    }
  },
  "loopback-all": () => app.loopback && app.loopback.info && saveLoopback(app.loopback.info.apps.map((item) => item.sid)),
  "loopback-choose": () => openLoopbackDialog(),
  "take-over-clash-links": () => runOperation("/api/links/clash", {}, "机场网站的「一键导入 Clash」现在会打开 ProxySwitch", "没有改过来"),
  "share-clear": async () => {
    try {
      app.shareActivity = await api("POST", "/api/share/clear");
      renderPage();
    } catch (error) {
      // 连接问题由 connectionLost 处理。
    }
  },
  "diagnose-start": () => startDiagnose(),
  "diagnose-stop": () => stopDiagnose(),
  "diagnose-perspective": (element) => {
    app.diagnosePerspective = element.dataset.value;
    const input = document.getElementById("diagnose-url");
    if (input) {
      app.diagnoseUrl = input.value.trim();
    }
    renderPage();
  },
  "diagnose-action": (element) => {
    const job = app.diagnoseJob;
    const action = job && job.verdict && job.verdict.actions[Number(element.dataset.index)];
    if (action) {
      runDiagnoseAction(action);
    }
  },
  "share-rule-menu": (element) => {
    const connection = app.shareActivity && app.shareActivity.recent[Number(element.dataset.index)];
    if (connection) {
      openShareRuleMenu(element, connection);
    }
  },
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
    if ((app.update && app.update.checking) || app.installing || app.restarting) {
      return;
    }
    app.update = { checking: true };
    app.installError = "";
    renderPage();
    try {
      app.update = await api("GET", "/api/update");
    } catch (error) {
      app.update = { error: error.message };
    }
    renderNav();
    renderPage();
  },
  "install-update": async () => {
    const update = knownUpdate();
    if (!update || !update.can_install || app.installing) {
      return;
    }
    app.update = update;
    app.installing = true;
    app.installError = "";
    renderPage();
    // 下载期间更频繁地同步状态，显示进度。
    const progressTimer = setInterval(() => refreshState(true).catch(() => {}), 500);
    try {
      await api("POST", "/api/update/install");
      app.restarting = true;
      // 马上改掉窗口标题：新版本启动时按标题查找已打开的设置窗口，不能找到这个即将关闭的窗口。
      document.title = "正在重新启动 · ProxySwitch";
      setTimeout(() => window.close(), 2500);
    } catch (error) {
      app.installError = error.message;
    } finally {
      clearInterval(progressTimer);
      app.installing = false;
    }
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
  if (element.matches("input[data-setting-number]")) {
    const path = element.dataset.settingNumber;
    const value = Number(element.value.trim());
    if (!Number.isInteger(value) || value < 1 || value > 65535) {
      toast("请填 1~65535 之间的数字", "warning", "端口不对");
      element.value = getPath(app.config, path);
      return;
    }
    if (value !== getPath(app.config, path)) {
      saveConfig((config) => setPath(config, path, value));
    }
    return;
  }
  if (element.matches("input[data-share-allowed], input[data-share-port]")) {
    saveShareInput(element);
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
  if (element.matches('[data-focus="custom-rule-policy"]')) {
    app.customRuleDraft.policy = element.value;
    return;
  }
  if (element.matches('[data-focus="custom-rule-type"]')) {
    app.customRuleDraft.type = element.value;
    app.customRuleDraft.error = "";
    renderPage();
    if (element.value === "program") {
      loadPrograms();
    }
    const input = document.querySelector('[data-focus="custom-rule-value"]');
    if (input) {
      input.focus();
    }
    return;
  }
  if (element.dataset.customRule !== undefined) {
    const index = Number(element.dataset.customRule);
    const value = element.value;
    saveConfig((config) => {
      config.custom_rules[index].policy = value;
    });
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

document.addEventListener("input", (event) => {
  if (event.target.matches('[data-focus="custom-rule-value"]')) {
    app.customRuleDraft.value = event.target.value;
    if (app.customRuleDraft.error) {
      app.customRuleDraft.error = "";
      const error = document.querySelector("[data-custom-rule-error]");
      if (error) {
        error.textContent = "";
      }
    }
  }
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && event.target.matches('[data-focus="custom-rule-value"]')) {
    submitCustomRule();
    return;
  }
  if (event.key === "Enter" && event.target.matches("#diagnose-url")) {
    startDiagnose();
    return;
  }
  if (event.key === "Enter" && event.target.matches(".page input.input")) {
    event.target.blur();
  }
});

// 像普通程序一样不弹出浏览器的右键菜单；输入框和选中的文字除外，方便复制粘贴。
document.addEventListener("contextmenu", (event) => {
  const selection = window.getSelection();
  if (event.target.closest("input, textarea") || (selection && !selection.isCollapsed)) {
    return;
  }
  event.preventDefault();
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
  let title = reason === "expired" ? "这个设置页面已经失效" : "ProxySwitch 已经退出";
  let message = reason === "expired"
    ? "ProxySwitch 重新启动过。请关闭这个窗口，再从托盘菜单打开「设置」。"
    : "托盘程序没有在运行。重新打开 ProxySwitch 后，从托盘菜单打开「设置」。";
  if (app.restarting) {
    title = "正在重新启动";
    message = "新版本启动后会打开新的设置窗口，可以关闭这个窗口。";
  }
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
    if (app.page === "share") {
      refreshShareActivity().catch(() => {});
    }
    if (app.page === "system") {
      refreshLoopback();
      refreshWsl();
      refreshWinHttp();
    }
    // 窗口是为程序的请求打开的（地址里就是请求的页面），例如托盘菜单的「检查更新」：执行请求的操作。
    const requested = state.navigate && state.navigate.page === initialPage && state.navigate.action;
    if (requested) {
      runRequestedAction(state.navigate.action, state.navigate.argument);
    }
    if (app.page === "diagnose" && !(requested && state.navigate.action === "diagnose")) {
      loadDiagnose();
    }
    schedulePoll();
  } catch (error) {
    // 连接问题由 connectionLost 显示。
  }
}

start();
