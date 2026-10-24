import XCTest
@testable import KeepUpdatedDesktop

final class AboutInfoTests: XCTestCase {
    func testPackagedIdentityUsesBundleVersionBuildAndSourceMetadata() throws {
        let metadata = Data(#"{"version":"0.1.0","source_commit":"abc123","source_tree_sha256":"tree-digest","working_tree_dirty":true,"signing":"not evidence of acceptance"}"#.utf8)
        let info = AboutInfo(info: ["CFBundleShortVersionString": "0.1.0", "CFBundleVersion": "8"], metadata: metadata)
        XCTAssertEqual(info.version, "0.1.0")
        XCTAssertEqual(info.build, "8")
        XCTAssertEqual(info.source?.commit, "abc123")
        XCTAssertEqual(info.source?.treeSHA256, "tree-digest")
        XCTAssertEqual(info.source?.dirty, true)
        let cleanData = Data(#"{"source_commit":"def456","source_tree_sha256":"another-tree","working_tree_dirty":false}"#.utf8)
        XCTAssertEqual(AboutInfo(info: [:], metadata: cleanData).source?.dirty, false)
    }

    func testMissingOrMalformedMetadataDoesNotInventIdentity() {
        for data in [nil, Data("not json".utf8), Data(#"{"source_commit":"abc123"}"#.utf8)] {
            let info = AboutInfo(info: [:], metadata: data)
            XCTAssertNil(info.version)
            XCTAssertNil(info.build)
            XCTAssertNil(info.source)
        }
        let empty = AboutInfo(info: ["CFBundleShortVersionString": "", "CFBundleVersion": 8], metadata: nil)
        XCTAssertNil(empty.version)
        XCTAssertNil(empty.build)
    }

    func testReadsPackagedIdentityFromBundleResources() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".app")
        defer { try? FileManager.default.removeItem(at: root) }
        let resources = root.appendingPathComponent("Contents/Resources")
        try FileManager.default.createDirectory(at: resources, withIntermediateDirectories: true)
        let info = ["CFBundleIdentifier": "com.example.about-fixture", "CFBundlePackageType": "APPL",
                    "CFBundleShortVersionString": "0.1.0", "CFBundleVersion": "8"]
        try PropertyListSerialization.data(fromPropertyList: info, format: .xml, options: 0)
            .write(to: root.appendingPathComponent("Contents/Info.plist"))
        try Data(#"{"source_commit":"packaged-commit","source_tree_sha256":"packaged-tree","working_tree_dirty":false}"#.utf8)
            .write(to: resources.appendingPathComponent("DESKTOP_BUILDINFO.json"))
        let bundled = AboutInfo(bundle: try XCTUnwrap(Bundle(url: root)))
        XCTAssertEqual(bundled.version, "0.1.0")
        XCTAssertEqual(bundled.build, "8")
        XCTAssertEqual(bundled.source?.commit, "packaged-commit")
        XCTAssertEqual(bundled.source?.dirty, false)
    }
}
