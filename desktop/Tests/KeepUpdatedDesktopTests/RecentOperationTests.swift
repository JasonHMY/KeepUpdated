import XCTest
@testable import KeepUpdatedDesktop

final class RecentOperationTests: XCTestCase {
    private let operationID = String(repeating: "a", count: 64)

    private func saved(_ action: OperationAction = .uninstall, unfinished: Bool = true) -> ApplyReport {
        ApplyReport(schema: "keepupdated/apply/v1", planId: operationID,
                    startedAt: "2026-10-19T01:00:00Z", finishedAt: unfinished ? "" : "2026-10-19T01:01:00Z", results: [
            .init(id: "done", name: "Same Name", status: "uninstalled", backupPath: "/private/fixture/backup"),
            .init(id: "original-failed", name: "Same Name", status: unfinished ? "verification_unknown" : "failed", reason: unfinished ? "operation_in_progress_or_interrupted" : "manager_command_failed", recoveryPath: "/private/fixture/recovery"),
            .init(id: "original-pending", name: "Later Item", status: "skipped", reason: "operation_not_started")
        ], action: action, state: unfinished ? "in_progress" : "finished", originalIds: ["done", "original-failed", "original-pending"])
    }
    private func envelope(_ report: ApplyReport) throws -> Data {
        let reportObject = try JSONSerialization.jsonObject(with: ReportJSON.encode(report))
        return try JSONSerialization.data(withJSONObject: ["schema": "keepupdated/recent-operation/v1", "operation": reportObject])
    }
    private func fixture(_ report: ApplyReport) throws -> PrivatePlanFile {
        let file = try PrivatePlanFile(data: Data())
        let root = file.directory
        try envelope(report).write(to: root.appendingPathComponent("saved"))
        var inventory = DemoData.report
        inventory.items = [DemoData.item("original-failed", "Same Name", "node-package", "npm", "available"),
                           DemoData.item("replacement", "Same Name", "node-package", "npm", "available")]
        try ReportJSON.encode(inventory).write(to: root.appendingPathComponent("inventory"))
        let plan = DemoData.plan(action: report.action!, items: [inventory.items[0]], cleanup: false)
        try DemoData.planData(plan, privilegedRequest: nil).write(to: root.appendingPathComponent("plan"))
        let script = """
        #!/bin/sh
        root='\(root.path)'
        printf '%s\\n' "$*" >> "$root/calls"
        case "$1" in
        results) cat "$root/saved" ;;
        scan) cat "$root/inventory" ;;
        plan)
            shift
            while [ "$#" -gt 0 ]; do
                case "$1" in --id) shift; printf '%s\\n' "$1" >> "$root/selected" ;; --ids-file) shift; cp "$1" "$root/selected" ;; esac
                shift
            done
            cat "$root/plan" ;;
        apply) printf 'unexpected apply' >&2; exit 2 ;;
        *) exit 2 ;;
        esac
        """
        try Data(script.utf8).write(to: file.url)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: file.url.path)
        return file
    }
    @MainActor private func wait(_ store: InventoryStore) async throws {
        for _ in 0..<500 {
            if !store.busy { return }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTFail("Store did not finish")
    }

    @MainActor func testNewStoreRestoresPartialResultsWithoutExecutablePlanOrMutation() async throws {
        for unfinished in [true, false] {
            let file = try fixture(saved(unfinished: unfinished))
            defer { file.remove() }
            for _ in 0..<2 {
                let store = InventoryStore(demo: false, client: CLIClient(testExecutable: file.url))
                await store.restoreLastResults()
                XCTAssertFalse(store.busy)
                XCTAssertFalse(store.recoveryReadFailed)
                XCTAssertEqual(store.lastResults?.planId, operationID)
                XCTAssertEqual(store.lastResults?.completed.map(\.id), ["done"])
                XCTAssertEqual(store.lastResults?.retryable.map(\.id), ["original-failed", "original-pending"])
                XCTAssertEqual(store.lastResults?.results[0].backupPath, "/private/fixture/backup")
                XCTAssertEqual(store.lastResults?.results[1].recoveryPath, "/private/fixture/recovery")
                XCTAssertEqual(store.lastResults?.isUnfinished, unfinished)
                XCTAssertEqual(store.recoveryNotice != nil, unfinished)
                XCTAssertFalse(store.hasConfirmedPlan)
                XCTAssertNil(store.preview)
                store.execute()
                XCTAssertFalse(store.busy)
                store.showLastResults()
                XCTAssertEqual(store.results?.planId, operationID)
            }
            let calls = try String(contentsOf: file.directory.appendingPathComponent("calls"), encoding: .utf8)
            XCTAssertEqual(calls, "results\nresults\n", "Startup/readback must not apply, scan, quit or restart")
        }
    }

    @MainActor func testRecoveredRetryUsesFreshScanAndOriginalIDsAndAction() async throws {
        for action in [OperationAction.update, .uninstall] {
            let file = try fixture(saved(action))
            defer { file.remove() }
            let store = InventoryStore(demo: false, client: CLIClient(testExecutable: file.url))
            await store.restoreLastResults()
            store.showLastResults()
            store.retryResults(ids: ["original-failed", "original-pending", "done", "replacement"])
            XCTAssertNil(store.preview)
            store.resultsSheetDismissed()
            try await wait(store)
            XCTAssertNil(store.error)
            XCTAssertEqual(store.preview?.action, action)
            XCTAssertEqual(store.preview?.items.map(\.id), ["original-failed"])
            XCTAssertNotEqual(store.preview?.planId, operationID)
            XCTAssertTrue(store.hasConfirmedPlan)
            XCTAssertEqual(store.lastResults?.planId, operationID)
            let selected = try String(contentsOf: file.directory.appendingPathComponent("selected"), encoding: .utf8)
            XCTAssertEqual(selected, "original-failed\n")
            let calls = try String(contentsOf: file.directory.appendingPathComponent("calls"), encoding: .utf8)
            XCTAssertTrue(calls.hasPrefix("results\nscan"))
            XCTAssertTrue(calls.contains("--action \(action.rawValue)"))
            XCTAssertFalse(calls.contains("apply"))
            XCTAssertFalse(calls.contains("--consent"))
            XCTAssertFalse(calls.contains("--quit-app"))
            XCTAssertFalse(calls.contains("--reopen-app"))
        }
    }

    @MainActor func testUnreadableRecordStaysVisibleThroughScanAndCanBeReloaded() async throws {
        let file = try fixture(saved())
        defer { file.remove() }
        try Data("{".utf8).write(to: file.directory.appendingPathComponent("saved"))
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: file.url))
        await store.restoreLastResults()
        XCTAssertTrue(store.recoveryReadFailed)
        XCTAssertNotNil(store.recoveryNotice)
        XCTAssertNil(store.lastResults)
        XCTAssertFalse(store.hasConfirmedPlan)
        store.refresh(online: false)
        try await wait(store)
        XCTAssertTrue(store.recoveryReadFailed, "A successful scan cannot conceal unreadable saved results")
        try envelope(saved(unfinished: false)).write(to: file.directory.appendingPathComponent("saved"))
        store.reloadLastResults()
        try await wait(store)
        XCTAssertFalse(store.recoveryReadFailed)
        XCTAssertEqual(store.results?.state, "finished")
    }

    @MainActor func testFailedApplyRestoresRecordedOutcomeAndDiscardsExactPlan() async throws {
        let file = try fixture(saved())
        defer { file.remove() }
        var plan = DemoData.plan(action: .uninstall, items: [DemoData.item("original-failed", "Same Name", "node-package", "npm", "available")], cleanup: false)
        plan.planId = operationID
        try DemoData.planData(plan, privilegedRequest: nil).write(to: file.directory.appendingPathComponent("plan"))
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: file.url))
        store.plan(action: .uninstall, ids: ["original-failed"])
        try await wait(store)
        XCTAssertTrue(store.hasConfirmedPlan)
        store.execute()
        try await wait(store)
        XCTAssertNotNil(store.error)
        XCTAssertNil(store.preview)
        XCTAssertFalse(store.hasConfirmedPlan)
        XCTAssertEqual(store.lastResults?.planId, operationID)
        XCTAssertTrue(store.lastResults?.isUnfinished == true)
        store.presentPendingResults()
        XCTAssertEqual(store.results?.planId, operationID)
        store.execute()
        let calls = try String(contentsOf: file.directory.appendingPathComponent("calls"), encoding: .utf8).split(separator: "\n")
        XCTAssertEqual(calls.filter { $0.hasPrefix("apply ") }.count, 1)
        XCTAssertEqual(calls.last, "results")
    }

    func testInvalidVersionsBoundsAndIdentityCannotRestore() throws {
        let data = try envelope(saved())
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        for key in ["schema", "action", "state", "original_ids", "finished_at", "plan_id"] {
            var bad = object
            if key == "schema" { bad["schema"] = "keepupdated/recent-operation/v2" }
            else {
                var report = try XCTUnwrap(bad["operation"] as? [String: Any])
                report[key] = key == "original_ids" ? ["wrong"] : "wrong"
                bad["operation"] = report
            }
            XCTAssertThrowsError(try RecentOperation.decode(JSONSerialization.data(withJSONObject: bad)), key)
        }
        XCTAssertThrowsError(try RecentOperation.decode(data + Data(repeating: 32, count: 8 * 1024 * 1024 + 2 - data.count)))
        XCTAssertThrowsError(try RecentOperation.decode(Data("{".utf8)))
        XCTAssertNil(try RecentOperation.decode(Data(#"{"schema":"keepupdated/recent-operation/v1","operation":null}"#.utf8)).operation)
    }
}
