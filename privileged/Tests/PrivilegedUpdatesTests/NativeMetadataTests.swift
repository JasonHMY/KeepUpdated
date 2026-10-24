import XCTest
@testable import PrivilegedUpdates

final class NativeMetadataTests: XCTestCase {
    func testRealIsolatedComponentArchiveParsesAndUnsignedPackageIsRejected() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-pkgbuild-test-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: directory) }
        let root = canonicalPath(directory.path)!
        let source = root + "/source/Fixture.app", installed = root + "/installed/Fixture.app", expanded = root + "/metadata"
        for path in [source + "/Contents/MacOS", installed + "/Contents", expanded] {
            try FileManager.default.createDirectory(atPath: path, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        }
        let info: [String: Any] = ["CFBundleIdentifier": "com.example.fixture", "CFBundleExecutable": "fixture", "CFBundlePackageType": "APPL", "CFBundleVersion": "2", "CFBundleShortVersionString": "2.0"]
        try PropertyListSerialization.data(fromPropertyList: info, format: .xml, options: 0).write(to: URL(fileURLWithPath: source + "/Contents/Info.plist"))
        try Data("fixture data; never executed".utf8).write(to: URL(fileURLWithPath: source + "/Contents/MacOS/fixture"))
        let path = root + "/fixture.pkg"
        let build = Process()
        build.executableURL = URL(fileURLWithPath: "/usr/bin/pkgbuild")
        build.arguments = ["--component", source, "--install-location", root + "/installed", "--identifier", "com.example.fixture", "--version", "2.0", path]
        build.environment = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL": "C"]
        build.standardOutput = FileHandle.nullDevice; build.standardError = FileHandle.nullDevice
        try build.run(); build.waitUntilExit()
        XCTAssertEqual(build.terminationStatus, 0)
        let provider = Providers()
        let metadata = try provider.componentMetadata(path, directory: expanded)
        XCTAssertEqual(metadata.id, "com.example.fixture")
        XCTAssertEqual(metadata.version, "2.0")
        XCTAssertEqual(metadata.application, installed)
        XCTAssertEqual(metadata.bundleID, "com.example.fixture")
        XCTAssertThrowsError(try provider.packageTeam(path)) { XCTAssertEqual($0 as? PrivilegeError, .packageSignatureRejected) }
    }
}
