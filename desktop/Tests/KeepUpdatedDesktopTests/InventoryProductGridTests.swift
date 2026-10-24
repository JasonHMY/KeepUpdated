import XCTest
@testable import KeepUpdatedDesktop

final class InventoryProductGridTests: XCTestCase {
    func testWindowAppendsOneBatchAndIgnoresHiddenOrStaleAppearances() {
        var window = InventoryRenderWindow()
        XCTAssertEqual(window.limit, 100)
        XCTAssertFalse(window.extend(approaching: 99, total: 729, active: false))
        XCTAssertFalse(window.extend(approaching: 87, total: 729, active: true))
        XCTAssertFalse(window.extend(approaching: 100, total: 729, active: true))
        XCTAssertTrue(window.extend(approaching: 88, total: 729, active: true))
        XCTAssertEqual(window.limit, 200)
        for oldIndex in 88..<100 {
            XCTAssertFalse(window.extend(approaching: oldIndex, total: 729, active: true))
        }
        for expected in [300, 400, 500, 600, 700, 729] {
            XCTAssertTrue(window.extend(approaching: window.limit - 1, total: 729, active: true))
            XCTAssertEqual(window.limit, expected)
        }
        XCTAssertFalse(window.extend(approaching: 728, total: 729, active: true))
        for total in [0, 99, 100] {
            var small = InventoryRenderWindow()
            XCTAssertFalse(small.extend(approaching: max(0, total - 1), total: total, active: true))
        }
        var remainder = InventoryRenderWindow()
        XCTAssertTrue(remainder.extend(approaching: 99, total: 101, active: true))
        XCTAssertEqual(remainder.limit, 101)
    }

    func testEntranceIsOrderedBoundedAndDoesNotReplayOrDelayLaterScrolls() throws {
        let entrances = InventoryCardEntrances()
        let firstRow = try (0..<4).map { try XCTUnwrap(entrances.delay(for: "item-\($0)", index: $0, now: 10)) }
        XCTAssertEqual(firstRow.first, 0)
        XCTAssertTrue(zip(firstRow, firstRow.dropFirst()).allSatisfy { $0 < $1 })
        let nextRow = try XCTUnwrap(entrances.delay(for: "item-4", index: 4, now: 10))
        XCTAssertGreaterThan(nextRow, firstRow.last!)
        XCTAssertLessThanOrEqual(try XCTUnwrap(entrances.delay(for: "item-99", index: 99, now: 10)), 0.3)
        XCTAssertTrue(entrances.contains("item-0"))
        XCTAssertNil(entrances.delay(for: "item-0", index: 0, now: 11))
        XCTAssertEqual(entrances.delay(for: "item-100", index: 100, now: 11), 0)
    }

    @MainActor
    func testRenderingPrefixLeavesLateSearchMatchesAndFullSelectionAvailable() throws {
        let store = InventoryStore(demo: true)
        var report = DemoData.report
        report.items = (0..<201).map {
            DemoData.item("fixture-\($0)", String(format: "Package %03d", $0), "node-package", "npm")
        }
        store.replaceReport(report)
        let presentation = store.presentation()
        XCTAssertEqual(presentation.products.count, 201)
        XCTAssertEqual(presentation.displayCount, 201)
        XCTAssertEqual(presentation.products.prefix(InventoryRenderWindow().limit).count, 100)

        store.selectVisible()
        XCTAssertEqual(store.selectedIDs, Set(report.items.map(\.id)))
        store.query = "Package 200"
        let lateMatch = try XCTUnwrap(store.presentation().products.first)
        XCTAssertEqual(lateMatch.installations.map(\.id), ["fixture-200"])
        XCTAssertEqual(store.product(id: lateMatch.id)?.id, lateMatch.id)
        XCTAssertEqual(store.selectedIDs.count, 201)
        store.clearSelection()
        XCTAssertTrue(store.selectedIDs.isEmpty)
    }
}
