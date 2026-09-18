#!/usr/bin/env python3
"""设置页面的自动化测试（Playwright + Chromium）。

用法：先 `go run . --dev-settings --dir=/tmp/x` 拿到 URL，再 `python3 tools/ui_test.py <URL> <截图目录>`。
会走一遍：加载 → 截图 → 新建配置（含校验）→ 录入快捷键 → 保存 → 校验配置文件 → 切换/开关 → 深色模式截图。
"""
import json
import os
import sys

from playwright.sync_api import sync_playwright

url = sys.argv[1]
shots = sys.argv[2] if len(sys.argv) > 2 else "."
cfg_dir = sys.argv[3] if len(sys.argv) > 3 else None
os.makedirs(shots, exist_ok=True)

failures = []


def check(cond, msg):
    if not cond:
        failures.append(msg)
        print("FAIL:", msg)
    else:
        print("ok:", msg)


with sync_playwright() as p:
    browser = p.chromium.launch()
    ctx = browser.new_context(viewport={"width": 1100, "height": 900}, device_scale_factor=1, locale="zh-CN", bypass_csp=True)
    page = ctx.new_page()
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)

    page.goto(url)
    page.wait_for_selector(".profile")
    check(page.url.endswith("/") and "token" not in page.url, "token 从地址栏移除")
    check(page.locator(".profile").count() == 3, "默认 3 套配置渲染出来")
    page.screenshot(path=f"{shots}/ui_01_home.png", full_page=True)

    # 新建配置：先触发校验错误
    page.click("#btnAdd")
    page.wait_for_selector("#modal:not(.hidden)")
    page.click("#btnOk")
    check(page.locator("#eName").inner_text().strip() != "", "空名称提示错误")
    page.fill("#pName", "本地 Clash")
    page.fill("#pHost", "127.0.0.1")
    page.fill("#pPort", "7890")
    page.click("#btnOk")
    check("同名" in page.locator("#eName").inner_text(), "同名配置提示错误")
    page.fill("#pName", "测试代理")
    page.fill("#pPort", "99999")
    page.click("#btnOk")
    check("端口" in page.locator("#eServer").inner_text(), "非法端口提示错误")
    page.fill("#pPort", "1080")
    # 勾选环境变量
    page.check("#targets input[value=env]")
    page.screenshot(path=f"{shots}/ui_02_modal.png")
    # 测试连接（本机 1080 应该连不上）
    page.click("#btnTest")
    page.wait_for_function("document.querySelector('#testResult').textContent.trim() !== '测试中…' && document.querySelector('#testResult').textContent.trim() !== ''")
    print("test result:", page.locator("#testResult").inner_text())
    check(page.locator("#testResult").inner_text().startswith("✕"), "连接测试给出结果（预期连不上）")
    page.click("#btnOk")
    page.wait_for_selector("#modal", state="hidden")
    check(page.locator(".profile").count() == 4, "新建后列表有 4 套配置")
    check(not page.locator("#btnSave").is_disabled(), "有修改后保存按钮可用")
    check(page.locator(".btn-use").first.is_disabled(), "有未保存修改时「使用」按钮禁用")

    # PAC 模式校验：勾了 env 应报错
    page.click("#btnAdd")
    page.fill("#pName", "PAC 测试")
    page.click(".seg button[data-mode=pac]")
    page.fill("#pPac", "http://example.com/proxy.pac")
    page.check("#targets input[value=git]")
    page.click("#btnOk")
    check("PAC" in page.locator("#eTargets").inner_text(), "PAC 模式勾选 git 时提示错误")
    page.uncheck("#targets input[value=git]")
    page.click("#btnOk")
    page.wait_for_selector("#modal", state="hidden")
    check(page.locator(".profile").count() == 5, "PAC 配置添加成功")

    # 快捷键录入
    page.click("#hotkey")
    page.keyboard.press("Control+Shift+F9")
    check(page.input_value("#hotkey") == "Ctrl+Shift+F9", "快捷键录入为 Ctrl+Shift+F9，实际 " + page.input_value("#hotkey"))

    # 上移 / 删除
    page.locator(".profile").nth(3).locator("button[data-act=up]").click()
    check(page.locator(".profile").nth(2).inner_text().startswith("测试代理"), "上移生效")
    page.locator(".profile").nth(1).locator("button[data-act=del]").click()
    page.locator(".profile").nth(1).locator("button[data-act=del]").click()
    check(page.locator(".profile").count() == 4, "两次点击删除生效")

    # 保存
    page.click("#btnSave")
    page.wait_for_function("document.querySelector('#dirtyHint').textContent.includes('没有未保存')")
    check(page.locator("#btnSave").is_disabled(), "保存后按钮变为禁用")
    page.screenshot(path=f"{shots}/ui_03_saved.png", full_page=True)
    if cfg_dir:
        raw = open(os.path.join(cfg_dir, "config.jsonc"), encoding="utf-8-sig").read()
        body = "\n".join(l for l in raw.splitlines() if not l.strip().startswith("//"))
        saved = json.loads(body)
        names = [p["name"] for p in saved["profiles"]]
        print("saved profiles:", names)
        check(saved["hotkey"] == "Ctrl+Shift+F9", "配置文件里的快捷键已更新")
        check("测试代理" in names and "PAC 测试" in names and "公司 PAC（示例）" not in names, "配置文件里的配置列表正确")
        tp = next(p for p in saved["profiles"] if p["name"] == "测试代理")
        check(tp["server"] == "127.0.0.1:1080" and tp["apply_to"] == ["system", "env"], "新配置的字段正确: %s" % tp)

    # 使用某套配置 → 状态变为开启
    page.locator(".profile").nth(1).locator("button[data-act=use]").click()
    page.locator(".profile").nth(1).locator(".tag", has_text="使用中").wait_for(timeout=15000)
    page.wait_for_function("document.querySelector('#statusText').textContent.includes('已开启')")
    check("使用中" in page.locator(".profile").nth(1).inner_text(), "「使用」后显示使用中")
    check(page.locator("#btnToggle").inner_text() == "关闭代理", "顶部按钮变为关闭代理")
    page.click("#btnToggle")
    page.wait_for_function("document.querySelector('#statusText').textContent.includes('已关闭')")
    check(True, "关闭代理成功")

    # 开机自启开关
    was = page.is_checked("#autostart")
    page.click("label.switch:has(#autostart) span")
    page.wait_for_timeout(500)
    check(page.is_checked("#autostart") != was, "开机自启开关立即生效")
    page.click("label.switch:has(#autostart) span")
    page.wait_for_timeout(500)
    check(page.is_checked("#autostart") == was, "开机自启开关可以切回")

    # 编辑已有配置：高级写法回填
    page.locator(".profile").nth(0).locator("button[data-act=edit]").click()
    page.wait_for_selector("#modal:not(.hidden)")
    check(page.input_value("#pHost") == "127.0.0.1" and page.input_value("#pPort") == "7890", "编辑时地址端口回填")
    page.click("#pRaw >> xpath=ancestor::details/summary")
    page.fill("#pRaw", "http=127.0.0.1:7890;socks=127.0.0.1:7891")
    page.click("#btnOk")
    page.wait_for_selector("#modal", state="hidden")
    check("http=127.0.0.1:7890;socks=127.0.0.1:7891" in page.locator(".profile").nth(0).inner_text(), "高级写法保存到列表")
    page.click("#btnDiscard")
    check(page.locator("#btnSave").is_disabled(), "放弃更改后恢复")

    # 深色模式截图
    page.emulate_media(color_scheme="dark")
    page.screenshot(path=f"{shots}/ui_04_dark.png", full_page=True)
    page.emulate_media(color_scheme="light")
    # 窄屏
    page.set_viewport_size({"width": 420, "height": 800})
    page.screenshot(path=f"{shots}/ui_05_mobile.png", full_page=True)

    check(not errors, "浏览器无 JS 报错: %s" % errors)
    browser.close()

print("\nRESULT:", "PASS" if not failures else f"{len(failures)} FAILED")
sys.exit(1 if failures else 0)
