import AppKit
import XCTest
@testable import KeepUpdatedDesktop

final class ApplicationIconLoaderTests: XCTestCase {
    private final class Probe: @unchecked Sendable {
        private let lock = NSLock()
        private var reads = 0
        private var mainThreadRead = false
        func read(_ path: String) -> NSImage {
            lock.lock()
            reads += 1
            mainThreadRead = mainThreadRead || Thread.isMainThread
            lock.unlock()
            return NSImage(size: NSSize(width: 32, height: 32))
        }
        var result: (reads: Int, mainThreadRead: Bool) {
            lock.lock(); defer { lock.unlock() }
            return (reads, mainThreadRead)
        }
    }

    @MainActor
    func testIconReadsRunOffMainReuseUnchangedFilesAndRefreshChangedApplications() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let app = directory.appendingPathComponent("Example.app")
        try FileManager.default.createDirectory(at: app, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let probe = Probe()
        let loader = ApplicationIconLoader(readIcon: { probe.read($0) })
        let first = await loader.icon(for: [app.path])
        let second = await loader.icon(for: [app.path])
        XCTAssertNotNil(first)
        XCTAssertTrue(first === second)
        XCTAssertEqual(probe.result.reads, 1)
        XCTAssertFalse(probe.result.mainThreadRead)

        try FileManager.default.setAttributes([.modificationDate: Date(timeIntervalSince1970: 100)], ofItemAtPath: app.path)
        let changed = await loader.icon(for: [app.path])
        XCTAssertNotNil(changed)
        XCTAssertFalse(changed === first)
        XCTAssertEqual(probe.result.reads, 2)
    }

    @MainActor
    func testMissingInvalidAndRemovedPathsDoNotLeaveAStaleIcon() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let app = directory.appendingPathComponent("Example.app")
        let file = directory.appendingPathComponent("File.app")
        try Data().write(to: file)
        let probe = Probe()
        let loader = ApplicationIconLoader(readIcon: { probe.read($0) })
        let missing = await loader.icon(for: ["relative.app", directory.path, file.path, app.path])
        XCTAssertNil(missing)
        XCTAssertEqual(probe.result.reads, 0)

        try FileManager.default.createDirectory(at: app, withIntermediateDirectories: true)
        let found = await loader.icon(for: [app.path])
        XCTAssertNotNil(found)
        XCTAssertEqual(probe.result.reads, 1)
        try FileManager.default.removeItem(at: app)
        let removed = await loader.icon(for: [app.path])
        XCTAssertNil(removed)
    }

    @MainActor
    func testNewReportRefreshesAnIconChangedInsideAnUnchangedBundleRoot() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let app = directory.appendingPathComponent("Example.app")
        let resources = app.appendingPathComponent("Contents/Resources")
        try FileManager.default.createDirectory(at: resources, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = resources.appendingPathComponent("AppIcon.icns")
        try Data([1]).write(to: file)
        let modified = try FileManager.default.attributesOfItem(atPath: app.path)[.modificationDate] as? Date
        let probe = Probe()
        let loader = ApplicationIconLoader(readIcon: { probe.read($0) })
        let first = await loader.icon(for: [app.path], revision: UUID())
        try Data([2]).write(to: file)
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: app.path)[.modificationDate] as? Date, modified)
        let refreshed = await loader.icon(for: [app.path], revision: UUID())
        XCTAssertNotNil(refreshed)
        XCTAssertFalse(first === refreshed)
        XCTAssertEqual(probe.result.reads, 2)
    }
}
