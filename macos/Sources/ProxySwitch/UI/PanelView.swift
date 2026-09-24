import AppKit
import SwiftUI

struct PanelActions {
    var openSettings: (SettingsPage?) -> Void
    var close: () -> Void
    var quit: () -> Void
}

/// 菜单栏面板：状态卡片和大开关、配置列表、快捷操作。
struct PanelView: View {
    @ObservedObject var state: AppState
    let actions: PanelActions
    @State private var testing = false
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            statusCard
            if case .external = state.status {
                externalCard
            }
            if !state.config.profiles.isEmpty {
                profileList
            } else {
                emptyCard
            }
            if let error = state.lastError {
                errorCard(error)
            }
            footer
        }
        .padding(12)
        .frame(width: 320)
        .background(GlassPanelBackground())
        .padding(8)
    }

    // MARK: - 状态

    private var isOn: Binding<Bool> {
        Binding(get: { state.status.isOn }, set: { _ in state.toggle() })
    }

    private var statusCard: some View {
        HStack(spacing: 12) {
            StatusBadge(status: state.status, health: state.health)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.system(size: 14, weight: .semibold))
                    .lineLimit(1)
                Text(subtitle)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer(minLength: 4)
            if state.busy {
                ProgressView()
                    .controlSize(.small)
            } else {
                Toggle("", isOn: isOn)
                    .toggleStyle(.switch)
                    .labelsHidden()
                    .disabled(state.config.profiles.isEmpty && !state.status.isOn && !isExternal)
            }
        }
        .padding(12)
        .glassCard(prominent: true)
    }

    private var isExternal: Bool {
        if case .external = state.status { return true }
        return false
    }

    private var title: String {
        switch state.status {
        case .on(let profile): return "已开启 · \(profile.name)"
        case .external: return "系统代理由其他程序设置"
        case .off(let next): return next == nil ? "还没有代理配置" : "代理已关闭"
        }
    }

    private var subtitle: String {
        switch state.status {
        case .on(let profile):
            if state.health == .down { return "代理服务器连不上" }
            if let result = state.testResults[profile.id], result.ok, let latency = result.latencyMs {
                return "\(profile.summary) · \(latency) ms"
            }
            return profile.summary
        case .external(let description):
            return description
        case .off(let next):
            if let next { return "下次开启：\(next.name)（\(next.summary)）" }
            return "在设置里添加一个代理配置"
        }
    }

    private var externalCard: some View {
        HStack(spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
            Text("别的程序改了系统代理，可以保存成配置以后一键切换")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
            Spacer()
            Button("保存") { state.saveExternalAsProfile() }
                .controlSize(.small)
        }
        .padding(10)
        .glassCard()
    }

    // MARK: - 配置列表

    private var profileList: some View {
        VStack(spacing: 2) {
            ForEach(state.config.profiles) { profile in
                Button {
                    state.use(profile)
                } label: {
                    ProfileRow(profile: profile, active: isActive(profile), result: state.testResults[profile.id])
                }
                .buttonStyle(HoverRowStyle())
            }
        }
        .padding(6)
        .glassCard()
    }

    private func isActive(_ profile: Profile) -> Bool {
        if case .on(let current) = state.status { return current.id == profile.id }
        return false
    }

    private var emptyCard: some View {
        VStack(spacing: 8) {
            Image(systemName: "network.slash")
                .font(.system(size: 24))
                .foregroundStyle(.secondary)
            Text("还没有代理配置")
                .font(.system(size: 12, weight: .medium))
            Button("添加代理配置") { actions.openSettings(.profiles) }
                .controlSize(.small)
        }
        .frame(maxWidth: .infinity)
        .padding(16)
        .glassCard()
    }

    private func errorCard(_ text: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "xmark.octagon.fill")
                .foregroundStyle(.red)
            Text(text)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .lineLimit(3)
            Spacer(minLength: 0)
            Button {
                state.lastError = nil
            } label: {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
        }
        .padding(10)
        .glassCard()
    }

    // MARK: - 底部操作

    private var footer: some View {
        HStack(spacing: 6) {
            Button {
                testing = true
                Task { @MainActor in
                    await state.testAll()
                    testing = false
                }
            } label: {
                if testing {
                    ProgressView().controlSize(.mini)
                } else {
                    Image(systemName: "gauge.with.needle")
                }
            }
            .buttonStyle(IconButtonStyle())
            .help("测试全部配置的连接和延迟")
            .disabled(state.config.profiles.isEmpty || testing)

            if case .on(let profile) = state.status, profile.kind != .pac {
                Menu {
                    Button("zsh / bash（终端、iTerm）") { copy(TerminalCommands.export(proxyURL: profile.proxyURL, noProxy: profile.noProxy)) }
                    Button("fish") { copy(TerminalCommands.fish(proxyURL: profile.proxyURL, noProxy: profile.noProxy)) }
                } label: {
                    Image(systemName: copied ? "checkmark" : "terminal")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .frame(width: 30, height: 30)
                .background(Circle().fill(Color.primary.opacity(0.05)))
                .help("复制在当前终端里使用代理的命令")
            }

            Spacer()

            if let release = state.latestRelease {
                Button {
                    NSWorkspace.shared.open(release.url)
                } label: {
                    Label("新版本 \(release.version)", systemImage: "arrow.down.circle")
                        .font(.system(size: 11))
                }
                .buttonStyle(.plain)
                .foregroundStyle(Color.accentColor)
            } else if let hotkey = state.config.toggleHotkey {
                Text("\(hotkey.display) 开关")
                    .font(.system(size: 11))
                    .foregroundStyle(.tertiary)
            }

            Button {
                actions.openSettings(nil)
            } label: {
                Image(systemName: "gearshape")
            }
            .buttonStyle(IconButtonStyle())
            .help("设置")

            Button {
                actions.quit()
            } label: {
                Image(systemName: "power")
            }
            .buttonStyle(IconButtonStyle())
            .help("退出 ProxySwitch")
        }
        .padding(.horizontal, 2)
    }

    private func copy(_ text: String) {
        TerminalCommands.copy(text)
        copied = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
    }
}

/// 状态卡片左侧的圆形图标。
struct StatusBadge: View {
    var status: ProxyStatus
    var health: Health

    var body: some View {
        ZStack {
            Circle()
                .fill(color.opacity(0.18))
            Circle()
                .strokeBorder(color.opacity(0.5), lineWidth: 1)
            Image(systemName: symbol)
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(color)
        }
        .frame(width: 40, height: 40)
    }

    private var color: Color {
        switch status {
        case .on(let profile): return health == .down ? .red : Color(hex: profile.color)
        case .external: return .orange
        case .off: return .secondary
        }
    }

    private var symbol: String {
        switch status {
        case .on: return health == .down ? "exclamationmark.triangle.fill" : "checkmark.shield.fill"
        case .external: return "questionmark.circle.fill"
        case .off: return "power"
        }
    }
}

struct ProfileRow: View {
    var profile: Profile
    var active: Bool
    var result: TestResult?

    var body: some View {
        HStack(spacing: 10) {
            ColorDot(hex: profile.color)
            VStack(alignment: .leading, spacing: 1) {
                Text(profile.name)
                    .font(.system(size: 12, weight: active ? .semibold : .regular))
                    .lineLimit(1)
                Text(profile.summary)
                    .font(.system(size: 10))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer(minLength: 4)
            if let result {
                Text(result.latencyText)
                    .font(.system(size: 10, weight: .medium, design: .rounded))
                    .foregroundStyle(result.ok ? Color.green : Color.red)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(Capsule().fill((result.ok ? Color.green : Color.red).opacity(0.12)))
            }
            Image(systemName: active ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(active ? Color(hex: profile.color) : Color.secondary.opacity(0.5))
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .contentShape(Rectangle())
    }
}
