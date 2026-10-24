// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "KeepUpdatedPrivileges",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "PrivilegedUpdates", targets: ["PrivilegedUpdates"]),
        .executable(name: "KeepUpdatedAuthorization", targets: ["KeepUpdatedAuthorization"]),
        .executable(name: "KeepUpdatedHelper", targets: ["KeepUpdatedHelper"])
    ],
    targets: [
        .target(name: "PrivilegedUpdates"),
        .executableTarget(name: "KeepUpdatedAuthorization", dependencies: ["PrivilegedUpdates"]),
        .executableTarget(name: "KeepUpdatedHelper", dependencies: ["PrivilegedUpdates"]),
        .testTarget(name: "PrivilegedUpdatesTests", dependencies: ["PrivilegedUpdates"])
    ]
)
