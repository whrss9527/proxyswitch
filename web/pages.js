"use strict";

// 各页面的内容：代理、局域网共享、网址诊断、自动切换、常规、系统集成、诊断、关于。每个函数根据 app 里的状态生成整页 HTML。

const pages = [
  { id: "proxies", label: "代理", icon: "globe" },
  { id: "share", label: "局域网共享", icon: "router" },
  { id: "diagnose", label: "网址诊断", icon: "stethoscope" },
  { id: "network", label: "自动切换", icon: "wifi" },
  { id: "general", label: "常规", icon: "sliders" },
  { id: "system", label: "系统集成", icon: "windows" },
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

// ---------- 订阅和代理内核 ----------

function formatBytes(bytes) {
  if (bytes >= 1 << 30) {
    return `${(bytes / (1 << 30)).toFixed(bytes >= 100 * (1 << 30) ? 0 : 1)} GB`;
  }
  if (bytes >= 1 << 20) {
    return `${(bytes / (1 << 20)).toFixed(0)} MB`;
  }
  return `${Math.max(0, Math.round(bytes / 1024))} KB`;
}

function formatDate(seconds) {
  const date = new Date(seconds * 1000);
  const pad = (value) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

// usageText 是机场给的已用流量和到期时间，例如「已用 6.0 GB / 100 GB · 2027-01-01 到期」。
function usageText(info) {
  const parts = [];
  if (info.total) {
    parts.push(`已用 ${formatBytes((info.upload || 0) + (info.download || 0))} / ${formatBytes(info.total)}`);
  }
  if (info.expire) {
    parts.push(`${formatDate(info.expire)} 到期`);
  }
  return parts.join(" · ");
}

// usageWarning 在流量快用完或快到期时返回提示。
function usageWarning(info) {
  const used = (info.upload || 0) + (info.download || 0);
  if (info.total && used >= info.total) {
    return "流量已用完";
  }
  if (info.expire && info.expire * 1000 < Date.now()) {
    return "订阅已到期";
  }
  if (info.total && used >= info.total * 0.9) {
    return "流量快用完了";
  }
  if (info.expire && info.expire * 1000 - Date.now() < 7 * 24 * 3600 * 1000) {
    return "订阅快到期了";
  }
  return "";
}

function relativeTime(iso) {
  const minutes = Math.floor((Date.now() - new Date(iso).getTime()) / 60000);
  if (!(minutes >= 1)) {
    return "刚刚";
  }
  if (minutes < 60) {
    return `${minutes} 分钟前`;
  }
  if (minutes < 24 * 60) {
    return `${Math.floor(minutes / 60)} 小时前`;
  }
  return `${Math.floor(minutes / 1440)} 天前`;
}

function hasSubscriptions() {
  return Boolean(app.config && app.config.profiles.some((profile) => profile.subscription));
}

// coreNotice 提示代理内核的问题：还没下载（可以一键下载）、正在下载、出错、可以更新。没有问题时为空。
// coreNotice 提示内核的问题（还没下载、正在下载、出错、可以更新），purpose 是需要内核的功能。
function coreNotice(purpose = "使用订阅") {
  const core = app.state.core;
  if (core.installing || app.coreInstalling) {
    const progress = core.installing || { received: 0, total: 0 };
    const percent = progress.total > 0 ? Math.min(100, Math.round((progress.received * 100) / progress.total)) : 0;
    return html`
      <div class="infobar info">${icon("download")}<div class="infobar-body">
        <div class="infobar-title">正在下载代理内核 ${core.version}</div>
        <div class="progress ${progress.total > 0 ? "" : "indeterminate"}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${percent}"><span style="width:${percent}%"></span></div>
        <div class="caption muted numeric">${progress.total > 0 ? `${megabytes(progress.received)} / ${megabytes(progress.total)}` : "正在连接…"}</div>
      </div></div>`;
  }
  if (!core.installed) {
    if (core.custom) {
      return html`<div class="infobar danger">${icon("error")}<div class="infobar-body"><div class="infobar-title">找不到代理内核</div><span class="mono">${core.path}</span>
        <div class="infobar-actions"><button class="button" data-action="goto" data-page="general">修改内核位置</button></div></div></div>`;
    }
    if (core.downloadable) {
      return html`<div class="infobar info">${icon("download")}<div class="infobar-body"><div class="infobar-title">${purpose}需要先下载代理内核</div>
        ProxySwitch 用 mihomo（Clash.Meta）内核连接订阅里的节点、提供局域网共享。只需下载一次，约 20 MB，下载后自动校验。
        <div class="infobar-actions"><button class="button accent" data-action="install-core">${icon("download")}下载内核</button></div></div></div>`;
    }
    return html`<div class="infobar info">${icon("info")}<div class="infobar-body"><div class="infobar-title">${purpose}需要 mihomo 内核</div>
      这个版本不能在程序里下载内核，请在「常规」里填写本机 mihomo 程序的位置。
      <div class="infobar-actions"><button class="button" data-action="goto" data-page="general">填写内核位置</button></div></div></div>`;
  }
  if (core.error) {
    return html`<div class="infobar warning">${icon("warning")}<div class="infobar-body"><div class="infobar-title">代理内核出错</div><div style="white-space:pre-wrap">${core.error}</div></div></div>`;
  }
  if (!core.custom && core.downloadable && core.installed_version && core.installed_version !== core.version) {
    return html`<div class="infobar info">${icon("download")}<div class="infobar-body"><div class="infobar-title">代理内核可以更新到 ${core.version}</div>当前是 ${core.installed_version}
      <div class="infobar-actions"><button class="button" data-action="install-core">${icon("download")}更新内核</button></div></div></div>`;
  }
  return html``;
}

// rulePresetFor 返回规则地址对应的预设（设置页里可以直接选的规则），不是预设时返回 null。
function rulePresetFor(url) {
  return (app.state.rule_presets || []).find((preset) => preset.url === url) || null;
}

// rulesName 是分流规则的简短说明：内置的大陆直连、预设的名字或「自定义规则」。
function rulesName(rules) {
  if (!rules) {
    return "大陆直连";
  }
  const preset = rulePresetFor(rules);
  return preset ? preset.name : "自定义规则";
}

// modeText 是订阅配置的分流方式，例如「按规则分流 · 黑名单 + 去广告」或「全局代理」。
function modeText(profile) {
  return profile.mode === "global" ? "全局代理" : `按规则分流 · ${rulesName(profile.rules)}`;
}

const finalTexts = { proxy: "其余网站走节点", direct: "其余网站直连", reject: "其余网站被拦截" };

// rulesMeta 是订阅配置的分流方式在列表里的说明，带上规则的下载状态：下载中、没下载成功时暂时按大陆直连分流。
function rulesMeta(profile) {
  if (profile.mode === "global" || !profile.rules) {
    return html`<span>${modeText(profile)}</span>`;
  }
  const info = (app.state.rules || {})[profile.id] || {};
  const name = rulesName(profile.rules);
  if (!info.updated) {
    if (info.error) {
      return html`<span style="color:var(--danger)" title="${info.error}">分流规则「${name}」没有下载成功，暂时按大陆直连分流</span>`;
    }
    return html`<span><span class="spinner" style="width:10px;height:10px;border-width:1.5px;vertical-align:-1px"></span> 正在下载分流规则「${name}」…</span>`;
  }
  const details = [`${(info.rules || 0).toLocaleString()} 条规则`, finalTexts[info.final]];
  if (info.skipped) {
    details.push(`${info.skipped} 条内核不支持，已跳过`);
  }
  return html`
    <span title="${details.join("，")}">${modeText(profile)} · ${(info.rules || 0).toLocaleString()} 条</span>
    ${info.failed_sets ? html`<span class="badge warning" title="下次更新时再试">${info.failed_sets} 个规则列表没下载到</span>` : ""}
    ${info.error ? html`<span class="badge warning" title="${info.error}">规则更新失败，仍在使用上次的规则</span>` : ""}`;
}

// subscriptionMeta 是订阅配置在列表里的说明：节点数、选中的节点、流量和到期时间、分流方式，或下载状态。
function subscriptionMeta(profile) {
  const info = app.state.subscriptions[profile.id] || {};
  if (!info.updated) {
    if (info.error) {
      return html`<span style="color:var(--danger)">订阅没有下载成功：${info.error}</span>`;
    }
    return html`<span><span class="spinner" style="width:10px;height:10px;border-width:1.5px;vertical-align:-1px"></span> 正在下载订阅…</span>`;
  }
  const usage = usageText(info);
  const warning = usageWarning(info);
  return html`
    <span>${describeServer(profile)}</span>
    <span>${info.nodes} 个节点</span>
    ${rulesMeta(profile)}
    ${usage ? html`<span>${usage}</span>` : ""}
    ${warning ? html`<span class="badge warning">${warning}</span>` : ""}
    ${info.error ? html`<span class="badge warning" title="${info.error}">更新失败，仍在使用上次的节点</span>` : ""}`;
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
    // 订阅配置显示实际在用的节点（自动选择时注明）和分流方式。
    const server = profile.subscription
      ? html`<span>${status.node ? `订阅 · ${status.node}${profile.node ? "" : "（自动选择）"}` : describeServer(profile)}</span><span>${modeText(profile)}</span>`
      : html`<span class="mono">${describeServer(profile)}</span>`;
    subtitle = html`<strong style="color:var(--text)">${profile.name}</strong>${server}<span class="chips">${status.applied.map((label) => html`<span class="chip">${label}</span>`)}</span>`;
    const latency = app.latency[profile.id];
    if (status.health === "down") {
      health = html`<span class="dot" style="background:var(--danger)"></span><span style="color:var(--danger)">连不上代理服务器：${status.health_message}</span>`;
    } else if (latency && latency.running) {
      health = html`<span class="spinner" style="width:12px;height:12px"></span>正在测速…`;
    } else if (latency && latency.ok && latency.millis) {
      health = html`<span class="dot" style="background:var(--success)"></span>连接正常，延迟 <span class="numeric">${latency.millis} ms</span>${latency.route ? html`<span class="faint">（${routeText(latency)}）</span>` : ""}`;
    } else if (latency && latency.ok) {
      health = html`<span class="dot" style="background:var(--success)"></span>${latency.message}`;
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
  const nodes = status.state === "on" && profile && profile.subscription;
  const canTest = status.state === "on" && profile && (profile.server || profile.pac);
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
          ${nodes ? html`<button class="button" data-action="nodes" data-id="${profile.id}">${icon("list")}节点</button>` : ""}
          ${canTest ? html`<button class="button" data-action="test-profile" data-id="${profile.id}">${icon("gauge")}测速</button>` : ""}
          ${terminal ? html`<button class="button" data-action="terminal-menu" title="复制在当前终端里使用代理的命令" aria-haspopup="menu">${icon("terminal")}终端命令</button>` : ""}
          ${saveExternal ? html`<button class="button" data-action="save-external">${icon("plus")}保存为配置</button>` : ""}
        </div>` : ""}
        ${hotkey ? html`<span class="caption faint" style="display:flex;gap:6px;align-items:center">快捷键 ${hotkeyKeys(hotkey)}</span>` : ""}
        ${speedView()}
      </div>
    </div>`;
}

// formatSpeed 与程序里的同名函数一致：把字节/秒写成 0 B/s、12.3 KB/s、1.2 MB/s。
function formatSpeed(bytesPerSecond) {
  let value = Math.max(0, bytesPerSecond);
  if (value < 1024) {
    return `${Math.round(value)} B/s`;
  }
  for (const unit of ["KB/s", "MB/s", "GB/s"]) {
    value /= 1024;
    if (value < 10) {
      return `${value.toFixed(2)} ${unit}`;
    }
    if (value < 100) {
      return `${value.toFixed(1)} ${unit}`;
    }
    if (value < 1000 || unit === "GB/s") {
      return `${Math.round(value)} ${unit}`;
    }
  }
  return "";
}

// speedView 是开关卡片上的实时网速，每次同步状态时更新。
function speedView() {
  const speed = app.state.speed;
  if (!speed || speed.mode === "none" || !speed.ready) {
    return "";
  }
  const title = speed.mode === "core" ? "实时网速：只算经过内置代理内核的流量" : "实时网速：这台电脑所有网卡的总速度";
  return html`<span class="caption muted numeric hero-speed" title="${title}"><span>↑ ${formatSpeed(speed.upload)}</span><span>↓ ${formatSpeed(speed.download)}</span></span>`;
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
  if (!latency.millis) {
    return html`<span class="badge success" title="${latency.message}">可用</span>`;
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
          ${profile.subscription ? subscriptionMeta(profile) : html`<span class="mono">${describeServer(profile)}</span>`}
          <span>${targetsText(profile)}</span>
          ${hotkey ? html`<kbd class="compact" title="快捷键">${hotkey}</kbd>` : ""}
        </div>
      </div>
      <div class="profile-actions">
        <span class="latency">${latencyBadge(profile)}</span>
        ${profile.subscription ? html`<button class="button" data-action="nodes" data-id="${profile.id}" title="选择节点">${icon("list")}节点</button>` : ""}
        ${active
          ? html`<span class="using">${icon("check")}正在使用</span>`
          : html`<button class="button" data-action="use" data-name="${profile.name}" ${app.busy ? raw("disabled") : ""}>使用</button>`}
        <button class="button subtle icon-only" data-action="edit" data-id="${profile.id}" title="编辑" aria-label="编辑「${profile.name}」">${icon("pencil")}</button>
        <button class="button subtle icon-only" data-action="profile-menu" data-id="${profile.id}" title="更多" aria-label="更多操作">${icon("more")}</button>
      </div>
    </div>`;
}

const policyLabels = { proxy: "走节点", direct: "直连", reject: "拦截" };

// normalizeRuleTarget 与程序里的同名函数一致：把网址、*.域名、带端口的写法整理成域名或 IP。
function normalizeRuleTarget(text) {
  let value = String(text || "").trim().toLowerCase();
  const scheme = value.indexOf("://");
  if (scheme >= 0) {
    value = value.slice(scheme + 3);
  }
  if (!/^[0-9a-f:.]+\/\d+$/.test(value)) {
    const cut = value.search(/[/?#]/);
    if (cut >= 0) {
      value = value.slice(0, cut);
    }
  }
  if (value.startsWith("[")) {
    const end = value.indexOf("]");
    if (end > 0) {
      value = value.slice(1, end);
    }
  } else {
    const parts = value.split(":");
    if (parts.length === 2 && /^\d+$/.test(parts[1])) {
      value = parts[0];
    }
  }
  return value.replace(/^\*\./, "").replace(/^\.+|\.+$/g, "");
}

// ruleTargetProblem 检查整理过的域名或 IP，没有问题时返回空。
function ruleTargetProblem(value) {
  if (!value) {
    return "请填写域名或 IP";
  }
  const ipv4 = /^(\d{1,3}\.){3}\d{1,3}(\/\d{1,2})?$/;
  const ipv6 = /^[0-9a-f:]*:[0-9a-f:]*(\/\d{1,3})?$/;
  const domain = /^[a-z0-9_-]+(\.[a-z0-9_-]+)+$/;
  if (ipv4.test(value) || ipv6.test(value) || domain.test(value)) {
    return "";
  }
  return `认不出「${value}」：填域名（例如 youtube.com）或 IP / 网段（例如 8.8.8.8、10.0.0.0/8）`;
}

// customRulesView 是代理页的自定义规则：域名或 IP 固定走节点、直连或被拦截。只对订阅配置起作用，没有订阅配置时不显示。
function customRulesView() {
  if (!app.config.profiles.some((profile) => profile.subscription)) {
    return "";
  }
  const rules = app.config.custom_rules || [];
  const policyOptions = (value) => Object.entries(policyLabels).map(([policy, label]) => html`<option value="${policy}" ${policy === value ? raw("selected") : ""}>${label}</option>`);
  const rows = rules.map((rule, index) => html`
    <div class="custom-rule ${rule.disabled ? "disabled" : ""}">
      <span class="mono custom-rule-value" title="${rule.value}">${rule.value}</span>
      <select class="select" data-custom-rule="${index}" aria-label="「${rule.value}」的去向">${policyOptions(rule.policy)}</select>
      <button class="switch" role="switch" aria-checked="${!rule.disabled}" data-action="custom-rule-toggle" data-index="${index}" aria-label="启用「${rule.value}」" title="${rule.disabled ? "已停用" : "已启用"}"></button>
      <button class="button subtle icon-only" data-action="custom-rule-delete" data-index="${index}" title="删除" aria-label="删除「${rule.value}」">${icon("trash")}</button>
    </div>`);
  return html`
    <div class="section-title">自定义规则<span class="caption faint">排在分流规则前面，全局代理时也生效</span></div>
    <div class="card custom-rules">
      ${rows.length ? html`<div class="custom-rule-list">${rows}</div>` : html`<p class="muted custom-rules-empty">还没有自定义规则。可以让某个网站固定走节点，或者让公司内网、局域网里的服务直连。</p>`}
      <div class="custom-rule-add">
        <input class="input mono" data-focus="custom-rule-value" value="${app.customRuleDraft.value}" placeholder="域名或 IP，例如 youtube.com、8.8.8.8、10.0.0.0/8" spellcheck="false" autocomplete="off" aria-label="域名或 IP">
        <select class="select" data-focus="custom-rule-policy" aria-label="去向">${policyOptions(app.customRuleDraft.policy)}</select>
        <button class="button" data-action="add-custom-rule">${icon("plus")}添加</button>
      </div>
      <div class="field-error" data-custom-rule-error></div>
      <p class="caption faint" style="margin:6px 0 0">域名包括它的子域名。只对订阅配置（内置的代理内核）起作用。</p>
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
        <button class="choice" data-action="add-subscription">${icon("list")}<strong>机场订阅</strong><span>填订阅地址，用内置的代理内核连接节点，不需要另外安装代理软件</span></button>
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
  if (hasSubscriptions()) {
    const notice = coreNotice();
    if (notice.text) {
      warnings.push(notice);
    }
  }
  return html`
    ${pageHeader("代理")}
    ${warnings}
    ${profiles.length === 0 && app.state.status.state === "off" ? "" : heroView()}
    ${shareBanner()}
    ${profiles.length === 0 ? html`<div style="margin-top:16px">${emptyView()}</div>` : html`
      <div class="section-title">代理配置
        <div class="actions">
          <button class="button subtle" data-action="test-all">${icon("gauge")}全部测速</button>
          <button class="button subtle" data-action="detect">${icon("search")}检测本机代理</button>
          <button class="button accent" data-action="add">${icon("plus")}添加</button>
        </div>
      </div>
      <div class="stack">${profiles.map((profile, index) => profileRow(profile, index))}</div>
      <p class="caption faint" style="margin:14px 2px 0">单击托盘图标开关代理，右键托盘图标可以快速切换配置。</p>
      ${customRulesView()}`}`;
}

// ---------- 局域网共享 ----------

// shareUpstreamText 是「现在转发到」的文字：共享的设备的流量往哪走。
function shareUpstreamText(upstream) {
  switch (upstream.kind) {
    case "core":
      return "内置代理（和本机一样的节点和分流规则）";
    case "proxy":
      return upstream.proxy.replace(/^http:\/\//, "");
    case "unsupported":
      return "直接连接（PAC 没法转发）";
  }
  return "直接连接（本机没开代理）";
}

function isLoopbackIp(ip) {
  return /^127\./.test(ip) || ip === "::1";
}

// shareUpstreamSummary 是代理页里局域网共享的一句话。
function shareUpstreamSummary(upstream) {
  switch (upstream.kind) {
    case "core":
      return "设备和本机一样走节点和分流规则";
    case "proxy":
      return `设备的流量转发到 ${shareUpstreamText(upstream)}`;
    case "unsupported":
      return "本机用的是 PAC，没法转发，设备暂时直连";
  }
  return "设备经这台电脑直接上网";
}

// shareBanner 是代理页里局域网共享的状态，开着共享时显示，点击打开共享页。
function shareBanner() {
  const share = app.state.config && app.state.config.share;
  if (!share || !share.enabled) {
    return "";
  }
  const status = app.state.core.share || {};
  const address = (app.state.share.addresses || [])[0];
  let detail = html`${shareUpstreamSummary(app.state.share.upstream)}`;
  if (status.error) {
    detail = html`<span style="color:var(--danger)">${status.error}</span>`;
  } else if (!status.listening) {
    detail = html`正在启动…`;
  }
  return html`
    <button class="card share-banner" data-action="goto" data-page="share" title="查看局域网共享">
      ${icon("router", "large")}
      <div class="share-banner-text">
        <div>局域网共享 · ${address ? html`<span class="mono">${address.ip}:${share.port}</span>` : "没有连上局域网"}</div>
        <div class="setting-description">${detail}</div>
      </div>
      ${icon("chevron")}
    </button>`;
}

// shareClientsProblem 与程序里的 parseShareClients 一致：检查允许的设备（IP 或网段），没有问题时返回空。
function shareClientsProblem(text) {
  const invalid = String(text || "").split(/[,，;\s]+/).filter(Boolean).filter((item) => {
    const [address, bits, extra] = item.split("/");
    if (extra !== undefined || (bits !== undefined && !/^\d{1,3}$/.test(bits))) {
      return true;
    }
    if (/^(\d{1,3}\.){3}\d{1,3}$/.test(address)) {
      return address.split(".").some((part) => Number(part) > 255) || Number(bits || 0) > 32;
    }
    return !(/^[0-9a-f:]+$/i.test(address) && address.includes(":")) || Number(bits || 0) > 128;
  });
  return invalid.length ? `认不出这些地址：${invalid.join("、")}（填 IP 或网段，例如 192.168.1.20、192.168.1.0/24）` : "";
}

// sharePortProblem 检查共享的端口，没有问题时返回空。
function sharePortProblem(text) {
  const port = Number(String(text).trim());
  if (!Number.isInteger(port) || port < 1024 || port > 65535) {
    return "端口需要在 1024~65535 之间";
  }
  if (port === app.config.core.port) {
    return `不能和代理内核的端口 ${port} 相同`;
  }
  return "";
}

function shareStatusView() {
  const share = app.state.core.share || {};
  if (!app.state.config.share.enabled) {
    return html`<span class="muted">未开启</span>`;
  }
  if (share.listening) {
    return html`<span style="color:var(--success)">正在监听端口 <span class="numeric">${share.port}</span>，局域网里的设备可以连接</span>`;
  }
  if (share.error) {
    return html`<span style="color:var(--danger);white-space:pre-wrap">${share.error}</span>`;
  }
  return html`<span class="muted" style="display:inline-flex;gap:6px;align-items:center"><span class="spinner" style="width:12px;height:12px"></span>正在启动…</span>`;
}

function shareAwakeView(share) {
  switch (app.state.share.awake) {
    case "holding":
      return html`<span class="badge success">${icon("sun")}正在保持唤醒</span>`;
    case "paused":
      return html`<span class="badge warning">${icon("battery")}用电池供电，已暂停</span>`;
  }
  return html`<span class="badge">${share.enabled && share.keep_awake ? "未保持" : "共享开启后生效"}</span>`;
}

function shareAddressView(share) {
  const addresses = app.state.share.addresses || [];
  const primary = addresses[0];
  const others = addresses.slice(1);
  const guide = html`
    <p class="caption muted share-guide"><strong>PS5</strong>：设置 → 网络 → 设置 → 设置互联网连接 → 选中正在用的网络 → 高级设置 → 代理服务器 → 使用，填上面的地址和端口。
      <strong>Switch</strong>：设置 → 互联网 → 互联网设置 → 选中网络 → 更改设置 → 代理服务器设置 → 启用。手机、平板在 Wi-Fi 的「配置代理 → 手动」里填同样的地址。</p>
    <p class="caption faint share-guide">电脑的 IP 变了设备就连不上了：建议在路由器里给这台电脑分配固定 IP。PS5 只把 HTTP / HTTPS 流量（商店、下载、登录、浏览器）交给代理，游戏联机的 UDP 流量仍然直连。</p>`;
  if (!primary) {
    return html`<div class="card share-address">
      <div class="share-address-row">${icon("warning")}<div><div class="setting-title">这台电脑现在没有连上局域网</div><div class="setting-description">连上 Wi-Fi 或网线后，这里会显示设备上要填的地址。</div></div></div>
      ${guide}</div>`;
  }
  const address = `${primary.ip}:${share.port}`;
  return html`
    <div class="card share-address">
      <div class="share-address-row">
        <div class="share-address-main">
          <div class="share-address-value mono" id="share-address">${primary.ip} : ${share.port}</div>
          <div class="setting-description">代理服务器地址填 <span class="mono">${primary.ip}</span>（这台电脑的「${primary.interface}」），端口填 <span class="mono">${share.port}</span></div>
        </div>
        <button class="button" data-action="copy-share-address" data-address="${address}">${icon("copy")}复制</button>
      </div>
      ${others.length ? html`<p class="caption muted share-guide">这台电脑还有别的网卡：${others.map((item) => `${item.interface} ${item.ip}`).join("、")}。设备要和电脑在同一个网络里才连得上，按实际情况选。</p>` : ""}
      ${guide}
    </div>`;
}

function shareActivityView() {
  const activity = app.shareActivity || { clients: [], recent: [] };
  const listening = Boolean(app.state.core.share && app.state.core.share.listening);
  const clients = activity.clients.map((client) => html`
    <div class="share-client">
      ${icon(isLoopbackIp(client.ip) ? "monitor" : "gamepad")}
      <div class="share-client-text">
        <div class="mono">${client.ip}${isLoopbackIp(client.ip) ? html` <span class="caption faint">这台电脑（测试）</span>` : ""}</div>
        <div class="caption muted">${client.connections} 个连接 · ↑ ${formatBytes(client.upload)} ↓ ${formatBytes(client.download)}${client.last_host ? ` · 最近 ${client.last_host}` : ""}${client.last_outbound ? ` → ${client.last_outbound}` : ""}</div>
      </div>
    </div>`);
  const recent = activity.recent.slice(0, 30).map((connection, index) => html`
    <div class="share-connection">
      <span class="mono share-connection-host" title="${connection.host}:${connection.port} · 来自 ${connection.client}">${connection.host}${connection.port && connection.port !== "443" && connection.port !== "80" ? `:${connection.port}` : ""}</span>
      <span class="caption faint share-connection-rule" title="${connection.rule}">${connection.rule}</span>
      <span class="share-connection-outbound ${connection.outbound === "DIRECT" ? "direct" : ""}">${connection.outbound === "DIRECT" ? "直连" : connection.outbound === "REJECT" ? "拦截" : connection.outbound}</span>
      ${connection.host ? html`<button class="button subtle icon-only" data-action="share-rule-menu" data-index="${index}" title="诊断或者添加自定义规则" aria-label="${connection.host} 的更多操作">${icon("more")}</button>` : ""}
    </div>`);
  return html`
    <div class="section-title">正在使用的设备</div>
    <div class="card share-list">
      ${clients.length ? html`${clients}<p class="caption faint" style="margin:6px 0 0">按来源 IP 归并，只统计现在还开着的连接。</p>`
        : html`<p class="caption muted" style="margin:0">${listening ? "还没有设备经这台电脑上网。PS5 上设置好后，打开商店或者测试互联网连接就能在这里看到它。" : "共享开启后，这里会列出正在使用的设备。"}</p>`}
    </div>
    <div class="section-title">最近的连接
      ${activity.recent.length ? html`<div class="actions"><button class="button subtle" data-action="share-clear">${icon("trash")}清空</button></div>` : ""}
    </div>
    <div class="card share-list">
      ${recent.length ? html`<div class="share-connections">${recent}</div>
        <p class="caption faint" style="margin:8px 0 0">PS5 的代理设置只对系统流量（联网测试、PSN、商店）和浏览器生效。打开 YouTube 这类应用时这里没有出现 youtube.com、googlevideo.com，说明那个应用没走代理，可以用 PS5 的浏览器打开同一个网站对照。域名一栏是 IP 时，说明设备自己解析的 DNS 被污染了，内核会从 TLS 握手里取回域名再分流。</p>`
        : html`<p class="caption muted" style="margin:0">设备经共享入口发起的连接会按时间列在这里：访问了哪个网站、走的是哪个节点还是直连、命中了哪条规则。</p>`}
    </div>`;
}

function sharePage() {
  if (!app.config) {
    return html`${pageHeader("局域网共享")}${configErrorView()}`;
  }
  const share = app.config.share;
  const core = app.state.core;
  const upstream = app.state.share.upstream;
  const listening = Boolean(core.share && core.share.listening);
  const test = app.shareTest || {};
  let testView = "";
  if (test.running) {
    testView = html`<span class="test-result muted"><span class="spinner" style="width:12px;height:12px"></span>正在经共享端口访问测速地址…</span>`;
  } else if (test.result) {
    testView = test.result.ok
      ? html`<span class="test-result" style="color:var(--success)">${icon("success")}${test.result.millis ? `${test.result.millis} ms · ` : ""}共享入口和上游都通。设备还连不上时，多半是 Windows 防火墙拦住了，或者设备和电脑不在同一个网络。</span>`
      : html`<span class="test-result" style="color:var(--danger)">${icon("error")}${test.result.message}</span>`;
  }
  const firewall = app.state.platform === "windows" || app.state.platform === "dev";
  const notice = !core.installed || core.installing || app.coreInstalling ? coreNotice("局域网共享") : "";
  return html`
    ${pageHeader("局域网共享", saveIndicator())}
    <p class="muted" style="margin:-12px 0 16px">让 PS5、Switch、手机这些同一局域网里的设备把这台电脑当代理服务器，享受和本机一样的网络。</p>
    ${notice}
    <div class="card card-group">
      ${settingCard({ iconName: "router", title: "允许局域网里的设备经这台电脑上网", description: shareStatusView(), control: html`<span class="switch-label">${share.enabled ? "开" : "关"}</span>${switchButton({ checked: app.state.config.share.enabled, action: "share-toggle", label: "局域网共享", disabled: app.busy })}` })}
      ${settingCard({ iconName: "signpost", title: "现在转发到", description: html`<span class="${upstream.kind === "proxy" ? "mono" : ""}">${shareUpstreamText(upstream)}</span>`, note: upstream.reason ? html`<span style="color:var(--warning)">${upstream.reason}</span>` : "" })}
    </div>
    <p class="caption faint" style="margin:8px 2px 0">跟着本机走：本机开着订阅配置，共享的设备就用同样的节点和分流规则；本机用其他代理（公司代理、别的代理软件），就转发给它；本机没开代理，就经这台电脑直接上网。本机切换配置时，共享的设备几秒内跟着变。</p>

    <div class="section-title">在 PS5 / Switch 上填写</div>
    ${shareAddressView(share)}

    <div class="section-title">谁能用、用哪个端口</div>
    <div class="card card-group">
      ${settingCard({ iconName: "shield", title: "允许的设备", description: "留空时同一局域网（10.x、172.16–31.x、192.168.x）里的任何设备都能用；在公共 Wi-Fi 上最好只填设备的 IP。本机自己总是允许的", control: html`<input class="input mono" style="width:240px" id="share-allowed" data-share-allowed value="${share.allowed}" placeholder="所有局域网设备" spellcheck="false" autocomplete="off" aria-label="允许的设备">`, note: app.shareInputError ? html`<span style="color:var(--danger)">${app.shareInputError}</span>` : "" })}
      ${settingCard({ iconName: "link", title: "端口", description: html`默认 17892，不能和代理内核的端口（${app.config.core.port}）相同。改了端口，设备上也要跟着改`, control: html`<input class="input mono numeric" style="width:96px" id="share-port" data-share-port value="${share.port}" inputmode="numeric" spellcheck="false" aria-label="端口"><button class="button" data-action="share-test" ${listening && !test.running ? "" : raw("disabled")} title="从本机经共享端口访问测速地址">${icon("gauge")}测试</button>`, note: testView })}
      ${firewall ? settingCard({ iconName: "shield", title: "Windows 防火墙", description: "第一次开启共享时 Windows 会询问是否允许 mihomo 访问网络，要点「允许」。点了取消、或者当前网络是「公用网络」时，设备会连不上：可以在这里放行（需要管理员权限）", control: html`<button class="button" data-action="share-firewall" ${app.shareFirewallBusy ? raw("disabled") : ""}>${icon("shield")}允许通过防火墙</button>` }) : ""}
    </div>

    <div class="section-title">保持唤醒</div>
    <div class="card card-group">
      ${settingCard({ iconName: "sun", title: "共享期间不让电脑睡眠", description: "电脑一睡，设备的网就断了。显示器照常可以关；合上笔记本的盖子仍然会睡眠。程序退出或关掉共享后恢复", control: html`${shareAwakeView(share)}${switchButton({ checked: share.keep_awake, setting: "share.keep_awake", label: "共享期间不让电脑睡眠" })}` })}
      ${settingCard({ iconName: "battery", title: "用电池时也保持", description: "默认只在接着电源时保持，免得忘了关把电用光", control: switchButton({ checked: share.keep_awake_on_battery, setting: "share.keep_awake_on_battery", label: "用电池时也保持", disabled: !share.keep_awake }) })}
    </div>

    ${shareActivityView()}`;
}

// ---------- 网址诊断 ----------

const diagnoseOutcomeIcons = { pending: "circle", pass: "success", warn: "warning", fail: "error", skipped: "minus" };

function diagnoseRowView(row) {
  const mark = row.outcome === "running"
    ? html`<span class="spinner" style="width:16px;height:16px"></span>`
    : icon(diagnoseOutcomeIcons[row.outcome] || "circle");
  return html`
    <div class="diagnose-row ${row.outcome}" data-row="${row.id}">
      <span class="diagnose-mark">${mark}</span>
      <div class="diagnose-row-text">
        <div class="diagnose-row-title">${row.title}</div>
        ${row.summary ? html`<div class="caption muted selectable">${row.summary}</div>` : ""}
        ${row.detail ? html`<div class="caption faint selectable">${row.detail}</div>` : ""}
      </div>
    </div>`;
}

function diagnoseVerdictView(job) {
  if (!job) {
    return html`<p class="caption muted" style="margin:0">检查完会在这里告诉你原因和怎么修。</p>`;
  }
  if (job.running) {
    return html`<p class="caption muted" style="margin:0;display:flex;gap:8px;align-items:center"><span class="spinner" style="width:14px;height:14px"></span>正在检查…</p>`;
  }
  if (!job.verdict) {
    return html`<p class="caption muted" style="margin:0">诊断已停止。</p>`;
  }
  const verdict = job.verdict;
  return html`
    <div class="diagnose-headline" id="diagnose-headline">${verdict.headline}</div>
    <p class="diagnose-explanation selectable">${verdict.explanation}</p>
    <div class="diagnose-actions">
      ${verdict.actions.map((action, index) => html`<button class="button ${index === 0 && action.kind !== "copy_report" ? "accent" : ""}" data-action="diagnose-action" data-index="${index}">${action.label}</button>`)}
      <button class="button subtle" data-action="diagnose-start">${icon("refresh")}再测一次</button>
    </div>`;
}

function diagnosePage() {
  const job = app.diagnoseJob;
  const running = Boolean(job && job.running);
  const perspective = app.diagnosePerspective;
  const perspectives = [["pc", "这台电脑"], ["device", "局域网设备（PS5 等）"]];
  return html`
    ${pageHeader("网址诊断")}
    <p class="muted" style="margin:-12px 0 16px">某个网站打不开？把链路走一遍，告诉你卡在哪、怎么修。</p>
    <div class="card diagnose-form">
      <div class="diagnose-input">
        <input class="input" id="diagnose-url" value="${app.diagnoseUrl}" placeholder="网址或域名，例如 youtube.com" spellcheck="false" autocomplete="off" aria-label="要诊断的网址">
        ${running
          ? html`<button class="button" data-action="diagnose-stop">${icon("close")}停止</button>`
          : html`<button class="button accent" data-action="diagnose-start">${icon("search")}开始诊断</button>`}
      </div>
      <div class="diagnose-perspective">
        <span class="caption muted">从谁的视角</span>
        <div class="segmented" role="group" aria-label="从谁的视角">${perspectives.map(([value, label]) => html`<button type="button" data-action="diagnose-perspective" data-value="${value}" aria-pressed="${perspective === value}">${label}</button>`)}</div>
      </div>
      ${app.diagnoseError ? html`<div class="field-error" style="margin-top:8px">${app.diagnoseError}</div>` : ""}
      <p class="caption faint" style="margin:10px 0 0">${perspective === "device"
        ? "从局域网共享的入口走一遍，和 PS5 等设备走的路径完全一样：共享入口 → 规则 → 上游。"
        : "按这台电脑现在的代理状态走一遍：本机代理、域名解析、直连、经代理、节点，逐项对比。"}</p>
    </div>
    <div class="section-title">检查结果${job ? html`<span class="caption faint mono">${job.url}</span>` : ""}</div>
    <div class="card diagnose-rows">
      ${job ? job.rows.map(diagnoseRowView) : html`<p class="caption muted" style="margin:0">填好网址点「开始诊断」。</p>`}
    </div>
    <div class="section-title">结论</div>
    <div class="card diagnose-verdict">${diagnoseVerdictView(job)}</div>`;
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
        <div class="network-values">${adapter.dns_suffix ? html`<span class="muted">DNS 后缀</span> <span class="mono">${adapter.dns_suffix}</span><span class="faint"> · </span>` : ""}<span class="muted">网关</span> <span class="mono">${adapter.gateway}</span>${adapter.gateway_mac ? html`<span class="faint"> · </span><span class="mono">${adapter.gateway_mac}</span>` : ""}</div>
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
      ${settingCard({ iconName: "gauge", title: "实时网速", description: "显示在托盘图标的提示和「代理」页的开关卡片上，每两秒更新一次", control: select("speed_display", config.speed_display, [["system", "系统网络总速度"], ["core", "只算内置代理"], ["none", "不显示"]]) })}
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

    ${hasSubscriptions() || config.core.path ? coreSettingsView(config) : ""}

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

// ---------- 系统集成 ----------

// systemPage 是和 Windows 打交道的设置：网页链接（proxyswitch:// 和机场网站的一键导入）等。
function systemPage() {
  if (!app.config) {
    return html`${pageHeader("系统集成")}${configErrorView()}`;
  }
  const config = app.config;
  return html`
    ${pageHeader("系统集成", saveIndicator())}

    <div class="section-title">网页链接</div>
    <div class="card card-group">
      ${settingCard({ iconName: "link", title: "网页链接", description: html`浏览器和脚本可以用链接操作 ProxySwitch，例如 <span class="mono">proxyswitch://toggle</span> 开关代理`, control: html`<span class="switch-label">${config.url_links ? "开" : "关"}</span>${switchButton({ checked: config.url_links, setting: "url_links", label: "网页链接" })}` })}
      ${config.url_links ? clashLinksCard() : ""}
    </div>

    <div class="section-title">微软商店应用</div>
    <div class="card card-group">
      ${settingCard({ iconName: "box", title: "让商店应用走代理", description: "商店、邮件、Xbox 等商店应用默认连不上本机的代理（127.0.0.1），开启代理后可能上不了网。允许后它们也能经代理上网，修改要管理员确认，重新打开这些应用后生效", control: loopbackControl() })}
    </div>`;
}

// loopbackControl 是商店应用一栏的状态和按钮：已允许几个应用、全部允许、选择应用。
function loopbackControl() {
  const loopback = app.loopback;
  if (!loopback || loopback.loading) {
    return html`<span class="caption muted" style="display:inline-flex;gap:6px;align-items:center"><span class="spinner" style="width:12px;height:12px"></span>正在读取应用</span>`;
  }
  if (loopback.error) {
    return html`<span class="caption" style="color:var(--danger)" title="${loopback.error}">读不到应用</span><button class="button" data-action="loopback-refresh">${icon("refresh")}重试</button>`;
  }
  const apps = loopback.info.apps;
  const allowed = apps.filter((item) => item.exempt).length;
  const busy = loopback.saving ? raw("disabled") : "";
  return html`
    <span class="caption muted" data-loopback-count>已允许 ${allowed} / ${apps.length} 个</span>
    ${allowed < apps.length ? html`<button class="button" data-action="loopback-all" ${busy}>${loopback.saving ? html`<span class="spinner"></span>` : ""}全部允许</button>` : ""}
    <button class="button" data-action="loopback-choose" ${busy}>选择应用…</button>`;
}

// clashLinksCard 说明机场网站的「一键导入 Clash」（clash:// 链接）由谁处理，别的程序在处理时可以接管。
function clashLinksCard() {
  const links = app.state.links || {};
  const effect = "点机场网站上的「一键导入 Clash」会打开 ProxySwitch，填好订阅地址";
  if (links.clash_ours) {
    return settingCard({ iconName: "download", title: "机场网站的一键导入", description: effect, control: html`<span class="badge success">由 ProxySwitch 处理</span>` });
  }
  const current = links.clash ? `现在由「${links.clash}」处理。` : "现在没有程序处理。";
  return settingCard({ iconName: "download", title: "机场网站的一键导入", description: `${current}改由 ProxySwitch 处理后，${effect}`, control: html`<button class="button" data-action="take-over-clash-links">改由 ProxySwitch 处理</button>` });
}

// coreSettingsView 是常规页里订阅使用的代理内核的设置：状态、本地端口、程序位置。
function coreSettingsView(config) {
  const core = app.state.core;
  let status = html`<span class="badge">没有运行</span>`;
  if (core.running) {
    status = html`<span class="badge success">运行中</span>`;
  } else if (core.error) {
    status = html`<span class="badge warning" title="${core.error}">出错</span>`;
  } else if (!core.installed) {
    status = html`<span class="badge">还没有下载</span>`;
  }
  const version = core.custom ? "本机的 mihomo" : core.installed_version || core.version;
  return html`
    <div class="section-title">代理内核</div>
    ${coreNotice()}
    <div class="card card-group">
      ${settingCard({ iconName: "box", title: "mihomo 内核", description: html`${version} · <span class="mono">${core.path}</span>`, control: status })}
      ${settingCard({ iconName: "link", title: "本地代理端口", description: "订阅配置开启后，系统代理和环境变量指向 127.0.0.1 的这个端口（HTTP 和 SOCKS5 共用）", control: html`<input class="input mono numeric" style="width:96px" id="core-port" data-setting-number="core.port" value="${config.core.port}" inputmode="numeric" spellcheck="false">` })}
      ${settingCard({ iconName: "folder", title: "内核程序的位置", description: core.downloadable ? "留空使用 ProxySwitch 下载的内核；也可以填本机已有的 mihomo 程序" : "填本机 mihomo 程序的完整路径", control: html`<input class="input mono" style="width:280px" id="core-path" data-setting-text="core.path" value="${config.core.path}" placeholder="${core.downloadable ? "自动下载" : "例如 C:\\mihomo\\mihomo.exe"}" spellcheck="false">` })}
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
      <button class="button" data-action="open-proxy-settings">${icon("external")}打开 Windows 代理设置</button>
      <button class="button danger" data-action="clear-all">${icon("trash")}清除所有代理设置</button>
    </div>
    ${diagnostics && diagnostics.last_crash ? html`
      <div class="section-title">${icon("warning")}最近一次意外退出<span class="caption faint">${diagnostics.last_crash_time}</span></div>
      <div class="infobar warning" style="margin-bottom:8px">${icon("warning")}<div class="infobar-body">ProxySwitch 在 ${diagnostics.last_crash_time} 出错退出过。反馈问题时请附上「复制诊断信息」的内容，里面包含下面的记录。</div></div>
      <div class="card"><pre class="log crash">${diagnostics.last_crash}</pre></div>` : ""}
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

function megabytes(bytes) {
  return `${(bytes / 1048576).toFixed(1)} MB`;
}

// renderNotes 把发布说明里常用的 Markdown（标题、列表、行内代码、加粗）转成简单的排版，其余按原文显示。
function renderNotes(text) {
  const inline = (line) => raw(escapeHtml(line)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>"));
  const blocks = [];
  let items = [];
  const flushList = () => {
    if (items.length) {
      blocks.push(html`<ul>${items.map((item) => html`<li>${inline(item)}</li>`)}</ul>`);
      items = [];
    }
  };
  for (const line of String(text || "").split(/\r?\n/)) {
    const trimmed = line.trim();
    if (/^[-*]\s+/.test(trimmed)) {
      items.push(trimmed.replace(/^[-*]\s+/, ""));
      continue;
    }
    flushList();
    const heading = /^#{1,6}\s+(.*)$/.exec(trimmed);
    if (heading) {
      blocks.push(html`<div class="notes-heading">${inline(heading[1])}</div>`);
    } else if (trimmed) {
      blocks.push(html`<p>${inline(trimmed)}</p>`);
    }
  }
  flushList();
  return html`${blocks}`;
}

// knownUpdate 是手动检查的结果，没有手动检查时用程序自动检查发现的新版本。
function knownUpdate() {
  return app.update || app.state.update || null;
}

function updateView() {
  const update = knownUpdate();
  if (app.restarting) {
    return html`
      <div class="infobar success"><span class="spinner"></span><div class="infobar-body">
        <div class="infobar-title">更新已安装，正在重新启动</div>
        新版本启动后会打开新的设置窗口，这个窗口会自动关闭。
      </div></div>`;
  }
  if (app.installing) {
    const progress = app.state.installing || { received: 0, total: 0 };
    const percent = progress.total > 0 ? Math.min(100, Math.round((progress.received * 100) / progress.total)) : 0;
    return html`
      <div class="infobar info">${icon("download")}<div class="infobar-body">
        <div class="infobar-title">正在下载 ProxySwitch ${update ? update.latest : ""}</div>
        <div class="progress ${progress.total > 0 ? "" : "indeterminate"}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${percent}"><span style="width:${percent}%"></span></div>
        <div class="caption muted numeric">${progress.total > 0 ? `${megabytes(progress.received)} / ${megabytes(progress.total)}` : progress.received > 0 ? megabytes(progress.received) : "正在连接…"}</div>
      </div></div>`;
  }
  if (!update) {
    return html``;
  }
  if (update.checking) {
    return html`<div class="infobar"><span class="spinner"></span><div class="infobar-body">正在检查更新…</div></div>`;
  }
  if (update.error) {
    return html`<div class="infobar warning">${icon("warning")}<div class="infobar-body">${update.error}</div></div>`;
  }
  if (!update.newer) {
    return html`<div class="infobar success">${icon("success")}<div class="infobar-body">已经是最新版本（${update.latest}）</div></div>`;
  }
  return html`
    <div class="infobar info">${icon("download")}<div class="infobar-body">
      <div class="infobar-title">发现新版本 ${update.latest}${update.published ? html`<span class="caption muted">（${update.published.slice(0, 10)} 发布）</span>` : ""}</div>
      ${update.notes ? html`<div class="caption release-notes">${renderNotes(update.notes)}</div>` : ""}
      ${app.installError ? html`<div class="caption" style="color:var(--danger);margin-top:6px">没有更新成功：${app.installError}</div>` : ""}
      <div class="infobar-actions">
        ${update.can_install
          ? html`<button class="button accent" data-action="install-update">${icon("download")}${update.asset_size ? `立即更新（${megabytes(update.asset_size)}）` : "立即更新"}</button>
            <button class="button" data-action="open-url" data-url="${update.url}">${icon("external")}查看发布页</button>`
          : html`<button class="button accent" data-action="open-url" data-url="${update.url}">${icon("external")}前往下载</button>`}
      </div>
      ${update.can_install ? html`<div class="caption faint" style="margin-top:8px">下载后自动校验并重新启动，设置和代理配置都会保留。</div>` : ""}
    </div></div>`;
}

function aboutPage() {
  const update = app.update;
  const busy = (update && update.checking) || app.installing || app.restarting;
  const repository = "https://github.com/whrss9527/proxyswitch";
  return html`
    ${pageHeader("关于")}
    <div class="card about-hero">
      ${logoSvg(app.state.palette[1] || "#2563eb", true)}
      <div style="flex:1">
        <div class="about-name">ProxySwitch</div>
        <div class="muted">快捷切换 Windows 代理 · 版本 <span class="numeric">${app.state.version}</span>${app.state.platform === "dev" ? html` <span class="badge warning">开发模式</span>` : ""}</div>
      </div>
      <button class="button" data-action="check-update" ${busy ? raw("disabled") : ""}>${icon("refresh")}检查更新</button>
    </div>
    <div style="margin-top:12px">${updateView()}</div>
    ${app.config ? html`
      <div class="section-title">更新</div>
      <div class="card">
        ${settingCard({ iconName: "download", title: "自动检查更新", description: "每天检查一次 GitHub 上的新版本，发现时在托盘提示，不会自动安装", control: html`<span class="switch-label">${app.config.check_updates ? "开" : "关"}</span>${switchButton({ checked: app.config.check_updates, setting: "check_updates", label: "自动检查更新" })}` })}
      </div>` : ""}
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
ProxySwitch.exe share [on|off]  开关局域网共享（不写表示切换）
ProxySwitch.exe diagnose 网址    网址诊断，加 --device 从局域网设备的视角
ProxySwitch.exe status          查看状态（退出码 0 开启，1 关闭）
ProxySwitch.exe settings        打开设置</pre></div>
    <div class="section-title">文件位置</div>
    <dl class="card kv">
      <dt>配置文件</dt><dd class="mono">${app.state.paths.config}</dd>
      <dt>日志</dt><dd class="mono">${app.state.paths.log}</dd>
      <dt>模式</dt><dd>${app.state.paths.portable ? "便携模式：配置和 exe 放在同一个文件夹" : "安装模式：配置保存在用户目录"}</dd>
    </dl>
    <p class="caption faint" style="margin-top:16px">MIT 许可证 · 不收集任何数据，只在检查更新时访问 GitHub</p>`;
}

const pageRenderers = {
  proxies: proxiesPage,
  share: sharePage,
  diagnose: diagnosePage,
  network: networkPage,
  general: generalPage,
  system: systemPage,
  diagnostics: diagnosticsPage,
  about: aboutPage,
};
