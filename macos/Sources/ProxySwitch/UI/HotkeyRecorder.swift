import AppKit
import SwiftUI

/// 快捷键录制框：点击后按下组合键。
struct HotkeyRecorder: NSViewRepresentable {
    @Binding var binding: HotkeyBinding?

    func makeNSView(context: Context) -> RecorderView {
        let view = RecorderView()
        view.onChange = { binding = $0 }
        view.binding = binding
        return view
    }

    func updateNSView(_ view: RecorderView, context: Context) {
        view.binding = binding
        view.onChange = { binding = $0 }
    }

    final class RecorderView: NSView {
        var onChange: ((HotkeyBinding?) -> Void)?
        var binding: HotkeyBinding? {
            didSet { needsDisplay = true }
        }
        private var recording = false {
            didSet { needsDisplay = true }
        }

        override var acceptsFirstResponder: Bool { true }

        override func becomeFirstResponder() -> Bool {
            recording = true
            return super.becomeFirstResponder()
        }

        override func resignFirstResponder() -> Bool {
            recording = false
            return super.resignFirstResponder()
        }

        override func mouseDown(with event: NSEvent) {
            window?.makeFirstResponder(self)
        }

        override func keyDown(with event: NSEvent) {
            guard recording else {
                super.keyDown(with: event)
                return
            }
            if event.keyCode == 53 { // Esc：取消
                window?.makeFirstResponder(nil)
                return
            }
            let modifiers = KeyNames.carbonModifiers(from: event.modifierFlags)
            guard modifiers != 0 else {
                NSSound.beep()
                return
            }
            let keyCode = UInt32(event.keyCode)
            let display = KeyNames.display(keyCode: keyCode, modifiers: modifiers)
            onChange?(HotkeyBinding(keyCode: keyCode, modifiers: modifiers, display: display))
            window?.makeFirstResponder(nil)
        }

        override func draw(_ dirtyRect: NSRect) {
            let rect = bounds.insetBy(dx: 0.5, dy: 0.5)
            let path = NSBezierPath(roundedRect: rect, xRadius: 7, yRadius: 7)
            (recording ? NSColor.controlAccentColor.withAlphaComponent(0.15) : NSColor.textBackgroundColor.withAlphaComponent(0.6)).setFill()
            path.fill()
            (recording ? NSColor.controlAccentColor : NSColor.separatorColor).setStroke()
            path.lineWidth = recording ? 1.5 : 1
            path.stroke()
            let text: String
            let color: NSColor
            if recording {
                text = "按下组合键…"
                color = .controlAccentColor
            } else if let binding {
                text = binding.display
                color = .labelColor
            } else {
                text = "点击设置"
                color = .secondaryLabelColor
            }
            let paragraph = NSMutableParagraphStyle()
            paragraph.alignment = .center
            let attributes: [NSAttributedString.Key: Any] = [
                .font: NSFont.systemFont(ofSize: 13, weight: .medium),
                .foregroundColor: color,
                .paragraphStyle: paragraph,
            ]
            let size = (text as NSString).size(withAttributes: attributes)
            let textRect = NSRect(x: 0, y: (bounds.height - size.height) / 2, width: bounds.width, height: size.height)
            (text as NSString).draw(in: textRect, withAttributes: attributes)
        }
    }
}
