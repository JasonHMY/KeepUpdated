import XCTest
@testable import KeepUpdatedDesktop

final class InventoryCleanupTests: XCTestCase {
    func testStoreNavigationUsesBoundProductOrFixedUpdatesPage() throws {
        let valid = DemoData.appStoreItem
        XCTAssertEqual(valid.appStoreURL?.absoluteString, "macappstore://apps.apple.com/au/app/id123456789")
        let data = try ReportJSON.encode(valid)
        let decoder = JSONDecoder(); decoder.keyDecodingStrategy = .convertFromSnakeCase
        XCTAssertEqual(try decoder.decode(InventoryItem.self, from: data).appStoreURL, valid.appStoreURL)
        let updates = URL(string: "macappstore://showUpdatesPage")
        var value = valid; value.appStore = nil; XCTAssertEqual(value.appStoreURL, updates)
        for id: Int64 in [0, -1] { value = valid; value.appStore?.id = id; XCTAssertEqual(value.appStoreURL, updates) }
        for country in ["", "a", "au/other", "AU?x=y", "中文", "a1"] {
            value = valid; value.appStore?.country = country; XCTAssertEqual(value.appStoreURL, updates)
        }
        value = valid; value.identity = "com.example.other"; XCTAssertEqual(value.appStoreURL, updates)
        value = valid; value.source = "unknown"; XCTAssertNil(value.appStoreURL)
        value = valid; value.owner = "homebrew"; XCTAssertNil(value.appStoreURL)
        for state in ["current", "unknown", "error"] { value = valid; value.update.status = state; XCTAssertEqual(value.appStoreURL, valid.appStoreURL) }
    }

    @MainActor func testNavigationDoesNotCreatePlanOrClaimUpdateAndRejectsStaleItem() {
        let store = InventoryStore(demo: false)
        let value = DemoData.appStoreItem
        var report = DemoData.report; report.items = [value]; store.replaceReport(report)
        var opened: [URL] = []
        store.setSelected(value.id, selected: true)
        XCTAssertEqual(store.selectedIDs, [value.id])
        XCTAssertNil(store.preview)
        store.openAppStore(value, open: { opened.append($0); return true })
        XCTAssertEqual(opened, [value.appStoreURL!])
        XCTAssertNil(store.preview)
        XCTAssertNil(store.results)
        XCTAssertEqual(store.report?.items.first?.update.status, "available")
        XCTAssertNotNil(store.lastNotice)
        var stale = value; stale.appStore?.id = 999
        store.openAppStore(stale, open: { opened.append($0); return true })
        XCTAssertEqual(opened.count, 1)
        store.openAppStoreUpdates(open: { opened.append($0); return true })
        XCTAssertEqual(opened.last?.absoluteString, "macappstore://showUpdatesPage")
        store.openAppStore(value, open: { _ in false })
        XCTAssertNotNil(store.error)
        let demo = InventoryStore(demo: true)
        demo.openAppStore(value, open: { _ in XCTFail("Demo opened an external app"); return true })
        XCTAssertNil(demo.preview)
    }

    @MainActor func testSkillFoldersCountDisplayGroupsAndKeepIndependentTargets() async {
        var a = DemoData.item("one", "writer", "skill", "git")
        a.path = "/Clients/One/writer/SKILL.md"; a.realPath = "/Shared/writer/SKILL.md"
        var alias = a; alias.id = "alias"; alias.path = "/Clients/Two/writer/SKILL.md"
        var copy = a; copy.id = "copy"; copy.path = "/Copies/writer/SKILL.md"; copy.realPath = copy.path
        copy.installedVersion = "0.8"
        var system = a; system.id = "system"; system.owner = "system"; system.path = "/System/core/SKILL.md"; system.realPath = system.path
        var unknown = a; unknown.id = "unknown"; unknown.path = "/Cache/market/writer/2/skills/writer/SKILL.md"; unknown.realPath = unknown.path
        unknown.owner = "plugin-cache"; unknown.source = "plugin-cache"; unknown.update = .init(status: "unsupported", code: "codex_plugin_update_in_client")
        unknown.pluginCache = .init(client: "codex", root: "/Cache/market/writer/2", id: "writer@market", version: "2", registration: "unverified")
        var old = unknown; old.id = "historical"; old.pluginCache?.registration = "unregistered"
        old.pluginCache?.root = "/Cache/market/writer/1"; old.path = "/Cache/market/writer/1/skills/writer/SKILL.md"; old.realPath = old.path
        old.update.code = "plugin_cache_unregistered"
        XCTAssertEqual(old.statusLabel, L10n.text("Unregistered cache"))
        XCTAssertEqual(unknown.statusLabel, L10n.text("Registration unverified"))
        XCTAssertTrue(unknown.matches("writer@market"))
        XCTAssertTrue(unknown.matches("codex"))
        XCTAssertFalse(unknown.matches("different@market"))
        let values = [a, alias, copy, system, unknown, old]
        var report = DemoData.report; report.items = values
        let store = InventoryStore(demo: true); store.replaceReport(report)
        XCTAssertEqual(store.count(.skills), 1) // all locations share one product; ownership still controls each action
        XCTAssertEqual(store.count(.all), 1)
        XCTAssertEqual(Set(store.presentation().products[0].installations.map(\.id)), ["one", "alias", "copy", "system"])
        XCTAssertEqual(Set(store.presentation().products[0].supportingItems.map(\.id)), ["unknown", "historical"])
        XCTAssertEqual(Set(store.tree.flatMap(\.items).map(\.id)), Set(values.map(\.id)))
        XCTAssertTrue(store.tree.contains { $0.isTechnicalDetail && $0.children.contains { $0.id.hasPrefix("cache:") && $0.items.map(\.id) == ["historical"] } })
        let folder = store.tree.first { $0.id.hasPrefix("skill-name:") }!
        XCTAssertEqual(Set(folder.items.map(\.id)), ["one", "alias", "copy"])
        let rows = InventoryTreeNode.rows(store.tree, matching: ["copy"], expanded: [], filtering: true)
        XCTAssertEqual(rows.compactMap { $0.node.item?.id }, ["copy"])
        store.setSelected("copy", selected: true)
        XCTAssertEqual(store.selectedIDs, ["copy"])
        store.plan(action: .update, ids: ["copy"])
        for _ in 0..<100 where store.preview == nil { try? await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertEqual(store.preview?.items.map(\.id), ["copy"])
        XCTAssertEqual(store.selectedIDs, ["copy"])
    }
}
