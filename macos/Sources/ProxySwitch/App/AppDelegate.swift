import AppKit

@main
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    static func main() {
        let app = NSApplication.shared
        let delegate = AppDelegate()
        app.delegate = delegate
        // 只有菜单栏图标，不在 Dock 里显示。
        app.setActivationPolicy(.accessory)
        app.run()
    }

    private var statusController: StatusItemController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let state = AppState.shared
        Notifier.shared.onOpen = { SettingsWindowController.shared.show(page: .profiles) }
        let controller = StatusItemController(state: state)
        statusController = controller
        state.onStatusChanged = { [weak controller] in controller?.updateIcon() }
        state.start()
        controller.updateIcon()
        Log.info("ProxySwitch 已启动，版本 \(UpdateChecker.currentVersion)")
        // 还没有任何配置时直接打开设置引导添加。
        if state.config.profiles.isEmpty {
            SettingsWindowController.shared.show(page: .profiles)
        }
    }

    func application(_ application: NSApplication, open urls: [URL]) {
        for url in urls {
            if let command = URLCommand.parse(url) {
                statusController?.perform(command)
            } else {
                Log.error("不认识的命令：\(url)")
            }
        }
    }

    /// 再次打开程序（Finder 里双击、Dock 里点击）时打开设置。
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        SettingsWindowController.shared.show(page: nil)
        return false
    }

    func applicationWillTerminate(_ notification: Notification) {
        AppState.shared.handleExit()
        Log.info("ProxySwitch 已退出")
    }
}
