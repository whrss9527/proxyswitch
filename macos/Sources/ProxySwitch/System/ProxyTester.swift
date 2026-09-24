import Foundation
import CFNetwork
import Network

struct TestResult: Equatable {
    var ok: Bool
    var latencyMs: Int?
    var message: String

    var latencyText: String {
        guard let latencyMs else { return ok ? "可用" : "失败" }
        return "\(latencyMs) ms"
    }
}

/// 经代理实际访问测速地址，测出延迟。PAC 由系统的 CFNetwork 执行，和浏览器一样。
enum ProxyTester {
    static func test(profile: Profile, testURL: String, timeout: TimeInterval = 8) async -> TestResult {
        guard let url = URL(string: testURL), url.host != nil else {
            return TestResult(ok: false, latencyMs: nil, message: "测速地址格式不对")
        }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = timeout
        configuration.timeoutIntervalForResource = timeout
        configuration.connectionProxyDictionary = proxyDictionary(for: profile)
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        var request = URLRequest(url: url)
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        request.httpMethod = "GET"
        let started = Date()
        do {
            let (_, response) = try await session.data(for: request)
            let millis = Int(Date().timeIntervalSince(started) * 1000)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            let route = profile.kind == .pac ? "按 PAC 脚本" : "经代理"
            if (200..<400).contains(status) {
                return TestResult(ok: true, latencyMs: millis, message: "\(route)访问成功，HTTP \(status)")
            }
            return TestResult(ok: false, latencyMs: millis, message: "\(route)访问返回 HTTP \(status)")
        } catch {
            return TestResult(ok: false, latencyMs: nil, message: friendlyError(error))
        }
    }

    /// URLSession 的代理设置。
    static func proxyDictionary(for profile: Profile) -> [AnyHashable: Any] {
        switch profile.kind {
        case .http:
            return [
                kCFNetworkProxiesHTTPEnable as String: 1,
                kCFNetworkProxiesHTTPProxy as String: profile.host,
                kCFNetworkProxiesHTTPPort as String: profile.port,
                kCFNetworkProxiesHTTPSEnable as String: 1,
                kCFNetworkProxiesHTTPSProxy as String: profile.host,
                kCFNetworkProxiesHTTPSPort as String: profile.port,
            ]
        case .socks5:
            return [
                kCFNetworkProxiesSOCKSEnable as String: 1,
                kCFNetworkProxiesSOCKSProxy as String: profile.host,
                kCFNetworkProxiesSOCKSPort as String: profile.port,
            ]
        case .pac:
            return [
                kCFNetworkProxiesProxyAutoConfigEnable as String: 1,
                kCFNetworkProxiesProxyAutoConfigURLString as String: profile.pacURL,
            ]
        }
    }

    static func friendlyError(_ error: Error) -> String {
        let nsError = error as NSError
        switch nsError.code {
        case NSURLErrorTimedOut: return "超时，代理没有响应"
        case NSURLErrorCannotConnectToHost: return "连不上代理服务器"
        case NSURLErrorCannotFindHost: return "解析不了主机名"
        case NSURLErrorNotConnectedToInternet: return "没有网络连接"
        default: return nsError.localizedDescription
        }
    }

    /// 检查代理服务器的端口能否连上（健康检查用），不发请求。
    static func reachable(host: String, port: Int, timeout: TimeInterval = 3) async -> Bool {
        guard let nwPort = NWEndpoint.Port(rawValue: UInt16(clamping: port)) else { return false }
        let connection = NWConnection(host: NWEndpoint.Host(host), port: nwPort, using: .tcp)
        return await withCheckedContinuation { continuation in
            let lock = NSLock()
            var finished = false
            func finish(_ value: Bool) {
                lock.lock()
                defer { lock.unlock() }
                if finished { return }
                finished = true
                connection.cancel()
                continuation.resume(returning: value)
            }
            connection.stateUpdateHandler = { state in
                switch state {
                case .ready: finish(true)
                case .failed, .cancelled: finish(false)
                case .waiting: finish(false)
                default: break
                }
            }
            connection.start(queue: DispatchQueue.global())
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout) {
                finish(false)
            }
        }
    }
}

/// 找出本机正在运行的代理软件监听的端口。
struct DetectedProxy: Identifiable, Equatable {
    var id: String { "\(host):\(port)" }
    var host: String
    var port: Int
    var kind: ProxyKind
    var process: String
    var latencyMs: Int?

    var suggestedName: String {
        process.isEmpty ? "本机代理 \(port)" : process
    }
}

enum LocalProxyDetector {
    /// 常见代理软件的端口。
    static let commonPorts: [Int] = [7890, 7891, 7897, 7898, 1080, 1081, 1087, 1089, 6152, 6153, 8080, 8888, 9090, 10808, 10809, 10810, 20171, 20172, 33210, 33211]

    struct Listener: Equatable {
        var port: Int
        var process: String
    }

    /// 解析 lsof -nP -iTCP -sTCP:LISTEN -F pcn 的输出：每个进程一行 p<pid>、一行 c<命令名>，之后每个监听地址一行 n<地址:端口>。
    static func parseLsof(_ output: String) -> [Listener] {
        var listeners: [Listener] = []
        var process = ""
        var seen = Set<Int>()
        for line in output.split(separator: "\n") {
            guard let first = line.first else { continue }
            let rest = String(line.dropFirst())
            switch first {
            case "p": process = ""
            case "c": process = rest
            case "n":
                if let colon = rest.lastIndex(of: ":"), let port = Int(rest[rest.index(after: colon)...]), seen.insert(port).inserted {
                    listeners.append(Listener(port: port, process: process))
                }
            default: break
            }
        }
        return listeners
    }

    static func listeners() async -> [Listener] {
        let result = try? await Shell.run("/usr/sbin/lsof", ["-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcn"], timeout: 15)
        return parseLsof(result?.output ?? "")
    }

    /// 找出能当代理用的本机端口：先看监听列表和常见端口，再逐个经它访问测速地址确认。
    static func detect(testURL: String) async -> [DetectedProxy] {
        var candidates: [Int: String] = [:]
        for listener in await listeners() where listener.port > 0 {
            candidates[listener.port] = listener.process
        }
        for port in commonPorts where candidates[port] == nil {
            if await ProxyTester.reachable(host: "127.0.0.1", port: port, timeout: 0.5) {
                candidates[port] = ""
            }
        }
        var found: [DetectedProxy] = []
        await withTaskGroup(of: DetectedProxy?.self) { group in
            for (port, process) in candidates {
                group.addTask {
                    for kind in [ProxyKind.http, ProxyKind.socks5] {
                        var profile = Profile(name: "", color: "", kind: kind, host: "127.0.0.1", port: port)
                        profile.kind = kind
                        let result = await ProxyTester.test(profile: profile, testURL: testURL, timeout: 4)
                        if result.ok {
                            return DetectedProxy(host: "127.0.0.1", port: port, kind: kind, process: process, latencyMs: result.latencyMs)
                        }
                    }
                    return nil
                }
            }
            for await item in group {
                if let item { found.append(item) }
            }
        }
        return found.sorted { $0.port < $1.port }
    }
}
