import Foundation

/// 左键点击菜单栏图标的动作。
enum ClickAction: String, Codable, CaseIterable, Identifiable {
    case panel
    case toggle

    var id: String { rawValue }

    var title: String {
        switch self {
        case .panel: return "打开面板"
        case .toggle: return "直接开关代理"
        }
    }
}

/// 关闭代理时系统代理怎么处理。
enum OffMode: String, Codable, CaseIterable, Identifiable {
    case direct
    case restore

    var id: String { rawValue }

    var title: String {
        switch self {
        case .direct: return "直接连接"
        case .restore: return "恢复开启前的设置"
        }
    }
}

enum NotifyLevel: String, Codable, CaseIterable, Identifiable {
    case all
    case problems
    case none

    var id: String { rawValue }

    var title: String {
        switch self {
        case .all: return "全部显示"
        case .problems: return "只显示问题"
        case .none: return "不显示"
        }
    }
}

/// 全局快捷键：Carbon 的键码和修饰键位，display 是显示用的文字（⌃⌥P）。
struct HotkeyBinding: Codable, Equatable {
    var keyCode: UInt32
    var modifiers: UInt32
    var display: String

    /// 默认 ⌃⌥P（P 的 Carbon 键码 0x23）。
    static let defaultToggle = HotkeyBinding(keyCode: 0x23, modifiers: KeyNames.controlKey | KeyNames.optionKey, display: "⌃⌥P")
}

struct AppConfig: Codable, Equatable {
    static let defaultTestURL = "https://cp.cloudflare.com/generate_204"

    var profiles: [Profile] = []
    var clickAction: ClickAction = .panel
    var toggleHotkey: HotkeyBinding? = HotkeyBinding.defaultToggle
    var offMode: OffMode = .direct
    var notifyLevel: NotifyLevel = .all
    var healthCheck: Bool = true
    var disableOnExit: Bool = false
    var testURL: String = AppConfig.defaultTestURL

    init() {}

    private enum CodingKeys: String, CodingKey {
        case profiles, clickAction, toggleHotkey, offMode, notifyLevel, healthCheck, disableOnExit, testURL
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        profiles = try container.decodeIfPresent([Profile].self, forKey: .profiles) ?? []
        clickAction = try container.decodeIfPresent(ClickAction.self, forKey: .clickAction) ?? .panel
        if container.contains(.toggleHotkey) {
            toggleHotkey = try container.decodeIfPresent(HotkeyBinding.self, forKey: .toggleHotkey)
        } else {
            toggleHotkey = HotkeyBinding.defaultToggle
        }
        offMode = try container.decodeIfPresent(OffMode.self, forKey: .offMode) ?? .direct
        notifyLevel = try container.decodeIfPresent(NotifyLevel.self, forKey: .notifyLevel) ?? .all
        healthCheck = try container.decodeIfPresent(Bool.self, forKey: .healthCheck) ?? true
        disableOnExit = try container.decodeIfPresent(Bool.self, forKey: .disableOnExit) ?? false
        testURL = try container.decodeIfPresent(String.self, forKey: .testURL) ?? AppConfig.defaultTestURL
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(profiles, forKey: .profiles)
        try container.encode(clickAction, forKey: .clickAction)
        // 明确写出 null，表示用户关掉了快捷键（缺少这个键时用默认值）。
        try container.encode(toggleHotkey, forKey: .toggleHotkey)
        try container.encode(offMode, forKey: .offMode)
        try container.encode(notifyLevel, forKey: .notifyLevel)
        try container.encode(healthCheck, forKey: .healthCheck)
        try container.encode(disableOnExit, forKey: .disableOnExit)
        try container.encode(testURL, forKey: .testURL)
    }

    func profile(id: UUID?) -> Profile? {
        guard let id else { return nil }
        return profiles.first { $0.id == id }
    }
}

/// 运行状态：上次使用的配置、是否由本程序开启、开启前的系统代理快照（关闭时恢复用）。
struct PersistedState: Codable, Equatable {
    var lastProfileID: UUID?
    var enabledByUs: Bool = false
    var original: ProxySnapshot?

    init() {}

    private enum CodingKeys: String, CodingKey {
        case lastProfileID, enabledByUs, original
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        lastProfileID = try container.decodeIfPresent(UUID.self, forKey: .lastProfileID)
        enabledByUs = try container.decodeIfPresent(Bool.self, forKey: .enabledByUs) ?? false
        original = try container.decodeIfPresent(ProxySnapshot.self, forKey: .original)
    }
}
