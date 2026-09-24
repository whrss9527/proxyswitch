import SwiftUI

/// 代理配置页：左边是配置列表，右边编辑选中的配置。
struct ProfilesPage: View {
    @ObservedObject var state: AppState
    @ObservedObject var navigation: SettingsNavigation
    @State private var showDetect = false

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: "代理配置", subtitle: "每套配置可以设置系统代理、环境变量、git 和 npm，在菜单栏里一键切换")
            HStack(alignment: .top, spacing: 16) {
                profileList
                    .frame(width: 250)
                editor
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 24)
        }
        .sheet(isPresented: $showDetect) {
            DetectSheet(state: state) { detected in
                var profile = Profile(name: detected.suggestedName, color: ProfilePalette.color(at: state.config.profiles.count), kind: detected.kind, host: detected.host, port: detected.port)
                profile.name = uniqueName(profile.name)
                state.addProfile(profile)
                navigation.selectedProfileID = profile.id
            }
        }
        .onAppear {
            if navigation.selectedProfileID == nil {
                navigation.selectedProfileID = state.selectedProfile?.id
            }
        }
    }

    private var profileList: some View {
        VStack(spacing: 8) {
            VStack(spacing: 2) {
                if state.config.profiles.isEmpty {
                    Text("还没有配置")
                        .font(.system(size: 12))
                        .foregroundStyle(.secondary)
                        .padding(20)
                }
                ForEach(state.config.profiles) { profile in
                    Button {
                        navigation.selectedProfileID = profile.id
                    } label: {
                        HStack(spacing: 10) {
                            ColorDot(hex: profile.color)
                            VStack(alignment: .leading, spacing: 1) {
                                Text(profile.name)
                                    .font(.system(size: 12, weight: .medium))
                                    .lineLimit(1)
                                Text(profile.summary)
                                    .font(.system(size: 10))
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                            }
                            Spacer()
                            if case .on(let current) = state.status, current.id == profile.id {
                                Image(systemName: "checkmark.circle.fill")
                                    .foregroundStyle(Color(hex: profile.color))
                            }
                        }
                        .padding(.horizontal, 8)
                        .padding(.vertical, 7)
                        .contentShape(Rectangle())
                        .background(
                            RoundedRectangle(cornerRadius: 10, style: .continuous)
                                .fill(navigation.selectedProfileID == profile.id ? Color.accentColor.opacity(0.18) : Color.clear)
                        )
                    }
                    .buttonStyle(HoverRowStyle())
                }
            }
            .padding(6)
            .glassCard()
            HStack(spacing: 8) {
                Button {
                    var profile = Profile(name: uniqueName("新配置"), color: ProfilePalette.color(at: state.config.profiles.count))
                    profile.name = uniqueName("新配置")
                    state.addProfile(profile)
                    navigation.selectedProfileID = profile.id
                } label: {
                    Label("新建", systemImage: "plus")
                }
                Button {
                    showDetect = true
                } label: {
                    Label("自动检测", systemImage: "wand.and.stars")
                }
                .help("找出本机正在运行的代理软件")
            }
            .controlSize(.small)
            Spacer()
        }
    }

    @ViewBuilder
    private var editor: some View {
        if let id = navigation.selectedProfileID, let profile = state.config.profile(id: id) {
            ProfileEditor(state: state, profile: profile, onDelete: {
                state.remove(profile)
                navigation.selectedProfileID = state.config.profiles.first?.id
            })
            .id(profile.id)
        } else {
            VStack(spacing: 10) {
                Image(systemName: "slider.horizontal.3")
                    .font(.system(size: 28))
                    .foregroundStyle(.secondary)
                Text(state.config.profiles.isEmpty ? "点「新建」手动填写，或者「自动检测」找出本机的代理软件" : "在左边选择一个配置")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .glassCard()
        }
    }

    private func uniqueName(_ base: String) -> String {
        var name = base
        var index = 1
        while state.config.profiles.contains(where: { $0.name == name }) {
            index += 1
            name = "\(base) \(index)"
        }
        return name
    }
}

/// 编辑一套配置。改动先放在草稿里，点「保存」才生效。
struct ProfileEditor: View {
    @ObservedObject var state: AppState
    @State var draft: Profile
    let original: Profile
    let onDelete: () -> Void
    @State private var portText: String
    @State private var problem: String?
    @State private var testing = false
    @State private var result: TestResult?
    @State private var saved = false

    init(state: AppState, profile: Profile, onDelete: @escaping () -> Void) {
        self.state = state
        self.original = profile
        self.onDelete = onDelete
        _draft = State(initialValue: profile)
        _portText = State(initialValue: String(profile.port))
    }

    private var dirty: Bool { draft != original }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    TextField("名称", text: $draft.name)
                    HStack {
                        Text("颜色")
                        Spacer()
                        ForEach(ProfilePalette.colors, id: \.self) { hex in
                            Button {
                                draft.color = hex
                            } label: {
                                ZStack {
                                    Circle().fill(Color(hex: hex)).frame(width: 18, height: 18)
                                    if draft.color == hex {
                                        Image(systemName: "checkmark")
                                            .font(.system(size: 9, weight: .bold))
                                            .foregroundStyle(.white)
                                    }
                                }
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    Picker("类型", selection: $draft.kind) {
                        ForEach(ProxyKind.allCases) { kind in
                            Text(kind.title).tag(kind)
                        }
                    }
                    .pickerStyle(.segmented)
                }
                Section(draft.kind == .pac ? "PAC 脚本" : "代理服务器") {
                    if draft.kind == .pac {
                        TextField("PAC 地址", text: $draft.pacURL, prompt: Text("http://127.0.0.1:7890/proxy.pac"))
                    } else {
                        TextField("主机", text: $draft.host, prompt: Text("127.0.0.1"))
                        TextField("端口", text: $portText, prompt: Text("7890"))
                            .onChange(of: portText) { _, value in
                                draft.port = Int(value.trimmingCharacters(in: .whitespaces)) ?? 0
                            }
                    }
                }
                Section("生效范围") {
                    ForEach(ProxyTarget.allCases) { target in
                        Toggle(isOn: Binding(
                            get: { draft.targets.contains(target) },
                            set: { enabled in
                                if enabled { draft.targets.insert(target) } else { draft.targets.remove(target) }
                            }
                        )) {
                            VStack(alignment: .leading, spacing: 1) {
                                Text(target.title)
                                Text(target.detail)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .disabled(target != .system && !draft.supportsNonSystemTargets)
                    }
                    if !draft.supportsNonSystemTargets {
                        Text("PAC 脚本只能用于系统代理")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Section("不经代理的地址") {
                    TextField("系统代理的例外（逗号分隔）", text: $draft.bypass, axis: .vertical)
                        .lineLimit(2...4)
                    if draft.supportsNonSystemTargets {
                        TextField("NO_PROXY（环境变量和 npm）", text: $draft.noProxy)
                    }
                }
                if let result {
                    Section("测试结果") {
                        Label(result.ok ? "\(result.latencyText)，\(result.message)" : result.message, systemImage: result.ok ? "checkmark.circle.fill" : "xmark.circle.fill")
                            .foregroundStyle(result.ok ? Color.green : Color.red)
                    }
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)

            HStack(spacing: 10) {
                Button(role: .destructive) {
                    onDelete()
                } label: {
                    Label("删除", systemImage: "trash")
                }
                Button {
                    test()
                } label: {
                    if testing {
                        ProgressView().controlSize(.small)
                    } else {
                        Label("测试连接", systemImage: "gauge.with.needle")
                    }
                }
                .disabled(testing)
                Spacer()
                if let problem {
                    Text(problem)
                        .font(.caption)
                        .foregroundStyle(.red)
                } else if saved {
                    Label("已保存", systemImage: "checkmark")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Button("保存") { save() }
                    .keyboardShortcut("s", modifiers: .command)
                    .buttonStyle(.borderedProminent)
                    .disabled(!dirty)
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 16)
        }
        .glassCard()
    }

    private func save() {
        draft.name = draft.name.trimmingCharacters(in: .whitespaces)
        draft.host = draft.host.trimmingCharacters(in: .whitespaces)
        draft.pacURL = draft.pacURL.trimmingCharacters(in: .whitespaces)
        if draft.kind == .pac {
            draft.targets = [.system]
        }
        if let error = draft.validate() {
            problem = error
            return
        }
        if state.config.profiles.contains(where: { $0.id != draft.id && $0.name == draft.name }) {
            problem = "已经有叫「\(draft.name)」的配置了"
            return
        }
        problem = nil
        state.update(draft)
        saved = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { saved = false }
    }

    private func test() {
        if let error = draft.validate() {
            problem = error
            return
        }
        problem = nil
        testing = true
        let profile = draft
        let testURL = state.config.testURL
        Task { @MainActor in
            result = await ProxyTester.test(profile: profile, testURL: testURL)
            testing = false
        }
    }
}

/// 自动检测本机代理软件。
struct DetectSheet: View {
    @ObservedObject var state: AppState
    let onAdd: (DetectedProxy) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var detecting = true
    @State private var found: [DetectedProxy] = []

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("自动检测本机代理")
                .font(.system(size: 16, weight: .semibold))
            Text("检查本机监听的端口，找出能当代理用的，并测出延迟。")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
            Group {
                if detecting {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("正在检测…")
                            .foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity, minHeight: 120)
                } else if found.isEmpty {
                    Text("没有找到正在运行的代理软件。请先启动代理软件，或者手动填写地址。")
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, minHeight: 120)
                } else {
                    VStack(spacing: 4) {
                        ForEach(found) { item in
                            HStack {
                                Image(systemName: item.kind == .socks5 ? "point.3.filled.connected.trianglepath.dotted" : "globe")
                                    .foregroundStyle(.secondary)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(item.suggestedName)
                                        .font(.system(size: 12, weight: .medium))
                                    Text("\(item.kind.title) · \(item.host):\(item.port)" + (item.latencyMs.map { " · \($0) ms" } ?? ""))
                                        .font(.system(size: 10))
                                        .foregroundStyle(.secondary)
                                }
                                Spacer()
                                Button("添加") {
                                    onAdd(item)
                                    dismiss()
                                }
                                .controlSize(.small)
                            }
                            .padding(8)
                        }
                    }
                    .glassCard()
                }
            }
            HStack {
                Button("重新检测") { detect() }
                    .disabled(detecting)
                Spacer()
                Button("关闭") { dismiss() }
                    .keyboardShortcut(.cancelAction)
            }
        }
        .padding(20)
        .frame(width: 440)
        .task { detect() }
    }

    private func detect() {
        detecting = true
        let testURL = state.config.testURL
        Task { @MainActor in
            found = await LocalProxyDetector.detect(testURL: testURL)
            detecting = false
        }
    }
}
