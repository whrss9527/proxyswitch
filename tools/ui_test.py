#!/usr/bin/env python3
"""设置页的浏览器自动化测试（Playwright + Chromium）。

自动编译并启动开发模式的设置服务（系统设置用内存模拟，带假的 HTTP / SOCKS5 代理），
走一遍首次使用、添加和编辑配置、测速、切换、自动切换、常规设置、诊断、深色模式、健康检查等流程。

用法：python3 tools/ui_test.py [截图目录]
截图目录里的 docs_light.png / docs_dark.png 可以直接用作 README 的截图。
"""
import hashlib
import http.server
import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

from playwright.sync_api import sync_playwright

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SHOTS = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "proxyswitch-ui")
os.makedirs(SHOTS, exist_ok=True)

failures = []


def check(condition, message):
    print(("ok   " if condition else "FAIL ") + message)
    if not condition:
        failures.append(message)


# 订阅由真实的 mihomo 内核代理：PROXYSWITCH_CORE 指向内核程序时测完整的订阅流程，否则只检查没有内核时的提示。
CORE = os.environ.get("PROXYSWITCH_CORE", "")

# 模拟 GitHub 的最新发布接口：版本 99.0.0，附件是一段假的程序内容和它的 SHA256SUMS.txt。
FAKE_PROGRAM = b"fake new version " * 4096
# 最新发布接口被访问的次数，用来确认页面确实检查了更新。
release_checks = {"count": 0}


def start_release_server():
    checksum = hashlib.sha256(FAKE_PROGRAM).hexdigest()
    sums = "".join(f"{checksum}  {name}\n" for name in ("ProxySwitch.exe", "ProxySwitch-arm64.exe")).encode()

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            base = f"http://127.0.0.1:{self.server.server_port}"
            if self.path == "/releases/latest":
                release_checks["count"] += 1
                assets = [{"name": name, "browser_download_url": f"{base}/download/program", "size": len(FAKE_PROGRAM)} for name in ("ProxySwitch.exe", "ProxySwitch-arm64.exe")]
                assets.append({"name": "SHA256SUMS.txt", "browser_download_url": f"{base}/download/sums", "size": len(sums)})
                body = json.dumps({"tag_name": "v99.0.0", "html_url": f"{base}/release", "published_at": "2026-10-01T00:00:00Z", "body": "- 新功能一\n- 修复二", "assets": assets}).encode()
            elif self.path == "/download/program":
                time.sleep(0.8)
                body = FAKE_PROGRAM
            elif self.path == "/download/sums":
                body = sums
            else:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{server.server_port}/releases/latest"


def start_server(directory):
    binary = os.path.join(directory, "proxyswitch-dev")
    subprocess.run(["go", "build", "-o", binary, "."], cwd=ROOT, check=True)
    arguments = [binary, "--dev-settings", f"--dir={directory}", f"--release-api={start_release_server()}"]
    if CORE:
        arguments.append(f"--core={CORE}")
    process = subprocess.Popen(arguments, stdout=subprocess.PIPE, text=True)
    info = json.loads(process.stdout.readline())
    return process, info


class Api:
    def __init__(self, url):
        self.base = url.split("/?")[0]
        self.token = url.split("token=")[1]

    def call(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, method=method, headers={"X-Token": self.token, "Content-Type": "application/json"})
        with urllib.request.urlopen(request) as response:
            return json.loads(response.read() or b"null")


def free_port():
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


# 模拟机场的订阅：3 个能用的节点（开发模式的假 HTTP 代理）和 2 个连不上的节点，带流量信息和机场名。
def start_subscription_server(http_proxy):
    host, port = http_proxy.rsplit(":", 1)
    dead = free_port()
    nodes = [("香港 01", port), ("香港 02", dead), ("日本 01", port), ("美国 01", dead), ("新加坡 01", port)]
    body = ("proxies:\n" + "".join(f"  - {{name: {json.dumps(name, ensure_ascii=False)}, type: http, server: {host}, port: {node_port}}}\n" for name, node_port in nodes)).encode()

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("subscription-userinfo", "upload=2147483648; download=6442450944; total=107374182400; expire=1798761600")
            self.send_header("Content-Disposition", "attachment; filename*=UTF-8''%E6%B5%8B%E8%AF%95%E6%9C%BA%E5%9C%BA")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{server.server_port}/api/v1/client/subscribe?token=test"


def start_rules_server():
    """小火箭格式的分流规则：两条直接写的规则、一个引用的规则列表、一条内核不支持的规则。"""
    listing = "".join(f"DOMAIN-SUFFIX,site{index}.example\n" for index in range(20)).encode()

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/rules.conf":
                body = (f"[General]\nipv6 = false\n\n[Rule]\nDOMAIN-SUFFIX,google.com,Proxy\nDOMAIN-SUFFIX,ads.example.com,Reject\n"
                        f"RULE-SET,http://127.0.0.1:{self.server.server_port}/proxy.list,PROXY\nUSER-AGENT,Instagram*,PROXY\nFINAL,DIRECT\n").encode()
            elif self.path == "/proxy.list":
                body = listing
            else:
                self.send_response(404)
                self.end_headers()
                return
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{server.server_port}/rules.conf"


def wait_until(predicate, timeout=8.0, interval=0.2):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if predicate():
            return True
        time.sleep(interval)
    return False


def main():
    directory = tempfile.mkdtemp(prefix="proxyswitch-ui-")
    process, info = start_server(directory)
    api = Api(info["url"])
    config_path = info["config"]
    errors = []
    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch()
            context = browser.new_context(viewport={"width": 1120, "height": 800}, device_scale_factor=1, locale="zh-CN", permissions=["clipboard-read", "clipboard-write"])
            page = context.new_page()
            page.on("pageerror", lambda error: errors.append(f"pageerror: {error}"))
            # 接口拒绝非法输入时返回 400 / 409，检查的订阅或规则地址不能用时返回 502，浏览器会把它们记成控制台错误，这是预期行为。
            expected = ("status of 400", "status of 409", "status of 502")
            page.on("console", lambda message: errors.append(f"console: {message.text}") if message.type == "error" and not any(text in message.text for text in expected) else None)
            run_flows(page, api, info, config_path)
            check(not errors, "没有脚本错误" + ("" if not errors else f"：{errors[:3]}"))
            browser.close()
    finally:
        process.terminate()
    print(f"\n截图：{SHOTS}")
    if failures:
        print(f"{len(failures)} 项失败")
        sys.exit(1)
    print("全部通过")


def shot(page, name):
    time.sleep(0.3)
    page.screenshot(path=os.path.join(SHOTS, f"{name}.png"))


def read_config(path):
    text = open(path, encoding="utf-8-sig").read()
    return json.loads("\n".join(line for line in text.splitlines() if not line.lstrip().startswith("//")))


def run_flows(page, api, info, config_path):
    # ---------- 首次使用 ----------
    page.goto(info["url"])
    page.wait_for_selector(".empty")
    check("token" not in page.url, "token 从地址栏移除")
    check(page.title() == "ProxySwitch 设置", "窗口标题与程序查找设置窗口用的标题一致")
    check(page.locator(".choice").count() == 4, "空状态提供四种添加方式（含机场订阅）")
    shot(page, "01_empty")

    # ---------- 自动检测并添加 ----------
    page.click(".choice[data-action=detect]")
    page.wait_for_selector(".dialog [data-action=detect-add]", timeout=10000)
    check(page.locator(".dialog [data-action=detect-add]").count() == 2, "检测到假的 HTTP 和 SOCKS5 代理")
    check("dev-http-proxy" in page.inner_text(".dialog .setting >> nth=0"), "HTTP 代理排在前面")
    shot(page, "02_detect")
    page.click(".dialog .setting:has-text('dev-http-proxy') [data-action=detect-add]")
    page.wait_for_selector(".dialog [data-field=name]")
    check(page.input_value(".dialog [data-field=port]") == info["http_proxy"].split(":")[1], "检测结果预填到编辑框")
    check(page.is_checked(".dialog [data-field=useAfterSave]"), "首次添加默认保存后立即使用")
    page.click(".dialog [data-action=dialog-save]")
    page.wait_for_selector(".profile.active", timeout=10000)
    state = api.call("GET", "/api/state")
    check(state["status"]["state"] == "on", "保存后代理已开启")
    system = api.call("GET", "/api/dev/system")
    check(system["system"]["proxy_enabled"] and system["system"]["server"] == info["http_proxy"], "系统代理指向检测到的地址")
    check(len(read_config(config_path)["profiles"]) == 1, "配置写入配置文件")

    # ---------- 手动添加：校验 ----------
    page.click(".section-title [data-action=add]")
    page.wait_for_selector(".dialog [data-field=name]")
    page.click(".dialog [data-action=dialog-save]")
    check("起个名字" in page.inner_text(".dialog [data-error=name]"), "名称为空时提示")
    check("地址" in page.inner_text(".dialog [data-error=host]"), "地址为空时提示")
    page.fill(".dialog [data-field=name]", state["config"]["profiles"][0]["name"])
    check("同名" in page.inner_text(".dialog [data-error=name]"), "同名配置提示")
    page.fill(".dialog [data-field=name]", "公司代理")
    page.fill(".dialog [data-field=host]", "http://10.0.0.1:8080")
    check(page.input_value(".dialog [data-field=host]") == "10.0.0.1" and page.input_value(".dialog [data-field=port]") == "8080", "粘贴完整地址自动拆成主机和端口")
    page.fill(".dialog [data-field=port]", "99999")
    check("65535" in page.inner_text(".dialog [data-error=port]"), "端口超出范围时提示")
    page.fill(".dialog [data-field=port]", "8080")
    page.check(".dialog input[data-target=git]")
    page.click(".dialog [data-kind=socks]")
    page.check(".dialog input[data-target=npm]")
    check("npm" in page.inner_text(".dialog [data-error=targets]"), "SOCKS5 勾选 npm 时提示")
    page.uncheck(".dialog input[data-target=npm]")
    page.click(".dialog [data-kind=http]")
    page.click(".dialog [data-color='#2563eb']")
    page.wait_for_selector(".dialog .preview li")
    check("git" in page.inner_text(".dialog .preview"), "预览列出 git 的修改")
    shot(page, "03_editor")
    page.click(".dialog [data-action=dialog-save]")
    page.wait_for_selector(".dialog", state="detached")

    # ---------- PAC ----------
    page.click(".section-title [data-action=add]")
    page.wait_for_selector(".dialog [data-field=name]")
    page.click(".dialog [data-kind=pac]")
    page.fill(".dialog [data-field=name]", "学校 PAC")
    page.fill(".dialog [data-field=pac]", "http://127.0.0.1:9/proxy.pac")
    page.check(".dialog input[data-target=env]")
    page.click(".dialog [data-action=dialog-save]")
    check("PAC" in page.inner_text(".dialog [data-error=targets]"), "PAC 勾选环境变量又没填代理服务器时提示")
    page.uncheck(".dialog input[data-target=env]")
    page.click(".dialog [data-action=dialog-save]")
    page.wait_for_selector(".dialog", state="detached")
    check(page.locator(".profile").count() == 3, "列表里有 3 个配置")

    # ---------- 测速 ----------
    page.click("[data-page=general]")
    page.wait_for_selector("#test-url")
    page.fill("#test-url", info["test_url"])
    page.press("#test-url", "Enter")
    check(wait_until(lambda: read_config(config_path)["test_url"] == info["test_url"]), "输入框按回车后保存")
    page.click("[data-page=proxies]")
    page.wait_for_selector("[data-action=test-all]")
    page.click("[data-action=test-all]")
    check(wait_until(lambda: page.locator(".profile .badge.success").count() >= 1, timeout=12), "测速显示延迟")

    # ---------- 切换、菜单 ----------
    page.click(".profile [data-action=use] >> nth=0")
    check(wait_until(lambda: api.call("GET", "/api/state")["status"]["profile"] == "公司代理"), "点「使用」切换配置")
    check(api.call("GET", "/api/dev/system")["git"]["http_proxy"] == "http://10.0.0.1:8080", "切换后 git 代理已设置")
    page.click(".profile [data-action=profile-menu] >> nth=2")
    page.click(".menu-item:has-text('上移')")
    check(wait_until(lambda: read_config(config_path)["profiles"][1]["name"] == "学校 PAC"), "上移后顺序改变")
    page.click(".profile [data-action=profile-menu] >> nth=1")
    page.click(".menu-item:has-text('复制一份')")
    page.wait_for_selector(".dialog [data-field=name]")
    check(page.input_value(".dialog [data-field=name]").endswith("副本"), "复制一份时名称带「副本」")
    page.click(".dialog [data-action=dialog-save]")
    page.wait_for_selector(".dialog", state="detached")
    page.click(".profile [data-action=profile-menu] >> nth=3")
    page.click(".menu-item:has-text('删除')")
    page.click(".dialog [data-dialog-result=yes]")
    check(wait_until(lambda: len(read_config(config_path)["profiles"]) == 3), "删除配置")
    shot(page, "04_home")

    # ---------- 编辑正在使用的配置并重命名 ----------
    active = page.locator(".profile.active [data-action=edit]")
    active.click()
    page.wait_for_selector(".dialog [data-field=name]")
    page.fill(".dialog [data-field=name]", "公司")
    page.click(".dialog [data-action=dialog-save]")
    page.wait_for_selector(".dialog", state="detached")
    check(wait_until(lambda: api.call("GET", "/api/state")["status"]["profile"] == "公司"), "重命名正在使用的配置后仍处于开启状态")

    # ---------- 自动切换 ----------
    page.click("[data-page=network]")
    page.wait_for_selector(".network")
    page.click("[data-action=quick-rule] >> nth=0")
    page.wait_for_selector(".dialog #rule-value")
    check(page.input_value(".dialog #rule-value") == "Home-WiFi", "快速添加规则时预填当前 Wi-Fi 名称")
    page.select_option(".dialog #rule-action", "off")
    page.click(".dialog [data-action=rule-save]")
    page.wait_for_selector(".dialog", state="detached")
    config = read_config(config_path)
    check(config["auto_switch"]["enabled"] and config["auto_switch"]["rules"][0]["action"] == "off", "添加第一条规则后自动开启自动切换")
    check(wait_until(lambda: api.call("GET", "/api/state")["status"]["state"] == "off"), "开启自动切换后按当前网络立即生效（家里关闭代理）")
    page.click("[data-action=add-rule]")
    page.wait_for_selector(".dialog #rule-value")
    page.click(".dialog [data-match=dns_suffix]")
    page.fill(".dialog #rule-value", "corp.example.com")
    page.select_option(".dialog #rule-action", "use:公司")
    page.click(".dialog [data-action=rule-save]")
    page.wait_for_selector(".dialog", state="detached")
    api.call("POST", "/api/dev/network", {"ssids": [], "adapters": [{"name": "以太网", "dns_suffix": "corp.example.com", "gateway": "10.1.0.1", "gateway_mac": "00-1a-2b-3c-4d-5e"}]})
    state = api.call("GET", "/api/state")
    check(state["status"]["state"] == "on" and state["status"]["profile"] == "公司", "网络变化后按规则切换到公司代理")
    check(state["auto_switch"]["match_index"] == 1, "当前网络匹配第 2 条规则")
    notices = api.call("GET", "/api/dev/notices")
    check(any("已切换到「公司」" in notice["title"] for notice in notices), "自动切换时发出通知")
    page.wait_for_selector(".rule.matched")
    shot(page, "05_network")
    page.fill("#rule-1-value", "")
    page.press("#rule-1-value", "Enter")
    page.wait_for_selector(".toast.danger")
    check(read_config(config_path)["auto_switch"]["rules"][1]["value"] == "corp.example.com", "规则的值清空时拒绝保存并提示")

    # ---------- 常规 ----------
    page.click("[data-page=general]")
    page.wait_for_selector(".hotkey-recorder")
    page.select_option("select[data-setting=notify_level]", "errors")
    check(wait_until(lambda: read_config(config_path)["notify_level"] == "errors"), "下拉框修改立即保存")
    page.click(".hotkey-recorder")
    page.keyboard.press("Control+Shift+K")
    check(wait_until(lambda: read_config(config_path)["hotkey"] == "Ctrl+Shift+K"), "录制快捷键")
    page.select_option("select[data-setting=profile_hotkeys]", "Ctrl+Alt")
    check(wait_until(lambda: read_config(config_path)["profile_hotkeys"] == "Ctrl+Alt"), "按数字切换配置的修饰键")
    page.click("button.switch[data-setting=disable_on_exit]")
    check(wait_until(lambda: read_config(config_path)["disable_on_exit"] is True), "开关类设置立即保存")
    page.click("button.switch[data-action=autostart]")
    check(wait_until(lambda: api.call("GET", "/api/state")["autostart"]), "开机自启")
    check(page.input_value("select[data-setting=speed_display]") == "system", "默认显示系统网络总速度")
    shot(page, "06_general")
    # 实时网速：显示在开关卡片上，选「不显示」时隐藏。
    page.click("[data-page=proxies]")
    check(wait_until(lambda: page.locator(".hero-speed").count() == 1 and "↓" in page.inner_text(".hero-speed"), timeout=10), "开关卡片上显示实时网速")
    page.click("[data-page=general]")
    page.select_option("select[data-setting=speed_display]", "none")
    check(wait_until(lambda: read_config(config_path)["speed_display"] == "none"), "修改网速的显示方式")
    page.click("[data-page=proxies]")
    check(wait_until(lambda: page.locator(".hero-speed").count() == 0, timeout=10), "不显示网速时开关卡片上没有网速")
    api.call("PUT", "/api/config", {**api.call("GET", "/api/state")["config"], "speed_display": "system"})
    check(wait_until(lambda: page.evaluate("app.config.speed_display") == "system", timeout=6), "页面同步到网速设置的修改")
    page.click("[data-page=general]")

    # ---------- 系统集成 ----------
    page.click("[data-page=system]")
    page.wait_for_selector("button.switch[data-setting=url_links]")
    check(page.get_attribute("button.switch[data-setting=url_links]", "aria-checked") == "true", "网页链接默认开启")
    check("现在由「clash-verge」处理" in page.inner_text(".page"), "说明 clash:// 链接现在由哪个程序处理")
    page.click("[data-action=take-over-clash-links]")
    check(wait_until(lambda: "由 ProxySwitch 处理" in page.inner_text(".page") and page.locator("[data-action=take-over-clash-links]").count() == 0), "改由 ProxySwitch 处理一键导入")
    shot(page, "07_system")
    page.click("button.switch[data-setting=url_links]")
    check(wait_until(lambda: read_config(config_path)["url_links"] is False), "关闭网页链接")
    check(wait_until(lambda: "机场网站的一键导入" not in page.inner_text(".page")), "关闭网页链接时不再显示一键导入")
    page.click("button.switch[data-setting=url_links]")
    check(wait_until(lambda: read_config(config_path)["url_links"] is True), "重新开启网页链接")
    # 商店应用：显示已允许的个数，在对话框里勾选后保存（Windows 上要管理员确认），也可以一键全部允许。
    check(wait_until(lambda: "已允许 1 / 4 个" in page.inner_text(".page")), "列出商店应用和已允许的个数")
    page.click("[data-action=loopback-choose]")
    page.wait_for_selector(".dialog [data-loopback-sid]")
    page.fill(".dialog [data-focus=loopback-search]", "net")
    check(page.locator(".dialog [data-loopback-sid]").count() == 1, "搜索商店应用")
    page.check(".dialog [data-loopback-sid='S-1-15-2-1002']")
    page.fill(".dialog [data-focus=loopback-search]", "")
    check(page.locator(".dialog [data-loopback-sid]").count() == 4 and "已选 2 个" in page.inner_text(".dialog"), "清空搜索后勾选的还在")
    shot(page, "08_loopback")
    page.click(".dialog [data-action=loopback-save]")
    page.wait_for_selector(".dialog", state="detached")
    check(wait_until(lambda: "已允许 2 / 4 个" in page.inner_text(".page")), "保存勾选的商店应用")
    loopback = api.call("GET", "/api/loopback")["apps"]
    check([item["name"] for item in loopback if item["exempt"]] == ["Netflix", "Xbox"], "允许的是勾选的应用")
    page.click("[data-action=loopback-all]")
    check(wait_until(lambda: "已允许 4 / 4 个" in page.inner_text(".page") and page.locator("[data-action=loopback-all]").count() == 0), "一键允许全部商店应用")
    # WSL：一键设置成镜像网络 + 自动代理，提示重启 WSL，重启后显示已设置。
    page.wait_for_selector("[data-action=wsl-setup]")
    check("Ubuntu" in page.inner_text(".page") and not api.call("GET", "/api/wsl")["mirrored"], "列出 WSL 发行版，还没有设置")
    page.click("[data-action=wsl-setup]")
    page.wait_for_selector("[data-action=wsl-restart]")
    wsl = api.call("GET", "/api/wsl")
    check(wsl["mirrored"] and wsl["auto_proxy"] and wsl["restart"], "设置 WSL 使用镜像网络和自动代理，等待重启")
    page.click("[data-action=wsl-restart]")
    check(wait_until(lambda: page.locator("[data-action=wsl-reset]").count() == 1 and "使用镜像网络" in page.inner_text(".page")), "重启 WSL 后显示已设置")
    check(not api.call("GET", "/api/wsl")["restart"], "重启后不再提示重启")
    # 撤销：恢复默认的 NAT 网络，同样要重启 WSL。
    page.click("[data-action=wsl-reset]")
    check(wait_until(lambda: "已恢复 WSL 默认的网络设置" in page.inner_text(".page")), "撤销后提示重启 WSL")
    wsl = api.call("GET", "/api/wsl")
    check(not wsl["mirrored"] and wsl["restart"], "撤销 WSL 的镜像网络设置")
    page.click("[data-action=wsl-restart]")
    check(wait_until(lambda: page.locator("[data-action=wsl-setup]").count() == 1), "撤销并重启后可以重新设置")
    # WinHTTP：代理关着时不能设；开启 HTTP 代理后设为当前代理，诊断里也能看到，再改回直连。
    before = api.call("GET", "/api/state")
    http_profile = next(profile["name"] for profile in before["config"]["profiles"] if profile.get("server") == info["http_proxy"])
    api.call("POST", "/api/off")
    check(wait_until(lambda: page.locator("[data-action=winhttp-proxy][disabled]").count() == 1 and "开启代理后可以设为当前代理" in page.inner_text(".page")), "代理关着时不能设 WinHTTP")
    api.call("POST", "/api/use", {"name": http_profile})
    check(wait_until(lambda: page.locator("[data-action=winhttp-proxy]:not([disabled])").count() == 1), "开启 HTTP 代理后可以设 WinHTTP")
    page.click("[data-action=winhttp-proxy]")
    check(wait_until(lambda: page.locator(".badge:has-text('正在使用当前代理')").count() == 1), "WinHTTP 设为当前代理")
    check(api.call("GET", "/api/winhttp")["proxy"] == info["http_proxy"] and api.call("GET", "/api/diagnostics")["winhttp"] == info["http_proxy"], "WinHTTP 用的是当前代理，诊断里能看到")
    page.click("[data-action=winhttp-direct]")
    check(wait_until(lambda: api.call("GET", "/api/winhttp")["proxy"] == "" and page.locator("[data-action=winhttp-direct]").count() == 0), "WinHTTP 改回直连")
    if before["status"]["state"] != "on":
        api.call("POST", "/api/off")
    elif before["status"]["profile"] != http_profile:
        api.call("POST", "/api/use", {"name": before["status"]["profile"]})
    page.click("[data-page=general]")

    # ---------- 配置文件被手动修改 ----------
    config = read_config(config_path)
    config["profiles"][0]["name"] = "手改的名字"
    config["auto_switch"]["rules"] = [rule for rule in config["auto_switch"]["rules"] if rule["action"] == "off"]
    time.sleep(1.1)
    with open(config_path, "w", encoding="utf-8") as file:
        json.dump(config, file, ensure_ascii=False)
    page.click("[data-page=proxies]")
    check(wait_until(lambda: "手改的名字" in page.inner_text("#page"), timeout=6), "手动修改配置文件后页面自动更新")
    with open(config_path, "w", encoding="utf-8") as file:
        file.write('{"profiles": [ ')
    check(wait_until(lambda: page.locator(".infobar.warning").count() > 0 and "修改前的配置" in page.inner_text("#page"), timeout=6), "配置文件有错误时提示并继续使用原来的配置")
    with open(config_path, "w", encoding="utf-8") as file:
        json.dump(config, file, ensure_ascii=False)
    check(wait_until(lambda: "修改前的配置" not in page.inner_text("#page"), timeout=6), "错误修正后提示消失")

    # ---------- 其他程序设置的代理 ----------
    api.call("POST", "/api/dev/external", {"server": "192.168.1.9:3128"})
    check(wait_until(lambda: "其他程序" in page.inner_text(".hero"), timeout=5), "识别其他程序设置的系统代理")
    page.click("[data-action=save-external]")
    page.wait_for_selector(".dialog [data-field=name]")
    check(page.input_value(".dialog [data-field=name]") == "原有代理" and page.input_value(".dialog [data-field=host]") == "192.168.1.9" and page.input_value(".dialog [data-field=port]") == "3128", "其他程序设置的代理可以一键保存为配置")
    page.click(".dialog [data-action=dialog-cancel]")
    page.wait_for_selector(".dialog", state="detached")
    page.click(".hero-switch")
    check(wait_until(lambda: api.call("GET", "/api/state")["status"]["state"] == "off"), "点开关关闭其他程序的代理")

    # ---------- 健康检查 ----------
    page.click(".profile:has-text('手改的名字') [data-action=use]")
    check(wait_until(lambda: api.call("GET", "/api/state")["status"]["state"] == "on"), "开启检测到的本机代理")

    # ---------- 守护系统代理 ----------
    page.click("[data-page=general]")
    page.click("button.switch[data-setting=guard_proxy]")
    check(wait_until(lambda: read_config(config_path).get("guard_proxy") is True), "开启守护系统代理")
    guarded = api.call("GET", "/api/dev/system")["system"]["server"]
    api.call("POST", "/api/dev/external", {"server": "192.168.1.9:3128"})
    check(wait_until(lambda: api.call("GET", "/api/dev/system")["system"]["server"] == guarded, timeout=6), "其他程序改掉系统代理后自动改回")
    check(any(notice["title"] == "已改回系统代理" and "192.168.1.9:3128" in notice["text"] for notice in api.call("GET", "/api/dev/notices")), "改回系统代理时通知")
    page.click("button.switch[data-setting=guard_proxy]")
    check(wait_until(lambda: read_config(config_path).get("guard_proxy") is False), "关闭守护系统代理")
    page.click("[data-page=proxies]")

    # ---------- 终端命令 ----------
    page.click("[data-action=terminal-menu]")
    check(page.locator(".menu .menu-item").count() == 3, "终端命令菜单有 PowerShell、命令提示符、Bash 三项")
    page.click(".menu-item:has-text('PowerShell')")
    page.wait_for_selector(".toast:has-text('PowerShell')")
    copied = page.evaluate("navigator.clipboard.readText()")
    check(copied.startswith("$env:HTTP_PROXY='http://" + info["http_proxy"] + "'"), "复制的 PowerShell 命令包含代理地址")
    api.call("POST", "/api/dev/proxy", {"running": False})
    check(wait_until(lambda: page.locator(".hero-switch.warn").count() == 1, timeout=8), "代理软件退出后提示连不上")
    api.call("POST", "/api/dev/proxy", {"running": True})
    check(wait_until(lambda: page.locator(".hero-switch.warn").count() == 0, timeout=8), "代理软件恢复后提示消失")

    # ---------- 诊断、关于 ----------
    with open(os.path.join(info["dir"], "crash-previous.log"), "w", encoding="utf-8") as file:
        file.write("panic: 测试崩溃\n\ngoroutine 1 [running]:\nmain.main()\n")
    page.click("[data-page=diagnostics]")
    page.wait_for_selector(".kv")
    check(info["http_proxy"] in page.inner_text(".kv >> nth=0"), "诊断页显示系统代理地址")
    check("测试崩溃" in page.inner_text(".log.crash"), "诊断页显示上次意外退出的记录")
    check("level=" in page.inner_text("#log"), "诊断页显示日志")
    page.click("[data-action=clear-all]")
    page.click(".dialog [data-dialog-result=yes]")
    check(wait_until(lambda: not api.call("GET", "/api/dev/system")["system"]["proxy_enabled"]), "清除所有代理设置")
    page.click("[data-page=about]")
    page.wait_for_selector(".about-hero")
    check(state["version"] in page.inner_text(".about-hero"), "关于页显示版本号")
    # 请我喝杯咖啡：窗口够宽时赞赏码在关于卡片右边，点一下放大。
    check(wait_until(lambda: page.evaluate("document.querySelector('.donate-image img').naturalWidth") == 720), "关于页显示赞赏码")
    hero, donate = page.locator(".about-hero").bounding_box(), page.locator(".donate-card").bounding_box()
    check(donate["x"] > hero["x"] + hero["width"] and abs(donate["y"] - hero["y"]) < 2, "窗口够宽时赞赏码在右边")
    page.click("[data-action=donate-enlarge]")
    page.wait_for_selector(".dialog.donate-dialog img")
    check(page.evaluate("document.querySelector('.donate-dialog img').getBoundingClientRect().width") > 380, "点赞赏码放大显示")
    page.click(".dialog.donate-dialog [data-dialog-result]")
    page.wait_for_selector(".dialog", state="detached")
    page.click("button.switch[data-setting=check_updates]")
    check(wait_until(lambda: read_config(config_path)["check_updates"] is False), "关闭自动检查更新")
    page.click("button.switch[data-setting=check_updates]")
    check(wait_until(lambda: read_config(config_path)["check_updates"] is True), "重新开启自动检查更新")

    # ---------- 程序请求切换页面、地址里带页面 ----------
    page.click("[data-page=proxies]")
    api.call("POST", "/api/dev/navigate", {"page": "general"})
    check(wait_until(lambda: page.evaluate("app.page") == "general", timeout=6), "点击托盘通知时设置页切到对应页面")
    other = page.context.new_page()
    other.goto(info["url"] + "#network")
    other.wait_for_selector(".network")
    check(other.evaluate("location.hash") == "#network" and "token" not in other.url, "带 #页面 打开时直接显示该页")
    other.close()

    # ---------- 文档截图与深色模式 ----------
    test_base = info["test_url"].rsplit("/", 1)[0]
    docs_config = {
        "test_url": info["test_url"],
        "profile_hotkeys": "Ctrl+Alt",
        "auto_switch": {
            "enabled": True,
            "rules": [
                {"match": "ssid", "value": "Office-5G", "action": "use", "profile": "公司代理"},
                {"match": "dns_suffix", "value": "corp.example.com", "action": "use", "profile": "公司代理"},
                {"match": "ssid", "value": "Home-WiFi", "action": "off"},
            ],
            "default_action": "keep",
        },
        "profiles": [
            {"name": "本机代理", "server": info["http_proxy"], "apply_to": ["system", "env", "git"]},
            {"name": "公司代理", "server": "10.0.0.1:8080", "apply_to": ["system"], "color": "#2563eb"},
            {"name": "学校 PAC", "pac": f"{test_base}/proxy.pac", "apply_to": ["system"], "color": "#7c3aed"},
            {"name": "SOCKS5 备用", "server": f"socks5://{info['socks_proxy']}", "apply_to": ["system"], "color": "#ea580c"},
        ],
    }
    api.call("PUT", "/api/config", docs_config)
    api.call("POST", "/api/dev/network", {"ssids": ["Home-WiFi"], "adapters": [{"name": "WLAN", "dns_suffix": "lan", "gateway": "192.168.1.1", "gateway_mac": "a4-91-b1-0c-22-9e", "wireless": True}]})
    api.call("POST", "/api/use", {"name": "本机代理"})
    page.reload()
    page.wait_for_selector(".nav-item")
    page.click("[data-page=proxies]")
    page.wait_for_selector(".profile")
    for name in ["本机代理", "学校 PAC", "SOCKS5 备用"]:
        page.click(f".profile:has-text('{name}') [data-action=profile-menu]")
        page.click(".menu-item:has-text('测速')")
    check(wait_until(lambda: page.locator(".profile .badge.success").count() == 3, timeout=12), "逐个测速")
    page.mouse.move(0, 0)
    shot(page, "docs_light")
    page.click("[data-page=network]")
    page.wait_for_selector(".rule")
    page.mouse.move(0, 0)
    shot(page, "docs_network")
    page.click("[data-page=general]")
    page.click("[data-action=set-theme][data-value=dark]")
    check(wait_until(lambda: page.evaluate("document.documentElement.dataset.theme") == "dark"), "切换到深色主题")
    page.click("[data-page=proxies]")
    for name in ["本机代理", "学校 PAC", "SOCKS5 备用"]:
        page.click(f".profile:has-text('{name}') [data-action=profile-menu]")
        page.click(".menu-item:has-text('测速')")
    wait_until(lambda: page.locator(".profile .badge.success").count() == 3, timeout=12)
    page.mouse.move(0, 0)
    shot(page, "docs_dark")

    # ---------- 机场订阅 ----------
    config = api.call("GET", "/api/state")["config"]
    config["core"]["port"] = free_port()
    api.call("PUT", "/api/config", config)
    # 等页面同步到新的端口：页面保存配置时发送的是它手里的整份配置。
    check(wait_until(lambda: page.evaluate("app.config.core.port") == config["core"]["port"], timeout=6), "页面同步到内核端口的修改")
    subscription_url = start_subscription_server(info["http_proxy"])
    page.click("[data-page=proxies]")
    page.click(".section-title [data-action=add]")
    page.click(".dialog [data-kind=subscription]")
    page.click(".dialog [data-action=dialog-save]")
    check("订阅地址" in page.inner_text(".dialog [data-error=subscription]"), "订阅地址为空时提示")
    page.fill(".dialog [data-field=subscription]", subscription_url)
    page.click(".dialog [data-action=dialog-test]")
    check(wait_until(lambda: "5 个节点" in page.inner_text(".dialog [data-check-result]"), timeout=10), "检查订阅显示节点数")
    check("已用 8.0 GB / 100 GB" in page.inner_text(".dialog [data-check-result]"), "检查订阅显示流量和到期时间")
    check(page.input_value(".dialog [data-field=name]") == "测试机场", "没填名字时用机场给的名字")
    # 本机的订阅文件：file:// 地址也能检查；不完整的路径提示写法。
    subscription_file = os.path.join(tempfile.mkdtemp(prefix="proxyswitch-sub-"), "我的节点.yaml")
    with open(subscription_file, "wb") as output:
        output.write(urllib.request.urlopen(subscription_url, timeout=5).read())
    page.fill(".dialog [data-field=subscription]", "sub.yaml")
    page.click(".dialog [data-action=dialog-test]")
    check(wait_until(lambda: "完整的路径" in page.inner_text(".dialog [data-error=subscription]"), timeout=3), "订阅地址不完整时提示本机文件的写法")
    page.fill(".dialog [data-field=subscription]", "file://" + subscription_file)
    page.click(".dialog [data-action=dialog-test]")
    check(wait_until(lambda: "5 个节点" in page.inner_text(".dialog [data-check-result]") and "已用" not in page.inner_text(".dialog [data-check-result]"), timeout=10), "检查本机的订阅文件")
    check(page.input_value(".dialog [data-field=name]") == "测试机场", "已经有名字时不用文件名")
    page.fill(".dialog [data-field=subscription]", subscription_url)
    # 分流：默认按规则分流、用内置的大陆直连；可以选小火箭规则的预设或填自定义规则的地址。
    check(page.get_attribute(".dialog [data-mode=rule]", "aria-pressed") == "true" and page.input_value(".dialog [data-field=rulesChoice]") == "", "默认按规则分流，用内置的大陆直连")
    page.click(".dialog [data-mode=global]")
    check(page.get_attribute(".dialog [data-mode=global]", "aria-pressed") == "true" and page.locator(".dialog [data-field=rulesChoice]").count() == 0, "选择全局代理时不用选规则")
    page.click(".dialog [data-mode=rule]")
    presets = api.call("GET", "/api/state")["rule_presets"]
    page.select_option(".dialog [data-field=rulesChoice]", presets[0]["url"])
    check(presets[0]["description"] in page.inner_text(".dialog"), "选择预设时说明它怎么分流")
    page.select_option(".dialog [data-field=rulesChoice]", "custom")
    page.click(".dialog [data-action=check-rules]")
    check("规则配置的地址" in page.inner_text(".dialog [data-error=rules]"), "自定义规则地址为空时提示")
    rules_url = start_rules_server()
    page.fill(".dialog [data-field=rules]", rules_url.replace("rules.conf", "missing.conf"))
    page.click(".dialog [data-action=check-rules]")
    check(wait_until(lambda: "404" in page.inner_text(".dialog [data-rules-result]"), timeout=10), "规则地址不对时说明原因")
    page.fill(".dialog [data-field=rules]", rules_url)
    page.click(".dialog [data-action=check-rules]")
    check(wait_until(lambda: "找到 2 条规则" in page.inner_text(".dialog [data-rules-result]"), timeout=10), "检查规则显示规则数")
    check("1 个规则列表" in page.inner_text(".dialog [data-rules-result]") and "1 条内核不支持" in page.inner_text(".dialog [data-rules-result]"), "检查规则说明引用的规则列表和跳过的规则")
    page.select_option(".dialog [data-field=rulesChoice]", "")
    page.select_option(".dialog [data-field=rulesChoice]", "custom")
    check(page.input_value(".dialog [data-field=rules]") == rules_url, "切换规则后自定义的地址还在")
    page.mouse.move(0, 0)
    shot(page, "11_rules_editor")
    page.click(".dialog [data-action=dialog-save]")
    page.wait_for_selector(".dialog", state="detached", timeout=20000)
    saved = next(profile for profile in read_config(config_path)["profiles"] if profile["name"] == "测试机场")
    check(saved["subscription"] == subscription_url and saved["mode"] == "rule" and saved.get("rules") == rules_url, "订阅配置和分流规则写入配置文件")
    card = ".profile:has-text('测试机场')"
    check(wait_until(lambda: "自定义规则 · 22 条" in page.inner_text(card), timeout=15), "保存后下载分流规则，列表显示规则数")

    # 机场网站的一键导入：打开添加订阅的对话框，填好地址和名字并立即检查；已经添加过的订阅只提示。
    api.call("POST", "/api/dev/navigate", {"page": "proxies", "action": "import-subscription", "argument": json.dumps({"url": subscription_url, "name": "测试机场"})})
    check(wait_until(lambda: "已经添加过这个订阅" in page.inner_text("body"), timeout=6), "导入已经添加过的订阅时提示")
    imported_url = subscription_url + "&from=import"
    api.call("POST", "/api/dev/navigate", {"page": "proxies", "action": "import-subscription", "argument": json.dumps({"url": imported_url, "name": "导入的机场"})})
    page.wait_for_selector(".dialog [data-field=subscription]", timeout=6000)
    check(page.input_value(".dialog [data-field=subscription]") == imported_url and page.input_value(".dialog [data-field=name]") == "导入的机场", "一键导入填好订阅地址和名字")
    check(wait_until(lambda: "5 个节点" in page.inner_text(".dialog [data-check-result]"), timeout=10), "一键导入时立即检查订阅")
    page.click(".dialog [data-action=dialog-cancel]")
    page.wait_for_selector(".dialog", state="detached")

    # 自定义规则：有订阅配置时代理页才显示；输入会整理成域名，已有的域名改去向。
    page.fill("[data-focus=custom-rule-value]", "youtube")
    page.press("[data-focus=custom-rule-value]", "Enter")
    check("认不出「youtube」" in page.inner_text("[data-custom-rule-error]"), "自定义规则认不出的输入给出提示")
    page.fill("[data-focus=custom-rule-value]", "https://www.YouTube.com/watch?v=1")
    page.press("[data-focus=custom-rule-value]", "Enter")
    custom_rules = lambda: read_config(config_path).get("custom_rules", [])
    check(wait_until(lambda: custom_rules() == [{"value": "www.youtube.com", "policy": "proxy"}]), "添加自定义规则，网址整理成域名")
    page.fill("[data-focus=custom-rule-value]", "10.1.2.3/8")
    page.select_option("[data-focus=custom-rule-policy]", "direct")
    page.click("[data-action=add-custom-rule]")
    check(wait_until(lambda: len(custom_rules()) == 2 and custom_rules()[1] == {"value": "10.0.0.0/8", "policy": "direct"}), "IP 段的自定义规则")
    page.select_option("select[data-custom-rule='0']", "reject")
    check(wait_until(lambda: custom_rules()[0]["policy"] == "reject"), "修改自定义规则的去向")
    page.click("[data-action=custom-rule-toggle][data-index='1']")
    check(wait_until(lambda: custom_rules()[1].get("disabled") is True), "停用自定义规则")
    page.click("[data-action=custom-rule-delete][data-index='1']")
    check(wait_until(lambda: len(custom_rules()) == 1), "删除自定义规则")
    # 按程序分流：选「程序」后可以从正在运行的程序里选。
    page.select_option("[data-focus=custom-rule-type]", "program")
    check(wait_until(lambda: page.locator("#running-programs option").count() == 5), "列出正在运行的程序供选择")
    check("程序名" in page.get_attribute("[data-focus=custom-rule-value]", "placeholder"), "选程序时输入框提示填程序名")
    page.fill("[data-focus=custom-rule-value]", "Telegram.exe")
    page.select_option("[data-focus=custom-rule-policy]", "direct")
    page.click("[data-action=add-custom-rule]")
    check(wait_until(lambda: len(custom_rules()) == 2 and custom_rules()[1] == {"type": "program", "value": "Telegram.exe", "policy": "direct"}), "添加按程序分流的规则")
    check(wait_until(lambda: "程序" in page.inner_text(".custom-rule-list")), "程序规则在列表里标出来")
    page.fill("[data-focus=custom-rule-value]", "a,b")
    # 保存的结果回来时页面会重绘（开着内核时要等内核重新加载，可能正好在输入的时候），正在输入的框不能丢掉焦点。
    page.evaluate("renderPage()")
    page.keyboard.press("Enter")
    check(wait_until(lambda: "写得不对" in page.inner_text("[data-custom-rule-error]")), "程序名不对时给出提示（页面重绘后焦点还在输入框）")
    page.evaluate("renderPage()")
    check("写得不对" in page.inner_text("[data-custom-rule-error]"), "页面随状态刷新后提示还在")
    page.fill("[data-focus=custom-rule-value]", "")
    page.select_option("[data-focus=custom-rule-type]", "")
    # TUN 模式：在常规页的代理内核里开关，没开订阅配置时说明什么时候生效。
    page.click("[data-page=general]")
    page.wait_for_selector("button.switch[data-setting='tun.enabled']")
    check("不认系统代理的程序" in page.inner_text(".page"), "说明 TUN 模式的作用")
    page.click("button.switch[data-setting='tun.enabled']")
    check(wait_until(lambda: read_config(config_path)["tun"]["enabled"] is True), "开启 TUN 模式")
    check(wait_until(lambda: "开启订阅配置后生效" in page.inner_text(".page")), "没开订阅配置时说明 TUN 什么时候生效")
    page.click("button.switch[data-setting='tun.enabled']")
    check(wait_until(lambda: read_config(config_path)["tun"]["enabled"] is False), "关闭 TUN 模式")
    # 按流量计费的网络上暂停每天的自动更新，默认开启；换到这样的网络时标出来。
    check(page.get_attribute("button.switch[data-setting=pause_on_metered]", "aria-checked") == "true", "默认在按流量计费的网络上暂停自动更新")
    network = api.call("GET", "/api/state")["network"]
    api.call("POST", "/api/dev/network", {**network, "metered": True})
    check(wait_until(lambda: "现在是按流量计费的网络，已暂停" in page.inner_text(".page")), "按流量计费的网络上说明已暂停自动更新")
    page.click("button.switch[data-setting=pause_on_metered]")
    check(wait_until(lambda: read_config(config_path)["pause_on_metered"] is False and "已暂停" not in page.inner_text(".page")), "可以关掉暂停")
    page.click("button.switch[data-setting=pause_on_metered]")
    check(wait_until(lambda: read_config(config_path)["pause_on_metered"] is True), "重新开启暂停")
    api.call("POST", "/api/dev/network", {**network, "metered": False})
    page.click("[data-page=proxies]")
    if CORE:
        check(wait_until(lambda: "5 个节点" in page.inner_text(card), timeout=10), "保存后下载订阅，列表显示节点数和流量")
        page.click(f"{card} [data-action=nodes]")
        page.wait_for_selector(".dialog .node", timeout=10000)
        check(page.locator(".dialog .node").count() == 6, "节点对话框列出自动选择和全部节点")
        page.click(".dialog [data-action=nodes-test]")
        check(wait_until(lambda: page.locator(".dialog .node .badge.success").count() == 3 and page.locator(".dialog .node .badge.danger").count() == 2, timeout=20), "测速后标出能用和超时的节点")
        page.click(".dialog [data-action=nodes-sort]")
        check(page.locator(".dialog .node").nth(4).locator(".badge").inner_text() == "超时", "按延迟排序时连不上的节点排在后面")
        page.fill(".dialog [data-focus=node-search]", "日本")
        check(page.locator(".dialog .node").count() == 1, "搜索节点")
        page.click(".dialog .node:has-text('日本 01')")
        check(wait_until(lambda: page.locator(".dialog .node[aria-pressed=true]:has-text('日本 01')").count() == 1), "选中节点")
        check(wait_until(lambda: next(p for p in read_config(config_path)["profiles"] if p["name"] == "测试机场").get("node") == "日本 01"), "选中的节点写入配置文件")
        page.click(".dialog [data-action=nodes-use]")
        check(wait_until(lambda: "订阅 · 日本 01" in page.inner_text(".hero"), timeout=10), "开启后显示在用的节点")
        system = api.call("GET", "/api/dev/system")
        check(system["system"]["server"] == f"127.0.0.1:{config['core']['port']}", "系统代理指向内核的端口")
        page.mouse.move(0, 0)
        shot(page, "10_subscription")
        page.click(".hero [data-action=nodes]")
        page.wait_for_selector(".dialog .node")
        page.click(".dialog .node[data-node='']")
        check(wait_until(lambda: page.locator(".dialog .node[data-node=''][aria-pressed=true]").count() == 1), "改为自动选择")
        page.click(".dialog [data-action=nodes-mode][data-value=global]")
        check(wait_until(lambda: next(p for p in read_config(config_path)["profiles"] if p["name"] == "测试机场")["mode"] == "global"), "在节点对话框里切换到全局代理")
        check(wait_until(lambda: page.get_attribute(".dialog [data-action=nodes-mode][data-value=global]", "aria-pressed") == "true"), "节点对话框显示当前的分流方式")
        page.click(".dialog [data-action=dialog-cancel]")
        check(wait_until(lambda: "（自动选择）" in page.inner_text(".hero") and "全局代理" in page.inner_text(".hero"), timeout=10), "自动选择时显示它选中的节点和分流方式")
        page.click(f"{card} [data-action=profile-menu]")
        page.click(".menu-item:has-text('切换到按规则分流')")
        check(wait_until(lambda: "按规则分流 · 自定义规则" in page.inner_text(".hero"), timeout=10), "从菜单切回按规则分流")
        page.click(f"{card} [data-action=profile-menu]")
        page.click(".menu-item:has-text('更新分流规则')")
        check(wait_until(lambda: "分流规则已更新" in page.inner_text("body"), timeout=10), "从菜单更新分流规则")
        page.click("[data-page=general]")
        page.wait_for_selector("#core-port")
        check("运行中" in page.inner_text(".card:has(#core-port)"), "常规页显示内核在运行")
        page.click("[data-page=proxies]")
        api.call("POST", "/api/use", {"name": "本机代理"})
    else:
        check(wait_until(lambda: "mihomo 内核" in page.inner_text("#page")), "没有内核时提示需要内核")

    # ---------- 局域网共享 ----------
    page.click("[data-page=share]")
    page.wait_for_selector("#share-port")
    check("未开启" in page.inner_text("#page"), "局域网共享默认关闭")
    share_config = lambda: read_config(config_path).get("share", {})
    page.fill("#share-port", "80")
    page.press("#share-port", "Enter")
    check(wait_until(lambda: "1024~65535" in page.inner_text("#page")), "共享端口太小时提示")
    page.fill("#share-port", str(config["core"]["port"]))
    page.press("#share-port", "Enter")
    port_problem = f"不能和代理内核的端口 {config['core']['port']} 相同"
    check(wait_until(lambda: port_problem in page.inner_text("#page")), "共享端口不能和内核的端口相同")
    share_port = free_port()
    page.fill("#share-port", str(share_port))
    page.press("#share-port", "Enter")
    check(wait_until(lambda: share_config().get("port") == share_port and not page.evaluate("app.saving")), "修改共享端口写入配置文件")
    check(port_problem not in page.inner_text("#page"), "改对后提示消失")
    page.fill("#share-allowed", "192.168.1.20, ps5")
    page.press("#share-allowed", "Enter")
    check(wait_until(lambda: "认不出这些地址：ps5" in page.inner_text("#page")), "允许的设备认不出时提示")
    page.fill("#share-allowed", "192.168.1.20，192.168.2.0/24")
    page.press("#share-allowed", "Enter")
    check(wait_until(lambda: share_config().get("allowed") == "192.168.1.20，192.168.2.0/24"), "允许的设备写入配置文件")
    page.fill("#share-allowed", "")
    page.press("#share-allowed", "Enter")
    check(wait_until(lambda: share_config().get("allowed") == ""), "清空允许的设备：局域网里的设备都能用")
    if CORE:
        page.click("[data-action=share-toggle]")
        check(wait_until(lambda: f"正在监听端口 {share_port}" in page.inner_text("#page"), timeout=15), "开启共享后内核监听共享端口")
        check(share_config().get("enabled") is True, "共享的开关写入配置文件")
        check(info["http_proxy"] in page.inner_text(".card:has([data-action=share-toggle])"), "本机用其他代理时共享的流量转发给它")
        page.click("[data-action=share-test]")
        check(wait_until(lambda: "共享入口和上游都通" in page.inner_text("#page"), timeout=15), "经共享端口测试")
        # 设备连上来：开着的连接出现在「正在使用的设备」里，关掉后留在「最近的连接」里。
        test_host = info["test_url"].split("/")[2]
        tunnel = socket.create_connection(("127.0.0.1", share_port), timeout=5)
        tunnel.sendall(f"CONNECT {test_host} HTTP/1.1\r\nHost: {test_host}\r\n\r\n".encode())
        check(b" 200 " in tunnel.recv(1024), "经共享入口建立隧道")
        check(wait_until(lambda: "127.0.0.1" in page.inner_text(".share-list >> nth=0"), timeout=10), "正在使用的设备列出连上来的设备")
        tunnel.close()
        check(wait_until(lambda: page.locator(".share-connection").count() > 0 and test_host in page.inner_text(".share-connections"), timeout=10), "最近的连接列出访问的地址")
        check("直连" in page.inner_text(".share-connections"), "最近的连接显示走的出口")
        page.click(".share-connection [data-action=share-rule-menu] >> nth=0")
        check(page.locator(".menu-item").count() == 4 and "走节点" in page.inner_text(".menu") and "诊断" in page.inner_text(".menu"), "最近的连接可以一键添加自定义规则或者诊断")
        page.keyboard.press("Escape")
        check("正在保持唤醒" in page.inner_text("#page"), "共享期间保持唤醒")
        api.call("POST", "/api/dev/battery", {"on_battery": True})
        check(wait_until(lambda: "用电池供电，已暂停" in page.inner_text("#page"), timeout=6), "用电池时暂停保持唤醒")
        page.click("[data-setting='share.keep_awake_on_battery']")
        check(wait_until(lambda: "正在保持唤醒" in page.inner_text("#page"), timeout=6) and share_config().get("keep_awake_on_battery") is True, "设置了用电池时也保持")
        api.call("POST", "/api/dev/battery", {"on_battery": False})
        page.click("[data-action=share-firewall]")
        check(wait_until(lambda: "已允许通过 Windows 防火墙" in page.inner_text(".toasts")), "允许通过 Windows 防火墙")
        check(api.call("GET", "/api/dev/firewall")["allowed"] == 1, "放行防火墙的请求交给了程序")
        page.click("[data-page=proxies]")
        check(wait_until(lambda: page.locator(".share-banner").count() == 1 and info["http_proxy"] in page.inner_text(".share-banner")), "代理页显示局域网共享的状态")
        page.click(".share-banner")
        page.wait_for_selector("#share-port")
        if page.locator("#share-address").count():
            page.click("[data-action=copy-share-address]")
            check(page.evaluate("navigator.clipboard.readText()").endswith(f":{share_port}"), "复制设备上要填的地址")
        else:
            check("没有连上局域网" in page.inner_text("#page"), "没有局域网地址时说明")
        page.mouse.move(0, 0)
        page.evaluate("document.getElementById('main').scrollTop = 0")
        page.evaluate("document.getElementById('toasts').replaceChildren()")
        shot(page, "12_share")
        page.click("[data-action=share-clear]")
        check(wait_until(lambda: page.locator(".share-connection").count() == 0), "清空最近的连接")
        page.click("[data-action=share-toggle]")
        check(wait_until(lambda: share_config().get("enabled") is False and "未开启" in page.inner_text("#page"), timeout=10), "关闭共享")
    else:
        check("局域网共享需要 mihomo 内核" in page.inner_text("#page"), "没有内核时共享页提示需要内核")
        page.click("[data-action=share-toggle]")
        check(wait_until(lambda: "局域网共享没有打开" in page.inner_text(".toasts")), "没有内核时开不了共享")

    # ---------- 网址诊断 ----------
    page.click("[data-page=diagnose]")
    page.wait_for_selector("#diagnose-url")
    page.click("[data-action=diagnose-start]")
    check(wait_until(lambda: "请填写网址" in page.inner_text("#page")), "没填网址时提示")
    page.fill("#diagnose-url", "not a url")
    page.press("#diagnose-url", "Enter")
    check(wait_until(lambda: "认不出这个网址" in page.inner_text("#page")), "认不出的网址时提示")
    page.fill("#diagnose-url", info["test_url"])
    page.press("#diagnose-url", "Enter")
    check(wait_until(lambda: page.locator("#diagnose-headline").count() == 1, timeout=30), "诊断完成后给出结论")
    check(page.inner_text("#diagnose-headline") == "链路正常", "经本机代理访问正常时链路正常")
    check("「本机代理」" in page.inner_text("[data-row=status]") and page.locator("[data-row=dns].skipped").count() == 1 and page.locator("[data-row=proxied].pass").count() == 1 and page.locator("[data-row=node].skipped").count() == 1, "逐项显示检查结果")
    page.mouse.move(0, 0)
    page.evaluate("document.getElementById('toasts').replaceChildren()")
    shot(page, "13_diagnose")
    page.click("[data-action=diagnose-action][data-kind=copy_report]")
    check(wait_until(lambda: "ProxySwitch 网址诊断" in page.evaluate("navigator.clipboard.readText()")), "复制诊断报告")
    serial = page.evaluate("app.diagnoseJob.serial")
    page.click("[data-action=diagnose-action][data-kind=flush_dns]")
    check(wait_until(lambda: api.call("GET", "/api/dev/dns")["flushes"] == 1 and "已清除 DNS 缓存" in page.inner_text("#toasts")), "结论里可以清除 DNS 缓存")
    check(wait_until(lambda: page.evaluate("app.diagnoseJob.serial") > serial and not page.evaluate("app.diagnoseJob.running") and page.inner_text("#diagnose-headline") == "链路正常", timeout=30), "清除 DNS 缓存后再诊断一次")
    page.click("[data-action=diagnose-perspective][data-value=device]")
    page.click("[data-action=diagnose-start]")
    check(wait_until(lambda: page.locator("#diagnose-headline").count() == 1 and page.inner_text("#diagnose-headline") == "共享入口没在监听", timeout=30), "从设备的视角看，没开共享时说明")
    page.click("[data-action=diagnose-action][data-kind=open_share]")
    check(wait_until(lambda: page.locator("#share-port").count() == 1), "结论里的按钮打开局域网共享页")
    # 命令行的「diagnose 网址」：打开的设置页切到网址诊断并立即诊断。
    api.call("POST", "/api/dev/navigate", {"page": "diagnose", "action": "diagnose", "argument": json.dumps({"url": info["test_url"], "perspective": "pc"})})
    check(wait_until(lambda: page.locator("#diagnose-url").count() == 1 and page.input_value("#diagnose-url") == info["test_url"] and page.locator("#diagnose-headline").count() == 1 and page.inner_text("#diagnose-headline") == "链路正常", timeout=30), "程序请求诊断网址时立即诊断")

    # ---------- 程序内更新（模拟的发布，开发模式只下载校验、不替换程序） ----------
    # 托盘菜单的「检查更新」：已经打开的设置窗口切到「关于」页并立即检查。
    api.call("POST", "/api/dev/navigate", {"page": "about", "action": "check-update"})
    check(wait_until(lambda: page.locator("[data-action=install-update]").count() == 1, timeout=10), "托盘菜单「检查更新」让打开的设置窗口切到关于页并检查")
    check("99.0.0" in page.inner_text(".infobar") and "新功能一" in page.inner_text(".infobar"), "检查更新显示新版本和更新内容")
    check(page.locator(".nav-item[data-page=about] .nav-badge").count() == 1, "有新版本时导航的「关于」带提示点")
    check((api.call("GET", "/api/state").get("update") or {}).get("latest") == "99.0.0", "记下检查发现的新版本，托盘菜单据此显示版本号")
    # 为「检查更新」新打开的设置窗口也立即检查。用 window.open 打开：与独立设置窗口一样，页面可以自己关闭窗口。
    checks = release_checks["count"]
    with page.expect_popup() as popup:
        page.evaluate(f"window.open({json.dumps(info['url'] + '#about')})")
    updater = popup.value
    updater.wait_for_selector(".about-hero")
    check(wait_until(lambda: release_checks["count"] > checks, timeout=10), "为「检查更新」打开的设置窗口立即检查")
    updater.wait_for_selector("[data-action=install-update]", timeout=10000)
    # 系统通知上的「立即更新」：程序请求页面直接安装，和点「一键更新」走同一个操作。
    updater.evaluate("runRequestedAction('install-update')")
    check(wait_until(lambda: updater.locator(".progress").count() == 1, timeout=3), "下载时显示进度")
    check(wait_until(lambda: updater.is_closed() or "正在重新启动" in updater.inner_text("#page"), timeout=10), "下载校验完成后提示正在重新启动")
    downloaded = os.path.join(info["dir"], "update.download")
    check(os.path.exists(downloaded) and open(downloaded, "rb").read() == FAKE_PROGRAM, "下载的新版本通过校验")
    try:
        if not updater.is_closed():
            updater.wait_for_event("close", timeout=6000)
    except Exception:
        pass
    check(updater.is_closed(), "更新完成后自动关闭旧的设置窗口")

    # ---------- Windows 高对比度主题 ----------
    contrast = page.context.browser.new_context(viewport={"width": 1120, "height": 800}, locale="zh-CN", forced_colors="active")
    high = contrast.new_page()
    high.goto(info["url"])
    high.wait_for_selector(".hero-switch")
    background = high.eval_on_selector(".hero-switch", "element => getComputedStyle(element).backgroundColor")
    marker = high.eval_on_selector(".profile-marker", "element => getComputedStyle(element).backgroundColor")
    check(background not in ("rgba(0, 0, 0, 0)", "transparent") and marker not in ("rgba(0, 0, 0, 0)", "transparent"), "高对比度主题下开关和配置颜色仍然可见")
    contrast.close()

    # ---------- 窄窗口与页面失效 ----------
    page.set_viewport_size({"width": 760, "height": 640})
    check(page.locator(".nav-item span").first.is_hidden(), "窗口较窄时导航只显示图标")
    page.reload()
    page.wait_for_selector(".nav-item")
    check(page.locator(".blocker").count() == 0, "刷新页面后仍可使用")
    other = page.context.new_page()
    other.goto(info["url"].split("?")[0] + "?token=wrong")
    other.wait_for_selector(".blocker")
    check("失效" in other.inner_text(".blocker"), "旧的链接显示已失效")
    other.close()


if __name__ == "__main__":
    main()
