import XCTest
import SwiftUI
@testable import KeepUpdatedDesktop

final class ManagerToolbarTests: XCTestCase {
    @MainActor
    func testLanguageChangeRebuildsPopupWithoutChangingFilter() throws {
        let previous = L10n.defaults
        let suite = "ManagerToolbarLanguageTests." + UUID().uuidString
        L10n.defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer {
            L10n.defaults.removePersistentDomain(forName: suite)
            L10n.defaults = previous
        }
        var selected = InventoryStatusFilter.manual
        let binding = Binding(get: { selected }, set: { selected = $0 })
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        let controller = NSHostingController(rootView: ToolbarFixture(category: "Skills", status: binding,
            state: 2, language: .simplifiedChinese))
        for language in [AppLanguage.simplifiedChinese, .english, .simplifiedChinese] {
            L10n.defaults.set(language.rawValue, forKey: L10n.preferenceKey)
            controller.rootView = ToolbarFixture(category: "Skills", status: binding, state: 2, language: language)
            let actual = try toolbarImage(controller)
            let fresh = NSHostingController(rootView: ToolbarFixture(category: "Skills", status: binding,
                state: 2, language: language))
            XCTAssertEqual(actual, try toolbarImage(fresh), "A language change should render the same labels as a fresh toolbar")
            XCTAssertEqual(selected, .manual)
            XCTAssertEqual(ManagerToolbar.countTitle(category: "Apps", count: 1), language == .english ? "Apps 1 item" : "Apps 1 项")
            XCTAssertEqual(ManagerToolbar.countTitle(category: "Apps", count: 0), language == .english ? "Apps 0 items" : "Apps 0 项")
            XCTAssertEqual(ManagerToolbar.countTitle(category: "Apps", count: 2), language == .english ? "Apps 2 items" : "Apps 2 项")
        }
    }

    @MainActor
    private func toolbarImage(_ controller: NSHostingController<ToolbarFixture>) throws -> Data {
        let host = controller.view
        host.frame = NSRect(x: 0, y: 0, width: 936, height: 64)
        host.layoutSubtreeIfNeeded()
        let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: bitmap)
        return try XCTUnwrap(bitmap.representation(using: .png, properties: [:]))
    }

    @MainActor
    func testToolbarFitsMinimumAndCommonWidthsInBothLanguages() throws {
        let previous = L10n.defaults
        let suite = "ManagerToolbarTests." + UUID().uuidString
        L10n.defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer {
            L10n.defaults.removePersistentDomain(forName: suite)
            L10n.defaults = previous
        }
        for language in ["en", "zh-Hans"] {
            L10n.defaults.set(language, forKey: L10n.preferenceKey)
            for width in [936.0, 1176.0] {
                for category in ItemCategory.allCases {
                    for status in InventoryStatusFilter.allCases {
                        for state in [0, 1, 2] {
                            let fixture = ToolbarFixture(category: category.title, status: .constant(status), state: state, language: L10n.language)
                            let controller = NSHostingController(rootView: fixture)
                            let host = controller.view
                            let size = controller.sizeThatFits(in: NSSize(width: width, height: 100))
                            XCTAssertLessThanOrEqual(size.width, width, "\(language) \(category) \(status) state \(state)")
                            XCTAssertLessThanOrEqual(size.height, 64)
                            if let output = ProcessInfo.processInfo.environment["KEEPUPDATED_TOOLBAR_FIXTURES"],
                               category == .all, status == .all {
                                host.frame = NSRect(x: 0, y: 0, width: width, height: 64)
                                host.layoutSubtreeIfNeeded()
                                let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
                                host.cacheDisplay(in: host.bounds, to: bitmap)
                                let data = try XCTUnwrap(bitmap.representation(using: .png, properties: [:]))
                                try data.write(to: URL(fileURLWithPath: output).appendingPathComponent("\(language)-\(Int(width))-\(state).png"))
                            }
                        }
                    }
                }
            }
        }
    }
}

private struct ToolbarFixture: View {
    let category: String
    @Binding var status: InventoryStatusFilter
    let state: Int
    let language: AppLanguage
    @FocusState private var focused: Bool
    var body: some View {
        ManagerToolbar(category: category, displayCount: 729, language: language,
            query: .constant(state == 2 ? "Example" : ""), searchExpanded: .constant(state == 1),
            searchFocused: $focused, onlyUpdates: .constant(false), statusFilter: $status,
            canSelectAll: true, home: {}, back: {}, selectAll: {})
            .background(Color(nsColor: .windowBackgroundColor))
    }
}
