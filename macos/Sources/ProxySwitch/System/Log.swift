import Foundation
import os

/// 日志写到系统的统一日志（Console.app 里按 subsystem 过滤），同时追加到 Application Support 里的 proxyswitch.log。
enum Log {
    private static let logger = Logger(subsystem: "com.whrss9527.proxyswitch", category: "app")
    private static let queue = DispatchQueue(label: "com.whrss9527.proxyswitch.log")
    private static let formatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd HH:mm:ss"
        return formatter
    }()

    static var fileURL: URL { Store.directory.appendingPathComponent("proxyswitch.log") }

    static func info(_ message: String) {
        logger.info("\(message, privacy: .public)")
        append("INFO", message)
    }

    static func error(_ message: String) {
        logger.error("\(message, privacy: .public)")
        append("ERROR", message)
    }

    private static func append(_ level: String, _ message: String) {
        let line = "\(formatter.string(from: Date())) \(level) \(message)\n"
        queue.async {
            let url = fileURL
            try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            if let attributes = try? FileManager.default.attributesOfItem(atPath: url.path), let size = attributes[.size] as? Int, size > 1 << 20 {
                try? FileManager.default.removeItem(at: url)
            }
            if let handle = try? FileHandle(forWritingTo: url) {
                handle.seekToEndOfFile()
                handle.write(Data(line.utf8))
                try? handle.close()
            } else {
                try? line.write(to: url, atomically: true, encoding: .utf8)
            }
        }
    }

    /// 日志文件最后几行，诊断页显示用。
    static func tail(lines maxLines: Int = 200) -> String {
        guard let content = try? String(contentsOf: fileURL, encoding: .utf8) else { return "" }
        let lines = content.split(separator: "\n").suffix(maxLines)
        return lines.joined(separator: "\n")
    }
}

/// 配置和状态文件：~/Library/Application Support/ProxySwitch/。
enum Store {
    static var directory: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first ?? URL(fileURLWithPath: NSHomeDirectory())
        return base.appendingPathComponent("ProxySwitch", isDirectory: true)
    }

    static var configURL: URL { directory.appendingPathComponent("config.json") }
    static var stateURL: URL { directory.appendingPathComponent("state.json") }

    static func loadConfig() -> AppConfig? {
        load(AppConfig.self, from: configURL)
    }

    static func loadState() -> PersistedState {
        load(PersistedState.self, from: stateURL) ?? PersistedState()
    }

    static func save(_ config: AppConfig) {
        save(config, to: configURL)
    }

    static func save(_ state: PersistedState) {
        save(state, to: stateURL)
    }

    private static func load<T: Decodable>(_ type: T.Type, from url: URL) -> T? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        do {
            return try JSONDecoder().decode(type, from: data)
        } catch {
            Log.error("读取 \(url.lastPathComponent) 失败：\(error)")
            return nil
        }
    }

    private static func save<T: Encodable>(_ value: T, to url: URL) {
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            try encoder.encode(value).write(to: url, options: .atomic)
        } catch {
            Log.error("保存 \(url.lastPathComponent) 失败：\(error)")
        }
    }
}
