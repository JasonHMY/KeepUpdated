import AppKit
import SwiftUI
import XCTest
@testable import KeepUpdatedDesktop

final class InventoryCategoryLayoutTests: XCTestCase {
    @MainActor
    func testMagnificationReservesSpaceWithoutCompressingControl() {
        let normal = NSHostingView(rootView: CategoryScaleLayout(scale: 1) {
            Color.clear.frame(width: 100, height: 34)
        })
        let selected = NSHostingView(rootView: CategoryScaleLayout(scale: 1.12) {
            Color.clear.frame(width: 100, height: 34).scaleEffect(1.12)
        })
        XCTAssertEqual(normal.fittingSize.width, 100, accuracy: 1.1)
        XCTAssertEqual(selected.fittingSize.width, 112, accuracy: 1.1)
        XCTAssertEqual(selected.fittingSize.height, 38.08, accuracy: 1.1)
    }

    @MainActor
    func testLongLocalizedLabelAndCountKeepTheirFullWidth() {
        let previousDefaults = L10n.defaults
        let suite = "KeepUpdatedCategoryLayoutTests." + UUID().uuidString
        L10n.defaults = UserDefaults(suiteName: suite)!
        defer {
            L10n.defaults.removePersistentDomain(forName: suite)
            L10n.defaults = previousDefaults
        }
        for language in [AppLanguage.english, .simplifiedChinese] {
            L10n.defaults.set(language.rawValue, forKey: L10n.preferenceKey)
            let normal = NSHostingView(rootView: InventoryCategoryButton(
                category: .all, count: 12345, selected: false, action: {}))
            let selected = NSHostingView(rootView: InventoryCategoryButton(
                category: .all, count: 12345, selected: true, action: {}))
            XCTAssertEqual(selected.fittingSize.width, normal.fittingSize.width * 1.12, accuracy: 1.1)
            XCTAssertLessThan(selected.fittingSize.height + 20, 64)
        }
    }

    @MainActor
    func testSelectingMiddleControlMovesNeighborsApartAndKeepsCentersAligned() {
        func frames(selected: Int) -> [Int: CGRect] {
            var frames: [Int: CGRect] = [:]
            let view = NSHostingView(rootView: HStack(spacing: 8) {
                ForEach(0..<3) { index in
                    CategoryScaleLayout(scale: index == selected ? 1.12 : 1) {
                        Color.clear.frame(width: 100, height: 34)
                    }
                    .background(GeometryReader { geometry in
                        Color.clear.preference(key: CategoryFrames.self,
                                               value: [index: geometry.frame(in: .named("row"))])
                    })
                }
            }.frame(width: 500, height: 64).coordinateSpace(name: "row")
                .onPreferenceChange(CategoryFrames.self) { frames = $0 })
            view.frame = CGRect(x: 0, y: 0, width: 500, height: 64)
            view.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
            return frames
        }
        let first = frames(selected: -1)
        let middle = frames(selected: 1)
        XCTAssertEqual(first.count, 3)
        XCTAssertEqual(middle.count, 3)
        guard let oldLeft = first[0], let newLeft = middle[0],
              let oldRight = first[2], let newRight = middle[2], let center = middle[1] else { return }
        XCTAssertLessThan(newLeft.midX, oldLeft.midX)
        XCTAssertGreaterThan(newRight.midX, oldRight.midX)
        XCTAssertEqual(newLeft.maxX + 8, center.minX, accuracy: 1.1)
        XCTAssertEqual(center.maxX + 8, newRight.minX, accuracy: 1.1)
        XCTAssertEqual(newLeft.midY, center.midY, accuracy: 1.1)
        XCTAssertEqual(newRight.midY, center.midY, accuracy: 1.1)
    }
}

private struct CategoryFrames: PreferenceKey {
    static var defaultValue: [Int: CGRect] = [:]
    static func reduce(value: inout [Int: CGRect], nextValue: () -> [Int: CGRect]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}
