// swift-tools-version: 5.9
import PackageDescription

// ProxySwitch for Mac：原生的菜单栏代理开关。用 swift build 编译，Scripts/build-app.sh 组装成 .app。
let package = Package(
    name: "ProxySwitch",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "ProxySwitch",
            path: "Sources/ProxySwitch"
        ),
        .testTarget(
            name: "ProxySwitchTests",
            dependencies: ["ProxySwitch"],
            path: "Tests/ProxySwitchTests"
        ),
    ],
    swiftLanguageVersions: [.v5]
)
