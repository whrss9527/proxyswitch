import Foundation

/// 环境变量 HTTP_PROXY / HTTPS_PROXY / NO_PROXY 写到用户的 launchd 环境（launchctl setenv），
/// 之后由 launchd 启动的程序（新打开的终端、图形程序）都能看到；已经打开的终端需要重开，或者用复制的终端命令。
/// 大小写两种都设置：curl 等只认小写的 http_proxy。
enum EnvironmentProxy {
    static let launchctlPath = "/bin/launchctl"
    static let names = ["HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"]

    static func set(proxyURL: String, noProxy: String) async throws {
        for name in ["HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"] {
            try await setenv(name, proxyURL)
        }
        let trimmed = noProxy.trimmingCharacters(in: .whitespaces)
        for name in ["NO_PROXY", "no_proxy"] {
            if trimmed.isEmpty {
                try await unsetenv(name)
            } else {
                try await setenv(name, trimmed)
            }
        }
    }

    static func clear() async throws {
        for name in ["HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"] {
            try await unsetenv(name)
        }
    }

    /// 当前 launchd 环境里的值，诊断显示用。
    static func current() async -> [String: String] {
        var values: [String: String] = [:]
        for name in names {
            let result = try? await Shell.run(launchctlPath, ["getenv", name], timeout: 10)
            values[name] = result?.trimmedOutput ?? ""
        }
        return values
    }

    private static func setenv(_ name: String, _ value: String) async throws {
        let result = try await Shell.run(launchctlPath, ["setenv", name, value], timeout: 10)
        if !result.succeeded {
            throw SystemProxyError.command("launchctl setenv \(name) 失败：\(result.trimmedOutput)")
        }
    }

    private static func unsetenv(_ name: String) async throws {
        let result = try await Shell.run(launchctlPath, ["unsetenv", name], timeout: 10)
        if !result.succeeded {
            throw SystemProxyError.command("launchctl unsetenv \(name) 失败：\(result.trimmedOutput)")
        }
    }
}

/// git 全局代理：调用 git 本身修改 ~/.gitconfig。
enum GitProxy {
    static var gitPath: String? { Shell.lookPath("git") }

    static func set(proxyURL: String) async throws {
        guard let git = gitPath else { throw SystemProxyError.command("没有找到 git") }
        for key in ["http.proxy", "https.proxy"] {
            let result = try await Shell.run(git, ["config", "--global", key, proxyURL])
            if !result.succeeded {
                throw SystemProxyError.command("git config \(key) 失败：\(result.trimmedOutput)")
            }
        }
    }

    /// 清除；没装 git 时不算错误。
    static func clear() async throws {
        guard let git = gitPath else { return }
        for key in ["http.proxy", "https.proxy"] {
            let result = try await Shell.run(git, ["config", "--global", "--unset-all", key])
            // 退出码 5 表示这个键本来就不存在。
            if !result.succeeded && result.status != 5 {
                throw SystemProxyError.command("git config --unset \(key) 失败：\(result.trimmedOutput)")
            }
        }
    }

    static func current() async -> String {
        guard let git = gitPath else { return "" }
        let result = try? await Shell.run(git, ["config", "--global", "--get", "http.proxy"], timeout: 10)
        return result?.trimmedOutput ?? ""
    }
}

/// npm / pnpm 的代理：改用户目录的 .npmrc。
enum NpmProxy {
    static var path: String {
        (NSHomeDirectory() as NSString).appendingPathComponent(".npmrc")
    }

    static func set(proxyURL: String, noProxy: String) throws {
        try write(update(read(), proxyURL: proxyURL, noProxy: noProxy))
    }

    static func clear() throws {
        try write(update(read(), proxyURL: "", noProxy: ""))
    }

    static func current() -> [String: String] {
        var values: [String: String] = [:]
        for line in read().split(separator: "\n") {
            let parts = line.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            if parts.count == 2, ["proxy", "https-proxy", "noproxy"].contains(parts[0]) {
                values[parts[0]] = parts[1]
            }
        }
        return values
    }

    /// 更新 .npmrc 的文本：proxy、https-proxy、noproxy 三项改成新值（空值表示删除），其余行原样保留。
    static func update(_ content: String, proxyURL: String, noProxy: String) -> String {
        let wanted: [(String, String)] = [("proxy", proxyURL), ("https-proxy", proxyURL), ("noproxy", noProxy)]
        var lines = content.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        if lines.last == "" { lines.removeLast() }
        var kept: [String] = []
        for line in lines {
            let key = line.split(separator: "=", maxSplits: 1).first.map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
            if wanted.contains(where: { $0.0 == key }) {
                continue
            }
            kept.append(line)
        }
        for (key, value) in wanted where !value.isEmpty {
            kept.append("\(key)=\(value)")
        }
        return kept.isEmpty ? "" : kept.joined(separator: "\n") + "\n"
    }

    private static func read() -> String {
        (try? String(contentsOfFile: path, encoding: .utf8)) ?? ""
    }

    private static func write(_ content: String) throws {
        try content.write(toFile: path, atomically: true, encoding: .utf8)
    }
}
