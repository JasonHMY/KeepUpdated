import XCTest
@testable import KeepUpdatedDesktop

final class OperationReviewTests: XCTestCase {
    private func item() -> PlanItem {
        PlanItem(id: "target", name: "Example", status: "ready", requiresAdmin: false, requiresQuit: false, requiresRestart: false, requiresScripts: false, specialApproved: true)
    }
    func testArchiveFormatPreservesLegacyZIPAndDMG() throws {
        for format in [nil, "dmg", "unknown"] as [String?] {
            var object: [String: Any] = ["version": "2.0", "build": "20", "length": 512]
            if let format { object["format"] = format }
            let data = try JSONSerialization.data(withJSONObject: object)
            let archive = try JSONDecoder().decode(ApplicationUpdatePreview.Archive.self, from: data)
            XCTAssertEqual(archive.format, format)
            let encoded = try JSONEncoder().encode(archive)
            let actual = try XCTUnwrap(JSONSerialization.jsonObject(with: encoded) as? [String: Any])
            XCTAssertEqual(actual["format"] as? String, format)
            if format == nil { XCTAssertNil(actual["format"]) }
        }
    }

    func testSpecialDoesNotGrantTypedOrUnknownConsents() {
        var item = item()
        item.requiredConsents = ["runtime_update", "privileged_install"]
        XCTAssertFalse(item.isExecutable)
        XCTAssertFalse(item.isExecutable(in: .init(special: true)))
        XCTAssertFalse(item.isExecutable(in: .init(consents: ["runtime_update"])))
        XCTAssertTrue(item.isExecutable(in: .init(consents: ["runtime_update", "privileged_install"])))
        item.requiresAdmin = true
        XCTAssertFalse(item.isExecutable(in: .init(special: true)))
        item.requiredConsents = []
        XCTAssertFalse(item.isExecutable(in: .init(special: true, consents: ["privileged_install"])))
        item.requiresAdmin = false
        item.requiresRestart = true
        XCTAssertFalse(item.isExecutable(in: .init(special: true)))
        item.requiresRestart = false
        item.requiredConsents = ["future_permission"]
        XCTAssertFalse(item.isExecutable(in: .init(special: true, consents: ["future_permission"])))
    }
    func testExitIsBoundToExactApplicationAndReopenIsIndependent() {
        var item = item()
        item.requiresQuit = true
        item.applications = [.init(id: "app-1", path: "/Example/Editor.app", identity: "org.example.editor")]
        XCTAssertFalse(item.isExecutable(in: .init(special: true)))
        XCTAssertFalse(item.isExecutable(in: .init(quitApps: ["same-name-other-app"])))
        XCTAssertTrue(item.isExecutable(in: .init(quitApps: ["app-1"])))
        XCTAssertTrue(item.isExecutable(in: .init(quitApps: ["app-1"], reopenApps: ["app-1"])))
        item.applications = []
        XCTAssertFalse(item.isExecutable(in: .init(quitApps: ["app-1"])))
    }
    func testMCPReferenceMustMatchAndPendingRestartIsNotCompleted() throws {
        var item = item()
        item.mcp = .init(operation: "replace-package-reference", packageReference: .init(launcher: "npx", before: "example@1.0.0", after: "example@2.0.0"))
        XCTAssertFalse(item.isExecutable(in: .init(special: true)))
        XCTAssertFalse(item.isExecutable(in: .init(mcpReference: "example@3.0.0")))
        XCTAssertFalse(item.isExecutable(in: .init(mcpReference: "example@2.0.0")))
        XCTAssertTrue(item.isExecutable(in: .init(special: true, mcpReference: "example@2.0.0")))
        item.specialApproved = false
        XCTAssertFalse(item.isExecutable(in: .init(special: true, mcpReference: "example@2.0.0")))
        let data = Data(#"{"schema":"keepupdated/apply/v1","plan_id":"p","started_at":"","finished_at":"","results":[{"id":"os","name":"System","status":"awaiting_restart"},{"id":"app","name":"Editor","status":"success","application_lifecycle":[{"id":"app","path":"/Example/Editor.app","quit_status":"exited","quit_reason":"application_exit_verified","reopen_status":"failed","reopen_reason":"application_reopen_unverified"}]}]}"#.utf8)
        let result = try ReportJSON.decode(ApplyReport.self, from: data, schema: "keepupdated/apply/v1")
        XCTAssertEqual(result.awaitingRestart.map(\.id), ["os"])
        XCTAssertEqual(result.completed.map(\.id), ["app"])
        XCTAssertTrue(result.failed.isEmpty)
        XCTAssertEqual(result.completed.first?.applicationLifecycle?.first?.reopenStatus, "failed")
    }
    func testReviewArgumentsKeepValuesSeparateAndDeterministic() {
        let choices = ReviewChoices(consents: ["runtime_update", "privileged_install"], quitApps: ["app B", "app A"], reopenApps: ["app A"], mcpReference: "@example/tool@2.0.0")
        XCTAssertEqual(choices.arguments, ["--consent", "privileged_install", "--consent", "runtime_update", "--quit-app", "app A", "--quit-app", "app B", "--reopen-app", "app A", "--mcp-reference", "@example/tool@2.0.0"])
        XCTAssertFalse(choices.arguments.contains("--special"))
    }
    @MainActor func testReviewedPlanForwardsExactChoicesAndPreservesOriginalJSONForApply() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-review-test-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        var target = item()
        target.requiredConsents = ["runtime_update"]
        let plan = OperationPlan(schema: "keepupdated/plan/v1", planId: "review-test", createdAt: "2026-01-01T00:00:00Z", expiresAt: "2999-01-01T00:00:00Z", action: .update, items: [target], selection: .init(special: false, consents: ["runtime_update"], ids: [target.id], all: false, cleanup: false))
        var object = try XCTUnwrap(JSONSerialization.jsonObject(with: ReportJSON.encode(plan)) as? [String: Any])
        object["future_field"] = ["retain_exact_bytes": true]
        var items = try XCTUnwrap(object["items"] as? [[String: Any]])
        items[0]["binding"] = ["resolved_target": ["application_update": ["bundle_id": "com.example.Native", "team_id": "ABCDE12345", "architecture": "arm64", "macos": "14.0", "archive": ["format": "dmg", "version": "2.0", "build": "20", "length": 512, "future_archive_field": "keep exact bytes"]]]]
        object["items"] = items
        let original = try JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys])
        try original.write(to: directory.appendingPathComponent("preview.json"))
        let result = ApplyReport(schema: "keepupdated/apply/v1", planId: "review-test", startedAt: "", finishedAt: "", results: [.init(id: "target", name: "Example", status: "success")])
        try ReportJSON.encode(result).write(to: directory.appendingPathComponent("result.json"))
        let inventory = InventoryReport(schema: "keepupdated/v1", command: "scan", generatedAt: "", items: [], diagnostics: [], coverage: [])
        try ReportJSON.encode(inventory).write(to: directory.appendingPathComponent("scan.json"))
        let script = directory.appendingPathComponent("fixture")
        let root = "'" + directory.path.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
        let contents = """
        #!/bin/sh
        root=\(root)
        printf '%s\\n' "$@" >> "$root/arguments"
        case "$1" in
          plan) cat "$root/preview.json" ;;
          apply)
            shift
            while [ "$#" -gt 0 ]; do
              if [ "$1" = --plan ]; then shift; cat "$1" > "$root/applied.json"; break; fi
              shift
            done
            cat "$root/result.json" ;;
          scan) cat "$root/scan.json" ;;
          *) exit 2 ;;
        esac
        """
        try Data(contents.utf8).write(to: script)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.path)
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script))
        store.preview = plan
        store.planReviewed(target, choices: ReviewChoices(consents: ["runtime_update"]))
        for _ in 0..<400 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertFalse(store.busy)
        XCTAssertNil(store.error)
        XCTAssertEqual(store.preview?.executable.map(\.id), ["target"])
        XCTAssertEqual(store.preview?.items.first?.binding?.resolvedTarget?.applicationUpdate?.archive.format, "dmg")
        let arguments = try String(contentsOf: directory.appendingPathComponent("arguments"))
        XCTAssertTrue(arguments.contains("--consent\nruntime_update\n"))
        XCTAssertFalse(arguments.contains("--special"))
        store.execute()
        for _ in 0..<400 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertFalse(store.busy)
        XCTAssertNil(store.error)
        XCTAssertEqual(try Data(contentsOf: directory.appendingPathComponent("applied.json")), original)
        XCTAssertEqual(store.lastResults?.completed.map(\.id), ["target"])
        // A backend returning a permission that was never selected must not produce an executable preview.
        store.plan(action: .update, ids: ["target"])
        for _ in 0..<400 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertFalse(store.hasConfirmedPlan)
        XCTAssertNotNil(store.error)
    }

    func testMCPReferenceApprovalIsSeparateAndResetsWhenReferenceChanges() {
        var choices = ReviewChoices(mcpReference: "example@2.0.0", special: true)
        XCTAssertFalse(choices.specialForPreview, "Script approval cannot authorize a reference change")
        choices.mcpReferenceApproved = true
        XCTAssertTrue(choices.specialForPreview)
        choices.mcpReference = "example@3.0.0"
        XCTAssertFalse(choices.mcpReferenceApproved)
        XCTAssertFalse(choices.specialForPreview)
    }

    @MainActor func testMCPReferenceReviewReachesConfiguredWithOneExactPreview() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-mcp-review-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        let original = Data(#"{"schema":"keepupdated/plan/v1","plan_id":"mcp-review","created_at":"2026-01-01T00:00:00Z","expires_at":"2999-01-01T00:00:00Z","action":"update","selection":{"action":"update","ids":["server"],"all":false,"cleanup":false,"special":true,"mcp_reference":"example@2.0.0"},"items":[{"id":"server","name":"Example MCP","kind":"mcp-server","provider":"mcp-config","from_version":"","status":"ready","reason":"mcp_package_reference_change_only_not_installation","requires_admin":false,"requires_quit":false,"requires_restart":false,"requires_scripts":false,"special_approved":true,"cleanup":false,"affected_paths":["/Example/mcp.json"],"affected_ids":[],"binding":{"path":"/Example/mcp.json","real_path":"/Example/mcp.json","identity":"example","fingerprint":"config-digest"},"mcp":{"operation":"replace-package-reference","item_id":"server","config_path":"/Example/mcp.json","real_path":"/Example/mcp.json","sha256":"config-digest","scope":"user-or-explicit-project","server_key":"example","schema":"mcpServers","json_path":["mcpServers","example"],"file_mode":384,"package_reference":{"launcher":"npx","before":"example@1.0.0","after":"example@2.0.0"}}}]}"#.utf8)
        try original.write(to: directory.appendingPathComponent("preview.json"))
        let result = Data(#"{"schema":"keepupdated/apply/v1","plan_id":"mcp-review","started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","results":[{"id":"server","name":"Example MCP","status":"configured","reason":"package_reference_changed_server_not_started","changed":true,"executed":true,"command_succeeded":true,"backup_path":"/Example/backup.json"}]}"#.utf8)
        try result.write(to: directory.appendingPathComponent("result.json"))
        try ReportJSON.encode(InventoryReport(schema: "keepupdated/v1", command: "scan", generatedAt: "", items: [], diagnostics: [], coverage: [])).write(to: directory.appendingPathComponent("scan.json"))
        let script = directory.appendingPathComponent("fixture")
        let root = "'" + directory.path.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
        let contents = """
        #!/bin/sh
        root=\(root)
        printf '%s\\n' "$1" >> "$root/calls"
        case "$1" in
          plan)
            shift
            special=no; reference=; target=; action=
            while [ "$#" -gt 0 ]; do
              case "$1" in
                --special) special=yes ;;
                --mcp-reference) shift; reference="$1" ;;
                --id) shift; target="$1" ;;
                --action) shift; action="$1" ;;
                --all|--cleanup|--consent|--quit-app|--reopen-app) exit 2 ;;
              esac
              shift
            done
            [ "$special" = yes ] && [ "$reference" = example@2.0.0 ] && [ "$target" = server ] && [ "$action" = update ] || exit 2
            cat "$root/preview.json" ;;
          apply)
            shift
            while [ "$#" -gt 0 ]; do
              if [ "$1" = --plan ]; then shift; cat "$1" > "$root/applied.json"; break; fi
              shift
            done
            cat "$root/result.json" ;;
          scan) cat "$root/scan.json" ;;
          *) exit 2 ;;
        esac
        """
        try Data(contents.utf8).write(to: script)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.path)
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script))
        let initial = PlanItem(id: "server", name: "Example MCP", kind: "mcp-server", status: "manual", reason: "launcher_reference_requires_explicit_migration")
        store.preview = OperationPlan(schema: "keepupdated/plan/v1", planId: "initial", createdAt: "", expiresAt: "2999-01-01T00:00:00Z", action: .update, items: [initial], selection: .init(special: false))
        var choices = ReviewChoices(mcpReference: "example@2.0.0", special: true)
        store.planReviewed(initial, choices: choices)
        XCTAssertFalse(store.busy)
        XCTAssertFalse(FileManager.default.fileExists(atPath: directory.appendingPathComponent("calls").path))
        choices.mcpReferenceApproved = true
        store.planReviewed(initial, choices: choices)
        for _ in 0..<400 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertNil(store.error)
        XCTAssertEqual(store.preview?.executable.map(\.id), ["server"])
        XCTAssertEqual(store.preview?.selection?.special, true)
        XCTAssertEqual(store.preview?.items.first?.mcp?.packageReference?.after, "example@2.0.0")
        store.execute()
        for _ in 0..<400 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertFalse(store.busy)
        XCTAssertNil(store.error)
        XCTAssertEqual(store.lastResults?.completed.first?.status, "configured")
        XCTAssertTrue(store.lastResults?.failed.isEmpty == true)
        XCTAssertNil(store.lastResults?.completed.first?.afterVersion, "A reference migration is not an installed-server version claim")
        XCTAssertEqual(try Data(contentsOf: directory.appendingPathComponent("applied.json")), original)
        XCTAssertEqual(try String(contentsOf: directory.appendingPathComponent("calls")).split(separator: "\n"), ["plan", "apply", "scan"])
    }

}
