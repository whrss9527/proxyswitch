import Foundation

/// 代理的种类：HTTP（同时用于 HTTPS）、SOCKS5，或者 PAC 脚本。
enum ProxyKind: String, Codable, CaseIterable, Identifiable {
    case http
    case socks5
    case pac

    var id: String { rawValue }

    var title: String {
        switch self {
        case .http: return "HTTP / HTTPS"
        case .socks5: return "SOCKS5"
        case .pac: return "PAC 脚本"
        }
    }
}

/// 开启配置时要改动的地方。
enum ProxyTarget: String, Codable, CaseIterable, Identifiable {
    case system
    case environment
    case git
    case npm

    var id: String { rawValue }

    var title: String {
        switch self {
        case .system: return "系统代理"
        case .environment: return "环境变量"
        case .git: return "git"
        case .npm: return "npm / pnpm"
        }
    }

    var detail: String {
        switch self {
        case .system: return "浏览器和大多数软件都走它"
        case .environment: return "launchd 环境：之后新开的终端和程序生效"
        case .git: return "git clone、pull 等（全局 http.proxy）"
        case .npm: return "写入用户目录的 .npmrc"
        }
    }
}

/// 配置的颜色候选，与 Windows 版一致。
enum ProfilePalette {
    static let colors = ["#16a34a", "#2563eb", "#7c3aed", "#db2777", "#ea580c", "#0891b2"]

    static func color(at index: Int) -> String {
        colors[((index % colors.count) + colors.count) % colors.count]
    }
}

/// 一套代理配置。
struct Profile: Codable, Identifiable, Equatable, Hashable {
    static let defaultBypass = "localhost, 127.0.0.1, *.local, 169.254/16, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16"
    static let defaultNoProxy = "localhost,127.0.0.1,::1"

    var id: UUID = UUID()
    var name: String = "新配置"
    var color: String = ProfilePalette.colors[0]
    var kind: ProxyKind = .http
    var host: String = "127.0.0.1"
    var port: Int = 7890
    var pacURL: String = ""
    var bypass: String = Profile.defaultBypass
    var noProxy: String = Profile.defaultNoProxy
    var targets: Set<ProxyTarget> = [.system]

    init() {}

    init(name: String, color: String, kind: ProxyKind = .http, host: String = "127.0.0.1", port: Int = 7890, pacURL: String = "", targets: Set<ProxyTarget> = [.system]) {
        self.name = name
        self.color = color
        self.kind = kind
        self.host = host
        self.port = port
        self.pacURL = pacURL
        self.targets = targets
    }

    private enum CodingKeys: String, CodingKey {
        case id, name, color, kind, host, port, pacURL, bypass, noProxy, targets
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decodeIfPresent(UUID.self, forKey: .id) ?? UUID()
        name = try container.decodeIfPresent(String.self, forKey: .name) ?? "新配置"
        color = try container.decodeIfPresent(String.self, forKey: .color) ?? ProfilePalette.colors[0]
        kind = try container.decodeIfPresent(ProxyKind.self, forKey: .kind) ?? .http
        host = try container.decodeIfPresent(String.self, forKey: .host) ?? "127.0.0.1"
        port = try container.decodeIfPresent(Int.self, forKey: .port) ?? 7890
        pacURL = try container.decodeIfPresent(String.self, forKey: .pacURL) ?? ""
        bypass = try container.decodeIfPresent(String.self, forKey: .bypass) ?? Profile.defaultBypass
        noProxy = try container.decodeIfPresent(String.self, forKey: .noProxy) ?? Profile.defaultNoProxy
        targets = try container.decodeIfPresent(Set<ProxyTarget>.self, forKey: .targets) ?? [.system]
    }

    /// host:port。
    var serverAddress: String { "\(host):\(port)" }

    /// 环境变量、git、npm 使用的地址。
    var proxyURL: String {
        switch kind {
        case .socks5: return "socks5://\(host):\(port)"
        default: return "http://\(host):\(port)"
        }
    }

    /// 菜单和列表里显示的一句话。
    var summary: String {
        switch kind {
        case .pac: return pacURL.isEmpty ? "PAC 脚本" : pacURL
        case .socks5: return "socks5://\(serverAddress)"
        case .http: return serverAddress
        }
    }

    /// 环境变量、git、npm 需要一个服务器地址；PAC 配置只能设置系统代理。
    var supportsNonSystemTargets: Bool { kind != .pac }

    /// 例外列表拆成 networksetup 需要的条目。
    var bypassDomains: [String] { BypassList.domains(from: bypass) }

    /// 校验，返回问题描述；没有问题返回 nil。
    func validate() -> String? {
        if name.trimmingCharacters(in: .whitespaces).isEmpty {
            return "请填写配置名称"
        }
        switch kind {
        case .pac:
            let text = pacURL.trimmingCharacters(in: .whitespaces)
            guard let url = URL(string: text), let scheme = url.scheme?.lowercased(), ["http", "https", "file"].contains(scheme) else {
                return "PAC 地址要以 http://、https:// 或 file:// 开头"
            }
        case .http, .socks5:
            let trimmedHost = host.trimmingCharacters(in: .whitespaces)
            if trimmedHost.isEmpty || trimmedHost.contains(where: { $0.isWhitespace || "/?#@".contains($0) }) {
                return "请填写正确的主机地址，例如 127.0.0.1"
            }
            if port < 1 || port > 65535 {
                return "端口需要是 1~65535 之间的数字"
            }
        }
        if targets.isEmpty {
            return "至少选择一个生效范围"
        }
        return nil
    }
}

/// 例外列表（不经代理的地址）的解析。
enum BypassList {
    /// 按逗号、分号、空格拆分，去掉 Windows 风格的 <local>，"10.*" 这样的通配 IP 转成 CIDR。
    static func domains(from text: String) -> [String] {
        var result: [String] = []
        var seen = Set<String>()
        for raw in text.split(whereSeparator: { $0 == "," || $0 == ";" || $0 == " " || $0 == "\n" }) {
            var item = String(raw).trimmingCharacters(in: .whitespaces)
            if item.isEmpty || item == "*" || item.lowercased() == "<local>" || item.lowercased() == "<-loopback>" {
                continue
            }
            if item.hasSuffix(".*") {
                let octets = item.dropLast(2).split(separator: ".").map(String.init)
                if octets.count >= 1 && octets.count <= 3 && octets.allSatisfy({ Int($0) != nil }) {
                    var padded = octets
                    while padded.count < 4 { padded.append("0") }
                    item = padded.joined(separator: ".") + "/\(octets.count * 8)"
                }
            }
            if seen.insert(item).inserted {
                result.append(item)
            }
        }
        return result
    }
}
