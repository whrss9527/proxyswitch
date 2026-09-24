import XCTest
@testable import ProxySwitch

final class DesiredProxyTests: XCTestCase {
    func testHttpProfileCommands() {
        var profile = Profile(name: "本机", color: "#16a34a", kind: .http, host: "127.0.0.1", port: 7890)
        profile.bypass = "localhost, 10.*, <local>"
        let commands = DesiredProxy(profile: profile).commands(service: "Wi-Fi").map { $0.joined(separator: " ") }
        XCTAssertEqual(commands, [
            "-setwebproxy Wi-Fi 127.0.0.1 7890",
            "-setwebproxystate Wi-Fi on",
            "-setsecurewebproxy Wi-Fi 127.0.0.1 7890",
            "-setsecurewebproxystate Wi-Fi on",
            "-setsocksfirewallproxystate Wi-Fi off",
            "-setautoproxystate Wi-Fi off",
            "-setproxyautodiscovery Wi-Fi off",
            "-setproxybypassdomains Wi-Fi localhost 10.0.0.0/8",
        ])
    }

    func testOffKeepsAddressesAndRestoresDiscovery() {
        let commands = DesiredProxy(offWithAutoDiscovery: true, bypassDomains: []).commands(service: "以太网").map { $0.joined(separator: " ") }
        XCTAssertFalse(commands.contains { $0.hasPrefix("-setwebproxy ") || $0.hasPrefix("-setsecurewebproxy ") || $0.hasPrefix("-setautoproxyurl") })
        XCTAssertTrue(commands.contains("-setproxyautodiscovery 以太网 on"))
        XCTAssertEqual(commands.last, "-setproxybypassdomains 以太网 Empty")
    }

    func testPacAndSocks() {
        let pac = Profile(name: "PAC", color: "#000000", kind: .pac, pacURL: "http://127.0.0.1:7890/proxy.pac")
        let pacCommands = DesiredProxy(profile: pac).commands(service: "Wi-Fi").map { $0.joined(separator: " ") }
        XCTAssertTrue(pacCommands.contains("-setautoproxyurl Wi-Fi http://127.0.0.1:7890/proxy.pac"))
        XCTAssertTrue(pacCommands.contains("-setautoproxystate Wi-Fi on"))
        XCTAssertTrue(pacCommands.contains("-setwebproxystate Wi-Fi off"))

        let socks = Profile(name: "SOCKS", color: "#000000", kind: .socks5, host: "127.0.0.1", port: 1080)
        let socksCommands = DesiredProxy(profile: socks).commands(service: "Wi-Fi").map { $0.joined(separator: " ") }
        XCTAssertTrue(socksCommands.contains("-setsocksfirewallproxy Wi-Fi 127.0.0.1 1080"))
        XCTAssertTrue(socksCommands.contains("-setsocksfirewallproxystate Wi-Fi on"))
        XCTAssertTrue(socksCommands.contains("-setwebproxystate Wi-Fi off"))
    }

    func testRestoreSnapshot() {
        var snapshot = ProxySnapshot()
        snapshot.httpEnabled = true
        snapshot.httpHost = "proxy.corp"
        snapshot.httpPort = 3128
        snapshot.autoDiscovery = true
        snapshot.exceptions = ["*.corp"]
        let desired = DesiredProxy(restoring: snapshot)
        XCTAssertEqual(desired.http, DesiredProxy.Endpoint(host: "proxy.corp", port: 3128))
        XCTAssertTrue(desired.autoDiscovery)
        XCTAssertEqual(desired.bypassDomains, ["*.corp"])
    }
}

final class BypassListTests: XCTestCase {
    func testConversion() {
        XCTAssertEqual(BypassList.domains(from: "localhost;127.*;10.*;172.16.*;192.168.1.*;<local>;*.local, 169.254/16"),
                       ["localhost", "127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/16", "192.168.1.0/24", "*.local", "169.254/16"])
        XCTAssertEqual(BypassList.domains(from: ""), [])
        XCTAssertEqual(BypassList.domains(from: "a.com, a.com"), ["a.com"])
    }
}

final class ProxySnapshotTests: XCTestCase {
    func testParseAndMatch() {
        let snapshot = ProxySnapshot(dictionary: [
            "HTTPEnable": 1, "HTTPProxy": "127.0.0.1", "HTTPPort": 7890,
            "HTTPSEnable": 1, "HTTPSProxy": "127.0.0.1", "HTTPSPort": 7890,
            "SOCKSEnable": 0, "SOCKSProxy": "127.0.0.1", "SOCKSPort": 7891,
            "ProxyAutoConfigEnable": 0, "ProxyAutoConfigURLString": "http://x/proxy.pac",
            "ProxyAutoDiscoveryEnable": 1, "ExceptionsList": ["*.local", "169.254/16"],
        ])
        XCTAssertTrue(snapshot.isActive)
        XCTAssertTrue(snapshot.httpActive)
        XCTAssertFalse(snapshot.socksActive)
        XCTAssertFalse(snapshot.pacActive)
        XCTAssertTrue(snapshot.autoDiscovery)
        XCTAssertEqual(snapshot.summary, "127.0.0.1:7890")
        let profile = Profile(name: "本机", color: "#16a34a", kind: .http, host: "127.0.0.1", port: 7890)
        XCTAssertTrue(snapshot.matches(profile))
        let other = Profile(name: "其他", color: "#16a34a", kind: .http, host: "127.0.0.1", port: 8080)
        XCTAssertFalse(snapshot.matches(other))
        let socks = Profile(name: "SOCKS", color: "#16a34a", kind: .socks5, host: "127.0.0.1", port: 7891)
        XCTAssertFalse(snapshot.matches(socks))
        XCTAssertEqual(snapshot.asProfile(name: "系统代理")?.serverAddress, "127.0.0.1:7890")
        XCTAssertFalse(ProxySnapshot(dictionary: [:]).isActive)
        XCTAssertEqual(ProxySnapshot(dictionary: [:]).summary, "未开启")
    }

    func testPacMatchIgnoresCase() {
        let snapshot = ProxySnapshot(dictionary: ["ProxyAutoConfigEnable": 1, "ProxyAutoConfigURLString": "http://127.0.0.1:7890/Proxy.pac"])
        let profile = Profile(name: "PAC", color: "#000", kind: .pac, pacURL: "http://127.0.0.1:7890/proxy.pac")
        XCTAssertTrue(snapshot.matches(profile))
        XCTAssertEqual(snapshot.summary, "PAC http://127.0.0.1:7890/Proxy.pac")
    }
}

final class NpmProxyTests: XCTestCase {
    func testUpdate() {
        let content = "registry=https://registry.npmmirror.com\nproxy=http://old:1\nhttps-proxy=http://old:1\n"
        let updated = NpmProxy.update(content, proxyURL: "http://127.0.0.1:7890", noProxy: "localhost")
        XCTAssertEqual(updated, "registry=https://registry.npmmirror.com\nproxy=http://127.0.0.1:7890\nhttps-proxy=http://127.0.0.1:7890\nnoproxy=localhost\n")
        XCTAssertEqual(NpmProxy.update(updated, proxyURL: "", noProxy: ""), "registry=https://registry.npmmirror.com\n")
        XCTAssertEqual(NpmProxy.update("", proxyURL: "", noProxy: ""), "")
    }
}

final class TerminalCommandsTests: XCTestCase {
    func testExportAndFish() {
        let export = TerminalCommands.export(proxyURL: "http://127.0.0.1:7890", noProxy: "it's")
        XCTAssertTrue(export.hasPrefix("export http_proxy='http://127.0.0.1:7890' https_proxy='http://127.0.0.1:7890' no_proxy='it'\\''s' HTTP_PROXY="))
        let fish = TerminalCommands.fish(proxyURL: "socks5://127.0.0.1:1080", noProxy: "")
        XCTAssertTrue(fish.hasPrefix("set -gx http_proxy 'socks5://127.0.0.1:1080'; set -gx HTTP_PROXY 'socks5://127.0.0.1:1080'; "))
        XCTAssertTrue(fish.contains("set -gx no_proxy '\(Profile.defaultNoProxy)'"))
    }
}

final class ParsingTests: XCTestCase {
    func testLsof() {
        let output = "p512\ncClashX\nf23\nn*:7890\nf24\nn127.0.0.1:7891\np9000\ncnode\nf18\nn[::1]:3000\n"
        let listeners = LocalProxyDetector.parseLsof(output)
        XCTAssertEqual(listeners, [
            LocalProxyDetector.Listener(port: 7890, process: "ClashX"),
            LocalProxyDetector.Listener(port: 7891, process: "ClashX"),
            LocalProxyDetector.Listener(port: 3000, process: "node"),
        ])
    }

    func testURLCommands() {
        XCTAssertEqual(URLCommand.parse(URL(string: "proxyswitch://toggle")!), .toggle)
        XCTAssertEqual(URLCommand.parse(URL(string: "proxyswitch://on")!), .turnOn)
        XCTAssertEqual(URLCommand.parse(URL(string: "proxyswitch://off")!), .turnOff)
        XCTAssertEqual(URLCommand.parse(URL(string: "proxyswitch://settings")!), .settings)
        XCTAssertEqual(URLCommand.parse(URL(string: "proxyswitch://panel")!), .panel)
        XCTAssertEqual(URLCommand.parse(URL(string: "proxyswitch://use?name=%E5%85%AC%E5%8F%B8")!), .use("公司"))
        XCTAssertEqual(URLCommand.parse(URL(string: "proxyswitch://use/home")!), .use("home"))
        XCTAssertNil(URLCommand.parse(URL(string: "proxyswitch://nope")!))
        XCTAssertNil(URLCommand.parse(URL(string: "https://example.com/toggle")!))
    }

    func testServiceSelection() {
        let services = [
            NetworkServices.Service(name: "Wi-Fi", bsdName: "en0", enabled: true),
            NetworkServices.Service(name: "Thunderbolt Bridge", bsdName: "bridge0", enabled: true),
            NetworkServices.Service(name: "Bluetooth PAN", bsdName: "en3", enabled: false),
            NetworkServices.Service(name: "VPN", bsdName: nil, enabled: true),
        ]
        XCTAssertEqual(NetworkServices.select(services: services) { $0 == "en0" || $0 == "en3" }, ["Wi-Fi"])
        XCTAssertEqual(NetworkServices.select(services: services) { _ in false }, ["Wi-Fi", "Thunderbolt Bridge", "VPN"])
    }

    func testVersionCompare() {
        XCTAssertTrue(UpdateChecker.isNewer("1.2.0", than: "1.1.9"))
        XCTAssertTrue(UpdateChecker.isNewer("1.2", than: "1.1.9"))
        XCTAssertFalse(UpdateChecker.isNewer("1.1.9", than: "1.1.9"))
        XCTAssertFalse(UpdateChecker.isNewer("0.9", than: "1.0"))
    }

    func testProfileValidation() {
        var profile = Profile(name: "x", color: "#000", kind: .http, host: "127.0.0.1", port: 7890)
        XCTAssertNil(profile.validate())
        profile.port = 70000
        XCTAssertNotNil(profile.validate())
        profile.port = 80
        profile.host = "a b"
        XCTAssertNotNil(profile.validate())
        var pac = Profile(name: "p", color: "#000", kind: .pac, pacURL: "ftp://x")
        XCTAssertNotNil(pac.validate())
        pac.pacURL = "http://127.0.0.1/proxy.pac"
        XCTAssertNil(pac.validate())
        var empty = Profile(name: "e", color: "#000")
        empty.targets = []
        XCTAssertNotNil(empty.validate())
    }

    func testConfigRoundTrip() throws {
        var config = AppConfig()
        config.profiles = [Profile(name: "a", color: "#111111", kind: .socks5, host: "h", port: 1)]
        config.toggleHotkey = nil
        let data = try JSONEncoder().encode(config)
        let decoded = try JSONDecoder().decode(AppConfig.self, from: data)
        XCTAssertEqual(decoded, config)
        XCTAssertNil(decoded.toggleHotkey)
        // 缺少 toggleHotkey 键时用默认快捷键。
        let minimal = try JSONDecoder().decode(AppConfig.self, from: Data("{}".utf8))
        XCTAssertEqual(minimal.toggleHotkey, HotkeyBinding.defaultToggle)
    }
}
