import AppKit
import Carbon

/// 全局快捷键：Carbon 的 RegisterEventHotKey，不需要辅助功能权限。
final class HotkeyCenter {
    static let shared = HotkeyCenter()

    private var handlerRef: EventHandlerRef?
    private var registrations: [UInt32: (ref: EventHotKeyRef, action: () -> Void)] = [:]
    private let signature: OSType = 0x5078_5377 // 'PxSw'

    private init() {}

    private func installHandlerIfNeeded() {
        guard handlerRef == nil else { return }
        var eventType = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let status = InstallEventHandler(GetApplicationEventTarget(), { _, event, _ -> OSStatus in
            var hotKeyID = EventHotKeyID()
            let status = GetEventParameter(event, EventParamName(kEventParamDirectObject), EventParamType(typeEventHotKeyID), nil, MemoryLayout<EventHotKeyID>.size, nil, &hotKeyID)
            if status == noErr {
                HotkeyCenter.shared.fire(id: hotKeyID.id)
            }
            return noErr
        }, 1, &eventType, nil, &handlerRef)
        if status != noErr {
            Log.error("安装快捷键事件处理器失败：\(status)")
        }
    }

    private func fire(id: UInt32) {
        registrations[id]?.action()
    }

    /// 注册快捷键；已被其他程序占用时返回 false。
    @discardableResult
    func register(id: UInt32, binding: HotkeyBinding, action: @escaping () -> Void) -> Bool {
        installHandlerIfNeeded()
        unregister(id: id)
        var ref: EventHotKeyRef?
        let hotKeyID = EventHotKeyID(signature: signature, id: id)
        let status = RegisterEventHotKey(binding.keyCode, binding.modifiers, hotKeyID, GetApplicationEventTarget(), 0, &ref)
        guard status == noErr, let ref else {
            Log.error("注册快捷键 \(binding.display) 失败：\(status)")
            return false
        }
        registrations[id] = (ref, action)
        return true
    }

    func unregister(id: UInt32) {
        if let registration = registrations.removeValue(forKey: id) {
            UnregisterEventHotKey(registration.ref)
        }
    }
}

/// 键码和修饰键的名称，快捷键录制和显示用。
enum KeyNames {
    static let cmdKey = UInt32(Carbon.cmdKey)
    static let shiftKey = UInt32(Carbon.shiftKey)
    static let optionKey = UInt32(Carbon.optionKey)
    static let controlKey = UInt32(Carbon.controlKey)

    /// Carbon 键码（ANSI 布局）对应的显示名称。
    static let names: [UInt32: String] = [
        0x00: "A", 0x01: "S", 0x02: "D", 0x03: "F", 0x04: "H", 0x05: "G", 0x06: "Z", 0x07: "X", 0x08: "C", 0x09: "V",
        0x0B: "B", 0x0C: "Q", 0x0D: "W", 0x0E: "E", 0x0F: "R", 0x10: "Y", 0x11: "T", 0x12: "1", 0x13: "2", 0x14: "3",
        0x15: "4", 0x16: "6", 0x17: "5", 0x18: "=", 0x19: "9", 0x1A: "7", 0x1B: "-", 0x1C: "8", 0x1D: "0", 0x1E: "]",
        0x1F: "O", 0x20: "U", 0x21: "[", 0x22: "I", 0x23: "P", 0x24: "↩", 0x25: "L", 0x26: "J", 0x27: "'", 0x28: "K",
        0x29: ";", 0x2A: "\\", 0x2B: ",", 0x2C: "/", 0x2D: "N", 0x2E: "M", 0x2F: ".", 0x30: "⇥", 0x31: "Space", 0x32: "`",
        0x33: "⌫", 0x35: "⎋", 0x40: "F17", 0x4F: "F18", 0x50: "F19", 0x5A: "F20", 0x60: "F5", 0x61: "F6", 0x62: "F7", 0x63: "F3",
        0x64: "F8", 0x65: "F9", 0x67: "F11", 0x69: "F13", 0x6A: "F16", 0x6B: "F14", 0x6D: "F10", 0x6F: "F12", 0x71: "F15",
        0x72: "Help", 0x73: "↖", 0x74: "⇞", 0x75: "⌦", 0x76: "F4", 0x77: "↘", 0x78: "F2", 0x79: "⇟", 0x7A: "F1", 0x7B: "←",
        0x7C: "→", 0x7D: "↓", 0x7E: "↑",
    ]

    static func name(forKeyCode keyCode: UInt32) -> String {
        names[keyCode] ?? "键 \(keyCode)"
    }

    /// AppKit 的修饰键转成 Carbon 的位。
    static func carbonModifiers(from flags: NSEvent.ModifierFlags) -> UInt32 {
        var modifiers: UInt32 = 0
        if flags.contains(.control) { modifiers |= controlKey }
        if flags.contains(.option) { modifiers |= optionKey }
        if flags.contains(.shift) { modifiers |= shiftKey }
        if flags.contains(.command) { modifiers |= cmdKey }
        return modifiers
    }

    /// 显示用的文字，例如 ⌃⌥P。
    static func display(keyCode: UInt32, modifiers: UInt32) -> String {
        var text = ""
        if modifiers & controlKey != 0 { text += "⌃" }
        if modifiers & optionKey != 0 { text += "⌥" }
        if modifiers & shiftKey != 0 { text += "⇧" }
        if modifiers & cmdKey != 0 { text += "⌘" }
        return text + name(forKeyCode: keyCode)
    }
}
