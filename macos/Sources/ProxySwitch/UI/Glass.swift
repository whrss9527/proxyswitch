import AppKit
import SwiftUI

/// AppKit 的毛玻璃视图，SwiftUI 里当背景用。
struct VisualEffectView: NSViewRepresentable {
    var material: NSVisualEffectView.Material
    var blendingMode: NSVisualEffectView.BlendingMode = .behindWindow
    var isEmphasized = false

    func makeNSView(context: Context) -> NSVisualEffectView {
        let view = NSVisualEffectView()
        view.material = material
        view.blendingMode = blendingMode
        view.state = .active
        view.isEmphasized = isEmphasized
        return view
    }

    func updateNSView(_ view: NSVisualEffectView, context: Context) {
        view.material = material
        view.blendingMode = blendingMode
        view.isEmphasized = isEmphasized
    }
}

/// 面板的玻璃底：透过窗口看到后面的桌面和窗口（模糊），顶部一层高光，边缘一圈细线。
/// 用 Xcode 26 编译时，macOS 26 上换成系统的 Liquid Glass。
struct GlassPanelBackground: View {
    var cornerRadius: CGFloat = 20

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        ZStack {
            VisualEffectView(material: .hudWindow, blendingMode: .behindWindow)
            LinearGradient(colors: [Color.white.opacity(0.16), Color.white.opacity(0.03)], startPoint: .top, endPoint: .bottom)
        }
        .clipShape(shape)
        .overlay(shape.strokeBorder(Color.white.opacity(0.18), lineWidth: 1))
        .overlay(shape.strokeBorder(Color.black.opacity(0.18), lineWidth: 0.5).padding(-0.5))
    }
}

/// 卡片：半透明材质加细边，面板和设置页都用它。
struct GlassCard: ViewModifier {
    var cornerRadius: CGFloat = 14
    var prominent = false

    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        #if compiler(>=6.2)
        if #available(macOS 26, *) {
            return AnyView(content.glassEffect(.regular, in: shape))
        }
        #endif
        return AnyView(
            content
                .background(prominent ? AnyShapeStyle(.regularMaterial) : AnyShapeStyle(.thinMaterial), in: shape)
                .overlay(shape.strokeBorder(Color.primary.opacity(0.08), lineWidth: 1))
        )
    }
}

extension View {
    func glassCard(cornerRadius: CGFloat = 14, prominent: Bool = false) -> some View {
        modifier(GlassCard(cornerRadius: cornerRadius, prominent: prominent))
    }
}

/// 列表行的按钮样式：悬停时浅浅地亮起，按下时稍暗。
struct HoverRowStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        HoverRowBody(configuration: configuration)
    }

    private struct HoverRowBody: View {
        let configuration: ButtonStyleConfiguration
        @State private var hovering = false

        var body: some View {
            configuration.label
                .background(
                    RoundedRectangle(cornerRadius: 10, style: .continuous)
                        .fill(Color.primary.opacity(configuration.isPressed ? 0.12 : (hovering ? 0.07 : 0)))
                )
                .contentShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                .onHover { hovering = $0 }
                .animation(.easeOut(duration: 0.12), value: hovering)
        }
    }
}

/// 圆形的图标按钮。
struct IconButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        IconButtonBody(configuration: configuration)
    }

    private struct IconButtonBody: View {
        let configuration: ButtonStyleConfiguration
        @State private var hovering = false

        var body: some View {
            configuration.label
                .font(.system(size: 13, weight: .medium))
                .frame(width: 30, height: 30)
                .background(Circle().fill(Color.primary.opacity(configuration.isPressed ? 0.16 : (hovering ? 0.1 : 0.05))))
                .contentShape(Circle())
                .onHover { hovering = $0 }
                .animation(.easeOut(duration: 0.12), value: hovering)
        }
    }
}

/// 配置颜色的小圆点，带一圈柔光。
struct ColorDot: View {
    var hex: String
    var size: CGFloat = 10

    var body: some View {
        Circle()
            .fill(Color(hex: hex))
            .frame(width: size, height: size)
            .shadow(color: Color(hex: hex).opacity(0.6), radius: 3)
    }
}
