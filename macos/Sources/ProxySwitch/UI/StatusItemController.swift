import AppKit
import SwiftUI

/// 菜单栏图标：左键打开面板（或按设置直接开关），右键弹出简洁菜单。面板是一个无边框的毛玻璃浮动窗口。
@MainActor
final class StatusItemController: NSObject {
    private let state: AppState
    private let statusItem: NSStatusItem
    private var panel: PanelWindow?
    private var hostingView: NSHostingView<PanelView>?
    private var keyObserver: Any?

    init(state: AppState) {
        self.state = state
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        super.init()
        if let button = statusItem.button {
            button.target = self
            button.action = #selector(statusItemClicked(_:))
            _ = button.sendAction(on: [.leftMouseUp, .rightMouseUp])
            button.imagePosition = .imageOnly
        }
    }

    // MARK: - 图标

    func updateIcon() {
        guard let button = statusItem.button else { return }
        let iconState: StatusIconState
        switch state.status {
        case .on(let profile):
            let color = NSColor(hex: profile.color)
            iconState = state.health == .down ? .warning(color) : .on(color)
        case .external:
            iconState = .external
        case .off:
            iconState = .off
        }
        button.image = StatusIcon.image(for: iconState)
        button.toolTip = tooltip
        if let panel, panel.isVisible {
            resizePanel()
        }
    }

    private var tooltip: String {
        switch state.status {
        case .on(let profile):
            return state.health == .down ? "ProxySwitch\n已开启：\(profile.name)\n代理服务器连不上" : "ProxySwitch\n已开启：\(profile.name)\n\(profile.summary)"
        case .external(let description):
            return "ProxySwitch\n系统代理由其他程序设置\n\(description)"
        case .off(let next):
            if let next {
                return "ProxySwitch\n已关闭，下次开启：\(next.name)"
            }
            return "ProxySwitch\n还没有代理配置"
        }
    }

    // MARK: - 点击

    @objc private func statusItemClicked(_ sender: Any?) {
        let event = NSApp.currentEvent
        if event?.type == .rightMouseUp || event?.modifierFlags.contains(.control) == true {
            closePanel()
            showContextMenu()
            return
        }
        switch state.config.clickAction {
        case .toggle:
            closePanel()
            state.toggle()
        case .panel:
            togglePanel()
        }
    }

    func perform(_ command: URLCommand) {
        switch command {
        case .turnOn:
            if case .off(let next) = state.status, let next { state.turnOn(next) }
        case .turnOff:
            state.turnOff()
        case .toggle:
            state.toggle()
        case .use(let name):
            if !state.use(named: name) {
                state.notify(title: "没有找到配置", body: "没有叫「\(name)」的配置", problem: true)
            }
        case .settings:
            SettingsWindowController.shared.show(page: nil)
        case .panel:
            openPanel()
        }
    }

    // MARK: - 右键菜单

    private func showContextMenu() {
        let menu = NSMenu()
        menu.autoenablesItems = false
        switch state.status {
        case .on(let profile):
            menu.addItem(header("代理已开启：\(profile.name)"))
            menu.addItem(item("关闭代理", action: #selector(menuTurnOff), key: ""))
        case .external(let description):
            menu.addItem(header("系统代理由其他程序设置：\(description)"))
            menu.addItem(item("关闭系统代理", action: #selector(menuTurnOff), key: ""))
            menu.addItem(item("保存为配置", action: #selector(menuSaveExternal), key: ""))
        case .off(let next):
            menu.addItem(header("代理已关闭"))
            if next != nil {
                menu.addItem(item("开启代理", action: #selector(menuTurnOn), key: ""))
            }
        }
        if !state.config.profiles.isEmpty {
            menu.addItem(.separator())
            for profile in state.config.profiles {
                let menuItem = item(profile.name, action: #selector(menuUseProfile(_:)), key: "")
                menuItem.representedObject = profile.id.uuidString
                menuItem.image = StatusIcon.dotImage(color: NSColor(hex: profile.color))
                if case .on(let current) = state.status, current.id == profile.id {
                    menuItem.state = .on
                }
                menu.addItem(menuItem)
            }
        }
        menu.addItem(.separator())
        menu.addItem(item("设置…", action: #selector(menuSettings), key: ","))
        menu.addItem(item("退出 ProxySwitch", action: #selector(menuQuit), key: "q"))
        statusItem.menu = menu
        statusItem.button?.performClick(nil)
        statusItem.menu = nil
    }

    private func header(_ title: String) -> NSMenuItem {
        let menuItem = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        menuItem.isEnabled = false
        return menuItem
    }

    private func item(_ title: String, action: Selector, key: String) -> NSMenuItem {
        let menuItem = NSMenuItem(title: title, action: action, keyEquivalent: key)
        menuItem.target = self
        return menuItem
    }

    @objc private func menuTurnOn() {
        if case .off(let next) = state.status, let next { state.turnOn(next) }
    }

    @objc private func menuTurnOff() { state.turnOff() }
    @objc private func menuSaveExternal() { state.saveExternalAsProfile() }
    @objc private func menuSettings() { SettingsWindowController.shared.show(page: nil) }
    @objc private func menuQuit() { NSApp.terminate(nil) }

    @objc private func menuUseProfile(_ sender: NSMenuItem) {
        guard let text = sender.representedObject as? String, let id = UUID(uuidString: text),
              let profile = state.config.profile(id: id) else { return }
        state.use(profile)
    }

    // MARK: - 面板

    private func togglePanel() {
        if let panel, panel.isVisible {
            closePanel()
        } else {
            openPanel()
        }
    }

    func openPanel() {
        if panel == nil {
            let view = PanelView(state: state, actions: PanelActions(
                openSettings: { [weak self] page in
                    MainActor.assumeIsolated {
                        self?.closePanel()
                        SettingsWindowController.shared.show(page: page)
                    }
                },
                close: { [weak self] in
                    MainActor.assumeIsolated { self?.closePanel() }
                },
                quit: {
                    MainActor.assumeIsolated { NSApp.terminate(nil) }
                }
            ))
            let hosting = NSHostingView(rootView: view)
            hostingView = hosting
            let panel = PanelWindow(contentView: hosting)
            panel.onClose = { [weak self] in
                MainActor.assumeIsolated { self?.closePanel() }
            }
            self.panel = panel
        }
        guard let panel else { return }
        resizePanel()
        position(panel)
        panel.orderFrontRegardless()
        panel.makeKey()
        statusItem.button?.highlight(true)
    }

    func closePanel() {
        guard let panel, panel.isVisible else { return }
        panel.orderOut(nil)
        statusItem.button?.highlight(false)
    }

    private func resizePanel() {
        guard let panel, let hostingView else { return }
        let size = hostingView.fittingSize
        if size.width > 0 && size.height > 0 && size != panel.frame.size {
            let origin = NSPoint(x: panel.frame.origin.x, y: panel.frame.maxY - size.height)
            panel.setFrame(NSRect(origin: origin, size: size), display: true)
        }
    }

    private func position(_ panel: PanelWindow) {
        guard let button = statusItem.button, let buttonWindow = button.window else { return }
        let buttonRect = buttonWindow.convertToScreen(button.convert(button.bounds, to: nil))
        let size = panel.frame.size
        var origin = NSPoint(x: buttonRect.midX - size.width / 2, y: buttonRect.minY - size.height - 6)
        if let screen = buttonWindow.screen ?? NSScreen.main {
            let visible = screen.visibleFrame
            origin.x = min(max(origin.x, visible.minX + 8), visible.maxX - size.width - 8)
            origin.y = max(origin.y, visible.minY + 8)
        }
        panel.setFrameOrigin(origin)
    }
}

/// 无边框、不激活程序的浮动面板：点到别处或按 Esc 时关闭。
final class PanelWindow: NSPanel {
    var onClose: (() -> Void)?

    init(contentView: NSView) {
        super.init(contentRect: NSRect(x: 0, y: 0, width: 320, height: 200), styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        isFloatingPanel = true
        level = .popUpMenu
        collectionBehavior = [.canJoinAllSpaces, .transient, .ignoresCycle, .fullScreenAuxiliary]
        isOpaque = false
        backgroundColor = .clear
        hasShadow = true
        isMovable = false
        hidesOnDeactivate = false
        animationBehavior = .utilityWindow
        self.contentView = contentView
    }

    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }

    override func resignKey() {
        super.resignKey()
        onClose?()
    }

    override func cancelOperation(_ sender: Any?) {
        onClose?()
    }
}

extension StatusIcon {
    /// 菜单里配置的颜色圆点。
    static func dotImage(color: NSColor) -> NSImage {
        let image = NSImage(size: NSSize(width: 12, height: 12), flipped: false) { rect in
            color.setFill()
            NSBezierPath(ovalIn: rect.insetBy(dx: 2, dy: 2)).fill()
            return true
        }
        return image
    }
}
