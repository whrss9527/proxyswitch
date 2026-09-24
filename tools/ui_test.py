#!/usr/bin/env python3
"""设置页的浏览器自动化测试（Playwright + Chromium）。

自动编译并启动开发模式的设置服务（系统设置用内存模拟，带假的 HTTP / SOCKS5 代理），
走一遍首次使用、添加和编辑配置、测速、切换、自动切换、常规设置、诊断、深色模式、健康检查等流程。

用法：python3 tools/ui_test.py [截图目录]
截图目录里的 docs_light.png / docs_dark.png 可以直接用作 README 的截图。
"""
import json
import os
import subprocess
import sys
import tempfile
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


def start_server(directory):
    binary = os.path.join(directory, "proxyswitch-dev")
    subprocess.run(["go", "build", "-o", binary, "."], cwd=ROOT, check=True)
    process = subprocess.Popen([binary, "--dev-settings", f"--dir={directory}"], stdout=subprocess.PIPE, text=True)
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
            # 接口拒绝非法输入时返回 400 / 409，浏览器会把它记成控制台错误，这是预期行为。
            expected = ("status of 400", "status of 409")
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
    check(page.locator(".choice").count() == 3, "空状态提供三种添加方式")
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
    shot(page, "06_general")

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
    page.click("[data-page=diagnostics]")
    page.wait_for_selector(".kv")
    check(info["http_proxy"] in page.inner_text(".kv >> nth=0"), "诊断页显示系统代理地址")
    check("level=" in page.inner_text("#log"), "诊断页显示日志")
    page.click("[data-action=clear-all]")
    page.click(".dialog [data-dialog-result=yes]")
    check(wait_until(lambda: not api.call("GET", "/api/dev/system")["system"]["proxy_enabled"]), "清除所有代理设置")
    page.click("[data-page=about]")
    page.wait_for_selector(".about-hero")
    check(state["version"] in page.inner_text(".about-hero"), "关于页显示版本号")

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
