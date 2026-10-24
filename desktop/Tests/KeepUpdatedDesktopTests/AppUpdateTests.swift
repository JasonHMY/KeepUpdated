import XCTest
import AppKit
import Darwin
@testable import KeepUpdatedDesktop

final class AppUpdateTests: XCTestCase {
    private struct Fixture {
        let directory: URL
        let client: CLIClient
        let defaults: UserDefaults
        let suite: String
        func remove() { defaults.removePersistentDomain(forName: suite); try? FileManager.default.removeItem(at: directory) }
    }
    private func fixture(status: String = "available", install: Bool = true, prepareOK: Bool = true, ready: Bool = true, holdCheck: Bool = false) throws -> Fixture {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("self-update-tests-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        let suite = "AppUpdateTests." + UUID().uuidString
        let defaults = UserDefaults(suiteName: suite)!
        func write(_ name: String, _ value: [String: Any]) throws {
            try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]).write(to: root.appendingPathComponent(name))
        }
        let checkEvidence = ["repository": "verified", "release": "verified", "archive_length": "not_checked",
            "archive_hash": "not_checked", "archive_publisher_signature": "unverified",
            "installed_payload_signature": "verified", "candidate_payload_signature": "not_checked", "metadata_freshness": "unverified"]
        var preparedEvidence = checkEvidence
        preparedEvidence["archive_length"] = "verified"
        preparedEvidence["archive_hash"] = "verified"
        preparedEvidence["candidate_payload_signature"] = "verified"
        try write("check.json", ["schema": "keepupdated/self-update/v1", "status": status,
            "current_version": "1.0.0", "current_build": "10", "target_version": "2.0.0", "can_install": install,
            "release": ["body": "Synthetic release notes"], "evidence": checkEvidence, "unrecognized_proof": ["keep": "exact bytes"]])
        try write("prepared.json", ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "directory": root.path, "evidence": preparedEvidence])
        try write("state.json", ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "status": ready ? "waiting_for_exit" : "failed"])
        try write("inventory.json", ["schema": "keepupdated/v1", "command": "scan", "generated_at": "2026-01-01T00:00:00Z", "items": [], "diagnostics": [], "coverage": []])
        if holdCheck { try Data().write(to: root.appendingPathComponent("hold")) }
        let binary = root.appendingPathComponent("cli")
        let quoted = "'" + root.path.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
        let script = """
        #!/bin/sh
        root=\(quoted)
        printf '%s\\n' "$*" >> "$root/calls"
        if [ "$1" = scan ]; then cat "$root/inventory.json"; exit 0; fi
        [ "$1" = self-update ] || exit 2
        case "$2" in
          check)
            echo "$$" > "$root/check.pid"
            trap 'echo stopped > "$root/check.stopped"; exit 0' TERM
            while [ -e "$root/hold" ]; do sleep 0.01; done
            cat "$root/check.json"; exit \(status == "failed" ? 1 : 0) ;;
          prepare)
            shift; shift
            while [ "$#" -gt 0 ]; do
              if [ "$1" = --check ]; then shift; cat "$1" > "$root/exact-check.json"; fi
              shift
            done
            cat "$root/prepared.json"; exit \(prepareOK ? 0 : 1) ;;
          handoff) cat "$root/state.json" ;;
          inspect) cat "$root/inspection.json" ;;
          *) exit 2 ;;
        esac
        """
        try Data(script.utf8).write(to: binary)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        return Fixture(directory: root, client: CLIClient(testExecutable: binary), defaults: defaults, suite: suite)
    }
    @MainActor private func wait(_ predicate: () -> Bool) async throws {
        for _ in 0..<500 {
            if predicate() { return }
            try await Task.sleep(nanoseconds: 5_000_000)
        }
        XCTFail("App update did not reach expected state")
    }
    @MainActor func testLaunchCheckRunsOnceWithoutTakingInventoryLease() async throws {
        let f = try fixture(); defer { f.remove() }
        let inventory = InventoryStore(demo: false, client: f.client)
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults)
        let existingLease = try XCTUnwrap(inventory.acquireAppUpdate())
        updater.checkAtLaunch(using: inventory)
        XCTAssertEqual(updater.phase, "checking")
        updater.checkAtLaunch(using: inventory)
        try await wait { !updater.busy }
        XCTAssertTrue(inventory.busy, "Read-only check must not release someone else's lease")
        inventory.releaseAppUpdate(existingLease)
        updater.checkAtLaunch(using: inventory)
        XCTAssertFalse(inventory.busy)
        XCTAssertEqual(updater.phase, "available")
        let calls = try String(contentsOf: f.directory.appendingPathComponent("calls"))
        XCTAssertEqual(calls.split(separator: "\n").count, 1)
        XCTAssertTrue(calls.contains("self-update check --app"))
        updater.check(using: inventory)
        XCTAssertFalse(inventory.busy, "Manual version checks must not block inventory")
        try await wait { !updater.busy }
        let gate = try XCTUnwrap(inventory.acquireAppUpdate())
        updater.install(using: inventory)
        XCTAssertEqual(updater.phase, "available", "Install still respects the exclusive gate")
        inventory.releaseAppUpdate(gate)
    }
    @MainActor func testInitialInventoryScanFinishesWhileVersionCheckIsStillRunning() async throws {
        let f = try fixture(holdCheck: true); defer { f.remove() }
        let inventory = InventoryStore(demo: false, client: f.client)
        let updater = AppUpdateStore(client: CLIClient(testExecutable: f.directory.appendingPathComponent("cli")), defaults: f.defaults)
        updater.checkAtLaunch(using: inventory)
        inventory.refresh(online: false)
        try await wait { inventory.report != nil && !inventory.busy }
        XCTAssertEqual(updater.phase, "checking")
        XCTAssertNil(inventory.error)
        try FileManager.default.removeItem(at: f.directory.appendingPathComponent("hold"))
        try await wait { !updater.busy }
        XCTAssertEqual(updater.phase, "available")
    }
    @MainActor func testTerminationCancelsOnlyTheReadOnlyProcess() async throws {
        let f = try fixture(holdCheck: true); defer { f.remove() }
        let notifications = NotificationCenter()
        let inventory = InventoryStore(demo: false, client: f.client)
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults, notifications: notifications)
        updater.checkAtLaunch(using: inventory)
        let pidFile = f.directory.appendingPathComponent("check.pid")
        try await wait { FileManager.default.fileExists(atPath: pidFile.path) }
        let pid = try XCTUnwrap(Int32(String(contentsOf: pidFile).trimmingCharacters(in: .whitespacesAndNewlines)))
        XCTAssertTrue(updater.busy)
        XCTAssertFalse(updater.installationInProgress, "Settings must remain dismissible during checking")
        notifications.post(name: NSApplication.willTerminateNotification, object: nil)
        XCTAssertEqual(updater.phase, "idle")
        try await wait { kill(pid, 0) == -1 }
        XCTAssertNil(updater.result)
        XCTAssertNil(updater.detail, "A cancelled callback must not replace idle with failure")
        XCTAssertFalse(inventory.busy)
        XCTAssertTrue(FileManager.default.fileExists(atPath: f.directory.appendingPathComponent("check.stopped").path))
    }
    @MainActor func testCancelledCheckCannotOverwriteAnImmediateRetry() async throws {
        let f = try fixture(holdCheck: true); defer { f.remove() }
        let inventory = InventoryStore(demo: false, client: f.client)
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults)
        let pidFile = f.directory.appendingPathComponent("check.pid")
        updater.check(using: inventory)
        try await wait { FileManager.default.fileExists(atPath: pidFile.path) }
        let oldPID = try String(contentsOf: pidFile)
        updater.cancelReadOnlyCheck()
        updater.check(using: inventory)
        try await wait { (try? String(contentsOf: pidFile)) != oldPID }
        XCTAssertEqual(updater.phase, "checking", "The old cancellation must not clear the retry's state")
        XCTAssertNil(updater.detail)
        try FileManager.default.removeItem(at: f.directory.appendingPathComponent("hold"))
        try await wait { !updater.busy }
        XCTAssertEqual(updater.phase, "available")
        XCTAssertNil(updater.detail)
    }
    @MainActor func testNotPublishedFailedAndUntrustedRemainDistinct() async throws {
        for status in ["not_published", "failed", "available", "newer_than_release", "unknown", "up_to_date"] {
            let f = try fixture(status: status, install: false); defer { f.remove() }
            let inventory = InventoryStore(demo: false, client: f.client)
            let updater = AppUpdateStore(client: f.client, defaults: f.defaults)
            updater.check(using: inventory)
            try await wait { !updater.busy }
            XCTAssertEqual(updater.phase, status == "unknown" ? "failed" : status)
            XCTAssertEqual(updater.footerMessage == L10n.text("App is up to date"), status == "up_to_date")
            updater.install(using: inventory)
            XCTAssertFalse(inventory.busy)
            XCTAssertFalse(try String(contentsOf: f.directory.appendingPathComponent("calls")).contains("prepare"))
        }
    }
    @MainActor func testExactCheckAndConsentReachHandoffBeforeExit() async throws {
        let f = try fixture(); defer { f.remove() }
        let inventory = InventoryStore(demo: false, client: f.client)
        var exits = 0
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults, terminate: { exits += 1 })
        updater.check(using: inventory); try await wait { !updater.busy }
        updater.install(using: inventory)
        XCTAssertTrue(updater.installationInProgress)
        updater.cancelReadOnlyCheck()
        XCTAssertEqual(updater.phase, "preparing", "Read-only cancellation must not interrupt installation")
        try await wait { updater.phase == "waiting_for_exit" }
        updater.cancelReadOnlyCheck()
        XCTAssertTrue(updater.installationInProgress)
        XCTAssertEqual(updater.phase, "waiting_for_exit")
        XCTAssertEqual(exits, 1)
        XCTAssertEqual(updater.result?.evidence?.archiveHash, "not_checked")
        XCTAssertEqual(updater.preparedEvidence?.archiveHash, "verified")
        XCTAssertEqual(updater.preparedEvidence?.archivePublisherSignature, "unverified")
        XCTAssertTrue(inventory.busy, "A refused quit must retain exclusion until the worker finishes")
        XCTAssertEqual(try Data(contentsOf: f.directory.appendingPathComponent("check.json")), try Data(contentsOf: f.directory.appendingPathComponent("exact-check.json")))
        let calls = try String(contentsOf: f.directory.appendingPathComponent("calls"))
        XCTAssertTrue(calls.contains("--pid \(ProcessInfo.processInfo.processIdentifier)"))
        XCTAssertTrue(calls.contains("--confirm fixture-id"))
        let failed: [String: Any] = ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "status": "failed", "reason": "self_update_exit_timed_out"]
        try JSONSerialization.data(withJSONObject: failed).write(to: f.directory.appendingPathComponent("status.json"))
        let inspected: [String: Any] = ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "target": Bundle.main.bundleURL.path,
            "status": "not_replaced", "worker_stopped": true, "reopen_status": "not_verified"]
        try JSONSerialization.data(withJSONObject: inspected).write(to: f.directory.appendingPathComponent("inspection.json"))
        updater.refreshRecovery(using: inventory)
        try await wait { !updater.inspectingRecovery }
        XCTAssertFalse(inventory.busy)
        XCTAssertEqual(updater.phase, "failed")
        XCTAssertNotNil(updater.recoveryMessage)
    }
    @MainActor func testPreparationAndHandoffFailureNeverCloseApp() async throws {
        for preparation in [false, true] {
            let f = try fixture(prepareOK: preparation, ready: false); defer { f.remove() }
            let inventory = InventoryStore(demo: false, client: f.client)
            var exits = 0
            let updater = AppUpdateStore(client: f.client, defaults: f.defaults, terminate: { exits += 1 })
            updater.check(using: inventory); try await wait { !updater.busy }
            updater.install(using: inventory); try await wait { !updater.busy }
            XCTAssertEqual(exits, 0)
            XCTAssertEqual(updater.phase, "failed")
        }
    }
    @MainActor func testDemoUsesNoCLIAndNoTermination() async throws {
        let f = try fixture(); defer { f.remove() }
        let inventory = InventoryStore(demo: true, client: f.client)
        var exits = 0
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults, terminate: { exits += 1 })
        updater.check(using: inventory); try await wait { !updater.busy }
        updater.install(using: inventory)
        XCTAssertEqual(updater.phase, "demo")
        XCTAssertEqual(exits, 0)
        XCTAssertFalse(FileManager.default.fileExists(atPath: f.directory.appendingPathComponent("calls").path))
    }
    @MainActor func testStaleOrUnreadableRecoveryNeverReleasesCurrentHandoff() async throws {
        let f = try fixture(); defer { f.remove() }
        let inventory = InventoryStore(demo: false, client: f.client)
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults, terminate: {})
        let old = ["schema": "keepupdated/self-update/v1", "id": "older-id", "status": "reopened"]
        let record = f.directory.appendingPathComponent("status.json")
        try JSONSerialization.data(withJSONObject: old).write(to: record)
        f.defaults.set(record.path, forKey: "appUpdateRecoveryPath")
        f.defaults.set("older-id", forKey: "appUpdateRecoveryID")
        updater.readRecovery(demo: false)
        XCTAssertEqual(updater.recovery?.status, "reopened")
        updater.check(using: inventory); try await wait { !updater.busy }
        updater.install(using: inventory); try await wait { updater.phase == "waiting_for_exit" }
        XCTAssertNil(updater.recovery)
        updater.refreshRecovery(using: inventory)
        try await wait { !updater.inspectingRecovery }
        XCTAssertNil(updater.recovery)
        XCTAssertTrue(inventory.busy)
        try Data("broken".utf8).write(to: record)
        updater.refreshRecovery(using: inventory)
        try await wait { !updater.inspectingRecovery }
        XCTAssertNil(updater.recovery)
        XCTAssertTrue(inventory.busy)
        try FileManager.default.removeItem(at: record)
        updater.refreshRecovery(using: inventory)
        try await wait { !updater.inspectingRecovery }
        XCTAssertTrue(inventory.busy)
        XCTAssertEqual(updater.phase, "waiting_for_exit")
    }
    @MainActor func testStaleLeaseCannotReleaseAnotherOperation() throws {
        let inventory = InventoryStore(demo: true)
        let old = try XCTUnwrap(inventory.acquireAppUpdate())
        inventory.releaseAppUpdate(old)
        let current = try XCTUnwrap(inventory.acquireAppUpdate())
        inventory.releaseAppUpdate(old)
        XCTAssertTrue(inventory.busy)
        inventory.releaseAppUpdate(current)
        XCTAssertFalse(inventory.busy)
    }

    func testEvidenceDecodingIsOptionalAndUnknownValuesStayUnknown() throws {
        let legacy = Data(#"{"schema":"keepupdated/self-update/v1","status":"available","current_version":"1.0.0","current_build":"10","can_install":true}"#.utf8)
        let check = try JSONDecoder().decode(AppUpdateCheck.self, from: legacy)
        XCTAssertNil(check.evidence)
        XCTAssertTrue(check.canInstall)
        let oldState = try JSONDecoder().decode(AppUpdateRecovery.self, from: Data(#"{"schema":"keepupdated/self-update/v1","id":"old","status":"reopened"}"#.utf8))
        XCTAssertNil(oldState.evidence)
        let evidence = try JSONDecoder().decode(AppUpdateEvidence.self, from: Data(#"{"archive_hash":"verified","archive_publisher_signature":"unverified","metadata_freshness":"future-value"}"#.utf8))
        XCTAssertEqual(evidence.archiveHash, "verified")
        XCTAssertNil(evidence.repository)
        XCTAssertEqual(AppUpdateEvidence.label(evidence.metadataFreshness), L10n.text("Not recorded; cannot confirm"))
        XCTAssertEqual(AppUpdateEvidence.rows(nil, operation: false).count, 8)
        XCTAssertEqual(AppUpdateEvidence.rows(nil, operation: true).count, 10)
        XCTAssertTrue(AppUpdateEvidence.rows(nil, operation: true).allSatisfy { $0.1 == L10n.text("Not recorded; cannot confirm") })
    }

    @MainActor func testRecoveryEvidenceNeverInfersSuccessFromPhase() throws {
        let f = try fixture(); defer { f.remove() }
        let record = f.directory.appendingPathComponent("status.json")
        f.defaults.set(record.path, forKey: "appUpdateRecoveryPath")
        f.defaults.set("fixture-id", forKey: "appUpdateRecoveryID")
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults)
        for scenario in ["old", "phase-only", "contradictory", "installed", "reopened", "unknown", "wrong-id"] {
            var state: [String: Any] = ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "status": "reopened"]
            switch scenario {
            case "old": break
            case "phase-only": state["evidence"] = [:] as [String: String]
            case "contradictory": state["evidence"] = ["installation_readback": "verified", "reopen": "verified"]
            case "installed", "reopened":
                state["verification"] = "verified"
                state["reopen_status"] = scenario == "installed" ? "failed" : "reopened"
                state["evidence"] = ["installation_readback": "verified", "reopen": scenario == "installed" ? "unverified" : "verified"]
            case "unknown":
                state["verification"] = "unknown"
                state["evidence"] = ["installation_readback": "unverified"]
            default: state["id"] = "wrong-operation"
            }
            try JSONSerialization.data(withJSONObject: state).write(to: record)
            updater.readRecovery(demo: false)
            XCTAssertEqual(updater.recoveryMessage, L10n.text("The saved update record has not been checked against the current app files."), scenario)
            XCTAssertNil(updater.inspection)

        }
    }

    func testEvidenceTranslationsAndEnglishFallback() {
        let keys = ["Repository binding", "Release binding", "Archive length", "Archive SHA-256", "Archive publisher signature",
                    "Installed app Developer ID and Gatekeeper", "Candidate app Developer ID and Gatekeeper", "Authenticated metadata freshness",
                    "Installation readback", "App reopening", "Verified", "Not verified", "Not checked",
                    "Not applicable", "Not recorded; cannot confirm", "App update check evidence", "Download preparation evidence", "Saved app update evidence"]
        for key in keys {
            XCTAssertNotNil(L10n.catalog(.english)[key], key)
            XCTAssertNotNil(L10n.catalog(.simplifiedChinese)[key], key)
            XCTAssertEqual(L10n.translation(key, selected: [:], english: L10n.catalog(.english)), L10n.format(key, language: .english))
        }
        XCTAssertEqual(AppLanguage.system.resolved(preferredLanguages: ["fr-FR"]), .english)
    }

    @MainActor func testLegacyCurrentVersionDoesNotClaimPayloadVerification() async throws {
        let f = try fixture(status: "up_to_date", install: false); defer { f.remove() }
        let file = f.directory.appendingPathComponent("check.json")
        var check = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: file)) as? [String: Any])
        check.removeValue(forKey: "evidence")
        try JSONSerialization.data(withJSONObject: check).write(to: file)
        let inventory = InventoryStore(demo: false, client: f.client)
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults)
        updater.check(using: inventory); try await wait { !updater.busy }
        XCTAssertEqual(updater.phase, "up_to_date")
        XCTAssertEqual(updater.message, L10n.text("The release check reports no newer version. App verification cannot be confirmed here."))
    }

    @MainActor func testFailedPreparationShowsOnlyItsCollectedEvidence() async throws {
        let f = try fixture(prepareOK: false); defer { f.remove() }
        let file = f.directory.appendingPathComponent("prepared.json")
        var attempt = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: file)) as? [String: Any])
        attempt["evidence"] = ["archive_hash": "unverified", "candidate_payload_signature": "not_checked"]
        try JSONSerialization.data(withJSONObject: attempt).write(to: file)
        let inventory = InventoryStore(demo: false, client: f.client)
        var exits = 0
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults, terminate: { exits += 1 })
        updater.check(using: inventory); try await wait { !updater.busy }
        updater.install(using: inventory); try await wait { !updater.busy }
        XCTAssertEqual(updater.preparedEvidence?.archiveHash, "unverified")
        XCTAssertEqual(updater.preparedEvidence?.candidatePayloadSignature, "not_checked")
        XCTAssertEqual(exits, 0)
        XCTAssertFalse(inventory.busy)
        // An early failure on retry must not keep the previous attempt's evidence.
        try Data("null".utf8).write(to: file)
        updater.install(using: inventory); try await wait { !updater.busy }
        XCTAssertNil(updater.preparedEvidence)
        XCTAssertEqual(exits, 0)
    }

    @MainActor func testInspectionBindsOperationAndTargetAndKeepsReopenSeparate() async throws {
        let f = try fixture(); defer { f.remove() }
        let app = f.directory.appendingPathComponent("KeepUpdated.app")
        f.defaults.set(f.directory.appendingPathComponent("status.json").path, forKey: "appUpdateRecoveryPath")
        f.defaults.set("fixture-id", forKey: "appUpdateRecoveryID")
        let inventory = InventoryStore(demo: false, client: f.client)
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults, appURL: app)
        let cases = [
            "not_replaced": "The original app is still installed. The candidate has not replaced it.",
            "candidate_installed": "The candidate is installed and the retained original was verified.",
            "original_restored": "The original app is restored. The candidate is retained for review.",
            "unknown": "The update outcome cannot be confirmed. Keep the app and recovery files for review.",
            "busy": "An update worker is still active. Check again after it stops."
        ]
        for (status, message) in cases {
            let response: [String: Any] = ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "target": app.path,
                "status": status, "worker_stopped": false, "reopen_status": "not_verified"]
            try JSONSerialization.data(withJSONObject: response).write(to: f.directory.appendingPathComponent("inspection.json"))
            updater.refreshRecovery(using: inventory)
            try await wait { !updater.inspectingRecovery }
            XCTAssertEqual(updater.recoveryMessage, L10n.text(message))
            XCTAssertEqual(updater.recoveryReopenMessage, L10n.text("App reopening is not verified. This inspection did not reopen the app."))
            XCTAssertFalse(inventory.busy)
        }
        for bad in ["id", "target", "status", "schema"] {
            var response: [String: Any] = ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "target": app.path,
                "status": "candidate_installed", "worker_stopped": true, "reopen_status": "recorded_reopened"]
            response[bad] = "wrong"
            try JSONSerialization.data(withJSONObject: response).write(to: f.directory.appendingPathComponent("inspection.json"))
            updater.refreshRecovery(using: inventory); try await wait { !updater.inspectingRecovery }
            XCTAssertNil(updater.inspection)
            XCTAssertTrue(updater.inspectionFailed)
        }
        let calls = try String(contentsOf: f.directory.appendingPathComponent("calls"))
        XCTAssertTrue(calls.contains("self-update inspect --plan " + f.directory.appendingPathComponent("plan.json").path + " --operation fixture-id --app " + app.path))
        XCTAssertFalse(calls.contains("--confirm"))
        for key in Array(cases.values) + ["Inspect App Update Files", "Inspecting app update files…",
            "The update files could not be inspected. The outcome is unknown.",
            "The saved update record has not been checked against the current app files.",
            "The saved worker record reports a verified reopening. This inspection did not reopen the app.",
            "App reopening is not verified. This inspection did not reopen the app."] {
            XCTAssertNotNil(L10n.catalog(.english)[key], key)
            XCTAssertNotNil(L10n.catalog(.simplifiedChinese)[key], key)
        }
    }

    @MainActor func testBusyUnknownAndFailedInspectionCannotReleaseWaitingLease() async throws {
        let f = try fixture(); defer { f.remove() }
        let inventory = InventoryStore(demo: false, client: f.client)
        var exits = 0
        let updater = AppUpdateStore(client: f.client, defaults: f.defaults, terminate: { exits += 1 })
        updater.check(using: inventory); try await wait { !updater.busy }
        updater.install(using: inventory); try await wait { updater.phase == "waiting_for_exit" }
        for status in ["busy", "unknown", "malformed", "candidate_installed"] {
            let data: Data
            if status == "malformed" { data = Data("broken".utf8) }
            else { data = try JSONSerialization.data(withJSONObject: ["schema": "keepupdated/self-update/v1", "id": "fixture-id", "target": Bundle.main.bundleURL.path,
                    "status": status, "worker_stopped": status != "candidate_installed", "reopen_status": "not_verified"]) }
            try data.write(to: f.directory.appendingPathComponent("inspection.json"))
            updater.refreshRecovery(using: inventory); try await wait { !updater.inspectingRecovery }
            XCTAssertTrue(inventory.busy)
            XCTAssertEqual(updater.phase, "waiting_for_exit")
        }
        XCTAssertEqual(exits, 1)
    }

}
