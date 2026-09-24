import AppKit
import ServiceManagement
import UserNotifications

/// 登录时自动启动：macOS 13 起的 SMAppService，系统设置的「登录项」里可以看到和关闭。
enum LoginItem {
    static var isEnabled: Bool {
        SMAppService.mainApp.status == .enabled
    }

    static func set(enabled: Bool) throws {
        if enabled {
            try SMAppService.mainApp.register()
        } else {
            try SMAppService.mainApp.unregister()
        }
    }
}

/// 通知。只有从 .app 运行时才有通知中心（需要 bundle identifier），直接运行二进制时静默。
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let shared = Notifier()

    private var available: Bool { Bundle.main.bundleIdentifier != nil }
    private var authorizationRequested = false
    var onOpen: (@MainActor () -> Void)?

    func prepare() {
        guard available, !authorizationRequested else { return }
        authorizationRequested = true
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.requestAuthorization(options: [.alert, .sound]) { granted, error in
            if let error {
                Log.error("请求通知权限失败：\(error)")
            } else if !granted {
                Log.info("通知权限未授予，通知不会显示")
            }
        }
    }

    func show(title: String, body: String) {
        guard available else {
            Log.info("通知（没有 bundle，不显示）：\(title) \(body)")
            return
        }
        prepare()
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request) { error in
            if let error {
                Log.error("显示通知失败：\(error)")
            }
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        if response.actionIdentifier == UNNotificationDefaultActionIdentifier {
            Task { @MainActor in self.onOpen?() }
        }
        completionHandler()
    }
}

/// proxyswitch:// 命令：on、off、toggle、use?name=配置名、settings、panel。可以在终端里 open "proxyswitch://toggle"，也能接快捷指令。
enum URLCommand: Equatable {
    case turnOn
    case turnOff
    case toggle
    case use(String)
    case settings
    case panel

    static func parse(_ url: URL) -> URLCommand? {
        guard url.scheme?.lowercased() == "proxyswitch" else { return nil }
        let command = (url.host ?? url.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))).lowercased()
        switch command {
        case "on", "enable", "start": return .turnOn
        case "off", "disable", "stop": return .turnOff
        case "toggle": return .toggle
        case "settings", "preferences": return .settings
        case "panel", "menu": return .panel
        case "use", "switch":
            let components = URLComponents(url: url, resolvingAgainstBaseURL: false)
            var name = components?.queryItems?.first { $0.name == "name" }?.value ?? ""
            if name.isEmpty {
                name = url.path.trimmingCharacters(in: CharacterSet(charactersIn: "/")).removingPercentEncoding ?? ""
            }
            return name.isEmpty ? nil : .use(name)
        default: return nil
        }
    }
}

/// 在已经打开的终端里使用代理的命令：复制后粘贴运行，当前终端窗口就会使用代理。
enum TerminalCommands {
    static func shellQuote(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    /// zsh / bash 的 export 命令，大小写两种都设置。
    static func export(proxyURL: String, noProxy: String) -> String {
        let noProxyValue = noProxy.isEmpty ? Profile.defaultNoProxy : noProxy
        let pairs = [("http_proxy", proxyURL), ("https_proxy", proxyURL), ("no_proxy", noProxyValue)]
        let lower = pairs.map { "\($0.0)=\(shellQuote($0.1))" }
        let upper = pairs.map { "\($0.0.uppercased())=\(shellQuote($0.1))" }
        return "export " + (lower + upper).joined(separator: " ")
    }

    /// fish 的 set -gx 命令。
    static func fish(proxyURL: String, noProxy: String) -> String {
        let noProxyValue = noProxy.isEmpty ? Profile.defaultNoProxy : noProxy
        let pairs = [("http_proxy", proxyURL), ("https_proxy", proxyURL), ("no_proxy", noProxyValue)]
        return pairs.flatMap { ["set -gx \($0.0) \(shellQuote($0.1))", "set -gx \($0.0.uppercased()) \(shellQuote($0.1))"] }.joined(separator: "; ")
    }

    static func copy(_ text: String) {
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(text, forType: .string)
    }
}

/// 检查 GitHub 上的新版本（只提示，不自动安装）。
enum UpdateChecker {
    static let releasesURL = URL(string: "https://github.com/whrss9527/proxyswitch/releases")!
    static let apiURL = URL(string: "https://api.github.com/repos/whrss9527/proxyswitch/releases/latest")!

    static var currentVersion: String {
        (Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String) ?? "0.0.0"
    }

    struct Release: Equatable {
        var version: String
        var url: URL
        var hasMacAsset: Bool
    }

    static func latest() async -> Release? {
        var request = URLRequest(url: apiURL)
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        request.timeoutInterval = 15
        guard let response = try? await URLSession.shared.data(for: request),
              let json = try? JSONSerialization.jsonObject(with: response.0) as? [String: Any],
              let tag = json["tag_name"] as? String else {
            return nil
        }
        let assets = (json["assets"] as? [[String: Any]]) ?? []
        let hasMac = assets.contains { ($0["name"] as? String)?.contains("macos") == true }
        let url = (json["html_url"] as? String).flatMap(URL.init(string:)) ?? releasesURL
        return Release(version: tag.hasPrefix("v") ? String(tag.dropFirst()) : tag, url: url, hasMacAsset: hasMac)
    }

    /// 比较版本号：first 比 second 新返回 true。
    static func isNewer(_ first: String, than second: String) -> Bool {
        let a = first.split(separator: ".").map { Int($0) ?? 0 }
        let b = second.split(separator: ".").map { Int($0) ?? 0 }
        for index in 0..<max(a.count, b.count) {
            let x = index < a.count ? a[index] : 0
            let y = index < b.count ? b[index] : 0
            if x != y { return x > y }
        }
        return false
    }
}
