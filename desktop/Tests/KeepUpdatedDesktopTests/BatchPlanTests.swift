import XCTest
@testable import KeepUpdatedDesktop

final class BatchPlanTests: XCTestCase {
    func testArgumentCountFailureIsRecoverableAndBoundaryStillLaunches() async throws {
        let client = CLIClient(testExecutable: URL(fileURLWithPath: "/usr/bin/true"))
        do {
            _ = try await client.run(Array(repeating: "id", count: 4796))
            XCTFail("Oversized argument list must fail without terminating the app")
        } catch {
            XCTAssertTrue(error is ClientError)
        }
        let result = try await client.run(Array(repeating: "id", count: 4096))
        XCTAssertEqual(result.status, 0)
    }

    @MainActor
    func testLargeMixedUpdateAllPreservesSelectionAndExactPlanBytes() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let root = files.directory
        let managed = (0..<3000).map { DemoData.item("managed-\($0)", "Example \($0)", "node-package", "npm", "unknown") }
        let plan = DemoData.plan(action: .update, items: managed, cleanup: false)
        let data = Data(" \n".utf8) + (try DemoData.planData(plan, privilegedRequest: nil)) + Data("\n ".utf8)
        try data.write(to: root.appendingPathComponent("response"))
        let script = try PrivatePlanFile(data: Data("""
        #!/bin/sh
        root='\(root.path)'
        command="$1"; shift
        printf '%s\\n' "$command" >> "$root/commands"
        printf '%s\\n' "$@" > "$root/arguments"
        case "$command" in
        plan)
            while [ "$#" -gt 0 ]; do
                case "$1" in
                --ids-file)
                    shift
                    printf '%s' "$1" > "$root/selection-path"
                    stat -f '%Lp' "$1" > "$root/file-mode"
                    stat -f '%Lp' "$(dirname "$1")" > "$root/directory-mode"
                    cp "$1" "$root/selection"
                    ;;
                esac
                shift
            done
            cat "$root/response" ;;
        apply)
            while [ "$#" -gt 0 ]; do
                case "$1" in --plan) shift; cp "$1" "$root/applied-plan" ;; esac
                shift
            done
            printf '%s' '{"schema":"keepupdated/apply/v1","plan_id":"\(plan.planId)","started_at":"","finished_at":"","results":[]}' ;;
        scan)
            printf '%s' '{"schema":"keepupdated/v1","command":"scan","generated_at":"","items":[],"diagnostics":[],"coverage":[]}' ;;
        esac
        """.utf8))
        defer { script.remove() }
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.url.path)
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        var report = DemoData.report
        report.items = managed + [DemoData.appStoreItem, DemoData.manualAppItem]
        store.replaceReport(report)
        store.plan(action: .update, all: true)
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(store.preview?.items.map(\.id), managed.map(\.id))
        XCTAssertEqual(store.manualUpdates.count, 2)
        let arguments = try String(contentsOf: root.appendingPathComponent("arguments")).split(separator: "\n")
        XCTAssertFalse(arguments.contains("--all"))
        XCTAssertFalse(arguments.contains("--special"))
        XCTAssertTrue(arguments.contains("--progress"))
        let selection = try JSONDecoder().decode([String].self, from: Data(contentsOf: root.appendingPathComponent("selection")))
        XCTAssertEqual(selection, managed.map(\.id))
        XCTAssertEqual(try String(contentsOf: root.appendingPathComponent("file-mode")), "600\n")
        XCTAssertEqual(try String(contentsOf: root.appendingPathComponent("directory-mode")), "700\n")
        let path = try String(contentsOf: root.appendingPathComponent("selection-path"))
        XCTAssertFalse(FileManager.default.fileExists(atPath: path), "Selection file must be removed after the command")
        XCTAssertEqual(try String(contentsOf: root.appendingPathComponent("commands")), "plan\n", "Preview cannot apply updates")
        store.execute()
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(try Data(contentsOf: root.appendingPathComponent("applied-plan")), data)
    }

    @MainActor
    func testBatchSelectionFileIsRemovedOnCancellationAndCommandFailure() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let root = files.directory
        let script = try PrivatePlanFile(data: Data("""
        #!/bin/sh
        root='\(root.path)'
        while [ "$#" -gt 0 ]; do
            case "$1" in --ids-file) shift; printf '%s' "$1" > "$root/selection-path" ;; esac
            shift
        done
        if [ -f "$root/fail" ]; then printf 'controlled preview failure' >&2; exit 2; fi
        printf '%s\\n' '{"schema":"keepupdated/progress/v1","phase":"plan","completed":0,"total":null,"current":"Fixture","state":"running","unresolved":0}' >&2
        while :; do sleep 0.01; done
        """.utf8))
        defer { script.remove() }
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.url.path)
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        let ids = (0..<3000).map { "managed-\($0)" }
        store.plan(action: .update, ids: ids)
        try await waitUntil { store.progress != nil }
        let path = try String(contentsOf: root.appendingPathComponent("selection-path"))
        XCTAssertTrue(FileManager.default.fileExists(atPath: path))
        store.cancel()
        try await waitUntil { !store.busy }
        XCTAssertFalse(FileManager.default.fileExists(atPath: path))
        XCTAssertNil(store.preview)
        try Data().write(to: root.appendingPathComponent("fail"))
        store.plan(action: .update, ids: ids)
        try await waitUntil { !store.busy }
        XCTAssertNotNil(store.error)
        XCTAssertTrue(store.errorDetail?.contains("controlled preview failure") == true)
        XCTAssertNil(store.preview)
        XCTAssertFalse(store.hasConfirmedPlan)
        let failedPath = try String(contentsOf: root.appendingPathComponent("selection-path"))
        XCTAssertFalse(FileManager.default.fileExists(atPath: failedPath))
        store.dismissError()
        XCTAssertNil(store.error)
    }

    @MainActor
    private func waitUntil(_ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(10)
        while !condition() && Date() < deadline { try await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertTrue(condition(), "Condition did not become true")
    }
}
