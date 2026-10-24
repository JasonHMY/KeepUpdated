import XCTest
@testable import KeepUpdatedDesktop

final class ManualUpdateTests: XCTestCase {
    @MainActor
    func testManualOnlyPlanIsImmediateAndNeverStartsCLIOrOpensStore() throws {
        let script = try PrivatePlanFile(data: Data("#!/bin/sh\nexit 99\n".utf8))
        defer { script.remove() }
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.url.path)
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        var report = DemoData.report
        var unknownStore = DemoData.appStoreItem
        unknownStore.id = "unknown-store"; unknownStore.appStore = nil; unknownStore.update.status = "unknown"
        report.items = [DemoData.appStoreItem, unknownStore, DemoData.manualAppItem]
        store.replaceReport(report)
        store.plan(action: .update, all: true)
        XCTAssertFalse(store.busy)
        XCTAssertNil(store.preview)
        XCTAssertNil(store.results)
        XCTAssertNil(store.error)
        XCTAssertEqual(store.manualUpdates.map(\.id), report.items.map(\.id))
        XCTAssertEqual(unknownStore.appStoreURL?.absoluteString, "macappstore://showUpdatesPage")
        XCTAssertTrue(store.report!.items[0].canUpdate)
    }

    @MainActor
    func testMixedPlanExcludesManualAppsAndRetainsUnknownManagedItems() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let calls = files.directory.appendingPathComponent("calls")
        let release = files.directory.appendingPathComponent("release")
        let managed = DemoData.item("unknown-managed", "Example", "node-package", "npm", "unknown")
        let plan = DemoData.plan(action: .update, items: [managed], cleanup: false)
        let response = String(decoding: try DemoData.planData(plan, privilegedRequest: nil), as: UTF8.self)
        let script = try PrivatePlanFile(data: Data("""
        #!/bin/sh
        printf '%s\\n' "$*" > '\(calls.path)'
        while [ ! -f '\(release.path)' ]; do sleep 0.01; done
        printf '%s' '\(response)'
        """.utf8))
        defer { script.remove() }
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.url.path)
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        var report = DemoData.report; report.items = [DemoData.appStoreItem, DemoData.manualAppItem, managed]
        store.replaceReport(report)
        store.plan(action: .update, all: true)
        XCTAssertTrue(store.busy)
        XCTAssertEqual(store.manualUpdates.count, 2, "Manual links appear before the plan finishes")
        XCTAssertNil(store.preview)
        try await waitUntil { FileManager.default.fileExists(atPath: calls.path) }
        let arguments = try String(contentsOf: calls)
        XCTAssertTrue(arguments.contains("--id unknown-managed"))
        XCTAssertTrue(arguments.contains("--progress"))
        XCTAssertFalse(arguments.contains("--all"))
        XCTAssertFalse(arguments.contains(DemoData.appStoreItem.id))
        XCTAssertFalse(arguments.contains(DemoData.manualAppItem.id))
        try Data().write(to: release)
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(store.preview?.items.map(\.id), [managed.id])
        XCTAssertEqual(store.manualUpdates.count, 2)
    }

    @MainActor
    func testOpenAppValidatesExactTopLevelInstanceAndKeepsUpdateUnchanged() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let app = files.directory.appendingPathComponent("Original.app", isDirectory: true)
        try FileManager.default.createDirectory(at: app.appendingPathComponent("Contents/MacOS"), withIntermediateDirectories: true)
        let metadata = app.appendingPathComponent("Contents/Info.plist")
        let info = ["CFBundleIdentifier": "com.example.manual-fixture", "CFBundlePackageType": "APPL",
                    "CFBundleExecutable": "fixture", "CFBundleShortVersionString": "1.0.0", "CFBundleVersion": "1"]
        try PropertyListSerialization.data(fromPropertyList: info, format: .xml, options: 0).write(to: metadata)
        let executable = app.appendingPathComponent("Contents/MacOS/fixture")
        try Data("#!/bin/sh\nexit 0\n".utf8).write(to: executable)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: executable.path)
        var item = DemoData.manualAppItem
        item.id = "exact-fixture"; item.path = app.path; item.realPath = app.resolvingSymlinksInPath().path
        item.identity = info["CFBundleIdentifier"]; item.installedBuild = "1"
        var report = DemoData.report; report.items = [item]
        let store = InventoryStore(demo: false); store.replaceReport(report)
        var opened: [URL] = []
        store.openManualApplication(item) { opened.append($0) }
        try await waitUntil { !store.busy }
        XCTAssertEqual(opened.map(\.path), [app.path])
        XCTAssertNil(store.error)
        XCTAssertNil(store.results)
        XCTAssertEqual(store.report?.items.first?.update.status, "available")
        XCTAssertEqual(store.report?.items.first?.installedVersion, "1.0.0")

        var stale = item; stale.identity = "com.example.other"
        store.openManualApplication(stale) { _ in XCTFail("Stale row opened") }
        XCTAssertFalse(store.busy)
        var fallback = info; fallback.removeValue(forKey: "CFBundleShortVersionString")
        try PropertyListSerialization.data(fromPropertyList: fallback, format: .xml, options: 0).write(to: metadata)
        var buildVersion = item; buildVersion.installedVersion = "1"
        XCTAssertEqual(try InventoryStore.verifiedApplicationURL(buildVersion).path, app.path)
        var changed = info; changed["CFBundleIdentifier"] = "com.example.replacement"
        try PropertyListSerialization.data(fromPropertyList: changed, format: .xml, options: 0).write(to: metadata)
        store.openManualApplication(item) { _ in XCTFail("Replaced app opened") }
        try await waitUntil { !store.busy }
        XCTAssertNotNil(store.error)
        var helper = item; helper.path = app.appendingPathComponent("Contents/Helper.app").path
        XCTAssertFalse(helper.canOpenForManualUpdate)
        var system = item; system.owner = "system"; XCTAssertFalse(system.canOpenForManualUpdate)
        var unconfirmed = item; unconfirmed.update.status = "unknown"; XCTAssertFalse(unconfirmed.canOpenForManualUpdate)
        var supported = item; supported.source = "sparkle"; XCTAssertFalse(supported.needsManualUpdate)
    }

    @MainActor
    func testManualPreviewRetainsOpenAppEntryForFinalUnsupportedCandidate() {
        let store = InventoryStore(demo: false)
        var item = DemoData.manualAppItem; item.source = "sparkle"
        var report = DemoData.report; report.items = [item]; store.replaceReport(report)
        var plan = DemoData.plan(action: .update, items: [item], cleanup: false)
        plan.items[0].status = "manual"
        store.preview = plan
        XCTAssertEqual(store.inventoryItem(for: plan.items[0])?.path, item.path)
        XCTAssertTrue(store.inventoryItem(for: plan.items[0])?.canOpenForManualUpdate == true)
        plan.items[0].status = "ready"
        XCTAssertNil(store.inventoryItem(for: plan.items[0]))
    }

    @MainActor
    func testHomebrewKeepsManagedInventoryActionAndOffersOpenOnlyForManualPreview() {
        let store = InventoryStore(demo: false)
        var item = DemoData.manualAppItem
        item.owner = "homebrew"; item.source = "homebrew/cask"
        var report = DemoData.report; report.items = [item]; store.replaceReport(report)
        XCTAssertFalse(item.needsManualUpdate, "Homebrew still uses its managed preview action")
        XCTAssertTrue(store.canPreviewUpdate(item))
        var plan = DemoData.plan(action: .update, items: [item], cleanup: false)
        for status in ["manual", "blocked"] {
            plan.items[0].status = status
            store.preview = plan
            XCTAssertTrue(store.inventoryItem(for: plan.items[0])?.canOpenForManualUpdate == true)
        }
        plan.items[0].status = "ready"
        store.preview = plan
        XCTAssertNil(store.inventoryItem(for: plan.items[0]))
    }

    @MainActor
    func testManualPanelRebindsRefreshedItemsAndDropsMissingOrManagedTargets() throws {
        let store = InventoryStore(demo: false)
        let originalStore = DemoData.appStoreItem
        let originalApp = DemoData.manualAppItem
        var removed = originalApp; removed.id = "removed"
        var nowManaged = originalApp; nowManaged.id = "managed-after-refresh"
        var report = DemoData.report; report.items = [originalStore, originalApp, removed, nowManaged]
        store.replaceReport(report)
        store.plan(action: .update, all: true)
        XCTAssertEqual(store.manualUpdates.count, 4)
        XCTAssertFalse(store.busy)

        var refreshedStore = originalStore
        refreshedStore.installedVersion = "1.1.0"; refreshedStore.installedBuild = "2"
        refreshedStore.update.status = "current"; refreshedStore.appStore?.id = 987654321
        var refreshedApp = originalApp
        refreshedApp.installedVersion = "1.1.0"; refreshedApp.installedBuild = "2"
        refreshedApp.latestVersion = "1.2.0"
        nowManaged.source = "sparkle"
        report.items = [refreshedApp, refreshedStore, nowManaged]
        store.replaceReport(report)
        XCTAssertEqual(store.manualUpdates, [refreshedApp, refreshedStore])
        XCTAssertTrue(store.manualUpdates[0].canOpenForManualUpdate)
        XCTAssertEqual(store.manualUpdates[0].installedBuild, "2")
        XCTAssertEqual(store.manualUpdates[1].update.status, "current")
        var opened: [URL] = []
        store.openAppStore(originalStore, open: { opened.append($0); return true })
        XCTAssertTrue(opened.isEmpty, "The original stale click still fails its equality guard")
        let currentStore = try XCTUnwrap(store.manualUpdates.first(where: \.isAppStoreApplication))
        store.openAppStore(currentStore, open: { opened.append($0); return true })
        XCTAssertEqual(opened.map(\.absoluteString), ["macappstore://apps.apple.com/au/app/id987654321"])
        XCTAssertNil(store.results)
        report.items = []
        store.replaceReport(report)
        XCTAssertTrue(store.manualUpdates.isEmpty)
    }

    @MainActor
    private func waitUntil(_ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(3)
        while !condition() && Date() < deadline { try await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertTrue(condition(), "Condition did not become true")
    }
}
