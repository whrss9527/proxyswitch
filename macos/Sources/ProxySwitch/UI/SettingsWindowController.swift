import AppKit
import SwiftUI

enum SettingsPage: String, CaseIterable, Identifiable {
    case profiles
    case general
    case hotkey
    case diagnostics
    case about

    var id: String { rawValue }

    var title: String {
        switch self {
        case .profiles: return "代理配置"
        case .general: return "通用"
        case .hotkey: return "快捷键"
        case .diagnostics: return "诊断"
        case .about: return "关于"
        }
    }

    var symbol: String {
        switch self {
        case .profiles: return "point.3.connected.trianglepath.dotted"
        case .general: return "gearshape"
        case .hotkey: return "keyboard"
        case .diagnostics: return "stethoscope"
        case .about: return "info.circle"
        }
    }
}

@MainActor
final class SettingsNavigation: ObservableObject {
    @Published var page: SettingsPage = .profiles
    @Published var selectedProfileID: UUID?
}

/// 设置窗口：透明标题栏、全尺寸内容，内容是 SwiftUI。
@MainActor
final class SettingsWindowController: NSObject, NSWindowDelegate {
    static let shared = SettingsWindowController()

    let navigation = SettingsNavigation()
    private var window: NSWindow?

    func show(page: SettingsPage?) {
        if let page {
            navigation.page = page
        }
        if window == nil {
            window = makeWindow()
        }
        NSApp.activate(ignoringOtherApps: true)
        window?.makeKeyAndOrderFront(nil)
    }

    private func makeWindow() -> NSWindow {
        let root = SettingsRootView(state: AppState.shared, navigation: navigation)
        let hosting = NSHostingController(rootView: root)
        let window = NSWindow(contentViewController: hosting)
        window.title = "ProxySwitch 设置"
        window.styleMask = [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView]
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .hidden
        window.isMovableByWindowBackground = true
        window.setContentSize(NSSize(width: 900, height: 620))
        window.minSize = NSSize(width: 760, height: 520)
        window.center()
        window.isReleasedWhenClosed = false
        window.delegate = self
        return window
    }
}

/// 设置窗口的内容：左侧导航，右侧各页；整个窗口透出桌面的毛玻璃。
struct SettingsRootView: View {
    @ObservedObject var state: AppState
    @ObservedObject var navigation: SettingsNavigation

    var body: some View {
        NavigationSplitView {
            List(SettingsPage.allCases, selection: pageSelection) { page in
                Label(page.title, systemImage: page.symbol)
                    .tag(page)
            }
            .listStyle(.sidebar)
            .navigationSplitViewColumnWidth(min: 170, ideal: 190, max: 240)
            .safeAreaInset(edge: .top) {
                HStack(spacing: 8) {
                    Image(nsImage: NSApp.applicationIconImage)
                        .resizable()
                        .frame(width: 28, height: 28)
                    Text("ProxySwitch")
                        .font(.system(size: 14, weight: .semibold))
                    Spacer()
                }
                .padding(.horizontal, 14)
                .padding(.top, 34)
                .padding(.bottom, 4)
            }
        } detail: {
            detail
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(VisualEffectView(material: .underWindowBackground).ignoresSafeArea())
        }
        .frame(minWidth: 760, minHeight: 520)
    }

    private var pageSelection: Binding<SettingsPage?> {
        Binding(get: { navigation.page }, set: { if let page = $0 { navigation.page = page } })
    }

    @ViewBuilder
    private var detail: some View {
        switch navigation.page {
        case .profiles: ProfilesPage(state: state, navigation: navigation)
        case .general: GeneralPage(state: state)
        case .hotkey: HotkeyPage(state: state)
        case .diagnostics: DiagnosticsPage(state: state)
        case .about: AboutPage(state: state)
        }
    }
}

/// 页面标题。
struct PageHeader: View {
    var title: String
    var subtitle: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.system(size: 22, weight: .bold))
            Text(subtitle)
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 24)
        .padding(.top, 36)
        .padding(.bottom, 8)
    }
}

// MARK: - 通用

struct GeneralPage: View {
    @ObservedObject var state: AppState

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "通用", subtitle: "菜单栏图标的行为、关闭代理的方式、通知")
            Form {
                Section("启动") {
                    Toggle("登录时自动启动", isOn: Binding(get: { state.loginItemEnabled }, set: { state.setLoginItem($0) }))
                    Toggle("退出 ProxySwitch 时关闭代理", isOn: $state.config.disableOnExit)
                }
                Section("菜单栏图标") {
                    Picker("左键点击", selection: $state.config.clickAction) {
                        ForEach(ClickAction.allCases) { action in
                            Text(action.title).tag(action)
                        }
                    }
                    Text("右键或 Control + 点击总是弹出菜单")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Section("代理") {
                    Picker("关闭代理时", selection: $state.config.offMode) {
                        ForEach(OffMode.allCases) { mode in
                            Text(mode.title).tag(mode)
                        }
                    }
                    Toggle("定期检查代理服务器能否连上", isOn: $state.config.healthCheck)
                    TextField("测速地址", text: $state.config.testURL)
                        .textFieldStyle(.roundedBorder)
                }
                Section("通知") {
                    Picker("通知", selection: $state.config.notifyLevel) {
                        ForEach(NotifyLevel.allCases) { level in
                            Text(level.title).tag(level)
                        }
                    }
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
        }
    }
}

// MARK: - 快捷键

struct HotkeyPage: View {
    @ObservedObject var state: AppState

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "快捷键", subtitle: "在任何程序里按下它就能开关代理")
            Form {
                Section("开 / 关代理") {
                    HStack {
                        HotkeyRecorder(binding: $state.config.toggleHotkey)
                            .frame(width: 180, height: 28)
                        Button("清除") { state.config.toggleHotkey = nil }
                            .disabled(state.config.toggleHotkey == nil)
                    }
                    Text("点击方框后按下新的组合键，至少包含 ⌃、⌥、⇧、⌘ 中的一个。")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    if let error = state.lastError, error.contains("快捷键") {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .font(.caption)
                            .foregroundStyle(.orange)
                    }
                }
                Section("命令行与快捷指令") {
                    Text("终端里可以用 open 命令控制：")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    ForEach(["open proxyswitch://toggle", "open proxyswitch://on", "open proxyswitch://off", "open \"proxyswitch://use?name=配置名\""], id: \.self) { command in
                        HStack {
                            Text(command)
                                .font(.system(size: 12, design: .monospaced))
                                .textSelection(.enabled)
                            Spacer()
                            Button {
                                TerminalCommands.copy(command)
                            } label: {
                                Image(systemName: "doc.on.doc")
                            }
                            .buttonStyle(.plain)
                            .foregroundStyle(.secondary)
                        }
                    }
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
        }
    }
}

// MARK: - 诊断

struct DiagnosticsPage: View {
    @ObservedObject var state: AppState
    @State private var environment: [String: String] = [:]
    @State private var gitProxy = ""
    @State private var npm: [String: String] = [:]
    @State private var services: [NetworkServices.Service] = []
    @State private var logText = ""
    @State private var clearing = false

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "诊断", subtitle: "系统里各处的代理设置，以及运行日志")
            Form {
                Section("系统代理") {
                    LabeledContent("当前生效", value: state.snapshot.summary)
                    LabeledContent("自动发现（WPAD）", value: state.snapshot.autoDiscovery ? "开" : "关")
                    LabeledContent("例外", value: state.snapshot.exceptions.isEmpty ? "无" : state.snapshot.exceptions.joined(separator: ", "))
                    LabeledContent("网络服务", value: services.isEmpty ? "无" : services.map { $0.enabled ? $0.name : "\($0.name)（已停用）" }.joined(separator: "、"))
                    HStack {
                        Button("打开系统的代理设置") {
                            NSWorkspace.shared.open(URL(string: "x-apple.systempreferences:com.apple.Network-Settings.extension")!)
                        }
                        Button(clearing ? "正在清除…" : "清除所有代理设置") {
                            clearAll()
                        }
                        .disabled(clearing)
                    }
                }
                Section("环境变量（launchd）") {
                    ForEach(EnvironmentProxy.names, id: \.self) { name in
                        LabeledContent(name, value: environment[name]?.isEmpty == false ? environment[name]! : "未设置")
                    }
                    Text("新打开的终端和程序会读到这些变量；已经打开的终端请用面板里的「复制终端命令」。")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Section("git 与 npm") {
                    LabeledContent("git http.proxy", value: gitProxy.isEmpty ? "未设置" : gitProxy)
                    LabeledContent("npm proxy", value: npm["proxy"] ?? "未设置")
                    LabeledContent("npm https-proxy", value: npm["https-proxy"] ?? "未设置")
                }
                Section("文件") {
                    LabeledContent("配置目录", value: Store.directory.path)
                    HStack {
                        Button("打开配置目录") { NSWorkspace.shared.open(Store.directory) }
                        Button("刷新") { reload() }
                    }
                }
                Section("日志") {
                    ScrollView {
                        Text(logText.isEmpty ? "还没有日志" : logText)
                            .font(.system(size: 11, design: .monospaced))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(height: 220)
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
        }
        .task { reload() }
    }

    private func reload() {
        services = NetworkServices.all()
        logText = Log.tail(lines: 120)
        npm = NpmProxy.current()
        Task { @MainActor in
            environment = await EnvironmentProxy.current()
            gitProxy = await GitProxy.current()
        }
    }

    private func clearAll() {
        clearing = true
        Task { @MainActor in
            state.turnOff()
            try? await EnvironmentProxy.clear()
            try? await GitProxy.clear()
            try? NpmProxy.clear()
            clearing = false
            reload()
        }
    }
}

// MARK: - 关于

struct AboutPage: View {
    @ObservedObject var state: AppState
    @State private var checking = false
    @State private var checked = false

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "关于", subtitle: "ProxySwitch for Mac")
            VStack(spacing: 16) {
                Image(nsImage: NSApp.applicationIconImage)
                    .resizable()
                    .frame(width: 96, height: 96)
                    .shadow(color: .black.opacity(0.2), radius: 12, y: 6)
                Text("ProxySwitch")
                    .font(.system(size: 20, weight: .bold))
                Text("版本 \(UpdateChecker.currentVersion)")
                    .foregroundStyle(.secondary)
                Text("菜单栏里的代理开关：一键切换系统代理、环境变量、git 和 npm 的代理设置。")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 360)
                HStack(spacing: 10) {
                    Button("GitHub") { NSWorkspace.shared.open(URL(string: "https://github.com/whrss9527/proxyswitch")!) }
                    Button("反馈问题") { NSWorkspace.shared.open(URL(string: "https://github.com/whrss9527/proxyswitch/issues")!) }
                    Button(checking ? "正在检查…" : "检查更新") {
                        checking = true
                        Task { @MainActor in
                            await state.checkForUpdates()
                            checking = false
                            checked = true
                        }
                    }
                    .disabled(checking)
                }
                if let release = state.latestRelease {
                    Button {
                        NSWorkspace.shared.open(release.url)
                    } label: {
                        Label("有新版本 \(release.version)，去下载", systemImage: "arrow.down.circle.fill")
                    }
                    .buttonStyle(.borderedProminent)
                } else if checked {
                    Text("已经是最新版本")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Text("MIT License")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
            .frame(maxWidth: .infinity)
            .padding(28)
            .glassCard(cornerRadius: 20)
            .padding(24)
            Spacer()
        }
    }
}
