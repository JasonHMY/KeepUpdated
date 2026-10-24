import XCTest
@testable import KeepUpdatedDesktop

final class ProductInstallationSelectionTests: XCTestCase {
    func testFirstVisitIncludesAllClientLocationsAndVersions() {
        let selection = ProductInstallationSelection(ids: ["shared", "codex", "claude", "java-17", "java-21", "shared"])
        XCTAssertEqual(selection.selectedIDs, ["shared", "codex", "claude", "java-17", "java-21"])
    }

    func testRescanRetainsChoicesWithoutSelectingNewLocations() {
        var selection = ProductInstallationSelection(ids: ["shared", "codex", "claude"])
        selection.setSelected("codex", selected: false)
        selection.reconcile(ids: ["shared", "codex", "hermes"])
        XCTAssertEqual(selection.selectedIDs, ["shared"])
        selection.setSelected("hermes", selected: true)
        XCTAssertEqual(selection.selectedIDs, ["shared", "hermes"], "A new location requires an explicit choice")
    }

    func testSupportingOrMissingIDsCannotEnterAnOperationScope() {
        var selection = ProductInstallationSelection(ids: ["parent-app"])
        selection.setSelected("app-component", selected: true)
        selection.setSelected("dependency", selected: true)
        XCTAssertEqual(selection.selectedIDs, ["parent-app"])
        selection.reconcile(ids: [])
        selection.setSelected("parent-app", selected: true)
        XCTAssertTrue(selection.selectedIDs.isEmpty)
    }

    func testEmptyChoiceSurvivesReopeningAndRescanning() {
        var remembered = ProductInstallationSelection(ids: ["java-17", "java-21"])
        remembered.setSelected("java-17", selected: false)
        remembered.setSelected("java-21", selected: false)
        remembered.reconcile(ids: ["java-17", "java-21", "java-25"])
        XCTAssertTrue(remembered.selectedIDs.isEmpty)
    }

    func testRegroupingRetainsChoicesWithoutSelectingNewInstallations() throws {
        var first = ProductInstallationSelection(ids: ["java-17"])
        first.setSelected("java-17", selected: false)
        let second = ProductInstallationSelection(ids: ["java-21"])
        let grouped = try XCTUnwrap(ProductInstallationSelection.restoring(ids: ["java-17", "java-21", "java-25"], previous: [first, second]))
        XCTAssertEqual(grouped.selectedIDs, ["java-21"])

        let splitFirst = try XCTUnwrap(ProductInstallationSelection.restoring(ids: ["java-17"], previous: [grouped]))
        let splitSecond = try XCTUnwrap(ProductInstallationSelection.restoring(ids: ["java-21"], previous: [grouped]))
        let regrouped = try XCTUnwrap(ProductInstallationSelection.restoring(ids: ["java-17", "java-21"], previous: [splitFirst, splitSecond]))
        XCTAssertEqual(regrouped.selectedIDs, ["java-21"])
        XCTAssertNil(ProductInstallationSelection.restoring(ids: ["unrelated"], previous: [grouped]))
    }

    func testMissingProductReturnsWithUsableRememberedChoices() throws {
        var choice = ProductInstallationSelection(ids: ["shared", "codex"])
        choice.setSelected("codex", selected: false)
        let missing = ProductInstallationSelection.reconciling(["writer": choice], products: [:])
        XCTAssertEqual(missing["writer"], choice)

        let returned = ProductInstallationSelection.reconciling(missing,
            products: ["writer": ["shared", "codex", "hermes"]])
        var restored = try XCTUnwrap(returned["writer"])
        XCTAssertEqual(restored.selectedIDs, ["shared"])
        XCTAssertEqual(restored.availableIDs, ["shared", "codex", "hermes"])
        restored.setSelected("codex", selected: true)
        XCTAssertEqual(restored.selectedIDs, ["shared", "codex"], "Checkbox choices must work after reappearance")
        XCTAssertFalse(restored.selectedIDs.contains("hermes"), "New installations remain unselected")
    }

    func testMissingProductRegroupsWithoutRevivingSupersededChoices() throws {
        var original = ProductInstallationSelection(ids: ["java-17", "java-21"])
        original.setSelected("java-21", selected: false)
        let missing = ProductInstallationSelection.reconciling(["old-group": original], products: [:])
        let regrouped = ProductInstallationSelection.reconciling(missing,
            products: ["new-group": ["java-17", "java-21", "java-25"]])
        XCTAssertNil(regrouped["old-group"], "Superseded groups must not retain stale choices")
        XCTAssertEqual(regrouped["new-group"]?.selectedIDs, ["java-17"])

        var split = ProductInstallationSelection.reconciling(regrouped,
            products: ["first": ["java-17"], "second": ["java-21"]])
        XCTAssertNil(split["new-group"])
        split["first"]?.setSelected("java-17", selected: false)
        let mergedAgain = ProductInstallationSelection.reconciling(split,
            products: ["old-group": ["java-17", "java-21"]])
        XCTAssertTrue(try XCTUnwrap(mergedAgain["old-group"]).selectedIDs.isEmpty)
    }

    func testReturningGroupDoesNotOverrideAChoiceMadeWhileOnlyOneInstallationWasPresent() throws {
        let allSelected = ProductInstallationSelection(ids: ["a", "b"])
        var single = ProductInstallationSelection.reconciling(["group": allSelected], products: ["single-a": ["a"]])
        XCTAssertNil(single["group"])
        single["single-a"]?.setSelected("a", selected: false)

        let returned = ProductInstallationSelection.reconciling(single, products: ["group": ["a", "b"]])
        var selection = try XCTUnwrap(returned["group"])
        XCTAssertTrue(selection.selectedIDs.isEmpty, "Neither an old group nor a returned location may restore cancelled scope")
        selection.setSelected("a", selected: true)
        XCTAssertEqual(selection.selectedIDs, ["a"], "The returned group's checkbox still accepts a new explicit choice")
    }
}
