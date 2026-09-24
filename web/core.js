"use strict";

// 设置页的基础工具：安全的 HTML 模板、接口调用、图标、提示、对话框、菜单、主题色。

// ---------- HTML 模板：插入的值默认转义，只有 html`` 或 raw() 的结果原样插入 ----------

class SafeHtml {
  constructor(text) {
    this.text = text;
  }
  toString() {
    return this.text;
  }
}

function raw(text) {
  return new SafeHtml(String(text));
}

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
}

function renderValue(value) {
  if (value instanceof SafeHtml) {
    return value.text;
  }
  if (Array.isArray(value)) {
    return value.map(renderValue).join("");
  }
  if (value === null || value === undefined) {
    return "";
  }
  // 布尔值按 "true" / "false" 输出，用于 aria-checked 等属性。
  return escapeHtml(value);
}

function html(strings, ...values) {
  let result = strings[0];
  values.forEach((value, index) => {
    result += renderValue(value) + strings[index + 1];
  });
  return new SafeHtml(result);
}

function setHtml(element, content) {
  element.innerHTML = String(content);
}

function clone(value) {
  return value === undefined ? undefined : JSON.parse(JSON.stringify(value));
}

function sleep(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function debounce(action, delay) {
  let timer = 0;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => action(...args), delay);
  };
}

// ---------- 图标（20×20 线条图标） ----------

const iconPaths = {
  globe: "M10 2.5a7.5 7.5 0 1 0 0 15a7.5 7.5 0 0 0 0-15Z M2.5 10h15 M10 2.5c2 2.2 3 4.7 3 7.5s-1 5.3-3 7.5 M10 2.5c-2 2.2-3 4.7-3 7.5s1 5.3 3 7.5",
  wifi: "M2.5 7.8a10.6 10.6 0 0 1 15 0 M5 10.5a7 7 0 0 1 10 0 M7.5 13.2a3.5 3.5 0 0 1 5 0 M10 16v.01",
  sliders: "M3 6h8 M15 6h2 M3 14h2 M9 14h8 M11 6a2 2 0 1 0 4 0a2 2 0 1 0-4 0 M5 14a2 2 0 1 0 4 0a2 2 0 1 0-4 0",
  pulse: "M2.5 10h3l2-4.5 3.5 9 2-4.5h4.5",
  info: "M10 2.5a7.5 7.5 0 1 0 0 15a7.5 7.5 0 0 0 0-15Z M10 9v5 M10 6.3v.01",
  plus: "M10 4v12 M4 10h12",
  search: "M8.5 3.5a5 5 0 1 0 0 10a5 5 0 0 0 0-10Z M12.2 12.2 16.5 16.5",
  gauge: "M3.5 14a6.5 6.5 0 1 1 13 0 M10 14l3.2-4.6 M10 14v.01",
  pencil: "M12.5 4.5l3 3L7 16H4v-3Z M11 6l3 3",
  trash: "M4 6h12 M8 6V4.5h4V6 M5.5 6l.8 9.5h7.4l.8-9.5 M8.5 9v4 M11.5 9v4",
  up: "M10 15.5v-11 M5.5 9 10 4.5 14.5 9",
  down: "M10 4.5v11 M5.5 11l4.5 4.5 4.5-4.5",
  more: "M5 10v.01 M10 10v.01 M15 10v.01",
  copy: "M7 7h8.5v8.5H7Z M4.5 13V4.5H13",
  check: "M4.5 10.5l3.5 3.5 7.5-8",
  close: "M5 5l10 10 M15 5 5 15",
  warning: "M10 3.5l7 12.5H3Z M10 8.5v3.5 M10 14.3v.01",
  error: "M10 2.5a7.5 7.5 0 1 0 0 15a7.5 7.5 0 0 0 0-15Z M7.5 7.5l5 5 M12.5 7.5l-5 5",
  success: "M10 2.5a7.5 7.5 0 1 0 0 15a7.5 7.5 0 0 0 0-15Z M6.8 10.3l2.2 2.2 4.2-4.6",
  folder: "M2.5 5.5a1 1 0 0 1 1-1h4l1.5 1.5h7.5a1 1 0 0 1 1 1V15a1 1 0 0 1-1 1h-13a1 1 0 0 1-1-1Z",
  document: "M5 2.5h6.5l3.5 3.5v11.5H5Z M11.5 2.5V6H15 M7.5 10h5 M7.5 13h5",
  refresh: "M15.5 10a5.5 5.5 0 1 1-1.6-3.9 M15.5 3.5v3.5H12",
  external: "M11 4.5h4.5V9 M15.5 4.5 9 11 M13.5 12v3.5h-9v-9H8",
  download: "M10 3.5v9 M6 9l4 4 4-4 M4 16.5h12",
  upload: "M10 13V4 M6 7.5 10 3.5l4 4 M4 16.5h12",
  keyboard: "M2.5 5.5h15v9h-15Z M5.5 8.5h.01 M8.5 8.5h.01 M11.5 8.5h.01 M14.5 8.5h.01 M6.5 11.5h7",
  power: "M10 3v6 M6 5.5a6 6 0 1 0 8 0",
  bell: "M5.5 13.5V9a4.5 4.5 0 0 1 9 0v4.5l1.5 1.5h-12Z M8.5 17h3",
  mouse: "M10 2.5a4.5 4.5 0 0 0-4.5 4.5v6a4.5 4.5 0 0 0 9 0V7A4.5 4.5 0 0 0 10 2.5Z M10 5.5v2.5",
  palette: "M10 2.5a7.5 7.5 0 1 0 0 15c1 0 1.5-.7 1.5-1.5 0-1.2-1-1.3-1-2.3 0-.9.7-1.4 1.6-1.4h1.9A3.5 3.5 0 0 0 17.5 9 7.3 7.3 0 0 0 10 2.5Z M6.5 9v.01 M9 6v.01 M12.5 6.5v.01",
  window: "M3 4.5h14v11H3Z M3 7.5h14",
  link: "M8.5 11.5a3 3 0 0 0 4.2 0l2.3-2.3a3 3 0 0 0-4.2-4.2L9.7 6.1 M11.5 8.5a3 3 0 0 0-4.2 0L5 10.8A3 3 0 0 0 9.2 15l1.1-1.1",
  terminal: "M3 4.5h14v11H3Z M6 8l2.5 2L6 12 M10 12.5h4",
  box: "M10 2.8l6.5 3.5v7.4L10 17.2 3.5 13.7V6.3Z M3.5 6.3 10 9.8l6.5-3.5 M10 9.8v7.4",
  branch: "M6 4v12 M14 6.5c0 3.5-8 2.5-8 6 M6 4h.01 M14 6.5h.01 M6 16h.01",
  monitor: "M3 4h14v9.5H3Z M7.5 16.5h5 M10 13.5v3",
  clock: "M10 2.5a7.5 7.5 0 1 0 0 15a7.5 7.5 0 0 0 0-15Z M10 6v4l2.5 1.5",
  shield: "M10 2.5l6 2.5v4.5c0 3.8-2.6 6.6-6 8-3.4-1.4-6-4.2-6-8V5Z",
  chevron: "M8 5l5 5-5 5",
  swap: "M4 7h11l-3-3 M16 13H5l3 3",
  play: "M6.5 4.5l9 5.5-9 5.5Z",
  layers: "M10 3l7 4-7 4-7-4Z M3 11l7 4 7-4",
  signpost: "M10 2.5v15 M4.5 4.5h9l2 2-2 2h-9Z M15.5 10.5h-9l-2 2 2 2h9Z",
};

const boldIcons = new Set(["more"]);

function icon(name, className = "") {
  const width = boldIcons.has(name) ? ' style="stroke-width:2.4"' : "";
  return raw(`<svg class="icon ${className}" viewBox="0 0 20 20" aria-hidden="true"${width}><path d="${iconPaths[name] || ""}"/></svg>`);
}

// 与托盘图标相同的“开关”造型，on 时滑块在右侧。
function logoSvg(color, on, className = "brand-logo") {
  const knob = on ? 21 : 11;
  return raw(`<svg class="${className}" viewBox="0 0 32 32" aria-hidden="true"><rect x="1" y="6" width="30" height="20" rx="10" fill="${escapeHtml(color)}"/><circle cx="${knob}" cy="16" r="7" fill="#fff"/></svg>`);
}

// ---------- 接口 ----------

const tokenKey = "proxyswitch-token";
const pageToken = (() => {
  const query = new URLSearchParams(location.search);
  const token = query.get("token");
  if (token) {
    // 把 token 从地址栏移走：刷新仍然可用，复制地址也不会带上它。
    sessionStorage.setItem(tokenKey, token);
    history.replaceState(null, "", "/");
    return token;
  }
  return sessionStorage.getItem(tokenKey) || "";
})();

class ApiError extends Error {
  constructor(message, status, state) {
    super(message);
    this.status = status;
    this.state = state;
  }
}

let connectionLost = null;

async function api(method, path, body) {
  const options = { method, headers: { "X-Token": pageToken } };
  if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = typeof body === "string" ? body : JSON.stringify(body);
  }
  let response;
  try {
    response = await fetch(path, options);
  } catch (error) {
    if (connectionLost) {
      connectionLost("exited");
    }
    throw new ApiError("连不上 ProxySwitch，它可能已经退出", 0);
  }
  if (response.status === 403) {
    if (connectionLost) {
      connectionLost("expired");
    }
    throw new ApiError("页面已失效", 403);
  }
  let data = null;
  try {
    data = await response.json();
  } catch (error) {
    data = null;
  }
  if (!response.ok) {
    throw new ApiError((data && data.error) || `请求失败（HTTP ${response.status}）`, response.status, data && data.state);
  }
  return data;
}

// ---------- 提示 ----------

const toastIcons = { success: "success", info: "info", warning: "warning", danger: "error" };

function toast(message, kind = "success", title = "") {
  const container = document.getElementById("toasts");
  const element = document.createElement("div");
  element.className = `toast ${kind}`;
  setHtml(element, html`${icon(toastIcons[kind] || "info")}<div class="toast-text">${title ? html`<strong>${title}</strong>\n` : ""}${message}</div>`);
  container.appendChild(element);
  const duration = kind === "danger" || kind === "warning" ? 6000 : 3200;
  setTimeout(() => {
    element.classList.add("leaving");
    setTimeout(() => element.remove(), 200);
  }, duration);
}

// ---------- 对话框 ----------

let openDialogs = [];

// openDialog 打开模态对话框。render(dialog) 返回内容，可多次调用 dialog.render() 刷新。
function openDialog({ className = "", render, onMount, onClose, dismissible = true }) {
  const previousFocus = document.activeElement;
  const overlay = document.createElement("div");
  overlay.className = "overlay";
  const element = document.createElement("div");
  element.className = `dialog ${className}`;
  element.setAttribute("role", "dialog");
  element.setAttribute("aria-modal", "true");
  overlay.appendChild(element);
  document.body.appendChild(overlay);

  const dialog = {
    element,
    closed: false,
    render() {
      const focusKey = document.activeElement && element.contains(document.activeElement) ? document.activeElement.dataset.focus : null;
      const selection = focusKey && "selectionStart" in document.activeElement ? [document.activeElement.selectionStart, document.activeElement.selectionEnd] : null;
      const body = element.querySelector(".dialog-body");
      const scroll = body ? body.scrollTop : 0;
      setHtml(element, render(dialog));
      const newBody = element.querySelector(".dialog-body");
      if (newBody) {
        newBody.scrollTop = scroll;
      }
      if (focusKey) {
        const target = element.querySelector(`[data-focus="${focusKey}"]`);
        if (target) {
          target.focus();
          if (selection && "setSelectionRange" in target) {
            try {
              target.setSelectionRange(selection[0], selection[1]);
            } catch (error) {
              // 部分输入框类型不支持设置选区。
            }
          }
        }
      }
    },
    close(result) {
      if (dialog.closed) {
        return;
      }
      dialog.closed = true;
      overlay.remove();
      openDialogs = openDialogs.filter((item) => item !== dialog);
      if (previousFocus && previousFocus.focus) {
        previousFocus.focus();
      }
      if (onClose) {
        onClose(result);
      }
    },
    dismissible,
  };
  openDialogs.push(dialog);
  dialog.render();
  if (onMount) {
    onMount(dialog);
  }
  const first = element.querySelector("[autofocus], .dialog-body input, .dialog-body select, .dialog-body textarea, .dialog-footer .button.accent");
  if (first) {
    first.focus();
  }
  return dialog;
}

function topDialog() {
  return openDialogs[openDialogs.length - 1];
}

// 对话框内按 Tab 不离开对话框，Esc 关闭。
document.addEventListener("keydown", (event) => {
  const dialog = topDialog();
  if (!dialog) {
    return;
  }
  if (event.key === "Escape" && dialog.dismissible && !recordingHotkey) {
    event.preventDefault();
    dialog.close(null);
    return;
  }
  if (event.key !== "Tab") {
    return;
  }
  const focusable = [...dialog.element.querySelectorAll("button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex='0']")].filter((item) => item.offsetParent !== null);
  if (focusable.length === 0) {
    return;
  }
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
});

// confirmDialog 显示确认框，返回用户是否确认。
function confirmDialog({ title, message, confirmText = "确定", danger = false }) {
  return new Promise((resolve) => {
    openDialog({
      className: "small",
      render: () => html`
        <div class="dialog-body">
          <h2 class="dialog-title">${title}</h2>
          <div class="muted" style="white-space:pre-line">${message}</div>
        </div>
        <div class="dialog-footer">
          <button class="button ${danger ? "danger" : "accent"}" data-dialog-result="yes" autofocus>${confirmText}</button>
          <button class="button" data-dialog-result="no">取消</button>
        </div>`,
      onMount: (dialog) => {
        dialog.element.addEventListener("click", (event) => {
          const button = event.target.closest("[data-dialog-result]");
          if (button) {
            dialog.close(button.dataset.dialogResult === "yes");
          }
        });
      },
      onClose: (result) => resolve(result === true),
    });
  });
}

// ---------- 弹出菜单 ----------

let openMenuElement = null;

function closeMenu() {
  if (openMenuElement) {
    openMenuElement.remove();
    openMenuElement = null;
  }
}

// openMenu 在 anchor 下方弹出菜单，items: { label, icon, action, danger, disabled, title } 或 { separator: true }。
function openMenu(anchor, items) {
  closeMenu();
  const menu = document.createElement("div");
  menu.className = "menu";
  menu.setAttribute("role", "menu");
  setHtml(menu, html`${items.map((item, index) => item.separator
    ? html`<div class="menu-separator"></div>`
    : html`<button class="menu-item ${item.danger ? "danger" : ""}" role="menuitem" data-menu-index="${index}" ${item.title ? html`title="${item.title}"` : ""} ${item.disabled ? raw("disabled") : ""}>${item.icon ? icon(item.icon) : ""}<span>${item.label}</span></button>`)}`);
  document.body.appendChild(menu);
  const rect = anchor.getBoundingClientRect();
  const width = menu.offsetWidth;
  const height = menu.offsetHeight;
  let left = rect.right - width;
  let top = rect.bottom + 4;
  if (left < 8) {
    left = 8;
  }
  if (top + height > window.innerHeight - 8) {
    top = rect.top - height - 4;
  }
  menu.style.left = `${left}px`;
  menu.style.top = `${top}px`;
  openMenuElement = menu;
  menu.addEventListener("click", (event) => {
    const button = event.target.closest("[data-menu-index]");
    if (!button) {
      return;
    }
    closeMenu();
    const item = items[Number(button.dataset.menuIndex)];
    if (item && item.action) {
      item.action();
    }
  });
  const first = menu.querySelector(".menu-item:not(:disabled)");
  if (first) {
    first.focus();
  }
}

document.addEventListener("mousedown", (event) => {
  if (openMenuElement && !openMenuElement.contains(event.target)) {
    closeMenu();
  }
});

document.addEventListener("keydown", (event) => {
  if (!openMenuElement) {
    return;
  }
  const items = [...openMenuElement.querySelectorAll(".menu-item:not(:disabled)")];
  const index = items.indexOf(document.activeElement);
  if (event.key === "Escape") {
    event.stopPropagation();
    closeMenu();
  } else if (event.key === "ArrowDown") {
    event.preventDefault();
    items[(index + 1) % items.length].focus();
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    items[(index - 1 + items.length) % items.length].focus();
  }
}, true);

window.addEventListener("blur", closeMenu);
window.addEventListener("resize", closeMenu);

// ---------- 主题与强调色 ----------

const darkQuery = window.matchMedia("(prefers-color-scheme: dark)");

function currentTheme(setting) {
  if (setting === "light" || setting === "dark") {
    return setting;
  }
  return darkQuery.matches ? "dark" : "light";
}

function parseColor(hex) {
  const match = /^#?([0-9a-f]{6})$/i.exec(hex || "");
  if (!match) {
    return null;
  }
  const value = parseInt(match[1], 16);
  return [(value >> 16) & 255, (value >> 8) & 255, value & 255];
}

function toHex(rgb) {
  return "#" + rgb.map((part) => Math.round(Math.min(255, Math.max(0, part))).toString(16).padStart(2, "0")).join("");
}

function mixColor(first, second, weight) {
  return first.map((part, index) => part * (1 - weight) + second[index] * weight);
}

function luminance(rgb) {
  const [red, green, blue] = rgb.map((part) => {
    const value = part / 255;
    return value <= 0.03928 ? value / 12.92 : Math.pow((value + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

function contrast(first, second) {
  const [light, dark] = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return (light + 0.05) / (dark + 0.05);
}

// applyAccent 按系统强调色生成浅色 / 深色主题下的按钮色：浅色主题用深一些的颜色配白字，深色主题用浅一些的颜色配黑字。
function applyAccent(hex, theme) {
  const base = parseColor(hex) || [0, 103, 192];
  const root = document.documentElement.style;
  const white = [255, 255, 255];
  const black = [0, 0, 0];
  let accent;
  let onAccent;
  if (theme === "dark") {
    accent = mixColor(base, white, 0.4);
    for (let step = 0; step < 10 && contrast(accent, black) < 7; step++) {
      accent = mixColor(accent, white, 0.15);
    }
    onAccent = black;
  } else {
    accent = mixColor(base, black, 0.15);
    for (let step = 0; step < 10 && contrast(accent, white) < 4.5; step++) {
      accent = mixColor(accent, black, 0.15);
    }
    onAccent = white;
  }
  const background = theme === "dark" ? [32, 32, 32] : [243, 243, 243];
  root.setProperty("--accent", toHex(accent));
  root.setProperty("--accent-hover", toHex(mixColor(accent, background, 0.1)));
  root.setProperty("--accent-pressed", toHex(mixColor(accent, background, 0.2)));
  root.setProperty("--accent-text", toHex(accent));
  root.setProperty("--accent-soft", `rgba(${accent.map(Math.round).join(",")},${theme === "dark" ? 0.16 : 0.09})`);
  root.setProperty("--on-accent", toHex(onAccent));
}

// setFavicon 让窗口图标跟随代理状态。
function setFavicon(color, on) {
  const knob = on ? 21 : 11;
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'><rect x='1' y='6' width='30' height='20' rx='10' fill='${color}'/><circle cx='${knob}' cy='16' r='7' fill='white'/></svg>`;
  const link = document.getElementById("favicon");
  const href = "data:image/svg+xml," + encodeURIComponent(svg);
  if (link && link.getAttribute("href") !== href) {
    link.setAttribute("href", href);
  }
}

// ---------- 快捷键录制 ----------

let recordingHotkey = null;

const keyNames = {
  Space: "Space", Enter: "Enter", Tab: "Tab", Backspace: "Backspace", Insert: "Insert", Delete: "Delete",
  Home: "Home", End: "End", PageUp: "PageUp", PageDown: "PageDown", ArrowLeft: "Left", ArrowRight: "Right",
  ArrowUp: "Up", ArrowDown: "Down", Pause: "Pause", ScrollLock: "ScrollLock", PrintScreen: "PrintScreen",
  Backquote: "`", Minus: "-", Equal: "=", BracketLeft: "[", BracketRight: "]", Backslash: "\\",
  Semicolon: ";", Quote: "'", Comma: ",", Period: ".", Slash: "/",
};

// hotkeyFromEvent 把按键事件转成配置里的写法，例如 Ctrl+Alt+P；只按了修饰键时返回 null。
function hotkeyFromEvent(event) {
  const code = event.code;
  let key = null;
  if (/^Key[A-Z]$/.test(code)) {
    key = code.slice(3);
  } else if (/^Digit[0-9]$/.test(code)) {
    key = code.slice(5);
  } else if (/^Numpad[0-9]$/.test(code)) {
    key = code;
  } else if (/^F([1-9]|1[0-9]|2[0-4])$/.test(code)) {
    key = code;
  } else if (keyNames[code]) {
    key = keyNames[code];
  }
  if (!key) {
    return null;
  }
  const parts = [];
  if (event.ctrlKey) {
    parts.push("Ctrl");
  }
  if (event.altKey) {
    parts.push("Alt");
  }
  if (event.shiftKey) {
    parts.push("Shift");
  }
  if (event.metaKey) {
    parts.push("Win");
  }
  parts.push(key);
  return parts.join("+");
}

function hotkeyKeys(text) {
  if (!text) {
    return "";
  }
  const parts = text.endsWith("++") ? [...text.slice(0, -2).split("+"), "+"] : text.split("+");
  return html`<span class="keys">${parts.filter(Boolean).map((part) => html`<kbd>${part.trim()}</kbd>`)}</span>`;
}

// ---------- 其他 ----------

function downloadText(fileName, text) {
  const blob = new Blob([text], { type: "application/json" });
  const link = document.createElement("a");
  link.href = URL.createObjectURL(blob);
  link.download = fileName;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(link.href), 1000);
}

function pickFile(accept) {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = accept;
    input.addEventListener("change", () => resolve(input.files[0] || null));
    input.click();
  });
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch (error) {
    const area = document.createElement("textarea");
    area.value = text;
    document.body.appendChild(area);
    area.select();
    const copied = document.execCommand("copy");
    area.remove();
    return copied;
  }
}
