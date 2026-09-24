import Foundation

struct ShellResult {
    let output: String
    let status: Int32

    var succeeded: Bool { status == 0 }
    var trimmedOutput: String { output.trimmingCharacters(in: .whitespacesAndNewlines) }
}

enum ShellError: LocalizedError {
    case timeout(String)
    case launchFailed(String, String)

    var errorDescription: String? {
        switch self {
        case .timeout(let name): return "\(name) 执行超时"
        case .launchFailed(let name, let reason): return "无法运行 \(name)：\(reason)"
        }
    }
}

/// 运行系统命令。所有调用都在后台线程完成，不阻塞界面。
enum Shell {
    static func run(_ executable: String, _ arguments: [String], timeout: TimeInterval = 20) async throws -> ShellResult {
        try await Task.detached(priority: .userInitiated) {
            try runSync(executable, arguments, timeout: timeout)
        }.value
    }

    static func runSync(_ executable: String, _ arguments: [String], timeout: TimeInterval = 20) throws -> ShellResult {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        process.standardInput = FileHandle.nullDevice
        do {
            try process.run()
        } catch {
            throw ShellError.launchFailed((executable as NSString).lastPathComponent, error.localizedDescription)
        }
        let timer = DispatchWorkItem { [process] in
            if process.isRunning {
                process.terminate()
            }
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + timeout, execute: timer)
        // 先读完输出再等待结束，否则管道写满时进程会卡住。
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        let timedOut = timer.isCancelled == false && process.terminationReason == .uncaughtSignal
        timer.cancel()
        if timedOut {
            throw ShellError.timeout((executable as NSString).lastPathComponent)
        }
        return ShellResult(output: String(decoding: data, as: UTF8.self), status: process.terminationStatus)
    }

    /// 找到 PATH 里的命令；图形程序的 PATH 很短，补上常见的位置。
    static func lookPath(_ name: String) -> String? {
        var directories = (ProcessInfo.processInfo.environment["PATH"] ?? "").split(separator: ":").map(String.init)
        directories += ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"]
        for directory in directories {
            let path = (directory as NSString).appendingPathComponent(name)
            if FileManager.default.isExecutableFile(atPath: path) {
                return path
            }
        }
        return nil
    }

    /// 把文字写成 AppleScript 的字符串字面量。
    static func appleScriptString(_ text: String) -> String {
        "\"" + text.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"") + "\""
    }

    /// 把参数写成 shell 的单引号字面量。
    static func shellQuote(_ text: String) -> String {
        "'" + text.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
