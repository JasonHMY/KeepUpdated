import XCTest
@testable import KeepUpdatedDesktop

final class DuplicateActionTests: XCTestCase {
    private func plan() throws -> OperationPlan {
        try ReportJSON.decode(OperationPlan.self, from: Data(#"{"schema":"keepupdated/plan/v1","plan_id":"fixture","created_at":"2026-10-20T00:00:00Z","expires_at":"2026-10-20T00:30:00Z","action":"update","items":[{"id":"a","name":"Same name","status":"ready","reason":"original_manager_verified"},{"id":"z","name":"Same name","status":"skipped","reason":"duplicate_installation_target","canonical_id":"a"}]}"#.utf8), schema: "keepupdated/plan/v1")
    }

    func testPreviewRelationshipUsesIDAndPreservesBothRows() throws {
        var plan = try plan()
        let reference = plan.items[1]
        XCTAssertEqual(plan.executable.map(\.id), ["a"])
        XCTAssertEqual(plan.sharedReferences.map(\.id), ["z"])
        XCTAssertEqual(plan.displayItems.count, 2)
        XCTAssertTrue(plan.noOperation.isEmpty)
        XCTAssertEqual(plan.canonicalAction(for: reference)?.id, "a")
        plan.items.reverse()
        XCTAssertEqual(plan.canonicalAction(for: reference)?.id, "a")
        plan.items.removeAll { $0.id == "a" }
        XCTAssertNil(plan.canonicalAction(for: reference))
    }

    func testPreviewRejectsNonReadySelfAndAmbiguousCanonical() throws {
        var plan = try plan()
        let reference = plan.items[1]
        for status in ["blocked", "manual", "skipped", "future-status"] {
            plan.items[0].status = status
            XCTAssertNil(plan.canonicalAction(for: reference))
        }
        plan.items[0].status = "ready"
        plan.items[0].canonicalId = "z"
        XCTAssertNil(plan.canonicalAction(for: reference))
        plan.items[0].canonicalId = nil
        plan.items.append(plan.items[0])
        XCTAssertNil(plan.canonicalAction(for: reference))
        var selfLink = reference; selfLink.canonicalId = "z"
        XCTAssertNil(plan.canonicalAction(for: selfLink))
    }

    func testResultsDoNotCountReferenceAsSuccessOrRetryIt() throws {
        for status in ["updated", "failed", "verification_unknown", "blocked", "skipped"] {
            let data = Data("""
            {"schema":"keepupdated/apply/v1","plan_id":"fixture","started_at":"2026-10-20T00:00:00Z","finished_at":"2026-10-20T00:01:00Z","results":[{"id":"a","name":"Shared tool","status":"\(status)"},{"id":"z","name":"Reference","status":"skipped","reason":"duplicate_installation_target","canonical_id":"a"}]}
            """.utf8)
            var report = try ReportJSON.decode(ApplyReport.self, from: data, schema: "keepupdated/apply/v1")
            let reference = report.results[1]
            XCTAssertEqual(report.canonicalResult(for: reference)?.status, status)
            XCTAssertFalse(reference.isCompleted)
            XCTAssertEqual(report.completed.count, status == "updated" ? 1 : 0)
            XCTAssertFalse(report.retryable.map(\.id).contains("z"))
            report.results.reverse()
            XCTAssertEqual(report.canonicalResult(for: reference)?.id, "a")
            report.results.removeAll { $0.id == "a" }
            XCTAssertNil(report.canonicalResult(for: reference))
        }
    }

    func testLegacyRecordsHaveNoInventedRelationship() throws {
        var plan = try plan()
        plan.items[1].canonicalId = nil
        let data = try ReportJSON.encode(plan)
        let decoded = try ReportJSON.decode(OperationPlan.self, from: data, schema: "keepupdated/plan/v1")
        XCTAssertNil(decoded.items[1].canonicalId)
        XCTAssertNil(decoded.canonicalAction(for: decoded.items[1]))
        XCTAssertEqual(decoded.noOperation.count, 1)
        XCTAssertTrue(decoded.sharedReferences.isEmpty)
    }

    func testBilingualRelationshipAndFailureCopy() {
        for language in [AppLanguage.english, .simplifiedChinese] {
            let text = L10n.format("Shared action: %@ (%@) · %@", arguments: ["Tool", "a", "Failed"], language: language)
            XCTAssertTrue(text.contains("Tool"))
            XCTAssertTrue(text.contains("a"))
            XCTAssertFalse(text.contains("%@"))
            for key in ["The shared action did not run. This reference was not updated separately.", "The shared action did not finish with a verified result. This reference is not confirmed updated."] {
                XCTAssertNotNil(L10n.catalog(language)[key])
            }
        }
    }
}
