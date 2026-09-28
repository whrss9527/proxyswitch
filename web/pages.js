"use strict";

// 各页面的内容：代理、分流规则、连接、局域网共享、网址诊断、自动切换、常规、系统集成、诊断、关于。每个函数根据 app 里的状态生成整页 HTML。

const pages = [
  { id: "proxies", label: "代理", icon: "globe" },
  { id: "rules", label: "分流规则", icon: "signpost" },
  { id: "connections", label: "连接", icon: "traffic" },
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

// enabledRuleSets 是启用的规则集。
function enabledRuleSets() {
  return ((app.config && app.config.rule_sets) || []).filter((set) => !set.disabled);
}

// rulesSummary 与程序里的同名函数一致：只启用了一个规则集时是它的名字，否则是启用的个数。
function rulesSummary() {
  const sets = enabledRuleSets();
  if (sets.length === 0) {
    return "没有启用规则集";
  }
  return sets.length === 1 ? sets[0].name : `${sets.length} 个规则集`;
}

// modeText 是订阅配置的分流方式，例如「按规则分流 · 国内直连」或「全局代理」。
function modeText(profile) {
  return profile.mode === "global" ? "全局代理" : `按规则分流 · ${rulesSummary()}`;
}

// rulesMeta 是订阅配置的分流方式在列表里的说明，有规则集下载失败时标出来。
function rulesMeta(profile) {
  const failed = profile.mode === "global" ? [] : enabledRuleSets().filter((set) => ruleSetState(set).error);
  return html`
    <span>${modeText(profile)}</span>
    ${failed.length ? html`<span class="badge warning" title="${failed.map((set) => `${set.name}：${ruleSetState(set).error}`).join("\n")}">${failed.length} 个规则集下载失败</span>` : ""}`;
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
    const tun = app.state.core.tun && app.state.core.tun.active ? html`<span class="chip" title="TUN 模式：接管全部流量">TUN</span>` : "";
    subtitle = html`<strong style="color:var(--text)">${profile.name}</strong>${server}<span class="chips">${status.applied.map((label) => html`<span class="chip">${label}</span>`)}${tun}</span>`;
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

// policyLabel 是规则去向的说明：走节点、直连、拦截，或者走「策略组」。
function policyLabel(policy) {
  return policy.startsWith("group:") ? `走「${policy.slice(6)}」` : policyLabels[policy] || policy;
}

// policyChoices 是规则去向的候选：固定的三个加上现有的策略组。
function policyChoices() {
  return [...Object.keys(policyLabels), ...((app.config && app.config.policy_groups) || []).map((group) => `group:${group.name}`)];
}

function policyOptions(value) {
  return policyChoices().map((policy) => html`<option value="${policy}" ${policy === value ? raw("selected") : ""}>${policyLabel(policy)}</option>`);
}

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

// normalizeProgramTarget 与程序里的同名函数一致：去掉引号；Windows 上的程序名补上 .exe，路径里的 / 换成 \。
function normalizeProgramTarget(text) {
  const value = String(text || "").trim().replace(/^["']+|["']+$/g, "").trim();
  if (!value || app.state.platform !== "windows") {
    return value;
  }
  if (/[\\/]/.test(value)) {
    return value.replace(/\//g, "\\");
  }
  return /\.exe$/i.test(value) ? value : `${value}.exe`;
}

// programTargetProblem 检查整理过的程序名，没有问题时返回空。
function programTargetProblem(value) {
  if (!value) {
    return "请填写程序名，例如 WeChat.exe";
  }
  if (/[,\r\n\t]/.test(value) || value.length > 260) {
    return `程序「${value}」写得不对：填程序名（例如 WeChat.exe）或完整路径`;
  }
  return "";
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

// customRulesView 是分流规则页的自定义规则：域名、IP 或者程序固定走节点、直连、被拦截或者走某个策略组。
function customRulesView() {
  const rules = app.config.custom_rules || [];
  const draft = app.customRuleDraft;
  const program = draft.type === "program";
  const rows = rules.map((rule, index) => html`
    <div class="custom-rule ${rule.disabled ? "disabled" : ""}">
      <span class="mono custom-rule-value" title="${rule.value}">${rule.type === "program" ? html`<span class="badge" title="按程序分流">程序</span> ` : ""}${rule.value}</span>
      <select class="select" data-custom-rule="${index}" aria-label="「${rule.value}」的去向">${policyOptions(rule.policy)}</select>
      <button class="switch" role="switch" aria-checked="${!rule.disabled}" data-action="custom-rule-toggle" data-index="${index}" aria-label="启用「${rule.value}」" title="${rule.disabled ? "已停用" : "已启用"}"></button>
      <button class="button subtle icon-only" data-action="custom-rule-delete" data-index="${index}" title="删除" aria-label="删除「${rule.value}」">${icon("trash")}</button>
    </div>`);
  return html`
    <div class="section-title">自定义规则<span class="caption faint">最先匹配，全局代理时也生效</span></div>
    <div class="card custom-rules">
      ${rows.length ? html`<div class="custom-rule-list">${rows}</div>` : html`<p class="muted custom-rules-empty">还没有自定义规则。可以让某个网站固定走节点、走某个策略组，或者让公司内网、局域网里的服务直连。</p>`}
      <div class="custom-rule-add">
        <select class="select" data-focus="custom-rule-type" aria-label="按什么分流">${[["", "网站或 IP"], ["program", "程序"]].map(([value, label]) => html`<option value="${value}" ${value === draft.type ? raw("selected") : ""}>${label}</option>`)}</select>
        <input class="input mono" data-focus="custom-rule-value" value="${draft.value}" placeholder="${program ? "程序名，例如 WeChat.exe，也可以填完整路径" : "域名或 IP，例如 youtube.com、8.8.8.8、10.0.0.0/8"}" spellcheck="false" autocomplete="off" aria-label="${program ? "程序名" : "域名或 IP"}" ${program ? raw('list="running-programs"') : ""}>
        <select class="select" data-focus="custom-rule-policy" aria-label="去向">${policyOptions(draft.policy)}</select>
        <button class="button" data-action="add-custom-rule">${icon("plus")}添加</button>
      </div>
      ${program ? html`<datalist id="running-programs">${(app.programs || []).map((name) => html`<option value="${name}"></option>`)}</datalist>` : ""}
      <div class="field-error" data-custom-rule-error>${draft.error || ""}</div>
      <p class="caption faint" style="margin:6px 0 0">${program ? "按连接来自哪个程序分流，输入时可以从正在运行的程序里选。" : "域名包括它的子域名。"}改了立即生效，不用重启内核；局域网共享给其他设备的流量同样遵守。</p>
    </div>`;
}

// ---------- 分流规则 ----------

// ruleListExtensions 与程序里的一致：这些扩展名的是纯规则列表，交给内核加载；其余的当完整配置转换后并入。
const ruleListExtensions = ["list", "txt", "text", "yaml", "yml", "mrs"];

const ruleSetKinds = {
  builtin: { title: "内置", detail: "内置的规则，不用下载" },
  provider: { title: "规则列表", detail: "纯规则列表，由内核直接加载，更新不用重启内核" },
  convert: { title: "完整配置", detail: "小火箭、Surge 或 Clash 的完整配置，转换后并入，默认按文件里写的策略" },
};

const behaviorTitles = { classical: "规则", domain: "域名", ipcidr: "IP 段" };

// guessRuleSetKind 与程序里的 guessKind 一致：没下载前按地址猜加载方式。下载后以程序认出来的为准。
function guessRuleSetKind(url) {
  if (/^builtin:\/\//i.test(url)) {
    return "builtin";
  }
  let path = url;
  try {
    path = new URL(url).pathname;
  } catch (error) {
    // 不是完整的地址时直接看扩展名。
  }
  const match = /\.([a-z0-9]+)$/i.exec(path);
  return match && ruleListExtensions.includes(match[1].toLowerCase()) ? "provider" : "convert";
}

// ruleSetState 是规则集的下载情况（程序按地址给出）；刚添加、还没保存好时按地址猜。
function ruleSetState(set) {
  return (app.state.rule_sets || {})[set.url] || { kind: guessRuleSetKind(set.url), downloaded: false };
}

// ruleSetPolicy 是规则集实际的去向：没选时完整配置按文件里的（空），纯列表走节点，内置的直连。
function ruleSetPolicy(set, state) {
  if (set.policy) {
    return set.policy;
  }
  return { builtin: "direct", provider: "proxy" }[state.kind] || "";
}

// ruleSetDetail 是规则集的情况：规则数、更新时间和引用的列表，或者正在下载、没下载成功的原因。
function ruleSetDetail(set, state) {
  if (app.ruleSetUpdating[set.url]) {
    return html`<span class="spinner inline-spinner"></span>正在下载…`;
  }
  if (state.kind === "builtin") {
    const geo = app.state.core && app.state.core.geo_ready;
    return html`<span>国内的域名和 IP，不用下载规则${geo || set.disabled ? "" : "（地理数据下载好后生效）"}</span>`;
  }
  if (!state.downloaded) {
    if (set.disabled) {
      return html`<span>已停用</span>`;
    }
    if (state.error) {
      return html`<span class="rule-set-problem" title="${state.error}">没有下载成功：${state.error}。稍后自动重试，也可以点右边的按钮重新下载</span>`;
    }
    if (!hasSubscriptions()) {
      return html`<span>添加订阅后下载</span>`;
    }
    return html`<span class="spinner inline-spinner"></span>正在下载…`;
  }
  const parts = [];
  if (state.count) {
    parts.push(`${state.count.toLocaleString()} 条`);
  }
  if (state.sets) {
    parts.push(`引用 ${state.sets} 个规则列表`);
  }
  if (state.updated) {
    parts.push(`${relativeTime(state.updated)}更新`);
  }
  if (state.skipped) {
    parts.push(`${state.skipped} 条内核不支持，已跳过`);
  }
  return html`
    <span>${parts.join(" · ")}</span>
    ${state.failed_sets ? html`<span class="rule-set-problem">${state.failed_sets} 个引用的列表没下载到，下次更新时再试</span>` : ""}
    ${state.error ? html`<span class="rule-set-problem" title="${state.error}">更新失败，仍在用上次下载的：${state.error}</span>` : ""}`;
}

// ruleSetRow 是规则集列表的一行：开关、名字和情况、去向、重新下载、更多。
function ruleSetRow(set, index) {
  const state = ruleSetState(set);
  const kind = ruleSetKinds[state.kind] || ruleSetKinds.convert;
  const kindTitle = state.kind === "provider" && behaviorTitles[state.behavior] ? `${kind.title} · ${behaviorTitles[state.behavior]}` : kind.title;
  const policy = ruleSetPolicy(set, state);
  const choices = [...(state.kind === "convert" ? [["", "按文件里的"]] : []), ...policyChoices().map((value) => [value, policyLabel(value)])];
  const updating = Boolean(app.ruleSetUpdating[set.url]);
  return html`
    <div class="rule-set ${set.disabled ? "disabled" : ""}">
      <button class="switch" role="switch" aria-checked="${!set.disabled}" data-action="rule-set-toggle" data-index="${index}" aria-label="启用「${set.name}」" title="${set.disabled ? "已停用" : "已启用"}"></button>
      <div class="rule-set-text">
        <div class="rule-set-name"><span>${set.name}</span><span class="badge" title="${kind.detail}">${kindTitle}</span></div>
        ${state.kind === "builtin" ? "" : html`<div class="caption faint mono rule-set-url" title="${set.url}">${set.url}</div>`}
        <div class="caption muted rule-set-detail">${ruleSetDetail(set, state)}</div>
      </div>
      <select class="select" data-rule-set-policy="${index}" aria-label="「${set.name}」的去向" ${set.disabled ? raw("disabled") : ""}>${choices.map(([value, label]) => html`<option value="${value}" ${value === policy ? raw("selected") : ""}>${label}</option>`)}</select>
      ${state.kind === "builtin"
        ? html`<span class="rule-set-tool-spacer"></span>`
        : html`<button class="button subtle icon-only" data-action="rule-set-update" data-index="${index}" title="重新下载" aria-label="重新下载「${set.name}」" ${updating || set.disabled ? raw("disabled") : ""}>${icon("refresh")}</button>`}
      <button class="button subtle icon-only" data-action="rule-set-menu" data-index="${index}" title="更多" aria-label="「${set.name}」的更多操作">${icon("more")}</button>
    </div>`;
}

// ruleSetAddView 是规则集列表下面添加的一行：名字（可选）、地址、去向。
function ruleSetAddView() {
  const draft = app.ruleSetDraft;
  const choices = [["", "默认去向"], ...policyChoices().map((value) => [value, policyLabel(value)])];
  return html`
    <div class="rule-set-add">
      <input class="input rule-set-add-name" data-focus="rule-set-name" value="${draft.name}" placeholder="名字（可选）" spellcheck="false" autocomplete="off" aria-label="规则集的名字">
      <input class="input mono" data-focus="rule-set-url" value="${draft.url}" placeholder="规则地址：.list、.yaml、.mrs 列表或者 .conf 完整配置" spellcheck="false" autocomplete="off" aria-label="规则地址">
      <select class="select" data-focus="rule-set-policy" aria-label="去向" title="默认去向：完整配置按文件里写的策略，纯规则列表走节点">${choices.map(([value, label]) => html`<option value="${value}" ${value === draft.policy ? raw("selected") : ""}>${label}</option>`)}</select>
      <button class="button" data-action="add-rule-set">${icon("plus")}添加</button>
    </div>
    <div class="field-error" data-rule-set-error>${draft.error || ""}</div>`;
}

// finalView 是「其余流量」：没被任何规则命中的流量的去向。
function finalView() {
  const value = app.config.final_policy || "";
  // 跟随规则文件时用最后一个按文件里的策略分流的完整配置的 FINAL，写的是策略组的名字时走那个组（和程序里一样）。
  const fileFinal = enabledRuleSets().map((set) => ({ set, state: ruleSetState(set) })).filter(({ set, state }) => state.kind === "convert" && !set.policy && state.final).pop();
  let following = "现在没有完整配置写了 FINAL，其余流量走节点";
  if (fileFinal) {
    const name = (fileFinal.state.final_name || "").toLowerCase();
    const group = name && (app.config.policy_groups || []).find((item) => item.name.toLowerCase() === name);
    following = `现在按「${fileFinal.set.name}」里的 FINAL：${group ? policyLabel(`group:${group.name}`) : policyLabel(fileFinal.state.final)}`;
  }
  const choices = [["", "跟随规则文件"], ...policyChoices().map((policy) => [policy, policyLabel(policy)])];
  return html`
    <div class="section-title">其余流量</div>
    <div class="card">
      ${settingCard({
        iconName: "signpost",
        title: "没被任何规则命中的流量",
        description: value ? "规则集和自定义规则都没命中的流量走这里" : following,
        control: html`<select class="select" data-final-policy aria-label="其余流量的去向">${choices.map(([optionValue, label]) => html`<option value="${optionValue}" ${optionValue === value ? raw("selected") : ""}>${label}</option>`)}</select>`,
      })}
    </div>
    <p class="caption faint" style="margin:10px 4px 0">小火箭、Surge 的完整配置里有自己的 FINAL：黑名单类的是直连，白名单类的是走节点。「跟随规则文件」用最后一个按文件里的策略分流的完整配置的 FINAL；都是纯规则列表时其余流量走节点。</p>`;
}

function rulesPage() {
  if (!app.config) {
    return html`${pageHeader("分流规则")}${configErrorView()}`;
  }
  const sets = app.config.rule_sets || [];
  const notices = [];
  if (!hasSubscriptions()) {
    notices.push(html`
      <div class="infobar info">${icon("info")}<div class="infobar-body"><div class="infobar-title">分流规则只对订阅配置起作用</div>
        订阅配置用内置的代理内核连接机场的节点，按这里的规则决定哪些网站走节点。本机代理软件、PAC 这类配置由它们自己分流。
        <div class="infobar-actions"><button class="button" data-action="add-subscription">${icon("plus")}添加机场订阅</button></div></div></div>`);
  }
  if (hasSubscriptions()) {
    const notice = coreNotice("使用分流规则");
    if (notice.text) {
      notices.push(notice);
    }
  }
  const status = app.state.status;
  const active = status.state === "on" ? findProfile(status.profile) : null;
  if (active && active.subscription && active.mode === "global") {
    notices.push(html`
      <div class="infobar warning">${icon("warning")}<div class="infobar-body"><div class="infobar-title">「${active.name}」现在是全局代理</div>
        所有网站都经过节点，规则集和「其余流量」暂时不生效；自定义规则和局域网地址照常生效。
        <div class="infobar-actions"><button class="button" data-action="rules-mode-rule" data-id="${active.id}">${icon("swap")}切回按规则分流</button></div></div></div>`);
  }
  const downloadable = sets.some((set) => !set.disabled && guessRuleSetKind(set.url) !== "builtin");
  return html`
    ${pageHeader("分流规则", saveIndicator())}
    <p class="muted" style="margin:-12px 0 16px">哪些网站走节点、哪些直连、哪些拦截：自定义规则最先匹配，然后按规则集从上到下的顺序，都没命中的按「其余流量」。所有订阅配置共用这些规则。</p>
    ${notices}
    <div class="section-title">规则集<span class="caption faint">靠上的先匹配</span>
      <div class="actions">
        <button class="button subtle" data-action="rule-sets-update-all" ${app.ruleSetsUpdatingAll || !downloadable ? raw("disabled") : ""}>${app.ruleSetsUpdatingAll ? html`<span class="spinner"></span>` : icon("refresh")}全部更新</button>
        <button class="button accent" data-action="rule-library">${icon("layers")}从规则库添加</button>
      </div>
    </div>
    <div class="card rule-sets">
      ${sets.length ? html`<div class="rule-set-list">${sets.map((set, index) => ruleSetRow(set, index))}</div>` : html`<p class="muted rule-sets-empty">还没有规则集。从规则库里挑几条，或者把规则地址粘到下面。</p>`}
      ${ruleSetAddView()}
      <p class="caption faint" style="margin:6px 0 0">纯规则列表由内核直接加载；小火箭、Surge 的完整配置转换后并入，默认按文件里写的策略走，也可以改成统一的去向。规则集每天自动更新，GitHub 上的规则直连下载不了时会经节点或 jsDelivr 镜像下载；还没下载好的先跳过，下好了自动生效。</p>
    </div>
    ${customRulesView()}
    ${finalView()}`;
}

// ---------- 策略组 ----------

const groupTypes = {
  select: { title: "手动选择", detail: "自己选，默认跟随节点（正在使用的配置选中的节点）", icon: "mouse" },
  "url-test": { title: "自动选择", detail: "定期测延迟，自动用最低的那个", icon: "gauge" },
  fallback: { title: "故障转移", detail: "按顺序用第一个能用的节点，坏了自动换下一个", icon: "refresh" },
  "load-balance": { title: "负载均衡", detail: "筛出来的节点轮流用，分摊流量", icon: "swap" },
};

// groupSpecialLabels 是手动选择的组里三个特殊候选的名字（配置里的写法 → 显示名）。
const groupSpecialLabels = { "": "跟随节点", 自动选择: "自动选择", DIRECT: "直连" };

// groupState 是策略组在内核里的状态，内核没在运行时为空。
function groupState(name) {
  return ((app.state.groups && app.state.groups.states) || []).find((state) => state.name === name) || null;
}

// groupSourceProfile 是策略组的节点来源：正在使用的订阅配置。
function groupSourceProfile() {
  const source = app.state.groups && app.state.groups.source;
  return source ? app.config.profiles.find((profile) => profile.id === source) || null : null;
}

function memberText(member) {
  if (!member.node || !member.tested) {
    return member.label;
  }
  return `${member.label} · ${member.alive ? `${Math.max(1, member.delay || 0)} ms` : "超时"}`;
}

// groupPicker 是手动选择的组的候选下拉框：内核在运行时列出组里的全部候选（节点带延迟），否则只有三个特殊候选。
function groupPicker(group, index, state) {
  const node = group.node || "";
  let members = state ? state.members : Object.entries(groupSpecialLabels).map(([value, label]) => ({ value, label }));
  if (!members.some((member) => member.value === node)) {
    members = [...members, { value: node, label: `${node}（不在组里，跟随节点）` }];
  }
  const current = state && state.current && (node === "" || node === "自动选择") ? `：${state.current}` : "";
  return html`<select class="select group-picker" data-group-select="${group.name}" data-focus="group-select-${index}" aria-label="给「${group.name}」选节点" ${app.groupSelecting ? raw("disabled") : ""}>${members.map((member) => html`<option value="${member.value}" ${member.value === node ? raw("selected") : ""}>${memberText(member)}${member.value === node ? current : ""}</option>`)}</select>`;
}

// groupNowText 是自动挑选的组现在用的节点。
function groupNowText(group, state) {
  if (!state) {
    return html`<span class="caption faint">内核运行后显示在用的节点</span>`;
  }
  if (!state.current) {
    return html`<span class="caption muted">${group.type === "load-balance" ? "轮流使用筛出来的节点" : state.members.length && state.members[0].value === "COMPATIBLE" ? "没有筛到节点，暂时直连" : "还没挑出节点"}</span>`;
  }
  const member = state.members.find((item) => item.value === state.current);
  return html`<span class="caption muted" title="${groupTypes[group.type].detail}">现在用 <strong style="color:var(--text)">${member ? memberText(member) : state.current}</strong></span>`;
}

// policyGroupsView 是代理页的策略组：给某类流量单独选节点。只对订阅配置起作用，没有订阅配置时不显示。
function policyGroupsView() {
  if (!hasSubscriptions()) {
    return "";
  }
  const groups = app.config.policy_groups || [];
  const source = groupSourceProfile();
  const rows = groups.map((group, index) => {
    const state = groupState(group.name);
    const type = groupTypes[group.type] || groupTypes.select;
    return html`
      <div class="policy-group">
        <span class="policy-group-icon" title="${type.title}：${type.detail}">${icon(type.icon)}</span>
        <div class="policy-group-text">
          <div class="policy-group-name">${group.name}</div>
          <div class="caption muted policy-group-meta"><span>${type.title}</span><span class="mono" title="节点名的筛选">${group.filter || "全部节点"}</span></div>
        </div>
        <div class="policy-group-control">${group.type === "select" ? groupPicker(group, index, state) : groupNowText(group, state)}</div>
        <button class="button subtle icon-only" data-action="group-edit" data-index="${index}" title="编辑" aria-label="编辑「${group.name}」">${icon("pencil")}</button>
        <button class="button subtle icon-only" data-action="group-menu" data-index="${index}" title="更多" aria-label="「${group.name}」的更多操作">${icon("more")}</button>
      </div>`;
  });
  return html`
    <div class="section-title">策略组<span class="caption faint">${source ? `给某类流量单独选节点，节点来自「${source.name}」` : "给某类流量单独选节点"}</span>
      <div class="actions"><button class="button subtle" data-action="group-add">${icon("plus")}添加策略组</button></div>
    </div>
    <div class="card policy-groups">
      ${rows.length ? html`<div class="policy-group-list">${rows}</div>` : html`<p class="muted policy-groups-empty">还没有策略组。比如建一个「流媒体」组，在「分流规则」页把 netflix.com 或者 Netflix 的规则集指到它，就能单独给它选节点。</p>`}
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
      ${policyGroupsView()}`}`;
}

// ---------- 连接 ----------

// outboundLabel 是出口的名字：直连、拦截，其余是节点名或上游代理。
function outboundLabel(name) {
  if (!name || name === "DIRECT") {
    return "直连";
  }
  return /^REJECT/.test(name) ? "拦截" : name;
}

// connectionRoute 是连接走的路：经过策略组时是「流媒体 → 香港 01」，否则只有出口。
function connectionRoute(chains) {
  const outbound = (chains && chains[0]) || "DIRECT";
  const policy = chains && chains.length ? chains[chains.length - 1] : "";
  const group = policy !== outbound && ((app.config && app.config.policy_groups) || []).some((item) => item.name === policy) ? policy : "";
  return group ? `${group} → ${outboundLabel(outbound)}` : outboundLabel(outbound);
}

// connectionRule 是命中的规则：MATCH 是其余流量；规则集写成它的名字（内核里的名字以规则集的 id 开头）。
function connectionRule(rule) {
  const [kind, payload = ""] = String(rule || "").split(" ");
  if (kind === "Match") {
    return "其余流量";
  }
  if (kind === "RuleSet" && payload) {
    const set = ((app.config && app.config.rule_sets) || []).find((item) => {
      const id = ruleSetState(item).id;
      return id && (payload === id || payload.startsWith(`${id}-`));
    });
    if (set) {
      return `规则集「${set.name}」`;
    }
  }
  return rule || "";
}

// connectionSource 是连接的来源：发起连接的程序，局域网里的设备显示它的 IP。
function connectionSource(record) {
  if (record.process) {
    return record.process;
  }
  if (record.share) {
    return `${record.client}（设备）`;
  }
  return !record.client || isLoopbackIp(record.client) ? "本机" : record.client;
}

// formatSize 是连接和流量统计里的字节数：0 B、512 B、1.5 KB、23 MB、1.25 GB。
function formatSize(bytes) {
  let value = Math.max(0, bytes || 0);
  if (value < 1024) {
    return `${Math.round(value)} B`;
  }
  for (const unit of ["KB", "MB", "GB", "TB"]) {
    value /= 1024;
    if (value < 1023.5 || unit === "TB") {
      return `${value < 10 ? value.toFixed(value < 1 ? 2 : 1) : Math.round(value)} ${unit}`;
    }
  }
  return "";
}

// durationText 是连接开了多久。
function durationText(start) {
  const seconds = Math.floor((Date.now() - new Date(start).getTime()) / 1000);
  if (!(seconds >= 0)) {
    return "";
  }
  if (seconds < 60) {
    return `${seconds} 秒`;
  }
  if (seconds < 3600) {
    return `${Math.floor(seconds / 60)} 分钟`;
  }
  return `${Math.floor(seconds / 3600)} 小时 ${Math.floor((seconds % 3600) / 60)} 分`;
}

// exitPlace 是出口的地点：国家（按国家代码显示中文名）和城市。
function exitPlace(info) {
  let country = info.country || "";
  if (info.country_code) {
    try {
      country = new Intl.DisplayNames(["zh-CN"], { type: "region" }).of(info.country_code) || country;
    } catch (error) {
      // 不认识的国家代码就用接口给的名字。
    }
  }
  return [country, info.city && info.city !== info.country ? info.city : ""].filter(Boolean).join(" ");
}

// exitRow 是一种出口 IP：国家代码、IP、地点和运营商；经节点的还说明这次查询走的出口。
function exitRow({ label, kind, state, available, unavailableText }) {
  const info = state.info;
  const checking = state.checking || Boolean(app.exitChecking[kind]);
  let body;
  if (info) {
    const details = [exitPlace(info), info.organization].filter(Boolean).join(" · ");
    let route = "";
    if (kind === "proxy" && info.outbound) {
      let host = "";
      try {
        host = new URL(info.endpoint).hostname;
      } catch (error) {
        // 地址不对时不提主机名。
      }
      route = info.outbound === "DIRECT"
        ? html`<div class="caption exit-warning">这次查询按分流规则直连了，看到的是本机的出口。想看节点的出口，可以在分流规则里让 ${host || "查询接口"} 走节点</div>`
        : html`<div class="caption muted">经「${outboundLabel(info.outbound)}」出去</div>`;
    }
    body = html`
      <div class="exit-ip"><span class="badge exit-country" title="${exitPlace(info)}">${info.country_code || "?"}</span><span class="mono">${info.ip}</span></div>
      ${details ? html`<div class="caption muted">${details}</div>` : ""}
      ${route}`;
  } else if (checking) {
    body = html`<span class="caption muted"><span class="spinner inline-spinner"></span>正在查…</span>`;
  } else if (!available) {
    body = html`<span class="caption muted">${unavailableText}</span>`;
  } else if (state.error) {
    body = html`<span class="caption exit-warning">没查到：${state.error}</span>`;
  } else {
    body = html`<span class="caption muted">还没查</span>`;
  }
  return html`
    <div class="exit-row">
      <span class="exit-label">${label}</span>
      <div class="exit-body">${body}</div>
      <button class="button" data-action="exit-check" data-kind="${kind}" ${checking || !available ? raw("disabled") : ""}>${checking ? html`<span class="spinner"></span>` : icon("refresh")}${info ? "重查" : "查询"}</button>
    </div>`;
}

function trafficView(view) {
  const outbounds = view.traffic.outbounds || [];
  const top = outbounds.length ? Math.max(1, outbounds[0].upload + outbounds[0].download) : 1;
  const rows = outbounds.slice(0, 12).map((item) => html`
    <div class="traffic-row">
      <span class="traffic-name" title="${item.name}">${outboundLabel(item.name)}</span>
      <span class="traffic-bar"><span class="${item.name === "DIRECT" ? "direct" : ""}" style="width:${Math.max(1, Math.round(((item.upload + item.download) * 100) / top))}%"></span></span>
      <span class="caption muted numeric traffic-value">↑ ${formatSize(item.upload)} ↓ ${formatSize(item.download)}</span>
    </div>`);
  const since = view.traffic.since ? new Date(view.traffic.since) : null;
  const sinceText = since && !Number.isNaN(since.getTime()) ? `${formatDate(since.getTime() / 1000)} ${String(since.getHours()).padStart(2, "0")}:${String(since.getMinutes()).padStart(2, "0")}` : "";
  return html`
    <div class="section-title">流量统计
      <div class="actions"><button class="button subtle" data-action="traffic-reset" ${outbounds.length ? "" : raw("disabled")}>${icon("trash")}清零</button></div>
    </div>
    <div class="card traffic">
      <div class="traffic-session"><span>内核这次运行</span><span class="numeric">${view.running ? `↑ ${formatSize(view.session.upload)}　↓ ${formatSize(view.session.download)}` : "没有运行"}</span></div>
      ${rows.length ? html`<div class="traffic-list">${rows}</div>` : html`<p class="caption muted" style="margin:10px 0 0">有流量经过内核后，这里按节点（以及直连、上游代理）累计。</p>`}
      <p class="caption faint" style="margin:10px 0 0">${sinceText ? `从 ${sinceText} 起累计，` : ""}内核重启后接着算；每两秒采样一次，连接关掉前的最后一点流量算不进来，看趋势够用。</p>
    </div>`;
}

// connectionRow 是一条连接：目标和来源、命中的规则，右边是走的路和流量。list 是 active（正在进行的）或 recent。
function connectionRow(record, index, list) {
  const target = record.port && record.port !== "80" && record.port !== "443" ? `${record.host}:${record.port}` : record.host;
  const route = connectionRoute(record.chains);
  const direct = !record.chains || !record.chains.length || record.chains[0] === "DIRECT";
  const duration = durationText(record.start);
  return html`
    <div class="connection">
      <div class="connection-main">
        <div class="connection-target"><span class="mono" title="${record.host}:${record.port}">${target || "（未知）"}</span>${record.network && record.network !== "TCP" ? html`<span class="badge">${record.network}</span>` : ""}</div>
        <div class="caption muted connection-source">${[connectionSource(record), connectionRule(record.rule)].filter(Boolean).join(" · ")}</div>
      </div>
      <div class="connection-side">
        <div class="connection-route ${direct ? "direct" : ""}" title="${(record.chains || []).slice().reverse().join(" → ")}">${route}</div>
        <div class="caption muted numeric">${list === "active" ? `↑ ${formatSize(record.upload)} ↓ ${formatSize(record.download)}${duration ? ` · ${duration}` : ""}` : relativeTime(record.start)}</div>
      </div>
      <button class="button subtle icon-only" data-action="connection-menu" data-list="${list}" data-index="${index}" title="更多" aria-label="${record.host} 的更多操作">${icon("more")}</button>
    </div>`;
}

// filteredConnections 按页面上填的筛选（域名、程序、设备、规则或节点）过滤连接。
function filteredConnections(records) {
  const query = app.connectionsFilter.trim().toLowerCase();
  if (!query) {
    return records;
  }
  return records.filter((record) => [`${record.host}:${record.port}`, record.process, record.client, connectionRule(record.rule), record.rule, ...(record.chains || [])].some((value) => String(value || "").toLowerCase().includes(query)));
}

const connectionShownLimit = 80;

// connectionListsView 是正在进行的和最近的连接。筛选时只重绘这一块，不打断输入。
function connectionListsView() {
  const view = app.connections;
  const active = filteredConnections(view.active);
  const recent = filteredConnections(view.recent);
  const filtering = Boolean(app.connectionsFilter.trim());
  let activeContent;
  if (!view.running) {
    activeContent = html`<p class="caption muted" style="margin:0">内核没有运行。开启订阅配置或局域网共享后，经内核的连接会列在这里。</p>`;
  } else if (!active.length) {
    activeContent = html`<p class="caption muted" style="margin:0">${filtering ? "没有匹配的连接。" : "现在没有开着的连接。"}</p>`;
  } else {
    activeContent = html`
      <div class="connection-list">${active.slice(0, connectionShownLimit).map((record) => connectionRow(record, view.active.indexOf(record), "active"))}</div>
      ${active.length > connectionShownLimit ? html`<p class="caption faint" style="margin:8px 0 0">还有 ${active.length - connectionShownLimit} 条没有列出，用上面的筛选缩小范围。</p>` : ""}`;
  }
  return html`
    <div class="section-title">正在进行的连接<span class="caption faint numeric">${view.running ? `${view.active.length} 条` : ""}</span>
      <div class="actions"><button class="button subtle" data-action="connections-close-all" ${view.running && view.active.length ? "" : raw("disabled")}>${icon("close")}全部断开</button></div>
    </div>
    <div class="card connections">${activeContent}</div>
    <div class="section-title">最近的连接<span class="caption faint">短连接也记下来，最多 200 条</span>
      <div class="actions"><button class="button subtle" data-action="connections-clear" ${view.recent.length ? "" : raw("disabled")}>${icon("trash")}清空</button></div>
    </div>
    <div class="card connections">
      ${recent.length
        ? html`<div class="connection-list">${recent.slice(0, 60).map((record) => connectionRow(record, view.recent.indexOf(record), "recent"))}</div>`
        : html`<p class="caption muted" style="margin:0">${filtering && view.recent.length ? "没有匹配的连接。" : "经内核的连接会按时间记在这里：谁访问了什么、走了哪个节点、命中了哪条规则。"}</p>`}
    </div>`;
}

function connectionsPage() {
  if (!app.config) {
    return html`${pageHeader("连接")}${configErrorView()}`;
  }
  const view = app.connections;
  if (!view) {
    return html`${pageHeader("连接")}<div class="loading"><span class="spinner"></span>正在读取…</div>`;
  }
  const notices = [];
  if (hasSubscriptions()) {
    const notice = coreNotice("查看连接");
    if (notice.text) {
      notices.push(notice);
    }
  }
  return html`
    ${pageHeader("连接")}
    <p class="muted" style="margin:-12px 0 16px">谁在访问什么、走了哪个节点、命中了哪条规则；出口 IP 和按节点累计的流量。只统计经内置代理内核的连接。</p>
    ${notices}
    <div class="section-title">出口 IP</div>
    <div class="card exits">
      ${exitRow({ label: "经节点", kind: "proxy", state: view.exit.proxy, available: view.running, unavailableText: "内核运行后可以查" })}
      ${exitRow({ label: "直连", kind: "direct", state: view.exit.direct, available: true, unavailableText: "" })}
      <p class="caption faint" style="margin:8px 0 0">经节点的出口是网站看到的你的地址，切换节点后自动重查；直连的是这台电脑自己的公网地址。打开这一页时经 ip.sb、ipinfo.io、ipapi.co 这些公开接口查询。</p>
    </div>
    ${trafficView(view)}
    <div class="connections-toolbar">
      <input class="input" type="search" data-focus="connections-filter" value="${app.connectionsFilter}" placeholder="按域名、程序、设备、规则或节点筛选" aria-label="筛选连接" spellcheck="false" autocomplete="off">
    </div>
    <div data-connection-lists>${connectionListsView()}</div>
    <p class="caption faint" style="margin:10px 4px 0">来源是发起连接的程序，局域网共享的设备显示它的 IP。点一条连接右边的按钮，可以让这个网站或程序固定走某个去向、诊断它，或者断开它。</p>`;
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
      ${verdict.actions.map((action, index) => html`<button class="button ${index === 0 && action.kind !== "copy_report" && action.kind !== "flush_dns" ? "accent" : ""}" data-action="diagnose-action" data-index="${index}" data-kind="${action.kind}">${action.label}</button>`)}
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
      ${settingCard({ iconName: "shield", title: "守护系统代理", description: "代理开启期间，其他程序或 Windows 设置改掉系统代理时自动改回。被反复修改时会停下来提示", control: html`<span class="switch-label">${config.guard_proxy ? "开" : "关"}</span>${switchButton({ checked: config.guard_proxy, setting: "guard_proxy", label: "守护系统代理" })}` })}
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
      ${settingCard({ iconName: "refresh", title: "恢复默认设置", description: "把本页的启动、快捷键、托盘、通知、连接、外观和编辑器设置恢复为默认值，代理配置、规则和其他页的设置都会保留", control: html`<button class="button" data-action="reset-settings">恢复默认</button>` })}
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
    </div>

    <div class="section-title">WSL</div>
    <div class="card card-group">${wslCard()}</div>

    <div class="section-title">系统服务</div>
    <div class="card card-group">${winHttpCard()}</div>`;
}

// winHttpCard 是 WinHTTP 一栏：Windows 更新等系统服务用的代理，可以设为当前代理或改回直连（要管理员确认）。
// 它不跟随代理的开关，所以说明设成内置内核和其他代理软件时各要注意什么。
function winHttpCard() {
  const winhttp = app.winhttp;
  const title = "让系统服务走代理（WinHTTP）";
  const about = "Windows 更新、Microsoft Store 的下载等系统服务不看系统代理，用的是整台电脑的 WinHTTP 代理。它不跟随代理的开关，修改要管理员确认";
  if (!winhttp || winhttp.loading) {
    return settingCard({ iconName: "globe", title, description: about, control: html`<span class="caption muted" style="display:inline-flex;gap:6px;align-items:center"><span class="spinner" style="width:12px;height:12px"></span>正在读取</span>` });
  }
  if (winhttp.error) {
    return settingCard({ iconName: "globe", title, description: html`<span style="color:var(--danger)">${winhttp.error}</span>`, control: html`<button class="button" data-action="winhttp-refresh">${icon("refresh")}重试</button>` });
  }
  const info = winhttp.info;
  const busy = winhttp.busy ? raw("disabled") : "";
  const spinner = winhttp.busy ? html`<span class="spinner"></span>` : "";
  const using = info.proxy !== "" && info.proxy === info.suggested;
  let current = html`现在：直接连接`;
  if (info.error) {
    current = html`<span style="color:var(--danger)">${info.error}</span>`;
  } else if (info.proxy) {
    current = html`现在：<span class="mono">${info.proxy}</span>`;
  }
  let note = "";
  if (using && app.config && info.proxy === `127.0.0.1:${app.config.core.port}`) {
    note = "内置内核在 ProxySwitch 运行期间一直可用，退出 ProxySwitch 前改回直连";
  } else if (using) {
    note = "这个代理软件要一直开着，不用时改回直连";
  } else if (!info.suggested && info.unsupported) {
    note = info.unsupported;
  } else if (info.suggested) {
    note = html`当前代理：<span class="mono">${info.suggested}</span>`;
  }
  const direct = info.proxy ? html`<button class="button" data-action="winhttp-direct" ${busy}>${spinner}改回直连</button>` : "";
  const useProxy = using ? html`<span class="badge success">正在使用当前代理</span>` : html`<button class="button" data-action="winhttp-proxy" ${info.suggested ? busy : raw("disabled")} title="${info.unsupported || ""}">${info.proxy ? "" : spinner}设为当前代理</button>`;
  return settingCard({ iconName: "globe", title, description: html`${about}。${current}${note ? html`。${note}` : ""}`, control: html`${useProxy}${direct}` });
}

// wslCard 是 WSL 一栏：一键设置成使用本机的代理（镜像网络 + 自动代理），改完提示重启 WSL；
// 不支持镜像网络的 Windows 说明怎样经局域网共享使用代理。
function wslCard() {
  const wsl = app.wsl;
  const title = "让 WSL 走代理";
  if (!wsl || wsl.loading) {
    return settingCard({ iconName: "terminal", title, control: html`<span class="caption muted" style="display:inline-flex;gap:6px;align-items:center"><span class="spinner" style="width:12px;height:12px"></span>正在读取</span>` });
  }
  if (wsl.error) {
    return settingCard({ iconName: "terminal", title, description: html`<span style="color:var(--danger)">${wsl.error}</span>`, control: html`<button class="button" data-action="wsl-refresh">${icon("refresh")}重试</button>` });
  }
  const info = wsl.info;
  if (!info.installed) {
    return settingCard({ iconName: "terminal", title, description: "没有检测到 WSL（适用于 Linux 的 Windows 子系统）的 Linux 发行版" });
  }
  const distros = info.distros.join("、");
  if (!info.supported) {
    const port = app.config.share.port;
    const command = `export http_proxy=http://$(ip route show default | awk '{print $3}'):${port} https_proxy=$http_proxy all_proxy=$http_proxy`;
    return settingCard({ iconName: "terminal", title, description: html`${distros}。这个 Windows 版本的 WSL 连不到本机的 127.0.0.1（Windows 11 22H2 起才能用镜像网络）。可以打开局域网共享，在 WSL 里运行：<span class="mono" style="display:block;margin-top:4px;user-select:all">${command}</span>`, control: html`<button class="button" data-action="copy-wsl-command" data-command="${command}">${icon("copy")}复制命令</button>` });
  }
  const busy = wsl.busy ? raw("disabled") : "";
  const restartButton = (kind) => html`<button class="button ${kind}" data-action="wsl-restart" ${busy}>${wsl.busy ? html`<span class="spinner"></span>` : icon("refresh")}重启 WSL</button>`;
  const ready = info.mirrored && info.auto_proxy;
  if (info.restart) {
    const done = ready ? "已写好设置" : "已恢复 WSL 默认的网络设置";
    return settingCard({ iconName: "terminal", title, description: `${done}，重启 WSL 后生效（会关闭 WSL 里正在运行的程序，包括 Docker Desktop 的容器）`, control: restartButton("accent") });
  }
  if (ready) {
    return settingCard({ iconName: "terminal", title, description: `${distros}使用镜像网络，WSL 启动时自动使用 Windows 的代理设置。之后开关代理或者换了配置，要重启 WSL 才会跟着变`, control: html`<span class="badge success">已设置</span>${restartButton("")}<button class="button" data-action="wsl-reset" ${busy}>撤销</button>` });
  }
  return settingCard({ iconName: "terminal", title, description: html`${distros}。把 WSL 的网络设成镜像模式并自动使用 Windows 的代理，WSL 里的 Linux 程序也走本机的代理（写在 <span class="mono">${info.config}</span>）`, control: html`<button class="button" data-action="wsl-setup" ${busy}>${wsl.busy ? html`<span class="spinner"></span>` : ""}一键设置</button>` });
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

// tunDescription 说明 TUN 模式的作用和现在的状态：正在接管、没开起来的原因，或者等订阅配置开启。
function tunDescription(config) {
  const tun = app.state.core.tun || {};
  if (!config.tun.enabled) {
    return "让不认系统代理的程序（游戏、部分命令行工具等）也走订阅的节点和分流规则。内核要以管理员权限运行，启动时确认一次";
  }
  if (tun.active) {
    return html`<span style="color:var(--success)">正在接管这台电脑的全部流量</span>`;
  }
  if (tun.error) {
    return html`<span style="color:var(--danger)" data-tun-error>${tun.error}</span>${tun.error.includes("管理员") ? "。关掉再打开可以重新确认" : ""}`;
  }
  return "开启订阅配置后生效，到时内核会请求管理员权限";
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
      ${settingCard({ iconName: "layers", title: "TUN 模式（虚拟网卡）", description: tunDescription(config), control: html`<span class="switch-label">${config.tun.enabled ? "开" : "关"}</span>${switchButton({ checked: config.tun.enabled, setting: "tun.enabled", label: "TUN 模式" })}` })}
      ${settingCard({ iconName: "clock", title: "按流量计费时暂停更新", description: meteredDescription(config), control: html`<span class="switch-label">${config.pause_on_metered ? "开" : "关"}</span>${switchButton({ checked: config.pause_on_metered, setting: "pause_on_metered", label: "按流量计费时暂停更新" })}` })}
      ${settingCard({ iconName: "link", title: "本地代理端口", description: "订阅配置开启后，系统代理和环境变量指向 127.0.0.1 的这个端口（HTTP 和 SOCKS5 共用）", control: html`<input class="input mono numeric" style="width:96px" id="core-port" data-setting-number="core.port" value="${config.core.port}" inputmode="numeric" spellcheck="false">` })}
      ${settingCard({ iconName: "folder", title: "内核程序的位置", description: core.downloadable ? "留空使用 ProxySwitch 下载的内核；也可以填本机已有的 mihomo 程序" : "填本机 mihomo 程序的完整路径", control: html`<input class="input mono" style="width:280px" id="core-path" data-setting-text="core.path" value="${config.core.path}" placeholder="${core.downloadable ? "自动下载" : "例如 C:\\mihomo\\mihomo.exe"}" spellcheck="false">` })}
    </div>`;
}

// meteredDescription 说明按流量计费的网络上暂停哪些更新，现在正暂停着时标出来。
function meteredDescription(config) {
  const text = "手机热点、设成按流量计费的 Wi-Fi 上不自动更新订阅和分流规则，手动更新不受影响";
  if (config.pause_on_metered && app.state.network && app.state.network.metered) {
    return html`${text}。<strong>现在是按流量计费的网络，已暂停</strong>`;
  }
  return text;
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
        <dt>WinHTTP</dt><dd>${diagnostics.winhttp ? html`<span class="mono">${diagnostics.winhttp}</span>` : html`<span class="faint">直接连接</span>`}</dd>
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

// donateCard 是关于页的「请我喝杯咖啡」：微信赞赏码，窗口够宽时放在右边，窄的时候排到下面；点一下放大，方便手机扫。
function donateCard() {
  return html`
    <div class="card donate-card">
      <button class="donate-image" data-action="donate-enlarge" title="点击放大" aria-label="微信赞赏码：请我喝杯咖啡，点击放大">
        <img src="/assets/donate-wechat.png" alt="微信赞赏码：请我喝杯咖啡" width="192" height="256">
      </button>
      <div class="donate-text">觉得好用的话，<br>微信扫一扫请我喝杯咖啡</div>
      <div class="caption faint">点图片可以放大</div>
    </div>`;
}

function aboutPage() {
  const update = app.update;
  const busy = (update && update.checking) || app.installing || app.restarting;
  const repository = "https://github.com/whrss9527/proxyswitch";
  return html`
    ${pageHeader("关于")}
    <div class="about-top">
      <div class="about-main">
        <div class="card about-hero">
          ${logoSvg(app.state.palette[1] || "#2563eb", true)}
          <div style="flex:1">
            <div class="about-name">ProxySwitch</div>
            <div class="muted">快捷切换 Windows 代理 · 版本 <span class="numeric">${app.state.version}</span>${app.state.platform === "dev" ? html` <span class="badge warning">开发模式</span>` : ""}</div>
          </div>
          <button class="button" data-action="check-update" ${busy ? raw("disabled") : ""}>${icon("refresh")}检查更新</button>
        </div>
        <div style="margin-top:12px">${updateView()}</div>
      </div>
      ${donateCard()}
    </div>
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
  rules: rulesPage,
  connections: connectionsPage,
  share: sharePage,
  diagnose: diagnosePage,
  network: networkPage,
  general: generalPage,
  system: systemPage,
  diagnostics: diagnosticsPage,
  about: aboutPage,
};
