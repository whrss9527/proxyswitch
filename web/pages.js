"use strict";

// 各页面的内容：代理、自动切换、常规、诊断、关于。每个函数根据 app 里的状态生成整页 HTML。

const pages = [
  { id: "proxies", label: "代理", icon: "globe" },
  { id: "network", label: "自动切换", icon: "wifi" },
  { id: "general", label: "常规", icon: "sliders" },
  { id: "diagnostics", label: "诊断", icon: "pulse" },
  { id: "about", label: "关于", icon: "info" },
];

const grayTrack = "#8b919a";
const amberTrack = "#d97706";

function findProfile(name) {
  return app.config ? app.config.profiles.find((profile) => profile.name === name) : null;
}

function targetsText(profile) {
  const labels = { system: "系统代理", env: "环境变量", git: "git", npm: "npm" };
  return (profile.apply_to || []).map((target) => labels[target] || target).join("、");
}

function switchButton({ checked, setting = "", action = "", label = "", disabled = false }) {
  return html`<button class="switch" role="switch" aria-checked="${checked}" ${setting ? html`data-setting="${setting}"` : ""} ${action ? html`data-action="${action}"` : ""} aria-label="${label}" ${disabled ? raw("disabled") : ""}></button>`;
}

function select(setting, value, options, attributes = "") {
  return html`<select class="select" data-setting="${setting}" ${raw(attributes)}>${options.map(([optionValue, label]) => html`<option value="${optionValue}" ${String(optionValue) === String(value) ? raw("selected") : ""}>${label}</option>`)}</select>`;
}

function settingCard({ iconName, title, description = "", control = "", note = "", className = "" }) {
  return html`
    <div class="setting ${className}">
      ${iconName ? icon(iconName, "large") : html`<span></span>`}
      <div>
        <div class="setting-title">${title}</div>
        ${description ? html`<div class="setting-description">${description}</div>` : ""}
      </div>
      <div class="setting-control">${control}</div>
      ${note ? html`<div class="setting-note">${note}</div>` : ""}
    </div>`;
}

function saveIndicator() {
  if (app.saving) {
    return html`<span class="caption muted" style="display:inline-flex;gap:6px;align-items:center"><span class="spinner" style="width:12px;height:12px"></span>正在保存</span>`;
  }
  return html`<span class="caption faint">修改后自动保存并立即生效</span>`;
}

function pageHeader(title, extra = "") {
  return html`<div style="display:flex;align-items:baseline;gap:16px;margin-bottom:20px"><h1 class="page-title" style="margin:0">${title}</h1>${extra}</div>`;
}

function configErrorView() {
  return html`
    <div class="infobar danger">
      ${icon("error")}
      <div class="infobar-body">
        <div class="infobar-title">配置文件有错误，无法读取</div>
        <div style="margin-top:4px;white-space:pre-wrap">${app.state.config_error}</div>
        <div class="infobar-actions">
          <button class="button" data-action="open-config-file">${icon("document")}用编辑器打开配置文件</button>
          <button class="button" data-action="reset-config">恢复为默认配置</button>
        </div>
      </div>
    </div>`;
}

// ---------- 代理 ----------

function heroView() {
  const status = app.state.status;
  const profile = findProfile(status.profile);
  const busy = app.busy ? "busy" : "";
  let track = grayTrack;
  let title = "代理已关闭";
  let subtitle = html``;
  let health = html``;
  let checked = false;
  let warn = "";
  if (status.state === "on" && profile) {
    track = profile.color;
    checked = true;
    warn = status.health === "down" ? "warn" : "";
    title = "代理已开启";
    subtitle = html`<strong style="color:var(--text)">${profile.name}</strong><span class="mono">${describeServer(profile)}</span><span class="chips">${status.applied.map((label) => html`<span class="chip">${label}</span>`)}</span>`;
    const latency = app.latency[profile.id];
    if (status.health === "down") {
      health = html`<span class="dot" style="background:var(--danger)"></span><span style="color:var(--danger)">连不上代理服务器：${status.health_message}</span>`;
    } else if (latency && latency.running) {
      health = html`<span class="spinner" style="width:12px;height:12px"></span>正在测速…`;
    } else if (latency && latency.ok) {
      health = html`<span class="dot" style="background:var(--success)"></span>连接正常，延迟 <span class="numeric">${latency.millis} ms</span>`;
    } else if (latency) {
      health = html`<span class="dot" style="background:var(--danger)"></span><span style="color:var(--danger)">${latency.message}</span>`;
    } else if (status.health === "ok") {
      health = html`<span class="dot" style="background:var(--success)"></span>代理服务器可以连接`;
    }
  } else if (status.state === "external") {
    track = amberTrack;
    checked = true;
    title = "系统代理由其他程序设置";
    subtitle = html`<span class="mono">${status.external}</span>`;
    health = html`<span>可能是代理软件或 Windows 设置里开启的。点开关会改为直接连接；保存为配置后，就能用 ProxySwitch 随时开关它。</span>`;
  } else if (profile) {
    subtitle = html`<span>打开后使用</span><strong style="color:var(--text)">${profile.name}</strong><span class="mono">${describeServer(profile)}</span>`;
    if (status.health === "down") {
      health = html`<span class="dot" style="background:var(--warning)"></span><span>${status.health_message}</span>`;
    }
  } else {
    subtitle = html`<span>还没有代理配置</span>`;
  }
  const hotkey = app.state.hotkeys.toggle && !app.state.hotkeys.toggle_error ? app.state.hotkeys.toggle : "";
  const canTest = status.state === "on" && profile && profile.server;
  const terminal = status.state === "on" && status.terminal && status.terminal.length > 0;
  const saveExternal = status.state === "external" && status.external_profile;
  return html`
    <div class="card hero" style="--tint:${track === grayTrack ? "transparent" : track}">
      <button class="hero-switch ${busy} ${warn}" role="switch" aria-checked="${checked}" aria-label="开关代理" data-action="toggle" style="--track:${track}"></button>
      <div class="hero-text">
        <div class="hero-title">${title}</div>
        <div class="hero-subtitle">${subtitle}</div>
        ${health.text ? html`<div class="hero-health">${health}</div>` : ""}
      </div>
      <div class="hero-actions">
        ${canTest || terminal || saveExternal ? html`<div class="hero-buttons">
          ${canTest ? html`<button class="button" data-action="test-profile" data-id="${profile.id}">${icon("gauge")}测速</button>` : ""}
          ${terminal ? html`<button class="button" data-action="terminal-menu" title="复制在当前终端里使用代理的命令" aria-haspopup="menu">${icon("terminal")}终端命令</button>` : ""}
          ${saveExternal ? html`<button class="button" data-action="save-external">${icon("plus")}保存为配置</button>` : ""}
        </div>` : ""}
        ${hotkey ? html`<span class="caption faint" style="display:flex;gap:6px;align-items:center">快捷键 ${hotkeyKeys(hotkey)}</span>` : ""}
      </div>
    </div>`;
}

function latencyBadge(profile) {
  const latency = app.latency[profile.id];
  if (!latency) {
    return html``;
  }
  if (latency.running) {
    return html`<span class="badge"><span class="spinner" style="width:10px;height:10px;border-width:1.5px"></span>测速中</span>`;
  }
  if (!latency.ok) {
    return html`<span class="badge danger" title="${latency.message}">失败</span>`;
  }
  const level = latency.millis < 300 ? "success" : latency.millis < 1000 ? "warning" : "danger";
  return html`<span class="badge ${level} numeric" title="${latency.message}">${latency.millis} ms</span>`;
}

function profileRow(profile, index) {
  const status = app.state.status;
  const active = status.state === "on" && status.profile === profile.name;
  const modifiers = app.config.profile_hotkeys;
  const hotkey = modifiers && index < 9 ? `${modifiers}+${index + 1}` : "";
  return html`
    <div class="card profile ${active ? "active" : ""}" style="--profile-color:${profile.color}" data-profile-id="${profile.id}">
      <div class="profile-marker"></div>
      <div style="min-width:0">
        <div class="profile-name"><span>${profile.name}</span></div>
        <div class="profile-meta">
          <span class="mono">${describeServer(profile)}</span>
          <span>${targetsText(profile)}</span>
          ${hotkey ? html`<kbd class="compact" title="快捷键">${hotkey}</kbd>` : ""}
        </div>
      </div>
      <div class="profile-actions">
        <span class="latency">${latencyBadge(profile)}</span>
        ${active
          ? html`<span class="using">${icon("check")}正在使用</span>`
          : html`<button class="button" data-action="use" data-name="${profile.name}" ${app.busy ? raw("disabled") : ""}>使用</button>`}
        <button class="button subtle icon-only" data-action="edit" data-id="${profile.id}" title="编辑" aria-label="编辑「${profile.name}」">${icon("pencil")}</button>
        <button class="button subtle icon-only" data-action="profile-menu" data-id="${profile.id}" title="更多" aria-label="更多操作">${icon("more")}</button>
      </div>
    </div>`;
}

function emptyView() {
  return html`
    <div class="card empty">
      ${logoSvg(app.state.palette[0], true, "empty-logo")}
      <h2>添加第一个代理配置</h2>
      <p>ProxySwitch 用来一键开关 Windows 的代理。先告诉它代理的地址，之后单击任务栏右下角的托盘图标就能开关。</p>
      <div class="choices">
        <button class="choice" data-action="detect">${icon("search")}<strong>自动检测</strong><span>查找本机正在运行的代理软件，一键添加</span></button>
        <button class="choice" data-action="add">${icon("pencil")}<strong>手动填写</strong><span>填写代理服务器的地址和端口</span></button>
        <button class="choice" data-action="add-pac">${icon("document")}<strong>PAC 脚本</strong><span>公司或学校提供了自动配置脚本地址时使用</span></button>
      </div>
    </div>`;
}

function proxiesPage() {
  if (!app.config) {
    return html`${pageHeader("代理")}${configErrorView()}`;
  }
  const profiles = app.config.profiles;
  const warnings = [];
  if (app.state.config_error) {
    warnings.push(html`
      <div class="infobar warning">${icon("warning")}<div class="infobar-body"><div class="infobar-title">配置文件里的修改有错误，仍在使用修改前的配置</div><div style="white-space:pre-wrap">${app.state.config_error}</div>
      <div class="infobar-actions"><button class="button" data-action="open-config-file">${icon("document")}打开配置文件</button></div></div></div>`);
  }
  if (app.state.hotkeys.toggle_error || app.state.hotkeys.profiles_error) {
    warnings.push(html`
      <div class="infobar warning">${icon("keyboard")}<div class="infobar-body"><div class="infobar-title">快捷键被其他程序占用</div>${[app.state.hotkeys.toggle_error, app.state.hotkeys.profiles_error].filter(Boolean).join("；")}
      <div class="infobar-actions"><button class="button" data-action="goto" data-page="general">更换快捷键</button></div></div></div>`);
  }
  return html`
    ${pageHeader("代理")}
    ${warnings}
    ${profiles.length === 0 && app.state.status.state === "off" ? "" : heroView()}
    ${profiles.length === 0 ? html`<div style="margin-top:16px">${emptyView()}</div>` : html`
      <div class="section-title">代理配置
        <div class="actions">
          <button class="button subtle" data-action="test-all">${icon("gauge")}全部测速</button>
          <button class="button subtle" data-action="detect">${icon("search")}检测本机代理</button>
          <button class="button accent" data-action="add">${icon("plus")}添加</button>
        </div>
      </div>
      <div class="stack">${profiles.map((profile, index) => profileRow(profile, index))}</div>
      <p class="caption faint" style="margin:14px 2px 0">单击托盘图标开关代理，右键托盘图标可以快速切换配置。</p>`}`;
}

// ---------- 自动切换 ----------

function networkPage() {
  if (!app.config) {
    return html`${pageHeader("自动切换")}${configErrorView()}`;
  }
  const autoSwitch = app.config.auto_switch;
  const network = app.state.network;
  const status = app.state.auto_switch;
  const profiles = app.config.profiles;
  const actionOptions = [...profiles.map((profile) => [`use:${profile.name}`, `使用「${profile.name}」`]), ["off", "关闭代理"]];

  const quickButton = (match, value, label) => html`<button class="button subtle" data-action="quick-rule" data-match="${match}" data-value="${value}">${icon("plus")}${label}</button>`;
  const networkItems = [];
  network.ssids.forEach((ssid) => networkItems.push(html`
    <div class="network-item">
      <div><div class="caption muted">Wi-Fi 名称</div><div class="mono">${ssid}</div></div>
      <div class="network-quick">${quickButton("ssid", ssid, "按 Wi-Fi 名称添加规则")}</div>
    </div>`));
  network.adapters.forEach((adapter) => networkItems.push(html`
    <div class="network-item">
      <div>
        <div class="caption muted">${adapter.wireless ? "无线网卡" : "网卡"} · ${adapter.name}</div>
        <div class="mono">${adapter.dns_suffix ? `DNS 后缀 ${adapter.dns_suffix} · ` : ""}网关 ${adapter.gateway}${adapter.gateway_mac ? ` · ${adapter.gateway_mac}` : ""}</div>
      </div>
      <div class="network-quick">
        ${adapter.dns_suffix ? quickButton("dns_suffix", adapter.dns_suffix, "按 DNS 后缀") : ""}
        ${quickButton("gateway", adapter.gateway_mac || adapter.gateway, "按网关")}
      </div>
    </div>`));

  const matchBadge = !network.ssids.length && !network.adapters.length
    ? html`<span class="badge">未连接网络</span>`
    : status.match_index >= 0
      ? html`<span class="badge accent">匹配第 ${status.match_index + 1} 条规则</span>`
      : html`<span class="badge">没有匹配的规则</span>`;

  const rules = autoSwitch.rules.map((rule, index) => {
    const actionValue = rule.action === "off" ? "off" : `use:${rule.profile}`;
    return html`
      <div class="card rule ${status.match_index === index ? "matched" : ""}">
        <span class="index">${index + 1}</span>
        <span class="word">连上</span>
        <select class="select" data-rule="${index}" data-rule-field="match" aria-label="条件">${Object.entries(matchLabels).map(([value, label]) => html`<option value="${value}" ${rule.match === value ? raw("selected") : ""}>${label}</option>`)}</select>
        <span class="word">为</span>
        <input class="input mono" data-rule="${index}" data-rule-field="value" value="${rule.value}" spellcheck="false" aria-label="值" id="rule-${index}-value">
        <span class="word">时</span>
        <select class="select" data-rule="${index}" data-rule-field="action" aria-label="动作" title="${status.match_index === index ? "当前网络匹配这条规则" : ""}">${actionOptions.map(([value, label]) => html`<option value="${value}" ${actionValue === value ? raw("selected") : ""}>${label}</option>`)}</select>
        <span class="rule-tools">
          <button class="button subtle icon-only" data-action="rule-up" data-index="${index}" title="上移" ${index === 0 ? raw("disabled") : ""}>${icon("up")}</button>
          <button class="button subtle icon-only" data-action="rule-down" data-index="${index}" title="下移" ${index === autoSwitch.rules.length - 1 ? raw("disabled") : ""}>${icon("down")}</button>
          <button class="button subtle icon-only" data-action="rule-delete" data-index="${index}" title="删除">${icon("trash")}</button>
        </span>
      </div>`;
  });

  const defaultValue = autoSwitch.default_action === "use" ? `use:${autoSwitch.default_profile}` : autoSwitch.default_action;
  return html`
    ${pageHeader("按网络自动切换", saveIndicator())}
    <div class="card">
      ${settingCard({
        iconName: "wifi",
        title: "按所在网络自动切换",
        description: "连上公司 Wi-Fi 自动用公司的代理，回到家自动关闭。只在网络变化时动作，不会覆盖你的手动选择。",
        control: html`<span class="switch-label">${autoSwitch.enabled ? "开" : "关"}</span>${switchButton({ checked: autoSwitch.enabled, setting: "auto_switch.enabled", label: "按网络自动切换" })}`,
      })}
    </div>
    ${profiles.length === 0 ? html`<div class="infobar info" style="margin-top:12px">${icon("info")}<div class="infobar-body">先在「代理」页添加代理配置，才能设置规则。<div class="infobar-actions"><button class="button" data-action="goto" data-page="proxies">去添加</button></div></div></div>` : ""}

    <div class="section-title">当前网络</div>
    <div class="card network">
      <div class="network-head">${icon("wifi", "large")}<span class="network-name">${status.network}</span>${matchBadge}</div>
      ${network.ssid_error ? html`<div class="infobar warning" style="margin:12px 0 0">${icon("warning")}<div class="infobar-body">${network.ssid_error}<div class="infobar-actions"><button class="button" data-action="open-location-settings">${icon("external")}打开位置设置</button></div></div></div>` : ""}
      ${networkItems.length ? html`<div class="network-list">${networkItems}</div>` : ""}
    </div>

    <div class="section-title">规则<span class="caption faint">从上到下，第一条匹配的规则生效</span>
      <div class="actions"><button class="button" data-action="add-rule" ${profiles.length === 0 ? raw("disabled") : ""}>${icon("plus")}添加规则</button></div>
    </div>
    ${rules.length ? html`<div class="stack">${rules}</div>` : html`<div class="card" style="padding:18px"><span class="muted">还没有规则。可以点上面当前网络旁的按钮，一步添加。</span></div>`}

    <div class="section-title">其他网络</div>
    <div class="card">
      ${settingCard({
        iconName: "signpost",
        title: "没有规则匹配时",
        description: "连上规则里没有的网络时怎么做",
        control: html`<select class="select" data-setting="auto_switch.default">${[["keep", "保持不变"], ["off", "关闭代理"], ...profiles.map((profile) => [`use:${profile.name}`, `使用「${profile.name}」`])].map(([value, label]) => html`<option value="${value}" ${defaultValue === value ? raw("selected") : ""}>${label}</option>`)}</select>`,
      })}
    </div>
    <div style="display:flex;align-items:center;gap:12px;margin-top:16px;flex-wrap:wrap">
      <button class="button" data-action="apply-auto-switch" ${autoSwitch.rules.length === 0 && autoSwitch.default_action === "keep" ? raw("disabled") : ""}>${icon("play")}按当前网络立即应用</button>
      ${status.result ? html`<span class="caption muted">${icon("clock")} 最近一次：${status.time ? `${status.time} · ` : ""}${status.result}</span>` : ""}
    </div>`;
}

// ---------- 常规 ----------

function hotkeyRecorder(setting, value) {
  const recording = recordingHotkey && recordingHotkey.setting === setting;
  return html`
    <span style="display:inline-flex;gap:4px">
      <button class="hotkey-recorder ${recording ? "recording" : ""}" data-action="record-hotkey" data-setting-name="${setting}" aria-label="修改快捷键">
        ${recording ? html`${icon("keyboard")}<span>请按下新的快捷键…</span>` : value ? hotkeyKeys(value) : html`<span class="placeholder">未设置</span>`}
      </button>
      ${value && !recording ? html`<button class="button subtle icon-only" data-action="clear-hotkey" title="清除快捷键" aria-label="清除快捷键">${icon("close")}</button>` : ""}
    </span>`;
}

function generalPage() {
  if (!app.config) {
    return html`${pageHeader("常规")}${configErrorView()}`;
  }
  const config = app.config;
  const hotkeys = app.state.hotkeys;
  const modifierOptions = [["", "不启用"], ["Ctrl+Alt", "Ctrl + Alt + 数字"], ["Ctrl+Shift", "Ctrl + Shift + 数字"], ["Alt+Shift", "Alt + Shift + 数字"], ["Win+Alt", "Win + Alt + 数字"]];
  if (config.profile_hotkeys && !modifierOptions.some(([value]) => value === config.profile_hotkeys)) {
    modifierOptions.push([config.profile_hotkeys, `${config.profile_hotkeys} + 数字`]);
  }
  const secondsOptions = [[3, "3 秒"], [5, "5 秒"], [8, "8 秒"], [15, "15 秒"], [0, "由系统决定"]];
  if (!secondsOptions.some(([value]) => value === config.notify_seconds)) {
    secondsOptions.push([config.notify_seconds, `${config.notify_seconds} 秒`]);
  }
  const recordingNote = recordingHotkey ? html`<span class="muted">按 Esc 取消。当前正在使用的快捷键会被系统先拦截，按了没有反应时换一个试试。</span>` : "";
  return html`
    ${pageHeader("常规", saveIndicator())}

    <div class="section-title">启动</div>
    <div class="card card-group">
      ${settingCard({ iconName: "power", title: "开机自动启动", description: "登录 Windows 后在托盘里自动运行", control: html`<span class="switch-label">${app.state.autostart ? "开" : "关"}</span>${switchButton({ checked: app.state.autostart, action: "autostart", label: "开机自动启动" })}` })}
      ${settingCard({ iconName: "play", title: "启动时", description: "程序启动时怎样处理代理", control: select("startup_action", config.startup_action, [["keep", "保持上次的状态"], ["on", "自动开启代理"], ["off", "自动关闭代理"]]) })}
      ${settingCard({ iconName: "close", title: "退出程序时关闭代理", description: "包括关机和注销，避免代理软件没运行时上不了网", control: html`<span class="switch-label">${config.disable_on_exit ? "开" : "关"}</span>${switchButton({ checked: config.disable_on_exit, setting: "disable_on_exit", label: "退出时关闭代理" })}` })}
    </div>

    <div class="section-title">快捷键</div>
    <div class="card card-group">
      ${settingCard({ iconName: "keyboard", title: "开关代理", description: "在任何程序里按下都能开关代理", control: hotkeyRecorder("hotkey", config.hotkey), note: hotkeys.toggle_error ? html`<span style="color:var(--danger)">${hotkeys.toggle_error}</span>` : recordingNote })}
      ${settingCard({ iconName: "swap", title: "按数字切换配置", description: "例如 Ctrl + Alt + 1 切换到第 1 个配置；再按一次关闭代理", control: select("profile_hotkeys", config.profile_hotkeys, modifierOptions), note: hotkeys.profiles_error ? html`<span style="color:var(--danger)">${hotkeys.profiles_error}</span>` : "" })}
    </div>

    <div class="section-title">托盘图标</div>
    <div class="card card-group">
      ${settingCard({ iconName: "mouse", title: "单击托盘图标", control: select("tray_click", config.tray_click, [["toggle", "开关代理"], ["settings", "打开设置"], ["menu", "显示菜单"]]) })}
      ${settingCard({ iconName: "mouse", title: "双击托盘图标", description: config.tray_double_click !== "none" ? "设置了双击后，单击要稍等一下才生效" : "", control: select("tray_double_click", config.tray_double_click, [["none", "不响应"], ["settings", "打开设置"], ["toggle", "开关代理"]]) })}
    </div>

    <div class="section-title">通知</div>
    <div class="card card-group">
      ${settingCard({ iconName: "bell", title: "显示通知", description: "开关代理、切换配置、出现问题时在右下角提示", control: select("notify_level", config.notify_level, [["all", "全部"], ["errors", "只提示问题"], ["none", "不显示"]]) })}
      ${settingCard({ iconName: "clock", title: "通知自动消失", control: select("notify_seconds", config.notify_seconds, secondsOptions, config.notify_level === "none" ? "disabled" : "") })}
    </div>

    <div class="section-title">连接</div>
    <div class="card card-group">
      ${settingCard({ iconName: "power", title: "关闭代理时", description: "恢复开启前的设置：适合公司电脑原本就配置了代理的情况", control: select("off_mode", config.off_mode, [["direct", "直接连接"], ["restore", "恢复开启前的设置"]]) })}
      ${settingCard({ iconName: "shield", title: "代理服务器连不上时", description: "开启后每 5 秒检查一次代理服务器能否连接", control: select("health_check", config.health_check, [["notify", "提醒我"], ["auto_off", "自动关闭，恢复后重新开启"], ["off", "不检查"]]) })}
      ${settingCard({ iconName: "gauge", title: "测速地址", description: "测速时经代理访问这个地址，返回越快延迟越低", control: html`<input class="input mono" style="width:280px" id="test-url" data-setting-text="test_url" value="${config.test_url}" spellcheck="false">${config.test_url !== app.state.defaults.test_url ? html`<button class="button subtle icon-only" data-action="reset-test-url" title="恢复默认">${icon("refresh")}</button>` : ""}` })}
    </div>

    <div class="section-title">外观</div>
    <div class="card card-group">
      ${settingCard({ iconName: "palette", title: "设置界面的颜色", control: html`<div class="segmented" role="group" aria-label="主题">${[["system", "跟随系统"], ["light", "浅色"], ["dark", "深色"]].map(([value, label]) => html`<button type="button" data-action="set-theme" data-value="${value}" aria-pressed="${config.theme === value}">${label}</button>`)}</div>` })}
      ${settingCard({ iconName: "window", title: "设置界面的打开方式", description: "独立窗口需要系统里有 Edge 或 Chrome（Windows 10 / 11 都自带 Edge）", control: select("settings_window", config.settings_window, [["app", "独立窗口"], ["browser", "默认浏览器"]]) })}
    </div>

    <div class="section-title">配置文件</div>
    <div class="card card-group">
      ${settingCard({ iconName: "document", title: "配置文件", description: html`<span class="mono">${app.state.paths.config}</span>${app.state.paths.portable ? html` <span class="badge accent">便携模式</span>` : ""}`, control: html`<button class="button" data-action="open-config-dir">${icon("folder")}打开文件夹</button><button class="button" data-action="open-config-file">${icon("pencil")}编辑</button>` })}
      ${settingCard({ iconName: "terminal", title: "编辑器", description: "「编辑」配置文件时使用的程序，留空用记事本，也可以填 code 等命令", control: html`<input class="input" style="width:200px" id="editor" data-setting-text="editor" value="${config.editor}" placeholder="记事本" spellcheck="false">` })}
      ${settingCard({ iconName: "download", title: "备份与恢复", description: "导出全部设置和代理配置，换电脑时导入", control: html`<button class="button" data-action="export-config">${icon("download")}导出</button><button class="button" data-action="import-config">${icon("upload")}导入</button>` })}
      ${settingCard({ iconName: "refresh", title: "恢复默认设置", description: "把本页的设置恢复为默认值，代理配置和自动切换规则会保留", control: html`<button class="button" data-action="reset-settings">恢复默认</button>` })}
    </div>`;
}

// ---------- 诊断 ----------

function valueOrUnset(value) {
  return value ? html`<span class="mono">${value}</span>` : html`<span class="faint">未设置</span>`;
}

function diagnosticsPage() {
  const diagnostics = app.diagnostics;
  let systemCard = html`<div class="card" style="padding:18px;display:flex;gap:12px;align-items:center"><span class="spinner"></span>正在读取…</div>`;
  let toolsCard = systemCard;
  if (diagnostics) {
    const system = diagnostics.system;
    const stateBadges = [];
    if (system.proxy_enabled) {
      stateBadges.push(html`<span class="badge success">代理服务器</span>`);
    }
    if (system.pac_enabled) {
      stateBadges.push(html`<span class="badge success">PAC 脚本</span>`);
    }
    if (system.auto_detect) {
      stateBadges.push(html`<span class="badge">自动检测设置</span>`);
    }
    if (!system.proxy_enabled && !system.pac_enabled) {
      stateBadges.push(html`<span class="badge">直接连接</span>`);
    }
    systemCard = html`
      ${diagnostics.machine_policy ? html`<div class="infobar warning">${icon("warning")}<div class="infobar-body"><div class="infobar-title">组策略设置了按计算机统一设置代理</div>当前用户的代理设置不会生效，需要管理员调整组策略「使代理设置按计算机设置」。</div></div>` : ""}
      ${diagnostics.system_error ? html`<div class="infobar danger">${icon("error")}<div class="infobar-body">读取系统代理失败：${diagnostics.system_error}</div></div>` : ""}
      <dl class="card kv">
        <dt>当前状态</dt><dd class="chips">${stateBadges}</dd>
        <dt>代理服务器</dt><dd>${valueOrUnset(system.server)}${system.server && !system.proxy_enabled ? html` <span class="faint">（未启用）</span>` : ""}</dd>
        <dt>不走代理的地址</dt><dd>${system.bypass ? html`<span class="mono">${system.bypass.split(";").join("; ")}</span>` : html`<span class="faint">无</span>`}</dd>
        <dt>PAC 脚本</dt><dd>${system.pac_enabled ? valueOrUnset(system.pac) : html`<span class="faint">未使用</span>`}</dd>
        <dt>生效的连接</dt><dd>${diagnostics.connections.join("、")}</dd>
        <dt>读取方式</dt><dd class="faint">${diagnostics.system_source}</dd>
      </dl>`;
    const git = diagnostics.git;
    toolsCard = html`
      <dl class="card kv">
        <dt>HTTP_PROXY</dt><dd>${valueOrUnset(diagnostics.env.HTTP_PROXY)}</dd>
        <dt>HTTPS_PROXY</dt><dd>${valueOrUnset(diagnostics.env.HTTPS_PROXY)}</dd>
        <dt>NO_PROXY</dt><dd>${valueOrUnset(diagnostics.env.NO_PROXY)}</dd>
        <dt>git http.proxy</dt><dd>${git.available ? valueOrUnset(git.http_proxy) : html`<span class="faint">没有安装 git</span>`}</dd>
        <dt>git https.proxy</dt><dd>${git.available ? valueOrUnset(git.https_proxy) : html`<span class="faint">没有安装 git</span>`}</dd>
        <dt>npm proxy</dt><dd>${valueOrUnset(diagnostics.npm.proxy)}</dd>
        <dt>npm https-proxy</dt><dd>${valueOrUnset(diagnostics.npm["https-proxy"])}</dd>
        <dt>.npmrc</dt><dd class="faint mono">${diagnostics.npmrc_path}</dd>
      </dl>`;
  }
  const logLines = (app.log || "").split("\n").map((line) => {
    const level = /level=ERROR/.test(line) ? "error" : /level=WARN/.test(line) ? "warn" : "";
    return html`<span class="${level}">${line}</span>\n`;
  });
  return html`
    ${pageHeader("诊断")}
    <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:4px">
      <button class="button" data-action="refresh-diagnostics">${icon("refresh")}刷新</button>
      <button class="button" data-action="copy-diagnostics" ${diagnostics ? "" : raw("disabled")}>${icon("copy")}复制诊断信息</button>
      <button class="button danger" data-action="clear-all">${icon("trash")}清除所有代理设置</button>
    </div>
    <div class="section-title">${icon("monitor")}Windows 系统代理</div>
    ${systemCard}
    <div class="section-title">${icon("terminal")}命令行与开发工具</div>
    ${toolsCard}
    <div class="section-title">${icon("document")}运行日志
      <div class="actions">
        <button class="button subtle" data-action="refresh-log">${icon("refresh")}刷新</button>
        <button class="button subtle" data-action="open-log">${icon("external")}打开日志文件</button>
      </div>
    </div>
    <div class="card"><pre class="log" id="log">${app.log ? logLines : html`<span class="faint">还没有日志</span>`}</pre></div>`;
}

// ---------- 关于 ----------

function aboutPage() {
  const update = app.update;
  let updateView = html``;
  if (update && update.checking) {
    updateView = html`<div class="infobar">${raw('<span class="spinner"></span>')}<div class="infobar-body">正在检查更新…</div></div>`;
  } else if (update && update.error) {
    updateView = html`<div class="infobar warning">${icon("warning")}<div class="infobar-body">${update.error}</div></div>`;
  } else if (update && update.newer) {
    updateView = html`
      <div class="infobar info">${icon("download")}<div class="infobar-body">
        <div class="infobar-title">发现新版本 ${update.latest}${update.published ? html`<span class="caption muted">（${update.published.slice(0, 10)} 发布）</span>` : ""}</div>
        ${update.notes ? html`<div class="caption" style="white-space:pre-line;margin-top:6px;max-height:180px;overflow:auto">${update.notes}</div>` : ""}
        <div class="infobar-actions"><button class="button accent" data-action="open-url" data-url="${update.url}">${icon("external")}前往下载</button></div>
      </div></div>`;
  } else if (update) {
    updateView = html`<div class="infobar success">${icon("success")}<div class="infobar-body">已经是最新版本（${update.latest}）</div></div>`;
  }
  const repository = "https://github.com/whrss9527/proxyswitch";
  return html`
    ${pageHeader("关于")}
    <div class="card about-hero">
      ${logoSvg(app.state.palette[1] || "#2563eb", true)}
      <div style="flex:1">
        <div class="about-name">ProxySwitch</div>
        <div class="muted">快捷切换 Windows 代理 · 版本 <span class="numeric">${app.state.version}</span>${app.state.platform === "dev" ? html` <span class="badge warning">开发模式</span>` : ""}</div>
      </div>
      <button class="button" data-action="check-update" ${update && update.checking ? raw("disabled") : ""}>${icon("refresh")}检查更新</button>
    </div>
    <div style="margin-top:12px">${updateView}</div>
    <div class="section-title">链接</div>
    <div class="card card-group">
      ${settingCard({ iconName: "link", title: "项目主页", description: "源代码、使用说明", control: html`<button class="button" data-action="open-url" data-url="${repository}">${icon("external")}打开</button>` })}
      ${settingCard({ iconName: "bell", title: "问题反馈", description: "遇到问题时附上「诊断」页复制的信息会更快解决", control: html`<button class="button" data-action="open-url" data-url="${repository}/issues">${icon("external")}打开</button>` })}
      ${settingCard({ iconName: "layers", title: "版本历史", control: html`<button class="button" data-action="open-url" data-url="${repository}/releases">${icon("external")}打开</button>` })}
    </div>
    <div class="section-title">命令行</div>
    <div class="card"><pre class="code">ProxySwitch.exe on              开启代理（上次使用的配置）
ProxySwitch.exe off             关闭代理
ProxySwitch.exe toggle          开 / 关切换
ProxySwitch.exe use 配置名       切换到指定配置并开启
ProxySwitch.exe status          查看状态（退出码 0 开启，1 关闭）
ProxySwitch.exe settings        打开设置</pre></div>
    <div class="section-title">文件位置</div>
    <dl class="card kv">
      <dt>配置文件</dt><dd class="mono">${app.state.paths.config}</dd>
      <dt>日志</dt><dd class="mono">${app.state.paths.log}</dd>
      <dt>模式</dt><dd>${app.state.paths.portable ? "便携模式：配置和 exe 放在同一个文件夹" : "安装模式：配置保存在用户目录"}</dd>
    </dl>
    <p class="caption faint" style="margin-top:16px">MIT 许可证 · 不收集任何数据，只在你点「检查更新」时访问 GitHub</p>`;
}

const pageRenderers = {
  proxies: proxiesPage,
  network: networkPage,
  general: generalPage,
  diagnostics: diagnosticsPage,
  about: aboutPage,
};
