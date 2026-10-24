import XCTest
@testable import KeepUpdatedDesktop

final class PrivilegedPresentationTests: XCTestCase {
    private let operationID = String(repeating: "a", count: 64)
    private func application() -> InventoryItem {
        var item = DemoData.item("app-target", "Example App", "application", "standalone", "unknown")
        item.path = "/Example/Example.app"
        item.identity = "org.example.app"
        return item
    }
    private func system() -> InventoryItem {
        var item = DemoData.item("system-target", "macOS", "os", "system", "unknown")
        item.source = "apple"
        item.path = "/System/Library/CoreServices/SystemVersion.plist"
        return item
    }
    private func packageRequest() -> [String: Any] {
        ["kind": "package", "package": ["path": "/Example/Update.pkg", "installationPath": "/Example/Example.app", "bundleID": "org.example.app", "fromVersion": "1.0.0", "version": "2.0.0", "teamID": "EXAMPLETEA", "receiptID": "org.example.receipt", "sha256": String(repeating: "0", count: 64), "infoSHA256": String(repeating: "1", count: 64), "codeResourcesSHA256": String(repeating: "2", count: 64), "source": "local-signed-package"]]
    }
    private struct Fixture {
        let directory: URL
        let executable: URL
        let requestData: Data
        func remove() { try? FileManager.default.removeItem(at: directory) }
        func read(_ name: String) throws -> Data { try Data(contentsOf: directory.appendingPathComponent(name)) }
        func calls() throws -> String { try String(contentsOf: directory.appendingPathComponent("calls")) }
    }
    private func fixture(request: [String: Any], target: InventoryItem, blocker: String = "") throws -> Fixture {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-privileged-ui-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
        func write(_ name: String, _ object: Any) throws {
            try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]).write(to: directory.appendingPathComponent(name))
        }
        let requestData = try JSONSerialization.data(withJSONObject: request, options: [.sortedKeys])
        let restart = request["kind"] as? String == "restart"
        let required = restart ? ["privileged_install", "restart_system"] : ["privileged_install"]
        try write("preparation.json", ["targetID": target.id, "status": "prepared", "reason": "signed_package_bound_to_installed_app", "executionBlocker": blocker, "candidates": [request]])
        for (name, consents) in [("none", [String]()), ("privileged", ["privileged_install"]), ("both", ["privileged_install", "restart_system"])] {
            let ready = blocker.isEmpty && Set(required).isSubset(of: Set(consents))
            let plan: [String: Any] = ["schema": "keepupdated/plan/v1", "plan_id": operationID, "created_at": "2026-01-01T00:00:00Z", "expires_at": "2999-01-01T00:00:00Z", "action": "update",
                "selection": ["action": "update", "ids": [target.id], "all": false, "cleanup": false, "special": false, "consents": consents, "privileged_request": request],
                "items": [["id": target.id, "name": target.name, "kind": target.kind, "provider": "privileged", "status": ready ? "ready" : "blocked", "reason": blocker.isEmpty ? (ready ? "typed_privileged_target_verified" : "explicit_action_consent_required") : blocker, "from_version": "1.0.0", "target_version": "2.0.0", "requires_admin": true, "requires_restart": restart, "requires_quit": false, "requires_scripts": false, "special_approved": false, "required_consents": required, "privileged_request": request, "binding": ["fingerprint": "opaque-proof-kept-in-exact-plan"]]]]
            try write("plan-" + name + ".json", plan)
        }
        try write("result.json", ["schema": "keepupdated/apply/v1", "plan_id": operationID, "started_at": "", "finished_at": "", "results": [["id": target.id, "name": target.name, "status": "updated", "reason": "target_version_verified", "after_version": "2.0.0", "changed": true, "privileged_operation_id": operationID, "backup_path": "/Example/backup.app", "recovery_path": "/Example/recovery.json"]]])
        try write("operation.json", ["planID": operationID, "status": "awaiting_restart", "reason": "native_restart_dialog_shown", "backupPath": "/Example/backup.app", "recoveryPath": "/Example/recovery.json"])
        try write("helper.json", ["status": "helper_not_registered"])
        var updated = target
        updated.installedVersion = "2.0.0"
        try ReportJSON.encode(InventoryReport(schema: "keepupdated/v1", command: "scan", generatedAt: "", items: [updated], diagnostics: [], coverage: [])).write(to: directory.appendingPathComponent("scan.json"))
        let executable = directory.appendingPathComponent("fixture")
        let root = "'" + directory.path.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
        let script = """
        #!/bin/sh
        root=\(root)
        printf '%s\\n' "$*" >> "$root/calls"
        case "$1" in
          special)
            case "$2" in
              prepare) cat "$root/preparation.json" ;;
              status) cat "$root/helper.json" ;;
              register-helper) printf '{"status":"system_settings_approval_required"}' > "$root/helper.json"; cat "$root/helper.json" ;;
              operation-status) cat "$root/operation.json" ;;
              *) exit 2 ;;
            esac ;;
          plan)
            shift; mode=none; privileged=no; restart=no
            while [ "$#" -gt 0 ]; do
              case "$1" in
                --privileged-request)
                  shift; cat "$1" > "$root/request-read.json"; printf '%s' "$1" > "$root/request-path"
                  /usr/bin/stat -f '%Lp' "$1" > "$root/request-mode" ;;
                --consent) shift; case "$1" in privileged_install) privileged=yes ;; restart_system) restart=yes ;; *) exit 2 ;; esac ;;
                --all|--special|--cleanup|--mcp-reference) exit 2 ;;
              esac
              shift
            done
            [ "$privileged" = yes ] && mode=privileged
            [ "$privileged" = yes ] && [ "$restart" = yes ] && mode=both
            cat "$root/plan-$mode.json" ;;
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
        try Data(script.utf8).write(to: executable)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: executable.path)
        return .init(directory: directory, executable: executable, requestData: requestData)
    }
    @MainActor private func wait(_ store: InventoryStore) async throws {
        for _ in 0..<400 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertFalse(store.busy)
    }

    @MainActor func testPreparedPackageUsesPrivateUnchangedRequestAndExplicitConsent() async throws {
        let item = application(), fixture = try fixture(request: packageRequest(), target: application())
        defer { fixture.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.executable))
        var inventory = DemoData.report; inventory.items = [item]; store.replaceReport(inventory)
        XCTAssertTrue(item.needsManualUpdate)
        store.preparePrivileged(item, packagePath: "/Example/Update.pkg")
        try await wait(store)
        let preparation = try XCTUnwrap(store.privilegedPreparation)
        XCTAssertEqual(preparation.targetID, item.id)
        XCTAssertTrue(preparation.canPreview)
        XCTAssertEqual(preparation.candidates.first?.data, fixture.requestData)
        XCTAssertNil(store.preview)
        store.selectPrivilegedCandidate(0)
        XCTAssertNil(store.preview, "Wait for candidate sheet dismissal before opening the plan")
        store.privilegedPreparationDismissed()
        try await wait(store)
        XCTAssertNil(store.error)
        XCTAssertTrue(store.preview?.executable.isEmpty == true, "Prepared is not permission to execute")
        XCTAssertEqual(try fixture.read("request-read.json"), fixture.requestData)
        XCTAssertEqual(try String(contentsOf: fixture.directory.appendingPathComponent("request-mode")).trimmingCharacters(in: .whitespacesAndNewlines), "600")
        let privatePath = try String(contentsOf: fixture.directory.appendingPathComponent("request-path"))
        XCTAssertFalse(FileManager.default.fileExists(atPath: privatePath))
        let planItem = try XCTUnwrap(store.preview?.items.first)
        store.planReviewed(planItem, choices: .init(consents: ["privileged_install"]))
        try await wait(store)
        XCTAssertEqual(store.preview?.executable.map(\.id), [item.id])
        XCTAssertEqual(try fixture.read("request-read.json"), fixture.requestData, "Replanning retains the prepared request and hashes")
        store.execute() // Controlled shell fixture only; never a native helper or installer.
        try await wait(store)
        XCTAssertNil(store.error)
        XCTAssertEqual(try fixture.read("applied.json"), try fixture.read("plan-privileged.json"))
        XCTAssertEqual(store.lastResults?.completed.first?.privilegedOperationId, operationID)
        XCTAssertEqual(store.lastResults?.completed.first?.recoveryPath, "/Example/recovery.json")
    }

    @MainActor func testSignatureBlockRemainsBlockedAfterPreparingAndSelectingConsent() async throws {
        let fixture = try fixture(request: packageRequest(), target: application(), blocker: "developer_signature_required")
        defer { fixture.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.executable))
        store.preparePrivileged(application(), packagePath: "/Example/Update.pkg")
        try await wait(store)
        XCTAssertEqual(store.privilegedPreparation?.executionBlocker, "developer_signature_required")
        store.selectPrivilegedCandidate(0); store.privilegedPreparationDismissed()
        try await wait(store)
        store.planReviewed(try XCTUnwrap(store.preview?.items.first), choices: .init(consents: ["privileged_install"]))
        try await wait(store)
        XCTAssertEqual(store.preview?.items.first?.reason, "developer_signature_required")
        XCTAssertTrue(store.preview?.executable.isEmpty == true)
        store.execute()
        XCTAssertFalse(try fixture.calls().contains("apply"))
    }

    @MainActor func testHelperRegistrationUsesExplicitActionAndReadsActualStatusAgain() async throws {
        let fixture = try fixture(request: packageRequest(), target: application())
        defer { fixture.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.executable))
        store.registerHelper()
        XCTAssertFalse(FileManager.default.fileExists(atPath: fixture.directory.appendingPathComponent("calls").path))
        store.refreshHelperStatus(); try await wait(store)
        XCTAssertTrue(store.helperStatus?.canRequestRegistration == true)
        store.registerHelper(); try await wait(store) // Controlled fixture; no service registration.
        XCTAssertEqual(store.helperStatus?.status, "system_settings_approval_required")
        XCTAssertFalse(store.helperStatus?.canRequestRegistration == true)
        XCTAssertEqual(try fixture.calls().split(separator: "\n"), ["special status", "special register-helper", "special status"])
    }

    @MainActor func testUnverifiedRegistrationReadbackDoesNotClaimEnabled() async throws {
        let fixture = try fixture(request: packageRequest(), target: application())
        defer { fixture.remove() }
        let root = "'" + fixture.directory.path.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
        let script = """
        #!/bin/sh
        root=\(root)
        case "$2" in
          status) if [ -f "$root/registered" ]; then exit 2; fi; printf '{"status":"helper_not_registered"}' ;;
          register-helper) touch "$root/registered"; printf '{"status":"enabled"}' ;;
          *) exit 2 ;;
        esac
        """
        try Data(script.utf8).write(to: fixture.executable)
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.executable))
        store.refreshHelperStatus(); try await wait(store)
        XCTAssertTrue(store.helperStatus?.canRequestRegistration == true)
        store.registerHelper(); try await wait(store)
        XCTAssertNotNil(store.error)
        XCTAssertNil(store.helperStatus, "A claimed registration is not a successful independent status readback")
    }

    @MainActor func testRestartPreviewRequiresMatchingQueriedWaitingOperationAndBothConsents() async throws {
        let fixture = try fixture(request: ["kind": "restart", "restartOf": operationID], target: system())
        defer { fixture.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.executable))
        store.replaceReport(.init(schema: "keepupdated/v1", command: "scan", generatedAt: "", items: [system()], diagnostics: [], coverage: []))
        store.results = .init(schema: "keepupdated/apply/v1", planId: operationID, startedAt: "", finishedAt: "", results: [.init(id: "prior-installation", name: "Previous install", status: "awaiting_restart", privilegedOperationId: operationID)])
        XCTAssertFalse(store.canPreviewRestart(operationID))
        store.previewRestart(operationID)
        XCTAssertNotNil(store.results)
        store.checkPrivilegedOperation(operationID); try await wait(store)
        XCTAssertTrue(store.canPreviewRestart(operationID))
        store.previewRestart(operationID)
        XCTAssertNil(store.results)
        XCTAssertNil(store.preview)
        store.resultsSheetDismissed(); try await wait(store)
        XCTAssertEqual(store.preview?.items.first?.id, "system-target")
        XCTAssertEqual(store.preview?.displayActionKey, "Restart")
        XCTAssertTrue(store.preview?.executable.isEmpty == true)
        store.planReviewed(try XCTUnwrap(store.preview?.items.first), choices: .init(consents: ["privileged_install"]))
        try await wait(store)
        XCTAssertTrue(store.preview?.executable.isEmpty == true)
        store.planReviewed(try XCTUnwrap(store.preview?.items.first), choices: .init(consents: ["privileged_install", "restart_system"]))
        try await wait(store)
        XCTAssertEqual(store.preview?.executable.map(\.id), ["system-target"])
        XCTAssertEqual(try fixture.read("request-read.json"), fixture.requestData)
        XCTAssertFalse(try fixture.calls().contains("apply"), "This test never executes a restart")
    }

    @MainActor func testMismatchedRecoveryAndDemoNeverAuthorizeRealActions() async throws {
        let fixture = try fixture(request: packageRequest(), target: application())
        defer { fixture.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.executable))
        try Data(("{\"planID\":\"" + String(repeating: "b", count: 64) + "\",\"status\":\"awaiting_restart\"}").utf8).write(to: fixture.directory.appendingPathComponent("operation.json"))
        store.checkPrivilegedOperation(operationID); try await wait(store)
        XCTAssertNotNil(store.error)
        XCTAssertNil(store.operationStatuses[operationID])
        XCTAssertFalse(store.canPreviewRestart(operationID))
        let before = try fixture.calls()
        let demo = InventoryStore(demo: true, client: CLIClient(testExecutable: fixture.executable))
        var demoReport = DemoData.report; demoReport.items = [application()]; demo.replaceReport(demoReport)
        demo.refreshHelperStatus(); try await wait(demo)
        demo.registerHelper()
        demo.preparePrivileged(application(), packagePath: "/Example/Update.pkg"); try await wait(demo)
        demo.selectPrivilegedCandidate(0); demo.privilegedPreparationDismissed(); try await wait(demo)
        XCTAssertTrue(demo.preview?.executable.isEmpty == true)
        demo.checkPrivilegedOperation(operationID); try await wait(demo)
        XCTAssertEqual(try fixture.calls(), before, "Demo must never invoke even the injected CLI")
    }

    func testPreparationRejectsWrongTargetAndUnavailableCandidates() throws {
        let data = Data(#"{"targetID":"other","status":"prepared","reason":"candidate","executionBlocker":"","candidates":[]}"#.utf8)
        XCTAssertThrowsError(try PrivilegedPreparation.decode(data, targetID: "selected", targetName: "Example"))
        let blocked = try PrivilegedPreparation.decode(Data(#"{"targetID":"selected","status":"blocked","reason":"native_bridge_not_packaged","executionBlocker":"","candidates":[]}"#.utf8), targetID: "selected", targetName: "Example")
        XCTAssertFalse(blocked.canPreview)
        XCTAssertEqual(blocked.reason, "native_bridge_not_packaged")
    }
}
