import Foundation
import SystemConfiguration

/// 系统代理设置的一份快照（SCDynamicStoreCopyProxies 的内容）。关着的协议仍可能保留地址。
struct ProxySnapshot: Codable, Equatable {
    var httpEnabled = false
    var httpHost = ""
    var httpPort = 0
    var httpsEnabled = false
    var httpsHost = ""
    var httpsPort = 0
    var socksEnabled = false
    var socksHost = ""
    var socksPort = 0
    var pacEnabled = false
    var pacURL = ""
    var autoDiscovery = false
    var excludeSimpleHostnames = false
    var exceptions: [String] = []

    init() {}

    /// 从 SCDynamicStoreCopyProxies 返回的字典解析。
    init(dictionary: [String: Any]) {
        func flag(_ key: CFString) -> Bool { (dictionary[key as String] as? NSNumber)?.intValue == 1 }
        func text(_ key: CFString) -> String { (dictionary[key as String] as? String) ?? "" }
        func number(_ key: CFString) -> Int { (dictionary[key as String] as? NSNumber)?.intValue ?? 0 }
        httpEnabled = flag(kSCPropNetProxiesHTTPEnable)
        httpHost = text(kSCPropNetProxiesHTTPProxy)
        httpPort = number(kSCPropNetProxiesHTTPPort)
        httpsEnabled = flag(kSCPropNetProxiesHTTPSEnable)
        httpsHost = text(kSCPropNetProxiesHTTPSProxy)
        httpsPort = number(kSCPropNetProxiesHTTPSPort)
        socksEnabled = flag(kSCPropNetProxiesSOCKSEnable)
        socksHost = text(kSCPropNetProxiesSOCKSProxy)
        socksPort = number(kSCPropNetProxiesSOCKSPort)
        pacEnabled = flag(kSCPropNetProxiesProxyAutoConfigEnable)
        pacURL = text(kSCPropNetProxiesProxyAutoConfigURLString)
        autoDiscovery = flag(kSCPropNetProxiesProxyAutoDiscoveryEnable)
        excludeSimpleHostnames = flag(kSCPropNetProxiesExcludeSimpleHostnames)
        exceptions = (dictionary[kSCPropNetProxiesExceptionsList as String] as? [String]) ?? []
    }

    var httpActive: Bool { httpEnabled && !httpHost.isEmpty && httpPort > 0 }
    var httpsActive: Bool { httpsEnabled && !httpsHost.isEmpty && httpsPort > 0 }
    var socksActive: Bool { socksEnabled && !socksHost.isEmpty && socksPort > 0 }
    var pacActive: Bool { pacEnabled && !pacURL.isEmpty }

    /// 是否有任何代理在生效。
    var isActive: Bool { httpActive || httpsActive || socksActive || pacActive }

    /// 描述当前生效的代理，菜单和通知里用。
    var summary: String {
        var parts: [String] = []
        if pacActive { parts.append("PAC \(pacURL)") }
        if httpActive || httpsActive {
            let http = httpActive ? "\(httpHost):\(httpPort)" : ""
            let https = httpsActive ? "\(httpsHost):\(httpsPort)" : ""
            if http == https || https.isEmpty {
                parts.append(http)
            } else if http.isEmpty {
                parts.append(https)
            } else {
                parts.append("http=\(http) https=\(https)")
            }
        }
        if socksActive { parts.append("socks5://\(socksHost):\(socksPort)") }
        return parts.isEmpty ? "未开启" : parts.joined(separator: " + ")
    }

    /// 系统代理是否正是这个配置设置的。
    func matches(_ profile: Profile) -> Bool {
        guard profile.targets.contains(.system) else { return false }
        switch profile.kind {
        case .pac:
            return pacActive && pacURL.caseInsensitiveCompare(profile.pacURL.trimmingCharacters(in: .whitespaces)) == .orderedSame
        case .http:
            return !pacActive && (httpActive || httpsActive)
                && (!httpActive || (httpHost == profile.host && httpPort == profile.port))
                && (!httpsActive || (httpsHost == profile.host && httpsPort == profile.port))
        case .socks5:
            return !pacActive && !httpActive && !httpsActive && socksActive && socksHost == profile.host && socksPort == profile.port
        }
    }

    /// 把系统当前的代理转成一个配置，方便把别的程序设置的代理保存下来。
    func asProfile(name: String) -> Profile? {
        var profile = Profile(name: name, color: ProfilePalette.colors[4])
        if pacActive {
            profile.kind = .pac
            profile.pacURL = pacURL
        } else if httpActive || httpsActive {
            profile.kind = .http
            profile.host = httpActive ? httpHost : httpsHost
            profile.port = httpActive ? httpPort : httpsPort
        } else if socksActive {
            profile.kind = .socks5
            profile.host = socksHost
            profile.port = socksPort
        } else {
            return nil
        }
        if !exceptions.isEmpty {
            profile.bypass = exceptions.joined(separator: ", ")
        }
        return profile
    }
}
