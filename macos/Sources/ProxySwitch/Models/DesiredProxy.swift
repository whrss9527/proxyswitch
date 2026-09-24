import Foundation

/// 要写入系统的代理设置，以及生成 networksetup 命令的逻辑（纯函数，便于测试）。
struct DesiredProxy: Equatable {
    struct Endpoint: Equatable {
        var host: String
        var port: Int
    }

    var http: Endpoint?
    var socks: Endpoint?
    var pacURL: String?
    var autoDiscovery = false
    var bypassDomains: [String] = []

    /// 开启一个配置时的设置：开启期间关掉自动发现（WPAD），避免网络里的自动配置盖过手动设置。
    init(profile: Profile) {
        switch profile.kind {
        case .http:
            http = Endpoint(host: profile.host, port: profile.port)
        case .socks5:
            socks = Endpoint(host: profile.host, port: profile.port)
        case .pac:
            pacURL = profile.pacURL.trimmingCharacters(in: .whitespaces)
        }
        bypassDomains = profile.bypassDomains
    }

    /// 关闭代理：所有协议关掉，例外列表保留；autoDiscovery 恢复成开启前的值。
    init(offWithAutoDiscovery autoDiscovery: Bool, bypassDomains: [String]) {
        self.autoDiscovery = autoDiscovery
        self.bypassDomains = bypassDomains
    }

    /// 恢复到某个快照（关闭代理时的“恢复开启前的设置”）。
    init(restoring snapshot: ProxySnapshot) {
        if snapshot.httpActive {
            http = Endpoint(host: snapshot.httpHost, port: snapshot.httpPort)
        } else if snapshot.httpsActive {
            http = Endpoint(host: snapshot.httpsHost, port: snapshot.httpsPort)
        }
        if snapshot.socksActive {
            socks = Endpoint(host: snapshot.socksHost, port: snapshot.socksPort)
        }
        if snapshot.pacActive {
            pacURL = snapshot.pacURL
        }
        autoDiscovery = snapshot.autoDiscovery
        bypassDomains = snapshot.exceptions
    }

    /// 写到一个网络服务所需的 networksetup 参数列表（不含程序名），按顺序执行。
    /// 关闭某个协议时只改开关，保留记录的地址，和系统设置界面的行为一致。
    func commands(service: String) -> [[String]] {
        var commands: [[String]] = []
        func set(_ setter: String, _ switcher: String, _ endpoint: Endpoint?) {
            if let endpoint {
                commands.append([setter, service, endpoint.host, String(endpoint.port)])
                commands.append([switcher, service, "on"])
            } else {
                commands.append([switcher, service, "off"])
            }
        }
        set("-setwebproxy", "-setwebproxystate", http)
        set("-setsecurewebproxy", "-setsecurewebproxystate", http)
        set("-setsocksfirewallproxy", "-setsocksfirewallproxystate", socks)
        if let pacURL, !pacURL.isEmpty {
            commands.append(["-setautoproxyurl", service, pacURL])
            commands.append(["-setautoproxystate", service, "on"])
        } else {
            commands.append(["-setautoproxystate", service, "off"])
        }
        commands.append(["-setproxyautodiscovery", service, autoDiscovery ? "on" : "off"])
        commands.append(["-setproxybypassdomains", service] + (bypassDomains.isEmpty ? ["Empty"] : bypassDomains))
        return commands
    }
}
