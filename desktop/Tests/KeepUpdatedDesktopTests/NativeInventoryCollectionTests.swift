import AppKit
import SwiftUI
import XCTest
@testable import KeepUpdatedDesktop

final class NativeInventoryCollectionTests: XCTestCase {
    private typealias Coordinator = NativeInventoryCollection<AnyView>.Coordinator

    @MainActor
    func testHeightMeasurementCachesByProductAndWidthAndPreservesLongText() {
        let products = [InventoryTreeNode(id: "short", item: nil, title: "Short"),
                        InventoryTreeNode(id: "long", item: nil, title: String(repeating: "A long product name with several words. ", count: 12))]
        var builds = 0
        let coordinator = Coordinator(products: products, active: true, entrances: InventoryCardEntrances(),
            environment: EnvironmentValues()) { product in
                builds += 1
                return AnyView(Text(product.title).font(.body).fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, minHeight: 40, alignment: .topLeading).padding(16))
            }
        defer { coordinator.stop() }
        let short = coordinator.height(at: 0, width: 220)
        let long = coordinator.height(at: 1, width: 220)
        XCTAssertGreaterThan(long, short)
        XCTAssertEqual(builds, 2)
        XCTAssertEqual(coordinator.height(at: 1, width: 220), long)
        XCTAssertEqual(builds, 2)
        XCTAssertLessThan(coordinator.height(at: 1, width: 440), long)
        XCTAssertEqual(builds, 3)
    }

    @MainActor
    func testAXEnumerationDoesNotPaginateOrCauseWidthAndHostFeedback() throws {
        let coordinator = makeCoordinator(count: 333)
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        settle()
        XCTAssertEqual(coordinator.renderedCount, 100)
        XCTAssertTrue(collection.isAccessibilityEnabled())
        XCTAssertTrue(collection.accessibilityActionNames().contains(InventoryCollectionView.scrollTop))
        XCTAssertTrue(collection.accessibilityActionNames().contains(InventoryCollectionView.scrollBottom))
        let passes = scroll.layoutPassCount
        let created = coordinator.createdHostingViews
        for _ in 0..<3 {
            _ = collection.accessibilityChildren()
            collection.layoutSubtreeIfNeeded()
            settle()
        }
        XCTAssertEqual(coordinator.renderedCount, 100)
        XCTAssertEqual(scroll.contentView.bounds.minY, 0, accuracy: 0.5)
        XCTAssertEqual(collection.frame.width, scroll.contentView.bounds.width, accuracy: 0.5)
        XCTAssertLessThan(scroll.layoutPassCount - passes, 12)
        XCTAssertLessThan(coordinator.createdHostingViews - created, 101)
    }

    @MainActor
    func testEndpointActionCrossesBatchesAndReturnsToExactClipBoundary() throws {
        let coordinator = makeCoordinator(count: 333)
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        collection.accessibilityPerformAction(InventoryCollectionView.scrollBottom)
        XCTAssertTrue(wait { !coordinator.hasPendingScroll && coordinator.renderedCount == 333 })
        XCTAssertEqual(scroll.contentView.bounds.maxY, collection.bounds.maxY, accuracy: 0.5)
        let last = try XCTUnwrap(collection.collectionViewLayout?.layoutAttributesForItem(at: IndexPath(item: 332, section: 0)))
        XCTAssertTrue(last.frame.intersects(scroll.contentView.bounds))
        let passes = scroll.layoutPassCount
        settle()
        XCTAssertLessThan(scroll.layoutPassCount - passes, 8)
        collection.accessibilityPerformAction(InventoryCollectionView.scrollTop)
        XCTAssertTrue(wait { !coordinator.hasPendingScroll })
        XCTAssertEqual(scroll.contentView.bounds.minY, 0, accuracy: 0.5)
        XCTAssertEqual(coordinator.renderedCount, 333)
    }

    @MainActor
    func testInactiveAndDismantledCollectionsCancelQueuedEndpointWork() throws {
        let coordinator = makeCoordinator(count: 333)
        let (window, _) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        coordinator.requestEdge(bottom: true)
        coordinator.update(products: products(count: 333), active: false, environment: EnvironmentValues(),
                           card: { AnyView(Text($0.title).frame(minHeight: 140).padding(16)) })
        settle()
        XCTAssertEqual(coordinator.renderedCount, 100)
        XCTAssertFalse(coordinator.hasPendingScroll)
        coordinator.stop()
        XCTAssertNil(collection.dataSource)
        XCTAssertNil(collection.delegate)
        XCTAssertNil(collection.scrollToEdge)
        coordinator.requestEdge(bottom: true)
        settle()
        XCTAssertEqual(coordinator.renderedCount, 100)
    }

    @MainActor
    func testReplacingInventoryDuringInsertionWaitsForCompletionAndRejectsOldIndices() throws {
        let original = products(count: 333)
        let short = products(count: 5)
        var replacedDuringInsertion = false
        weak var owner: Coordinator?
        let coordinator = Coordinator(products: original, active: true, entrances: InventoryCardEntrances(),
            environment: EnvironmentValues()) { product in
                if product.id == "item-100", let owner, owner.mutationInProgress, !replacedDuringInsertion {
                    replacedDuringInsertion = true
                    owner.update(products: short, active: true, environment: EnvironmentValues(),
                                 card: { AnyView(Text($0.title).frame(minHeight: 140).padding(16)) })
                }
                return AnyView(Text(product.title).frame(minHeight: 140).padding(16))
            }
        owner = coordinator
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        coordinator.requestEdge(bottom: true)
        XCTAssertTrue(wait { replacedDuringInsertion && !coordinator.mutationInProgress && coordinator.renderedCount == 5 })
        XCTAssertFalse(coordinator.hasPendingScroll)
        XCTAssertEqual(coordinator.collection?.numberOfItems(inSection: 0), 5)
        XCTAssertEqual(scroll.contentView.bounds.minY, 0, accuracy: 0.5)
        XCTAssertEqual(coordinator.height(at: 332, width: 220), 1)
    }

    @MainActor
    func testOrdinaryScrollingAppendsOneBatchWithoutFollowingTheNewBottom() throws {
        let coordinator = makeCoordinator(count: 333)
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        let oldBottom = collection.bounds.maxY - scroll.contentView.bounds.height
        scroll.contentView.scroll(to: NSPoint(x: 0, y: oldBottom))
        scroll.reflectScrolledClipView(scroll.contentView)
        XCTAssertTrue(wait { coordinator.renderedCount == 200 && !coordinator.mutationInProgress })
        settle()
        XCTAssertEqual(coordinator.renderedCount, 200)
        XCTAssertFalse(coordinator.hasPendingScroll)
        XCTAssertEqual(scroll.contentView.bounds.minY, oldBottom, accuracy: 0.5)
        XCTAssertLessThan(scroll.contentView.bounds.maxY, collection.bounds.maxY)
    }

    @MainActor
    func testResizingRecomputesFlowMetricsAndKeepsExactlyFourColumns() throws {
        let coordinator = makeCoordinator(count: 120)
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        for width: CGFloat in [960, 1120, 1440, 2100, 960] {
            window.setContentSize(NSSize(width: width, height: 640))
            scroll.layoutSubtreeIfNeeded()
            collection.layoutSubtreeIfNeeded()
            settle()
            let frames = try (0..<5).map { index in
                try XCTUnwrap(collection.collectionViewLayout?.layoutAttributesForItem(at: IndexPath(item: index, section: 0))?.frame)
            }
            XCTAssertEqual(collection.bounds.width, scroll.contentView.bounds.width, accuracy: 0.5)
            for frame in frames.prefix(4) {
                XCTAssertEqual(frame.minY, frames[0].minY, accuracy: 0.5)
                XCTAssertEqual(frame.width, (collection.bounds.width - 78) / 4, accuracy: 0.5)
                XCTAssertLessThanOrEqual(frame.maxX, collection.bounds.width - 17.5)
            }
            XCTAssertGreaterThan(frames[4].minY, frames[0].maxY)
            XCTAssertEqual(frames[4].minX, frames[0].minX, accuracy: 0.5)
        }
    }

    @MainActor
    func testScrollerWidthChangeRemeasuresCardsBeforeParentLayout() throws {
        let coordinator = makeCoordinator(count: 333)
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        scroll.scrollerStyle = .overlay
        scroll.layoutSubtreeIfNeeded()
        collection.layoutSubtreeIfNeeded()
        settle()
        let originalWidth = collection.bounds.width
        for style: NSScroller.Style in [.legacy, .overlay] {
            scroll.scrollerStyle = style
            scroll.tile()
            collection.layoutSubtreeIfNeeded()
            let width = scroll.contentView.bounds.width
            if style == .legacy { XCTAssertLessThan(width, originalWidth) }
            else { XCTAssertEqual(width, originalWidth, accuracy: 0.5) }
            XCTAssertEqual(collection.bounds.width, width, accuracy: 0.5)
            let frames = try (0..<5).map { index in
                try XCTUnwrap(collection.collectionViewLayout?.layoutAttributesForItem(at: IndexPath(item: index, section: 0))?.frame)
            }
            for frame in frames.prefix(4) {
                XCTAssertEqual(frame.minY, frames[0].minY, accuracy: 0.5)
                XCTAssertEqual(frame.width, (width - 78) / 4, accuracy: 0.5)
                XCTAssertLessThanOrEqual(frame.maxX, width - 17.5)
            }
            XCTAssertGreaterThan(frames[4].minY, frames[0].maxY)
        }
        XCTAssertEqual(scroll.contentView.bounds.minY, 0, accuracy: 0.5)
    }

    @MainActor
    func testMixedHeightRowsKeepEveryCardTopAligned() throws {
        let coordinator = Coordinator(products: products(count: 8), active: true,
            entrances: InventoryCardEntrances(), environment: EnvironmentValues()) { product in
                AnyView(Text(product.title).frame(maxWidth: .infinity, minHeight: product.id == "item-0" ? 300 : 172))
            }
        let (window, _) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let layout = try XCTUnwrap(coordinator.collection?.collectionViewLayout)
        let firstRow = try (0..<4).map { index in
            try XCTUnwrap(layout.layoutAttributesForItem(at: IndexPath(item: index, section: 0))?.frame)
        }
        XCTAssertEqual(firstRow[0].height, 300, accuracy: 0.5)
        XCTAssertEqual(firstRow[1].height, 172, accuracy: 0.5)
        for frame in firstRow { XCTAssertEqual(frame.minY, firstRow[0].minY, accuracy: 0.5) }
        let visibleAttributes = layout.layoutAttributesForElements(in: CGRect(x: 0, y: 0, width: 960, height: 640))
        for attributes in visibleAttributes where (attributes.indexPath?.item ?? 4) < 4 {
            XCTAssertEqual(attributes.frame.minY, firstRow[0].minY, accuracy: 0.5)
        }
        let strip = NSRect(x: 0, y: firstRow[0].minY, width: 960, height: 20)
        XCTAssertEqual(Set(layout.layoutAttributesForElements(in: strip).compactMap { $0.indexPath?.item }), Set(0..<4))
        let shortColumn = NSRect(x: firstRow[1].minX + 5, y: firstRow[0].minY, width: 10, height: 20)
        XCTAssertEqual(layout.layoutAttributesForElements(in: shortColumn).compactMap { $0.indexPath?.item }, [1])
        let belowShortCard = NSRect(x: firstRow[1].minX + 5, y: firstRow[1].maxY + 5, width: 10, height: 20)
        XCTAssertTrue(layout.layoutAttributesForElements(in: belowShortCard).isEmpty)
    }

    @MainActor
    func testColorSchemeRemeasuresRealCardAndInvalidatesFlowMetrics() throws {
        let item = DemoData.item("theme-item", String(repeating: "A long inventory product title with multiple descriptive words ", count: 8), "node-package", "npm")
        let product = InventoryTreeNode(id: item.id, item: item, title: item.name)
        let makeCard: (InventoryTreeNode) -> AnyView = { product in
            AnyView(InventoryCandidateRow(product: product, selected: .constant(false), selectable: true, open: {}))
        }
        var dark = EnvironmentValues()
        dark.colorScheme = .dark
        var light = dark
        light.colorScheme = .light
        let coordinator = Coordinator(products: [product], active: true, entrances: InventoryCardEntrances(), environment: dark, card: makeCard)
        let reference = Coordinator(products: [product], active: true, entrances: InventoryCardEntrances(), environment: light, card: makeCard)
        defer { coordinator.stop(); reference.stop() }
        let sample = try XCTUnwrap(stride(from: CGFloat(180), through: CGFloat(300), by: 5).compactMap { width -> (CGFloat, CGFloat, CGFloat)? in
            let a = coordinator.height(at: 0, width: width)
            let b = reference.height(at: 0, width: width)
            return a == b ? nil : (width, a, b)
        }.first)
        let (window, scroll) = attach(coordinator)
        defer { window.close() }
        window.setContentSize(NSSize(width: sample.0 * 4 + 78, height: 1000))
        scroll.layoutSubtreeIfNeeded()
        coordinator.collection?.layoutSubtreeIfNeeded()
        settle()
        let width = try XCTUnwrap(coordinator.collection).bounds.width
        let cardWidth = (width - 78) / 4
        let expected = reference.height(at: 0, width: cardWidth)
        coordinator.update(products: [product], active: true, environment: light, card: makeCard)
        coordinator.collection?.layoutSubtreeIfNeeded()
        settle()
        XCTAssertEqual(coordinator.height(at: 0, width: cardWidth), expected, accuracy: 0.5)
        let actual = try XCTUnwrap(coordinator.collection?.collectionViewLayout?.layoutAttributesForItem(at: IndexPath(item: 0, section: 0))?.frame.height)
        XCTAssertEqual(actual, expected, accuracy: 0.5)
    }

    @MainActor
    func testAccessibilityChildrenAreAttachedViewportHostsAcrossBatches() throws {
        let coordinator = makeCoordinator(count: 333)
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        func exposedIndices() throws -> [Int] {
            let children = try XCTUnwrap(collection.accessibilityChildren())
            XCTAssertFalse(children.isEmpty)
            return try children.map { child in
                let host = try XCTUnwrap(child as? NSView)
                XCTAssertTrue(host.window === window)
                let frame = host.convert(host.bounds, to: collection)
                XCTAssertTrue(frame.intersects(scroll.contentView.bounds))
                let item = try XCTUnwrap(collection.visibleItems().first { item in
                    (item as? InventoryCollectionItem)?.host === host
                })
                return try XCTUnwrap(collection.indexPath(for: item)).item
            }
        }
        let initial = try exposedIndices()
        XCTAssertTrue(initial.contains(0))
        XCTAssertFalse(initial.contains(99))
        coordinator.requestEdge(bottom: true)
        XCTAssertTrue(wait { coordinator.renderedCount == 333 && !coordinator.hasPendingScroll })
        let final = try exposedIndices()
        let geometry = "indices=\(final), clip=\(scroll.contentView.bounds), document=\(collection.bounds), " +
            "pending=\(coordinator.hasPendingScroll), mutation=\(coordinator.mutationInProgress)"
        XCTAssertTrue(final.contains(332), geometry)
        XCTAssertTrue(final.allSatisfy { $0 >= 300 }, geometry)
        XCTAssertEqual(scroll.contentView.bounds.maxY, collection.bounds.maxY, accuracy: 0.5, geometry)
        let last = try XCTUnwrap(collection.collectionViewLayout?.layoutAttributesForItem(at: IndexPath(item: 332, section: 0)))
        XCTAssertEqual(last.frame.width, (collection.bounds.width - 78) / 4, accuracy: 0.5, geometry)
        XCTAssertEqual(final, final.sorted())
        XCTAssertEqual(Set(final).count, final.count)
        XCTAssertLessThan(final.count, 30)
    }

    @MainActor
    func testRebindingProductKeepsHostAndResetsOnlyTheSwiftUIIdentity() throws {
        _ = NSApplication.shared
        var observed: [String] = []
        let item = InventoryCollectionItem()
        XCTAssertTrue(item.setRoot(AnyView(StateIdentityCard(initial: "first", observe: { observed.append($0) }).id("a")), id: "a"))
        let host = try XCTUnwrap(item.host)
        let window = NSWindow(contentRect: NSRect(x: -10000, y: -10000, width: 240, height: 200),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = item.view
        defer { window.close() }
        item.view.layoutSubtreeIfNeeded()
        settle()
        XCTAssertEqual(observed.last, "first")
        observed.removeAll()
        XCTAssertFalse(item.setRoot(AnyView(StateIdentityCard(initial: "unused", observe: { observed.append($0) }).id("a")), id: "a"))
        item.view.layoutSubtreeIfNeeded()
        settle()
        XCTAssertTrue(item.host === host)
        XCTAssertEqual(observed.last, "first")
        observed.removeAll()
        XCTAssertFalse(item.setRoot(AnyView(StateIdentityCard(initial: "second", observe: { observed.append($0) }).id("b")), id: "b"))
        item.view.layoutSubtreeIfNeeded()
        settle()
        XCTAssertTrue(item.host === host)
        XCTAssertEqual(observed.last, "second")
    }

    @MainActor
    func testVisibleUpdatesReuseHostAndKeepScrollPosition() throws {
        let coordinator = makeCoordinator(count: 120)
        let (window, scroll) = attach(coordinator)
        defer { coordinator.stop(); window.close() }
        let collection = try XCTUnwrap(coordinator.collection)
        let visible = try XCTUnwrap(collection.visibleItems().compactMap { $0 as? InventoryCollectionItem }.first)
        let host = try XCTUnwrap(visible.host)
        let created = coordinator.createdHostingViews
        let origin = scroll.contentView.bounds.origin
        coordinator.update(products: products(count: 120), active: true, environment: EnvironmentValues(),
                           card: { AnyView(Text("Selected " + $0.title).frame(minHeight: 140).padding(16)) })
        settle()
        XCTAssertTrue(visible.host === host)
        XCTAssertEqual(coordinator.createdHostingViews, created)
        XCTAssertEqual(scroll.contentView.bounds.origin, origin)
        XCTAssertEqual(coordinator.renderedCount, 100)
    }

    @MainActor
    private func makeCoordinator(count: Int) -> Coordinator {
        Coordinator(products: products(count: count), active: true, entrances: InventoryCardEntrances(),
                    environment: EnvironmentValues()) { product in
            AnyView(VStack(alignment: .leading) {
                Text(product.title)
                Button("Open " + product.title) {}
            }.frame(maxWidth: .infinity, minHeight: 140).padding(16))
        }
    }

    private func products(count: Int) -> [InventoryTreeNode] {
        (0..<count).map { InventoryTreeNode(id: "item-\($0)", item: nil, title: "Item \($0)") }
    }

    @MainActor
    private func attach(_ coordinator: Coordinator) -> (NSWindow, NativeInventoryScrollView) {
        _ = NSApplication.shared
        let scroll = coordinator.makeScrollView()
        let window = NSWindow(contentRect: NSRect(x: -10000, y: -10000, width: 960, height: 640),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = scroll
        scroll.layoutSubtreeIfNeeded()
        settle()
        return (window, scroll)
    }

    @MainActor
    private func settle() { RunLoop.main.run(until: Date().addingTimeInterval(0.03)) }

    @MainActor
    private func wait(_ condition: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(3)
        while !condition(), Date() < deadline { settle() }
        return condition()
    }
}

private struct StateIdentityCard: View {
    @State private var identity: String
    let observe: (String) -> Void

    init(initial: String, observe: @escaping (String) -> Void) {
        _identity = State(initialValue: initial)
        self.observe = observe
    }

    var body: some View {
        let _ = observe(identity)
        Text(identity).frame(width: 200, height: 140)
    }
}
