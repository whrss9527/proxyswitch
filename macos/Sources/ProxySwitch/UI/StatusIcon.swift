import AppKit
import SwiftUI

/// 菜单栏图标的状态。
enum StatusIconState: Equatable {
    case off
    case on(NSColor)
    case external
    case warning(NSColor)
    case error
}

/// 菜单栏图标：一个“开关”造型，圆角轨道加圆形滑块，滑块在右表示开启。关闭时是模板图，跟随菜单栏的深浅色。
enum StatusIcon {
    static let amber = NSColor(red: 0xd9 / 255, green: 0x77 / 255, blue: 0x06 / 255, alpha: 1)
    static let red = NSColor(red: 0xdc / 255, green: 0x26 / 255, blue: 0x26 / 255, alpha: 1)

    static func image(for state: StatusIconState) -> NSImage {
        let size = NSSize(width: 24, height: 14)
        let image = NSImage(size: size, flipped: false) { rect in
            draw(state, in: rect)
            return true
        }
        image.isTemplate = state == .off
        return image
    }

    private static func draw(_ state: StatusIconState, in rect: NSRect) {
        let track = rect.insetBy(dx: 0.5, dy: 0.5)
        let radius = track.height / 2
        let trackPath = NSBezierPath(roundedRect: track, xRadius: radius, yRadius: radius)
        let knobInset: CGFloat = 2.5
        let knobDiameter = track.height - knobInset * 2
        func knobRect(right: Bool) -> NSRect {
            let x = right ? track.maxX - knobInset - knobDiameter : track.minX + knobInset
            return NSRect(x: x, y: track.minY + knobInset, width: knobDiameter, height: knobDiameter)
        }
        switch state {
        case .off:
            NSColor.black.setFill()
            trackPath.fill()
            // 模板图只看透明度：把滑块“挖空”，得到一个空心的滑块。
            NSGraphicsContext.current?.compositingOperation = .destinationOut
            NSBezierPath(ovalIn: knobRect(right: false)).fill()
        case .on(let color):
            color.setFill()
            trackPath.fill()
            NSColor.white.setFill()
            NSBezierPath(ovalIn: knobRect(right: true)).fill()
        case .external:
            amber.setFill()
            trackPath.fill()
            NSColor.white.setFill()
            NSBezierPath(ovalIn: knobRect(right: true)).fill()
        case .warning(let color):
            color.setFill()
            trackPath.fill()
            NSColor.white.setFill()
            NSBezierPath(ovalIn: knobRect(right: true)).fill()
            red.setFill()
            NSBezierPath(ovalIn: knobRect(right: true).insetBy(dx: 1.5, dy: 1.5)).fill()
        case .error:
            red.setFill()
            trackPath.fill()
            NSColor.white.setFill()
            NSBezierPath(ovalIn: knobRect(right: false)).fill()
        }
    }
}

extension NSColor {
    /// 解析 #rrggbb。
    convenience init(hex: String) {
        var text = hex.trimmingCharacters(in: .whitespaces)
        if text.hasPrefix("#") { text.removeFirst() }
        var value: UInt64 = 0
        Scanner(string: text).scanHexInt64(&value)
        let red = CGFloat((value >> 16) & 0xff) / 255
        let green = CGFloat((value >> 8) & 0xff) / 255
        let blue = CGFloat(value & 0xff) / 255
        self.init(srgbRed: red, green: green, blue: blue, alpha: 1)
    }
}

extension Color {
    init(hex: String) {
        self.init(nsColor: NSColor(hex: hex))
    }
}
