import XCTest
@testable import KeepUpdatedDesktop

final class InventoryPresentationTests: XCTestCase {
    private var previousDefaults: UserDefaults!
    private var suite: String!
    override func setUp() {
        super.setUp()
        previousDefaults = L10n.defaults
        suite = "KeepUpdatedPresentationTests." + UUID().uuidString
        L10n.defaults = UserDefaults(suiteName: suite)!
        L10n.defaults.set("en", forKey: L10n.preferenceKey)
    }
    override func tearDown() {
        L10n.defaults.removePersistentDomain(forName: suite)
        L10n.defaults = previousDefaults
        super.tearDown()
    }

    @MainActor
    func testPresentationPreservesMatchingOrderGroupsCountsAndCollapsedSelection() throws {
        let store = InventoryStore(demo: true)
        for language in ["en", "zh-Hans"] {
            L10n.defaults.set(language, forKey: L10n.preferenceKey)
            for category in ItemCategory.allCases {
                store.category = category
                for query in ["", " \n ", "Example", "更新", "no-matching-item"] {
                    store.query = query
                    for onlyUpdates in [false, true] {
                        store.onlyUpdates = onlyUpdates
                        let expected = store.filtered
                        let snapshot = store.presentation()
                        XCTAssertEqual(snapshot.items, expected)
                        XCTAssertEqual(snapshot.displayCount, store.visibleDisplayCount)
                        XCTAssertEqual(snapshot.previewCount, store.visiblePreviewCount)
                        let rows = InventoryTreeNode.rows(store.tree, matching: Set(expected.map(\.id)), expanded: [],
                            filtering: !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || onlyUpdates)
                        XCTAssertEqual(snapshot.rows.map(\.id), rows.map(\.id))
                        XCTAssertEqual(snapshot.rows.map(\.depth), rows.map(\.depth))
                        XCTAssertEqual(snapshot.rows.map(\.expanded), rows.map(\.expanded))
                    }
                }
            }
        }
        store.category = .all; store.query = ""; store.onlyUpdates = false
        let collapsed = store.presentation()
        let folder = try XCTUnwrap(collapsed.rows.first { !$0.node.children.isEmpty && !$0.node.isTechnicalDetail })
        XCTAssertFalse(folder.expanded)
        XCTAssertTrue(try XCTUnwrap(store.presentation(expanded: [folder.id]).rows.first { $0.id == folder.id }).expanded)
        store.query = try XCTUnwrap(folder.node.items.first).name
        XCTAssertTrue(try XCTUnwrap(store.presentation().rows.first { $0.id == folder.id }).expanded)
        XCTAssertFalse(try XCTUnwrap(store.presentation(collapsedWhileFiltering: [folder.id]).rows.first { $0.id == folder.id }).expanded)
        store.selectVisible()
        XCTAssertEqual(store.selectedIDs, Set(store.filtered.filter { store.canPreviewUpdate($0) }.map(\.id)))
    }

    @MainActor
    func testSameQueryReflectsLanguageStatusAndInventoryReplacementImmediately() {
        let store = InventoryStore(demo: true)
        var report = DemoData.report
        var item = DemoData.item("stable-id", "Example", "node-package", "npm", "current")
        report.items = [item]; store.replaceReport(report)
        store.query = "Up to date"
        XCTAssertEqual(store.presentation().items.map(\.id), [item.id])
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        XCTAssertTrue(store.presentation().items.isEmpty)
        store.query = "已是最新"
        XCTAssertEqual(store.presentation().items.map(\.id), [item.id])
        item.update.status = "available"
        report.items = [item]; store.replaceReport(report)
        XCTAssertTrue(store.presentation().items.isEmpty)
        store.query = "Example"; store.onlyUpdates = true
        XCTAssertEqual(store.presentation().items.map(\.id), [item.id])
        item.update.status = "current"
        report.items = [item]; store.replaceReport(report)
        XCTAssertTrue(store.presentation().items.isEmpty)
        store.onlyUpdates = false
        XCTAssertEqual(store.presentation().items.map(\.id), [item.id])
        report.items = []; store.replaceReport(report)
        XCTAssertTrue(store.presentation().rows.isEmpty)
        XCTAssertEqual(store.presentation().displayCount, 0)
    }

    func testSearchRetainsLocalizedStatusExplanationPluginAndWhitespaceMatches() {
        var item = DemoData.item("fixture", "Example", "application", "standalone", "unknown", code: "timeout")
        for language in ["en", "zh-Hans"] {
            L10n.defaults.set(language, forKey: L10n.preferenceKey)
            XCTAssertTrue(item.matches(" \n\t "))
            XCTAssertTrue(item.matches(item.statusLabel))
            XCTAssertTrue(item.matches(item.statusExplanation))
            XCTAssertTrue(item.matches(" example "))
            XCTAssertFalse(item.matches("no-such-value"))
        }
        item.pluginCache = .init(client: "fixture-client", root: "/cache", id: "fixture@market", version: "1", registration: "unknown")
        XCTAssertTrue(item.matches("fixture@market"))
        XCTAssertTrue(item.matches("fixture-client"))
    }

    @MainActor
    func testReportReplacementRefreshesProductsWithoutChangingAnExistingSnapshot() throws {
        let store = InventoryStore(demo: true)
        var report = DemoData.report
        report.items = [DemoData.item("first", "First", "application", "standalone", "current")]
        store.replaceReport(report)
        let previous = store.presentation()
        let first = try XCTUnwrap(previous.products.first)

        report.items = [DemoData.item("second", "Second", "application", "standalone", "current")]
        store.replaceReport(report)
        XCTAssertNil(store.product(id: first.id))
        XCTAssertEqual(store.presentation().products.first?.productTitle, "Second")
        XCTAssertEqual(previous.products.first?.productTitle, "First")
        XCTAssertEqual(previous.rows.first?.node.item?.id, "first")
        XCTAssertEqual(previous.displayCount, 1)
    }
}
