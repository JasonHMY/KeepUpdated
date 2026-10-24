import XCTest
@testable import KeepUpdatedDesktop

final class VoltaPresentationTests: XCTestCase {
    func testVoltaOwnershipAndStoredSelectionInBothLanguages() {
        let previous = L10n.defaults
        let suite = "KeepUpdatedVoltaTests." + UUID().uuidString
        L10n.defaults = UserDefaults(suiteName: suite)!
        defer { L10n.defaults.removePersistentDomain(forName: suite); L10n.defaults = previous }
        var item = DemoData.item("volta", "node", "runtime", "volta", "unsupported", code: "volta_owner_manual")
        item.activityKnown = true
        item.active = true
        for language in ["en", "zh-Hans"] {
            L10n.defaults.set(language, forKey: L10n.preferenceKey)
            XCTAssertEqual(item.statusLabel, language == "en" ? "Managed by Volta" : "由 Volta 管理")
            XCTAssertTrue(item.statusExplanation.contains("Volta"))
            XCTAssertEqual(item.statusExplanation, ReasonText.describe("volta_owner_manual"))
            XCTAssertFalse(item.canUpdate)
            item.kind = "runtime"; item.active = true
            XCTAssertTrue(VoltaPresentation.selection(item).contains(language == "en" ? "stored default" : "保存的默认"))
            item.active = false
            XCTAssertTrue(VoltaPresentation.selection(item).contains(language == "en" ? "Retained" : "保留"))
            item.activityKnown = false
            XCTAssertTrue(VoltaPresentation.selection(item).contains(language == "en" ? "unknown" : "未知"))
            item.kind = "runtime-pin"
            XCTAssertEqual(item.category, .runtimes)
            XCTAssertEqual(item.displayName, language == "en" ? "Volta project pin" : "Volta 项目版本固定")
            XCTAssertTrue(VoltaPresentation.selection(item).contains(language == "en" ? "does not install" : "不会安装"))
            item.activityKnown = true
        }
    }
}
