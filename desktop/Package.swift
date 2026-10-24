// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "KeepUpdatedDesktop",
    defaultLocalization: "en",
    platforms: [.macOS(.v13)],
    products: [.executable(name: "KeepUpdatedDesktop", targets: ["KeepUpdatedDesktop"])],
    targets: [
        .executableTarget(name: "KeepUpdatedDesktop", resources: [.process("Resources")]),
        .testTarget(name: "KeepUpdatedDesktopTests", dependencies: ["KeepUpdatedDesktop"])
    ]
)
