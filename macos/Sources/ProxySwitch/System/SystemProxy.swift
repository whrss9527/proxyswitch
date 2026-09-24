import Foundation
import SystemConfiguration

enum SystemProxyError: LocalizedError {
    case noServices
    case command(String)
    case needsAdmin(String)

    var errorDescription: String? {
        switch self {
        case .noServices: return "没有找到可用的网络服务"
        case .command(let message): return message
        case .needsAdmin(let message): return "修改系统代理需要管理员权限：\(message)"
        }
    }
}

/// macOS 系统代理（系统设置 → 网络 → 详细信息 → 代理）的读写。
/// 读取用 SystemConfiguration 的接口，不启动进程；写入用 networksetup 逐个网络服务设置，
/// 只写正在使用的服务（接口有地址的），都没有时写全部启用的服务。
/// networksetup 修改设置需要管理员账户；标准账户时用系统的授权对话框提权后再执行一次。
enum SystemProxy {
    static let networksetupPath = "/usr/sbin/networksetup"
    static let osascriptPath = "/usr/bin/osascript"

    /// 当前生效的系统代理。
    static func current() -> ProxySnapshot {
        guard let dictionary = SCDynamicStoreCopyProxies(nil) as? [String: Any] else {
            return ProxySnapshot()
        }
        return ProxySnapshot(dictionary: dictionary)
    }

    /// 写入代理设置。
    static func apply(_ desired: DesiredProxy) async throws {
        let services = NetworkServices.targetServiceNames()
        guard !services.isEmpty else { throw SystemProxyError.noServices }
        let commands = services.flatMap { desired.commands(service: $0) }
        do {
            try await runNetworksetup(commands)
        } catch SystemProxyError.needsAdmin {
            Log.info("networksetup 需要管理员权限，改用授权对话框")
            try await runNetworksetupPrivileged(commands)
        }
    }

    private static func runNetworksetup(_ commands: [[String]]) async throws {
        for arguments in commands {
            let result = try await Shell.run(networksetupPath, arguments)
            let text = result.trimmedOutput
            if !result.succeeded || text.contains("Error") {
                if isAdminError(text) {
                    throw SystemProxyError.needsAdmin(text)
                }
                throw SystemProxyError.command("networksetup \(arguments[0]) 失败：\(text)")
            }
        }
    }

    /// 用 AppleScript 的 do shell script ... with administrator privileges 一次执行全部命令，系统会弹一次输入密码的对话框。
    private static func runNetworksetupPrivileged(_ commands: [[String]]) async throws {
        let lines = commands.map { arguments in
            ([networksetupPath] + arguments).map(Shell.shellQuote).joined(separator: " ")
        }
        let script = "do shell script " + Shell.appleScriptString(lines.joined(separator: " && ")) + " with administrator privileges"
        let result = try await Shell.run(osascriptPath, ["-e", script], timeout: 180)
        if !result.succeeded {
            throw SystemProxyError.needsAdmin(result.trimmedOutput)
        }
    }

    static func isAdminError(_ text: String) -> Bool {
        let lowered = text.lowercased()
        return lowered.contains("admin") || lowered.contains("privilege") || lowered.contains("not permitted")
    }
}

/// 网络服务列表（Wi-Fi、以太网……），来自 SystemConfiguration，不需要管理员权限。
enum NetworkServices {
    struct Service: Equatable {
        var name: String
        var bsdName: String?
        var enabled: Bool
    }

    /// 当前网络位置里的服务，按系统的服务顺序。
    static func all() -> [Service] {
        guard let preferences = SCPreferencesCreate(nil, "ProxySwitch" as CFString, nil),
              let set = SCNetworkSetCopyCurrent(preferences),
              let services = SCNetworkSetCopyServices(set) as? [SCNetworkService] else {
            return []
        }
        let order = (SCNetworkSetGetServiceOrder(set) as? [String]) ?? []
        var byID: [String: Service] = [:]
        var unordered: [Service] = []
        for service in services {
            let name = (SCNetworkServiceGetName(service) as String?) ?? ""
            var bsdName: String?
            if let interface = SCNetworkServiceGetInterface(service) {
                bsdName = SCNetworkInterfaceGetBSDName(interface) as String?
            }
            let item = Service(name: name, bsdName: bsdName, enabled: SCNetworkServiceGetEnabled(service))
            if let id = SCNetworkServiceGetServiceID(service) as String? {
                byID[id] = item
            } else {
                unordered.append(item)
            }
        }
        var result: [Service] = []
        for id in order {
            if let item = byID.removeValue(forKey: id) {
                result.append(item)
            }
        }
        result += byID.values.sorted { $0.name < $1.name }
        result += unordered
        return result
    }

    /// 接口是否有 IPv4 地址（正在使用）。
    static func isActive(bsdName: String) -> Bool {
        SCDynamicStoreCopyValue(nil, "State:/Network/Interface/\(bsdName)/IPv4" as CFString) != nil
    }

    /// 要写入代理设置的服务名：接口有地址的服务；一个都没有时（离线）写全部启用的服务，这样联网后设置也是生效的。
    static func targetServiceNames() -> [String] {
        select(services: all(), isActive: isActive(bsdName:))
    }

    static func select(services: [Service], isActive: (String) -> Bool) -> [String] {
        let enabled = services.filter { $0.enabled && !$0.name.isEmpty }
        let active = enabled.filter { service in
            guard let bsdName = service.bsdName, !bsdName.isEmpty else { return false }
            return isActive(bsdName)
        }
        return (active.isEmpty ? enabled : active).map(\.name)
    }
}

/// 监听系统代理和网络的变化（别的程序改了代理、换了网络），变化时在主线程回调。
final class SystemWatcher {
    private var store: SCDynamicStore?
    private var source: CFRunLoopSource?
    private let onChange: @MainActor () -> Void

    init(onChange: @escaping @MainActor () -> Void) {
        self.onChange = onChange
    }

    func start() {
        var context = SCDynamicStoreContext(version: 0, info: Unmanaged.passUnretained(self).toOpaque(), retain: nil, release: nil, copyDescription: nil)
        let callback: SCDynamicStoreCallBack = { _, _, info in
            guard let info else { return }
            let watcher = Unmanaged<SystemWatcher>.fromOpaque(info).takeUnretainedValue()
            Task { @MainActor in
                watcher.onChange()
            }
        }
        guard let store = SCDynamicStoreCreate(nil, "ProxySwitch" as CFString, callback, &context) else {
            Log.error("无法创建 SCDynamicStore，改为定时轮询")
            return
        }
        let keys = ["State:/Network/Global/Proxies", "State:/Network/Global/IPv4", "State:/Network/Global/DNS"] as CFArray
        SCDynamicStoreSetNotificationKeys(store, keys, nil)
        guard let source = SCDynamicStoreCreateRunLoopSource(nil, store, 0) else { return }
        CFRunLoopAddSource(CFRunLoopGetMain(), source, .commonModes)
        self.store = store
        self.source = source
    }

    deinit {
        if let source {
            CFRunLoopRemoveSource(CFRunLoopGetMain(), source, .commonModes)
        }
    }
}
