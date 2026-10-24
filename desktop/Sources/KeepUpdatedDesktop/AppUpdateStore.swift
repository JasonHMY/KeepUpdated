import AppKit
import Combine
import Foundation

struct AppUpdateEvidence: Decodable {
    let repository: String?
    let release: String?
    let archiveLength: String?
    let archiveHash: String?
    let archivePublisherSignature: String?
    let installedPayloadSignature: String?
    let candidatePayloadSignature: String?
    let metadataFreshness: String?
    let installationReadback: String?
    let reopen: String?
    enum CodingKeys: String, CodingKey {
        case repository = "repository"
        case release = "release"
        case archiveLength = "archive_length"
        case archiveHash = "archive_hash"
        case archivePublisherSignature = "archive_publisher_signature"
        case installedPayloadSignature = "installed_payload_signature"
        case candidatePayloadSignature = "candidate_payload_signature"
        case metadataFreshness = "metadata_freshness"
        case installationReadback = "installation_readback"
        case reopen = "reopen"
    }
    static func label(_ value: String?) -> String {
        switch value {
        case "verified": return L10n.text("Verified")
        case "unverified": return L10n.text("Not verified")
        case "not_checked": return L10n.text("Not checked")
        case "not_applicable": return L10n.text("Not applicable")
        default: return L10n.text("Not recorded; cannot confirm")
        }
    }
    static func rows(_ evidence: AppUpdateEvidence?, operation: Bool) -> [(String, String)] {
        var rows: [(String, String)] = [
            (L10n.text("Repository binding"), label(evidence?.repository)),
            (L10n.text("Release binding"), label(evidence?.release)),
            (L10n.text("Archive length"), label(evidence?.archiveLength)),
            (L10n.text("Archive SHA-256"), label(evidence?.archiveHash)),
            (L10n.text("Archive publisher signature"), label(evidence?.archivePublisherSignature)),
            (L10n.text("Installed app Developer ID and Gatekeeper"), label(evidence?.installedPayloadSignature)),
            (L10n.text("Candidate app Developer ID and Gatekeeper"), label(evidence?.candidatePayloadSignature)),
            (L10n.text("Authenticated metadata freshness"), label(evidence?.metadataFreshness))
        ]
        if operation {
            rows += [
                (L10n.text("Installation readback"), label(evidence?.installationReadback)),
                (L10n.text("App reopening"), label(evidence?.reopen))
            ]
        }
        return rows
    }
}

struct AppUpdateCheck: Decodable {
    let evidence: AppUpdateEvidence?
    let schema: String
    let status: String
    let reason: String?
    let currentVersion: String
    let currentBuild: String
    let targetVersion: String?
    let canInstall: Bool
    let release: Release?
    struct Release: Decodable { let body: String }
    enum CodingKeys: String, CodingKey {
        case schema, status, reason, release, evidence
        case currentVersion = "current_version", currentBuild = "current_build"
        case targetVersion = "target_version", canInstall = "can_install"
    }
}
private struct PreparedAppUpdate: Decodable { let schema: String; let id: String; let directory: String; let evidence: AppUpdateEvidence? }
struct AppUpdateRecovery: Decodable {
    let evidence: AppUpdateEvidence?
    let schema: String
    let id: String
    let status: String
    let reason: String?
    let backup: String?
    let changed: Bool?
    let verification: String?
    let reopenStatus: String?
    enum CodingKeys: String, CodingKey {
        case schema, id, status, reason, backup, changed, verification, evidence
        case reopenStatus = "reopen_status"
    }
}

struct AppUpdateInspection: Decodable {
    let schema: String
    let id: String
    let target: String
    let status: String
    let reason: String?
    let reopenStatus: String
    let workerStopped: Bool
    enum CodingKeys: String, CodingKey {
        case schema, id, target, status, reason
        case reopenStatus = "reopen_status", workerStopped = "worker_stopped"
    }
}

@MainActor
final class AppUpdateStore: ObservableObject {
    @Published private(set) var result: AppUpdateCheck?
    @Published private(set) var preparedEvidence: AppUpdateEvidence?
    @Published private(set) var phase = "idle"
    @Published private(set) var detail: String?
    @Published private(set) var recovery: AppUpdateRecovery?
    @Published private(set) var recoveryURL: URL?
    @Published private(set) var inspection: AppUpdateInspection?
    @Published private(set) var inspectingRecovery = false
    @Published private(set) var inspectionFailed = false
    private var startupCheckStarted = false
    private var exactCheck: Data?
    private let client: CLIClient
    private let defaults: UserDefaults
    private let appURL: URL
    private let terminate: @MainActor () -> Void
    private var task: Task<Void, Never>?
    private var checkTask: Task<Void, Never>?
    private var checkID: UUID?
    private let notifications: NotificationCenter
    private var terminationObserver: NSObjectProtocol?
    private let recoveryKey = "appUpdateRecoveryPath"
    private let recoveryIDKey = "appUpdateRecoveryID"
    private var expectedRecoveryID: String?
    private var pendingLease: UUID?
    var installationInProgress: Bool { ["preparing", "handoff", "waiting_for_exit"].contains(phase) }
    var busy: Bool { phase == "checking" || installationInProgress || inspectingRecovery }
    var currentVersion: String {
        result.map { "\($0.currentVersion) (\($0.currentBuild))" }
            ?? "\(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "—") (\(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "—"))"
    }
    init(client: CLIClient = CLIClient(), defaults: UserDefaults = L10n.defaults,
         appURL: URL = Bundle.main.bundleURL, notifications: NotificationCenter = .default,
         terminate: (@MainActor () -> Void)? = nil) {
        self.client = client
        self.defaults = defaults
        self.appURL = appURL
        self.terminate = terminate ?? { NSApplication.shared.terminate(nil) }
        self.notifications = notifications
        terminationObserver = notifications.addObserver(forName: NSApplication.willTerminateNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.cancelReadOnlyCheck() }
        }
    }
    deinit {
        if let terminationObserver { notifications.removeObserver(terminationObserver) }
    }
    func checkAtLaunch(using inventory: InventoryStore) {
        guard !startupCheckStarted else { return }
        startupCheckStarted = true
        check(using: inventory)
    }
    func check(using inventory: InventoryStore) {
        guard !busy else { return }
        let id = UUID()
        let previousCheck = checkTask
        checkID = id
        phase = "checking"; detail = nil; result = nil; exactCheck = nil; preparedEvidence = nil
        checkTask = Task {
            // Drain a cancelled process before reusing this CLI client for an explicit retry.
            await previousCheck?.value
            guard checkID == id, !Task.isCancelled else { return }
            defer { if checkID == id { checkID = nil; checkTask = nil } }
            if inventory.demo {
                phase = "demo"
                return
            }
            do {
                let output = try await client.run(["self-update", "check", "--app", appURL.path])
                guard checkID == id, !Task.isCancelled else { return }
                let check = try JSONDecoder().decode(AppUpdateCheck.self, from: output.data)
                guard check.schema == "keepupdated/self-update/v1" else { throw ClientError.message("The app update response could not be verified.") }
                result = check
                if output.status == 0, ["not_published", "available", "up_to_date", "newer_than_release"].contains(check.status) {
                    exactCheck = output.data
                    phase = check.status
                } else { phase = "failed"; detail = check.reason }
            } catch {
                guard checkID == id, !Task.isCancelled else { return }
                phase = "failed"; detail = error.localizedDescription
            }
        }
    }
    func cancelReadOnlyCheck() {
        guard phase == "checking", checkID != nil else { return }
        checkID = nil
        checkTask?.cancel()
        client.cancel()
        phase = "idle"
        detail = nil
    }
    // The confirmation dialog authorizes this complete download/install/reopen sequence.
    func install(using inventory: InventoryStore) {
        guard !inventory.demo, !busy, result?.canInstall == true,
              let exactCheck, let lease = inventory.acquireAppUpdate("Updating KeepUpdated…") else { return }
        phase = "preparing"; detail = nil; preparedEvidence = nil
        inventory.updateAppUpdateActivity(lease, "Downloading and verifying the app update…")
        task = Task {
            defer { if phase != "waiting_for_exit" { inventory.releaseAppUpdate(lease) } }
            do {
                let file = try PrivatePlanFile(data: exactCheck)
                defer { file.remove() }
                let prepared = try await client.run(["self-update", "prepare", "--check", file.url.path,
                                                     "--pid", String(ProcessInfo.processInfo.processIdentifier)])
                guard prepared.status == 0 else {
                    if let attempt = try? JSONDecoder().decode(PreparedAppUpdate.self, from: prepared.data),
                       attempt.schema == "keepupdated/self-update/v1" { preparedEvidence = attempt.evidence }
                    throw ClientError.message("The update could not be verified. KeepUpdated has not been closed.")
                }
                let plan = try JSONDecoder().decode(PreparedAppUpdate.self, from: prepared.data)
                guard plan.schema == "keepupdated/self-update/v1", !plan.id.isEmpty, plan.directory.hasPrefix("/") else {
                    throw ClientError.message("The app update response could not be verified.")
                }
                preparedEvidence = plan.evidence
                let directory = URL(fileURLWithPath: plan.directory, isDirectory: true)
                recovery = nil
                inspection = nil; inspectionFailed = false
                expectedRecoveryID = plan.id
                recoveryURL = directory.appendingPathComponent("status.json")
                defaults.set(recoveryURL?.path, forKey: recoveryKey)
                defaults.set(plan.id, forKey: recoveryIDKey)
                phase = "handoff"
                inventory.updateAppUpdateActivity(lease, "Preparing to close, install and reopen KeepUpdated…")
                let output = try await client.run(["self-update", "handoff", "--plan", directory.appendingPathComponent("plan.json").path, "--confirm", plan.id])
                let state = try JSONDecoder().decode(AppUpdateRecovery.self, from: output.data)
                guard output.status == 0, state.schema == plan.schema, state.id == plan.id, state.status == "waiting_for_exit" else {
                    throw ClientError.message("The update handoff could not be verified. KeepUpdated will stay open.")
                }
                // Keep the busy lease until termination. A refused quit must not
                // allow an ordinary update while the worker is waiting for exit.
                phase = "waiting_for_exit"
                pendingLease = lease
                let acknowledgment = directory.appendingPathComponent("exit-approved.json")
                let data = try JSONSerialization.data(withJSONObject: ["id": plan.id])
                try data.write(to: acknowledgment, options: .atomic)
                try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: acknowledgment.path)
                let handle = try FileHandle(forWritingTo: acknowledgment)
                try handle.synchronize()
                try handle.close()
                terminate()
            } catch {
                if phase != "waiting_for_exit" { phase = "failed" }
                detail = error.localizedDescription
                readRecovery(demo: false)
            }
        }
    }
    func refreshRecovery(using inventory: InventoryStore) {
        guard !inventory.demo, !inspectingRecovery, !["checking", "preparing", "handoff"].contains(phase) else { return }
        readRecovery(demo: false)
        guard let expectedRecoveryID, let url = recoveryURL, url.lastPathComponent == "status.json" else { return }
        inspection = nil; inspectionFailed = false; inspectingRecovery = true
        task = Task {
            defer { inspectingRecovery = false }
            do {
                let output = try await client.run(["self-update", "inspect", "--plan", url.deletingLastPathComponent().appendingPathComponent("plan.json").path,
                                                   "--operation", expectedRecoveryID, "--app", appURL.path])
                let state = try JSONDecoder().decode(AppUpdateInspection.self, from: output.data)
                guard output.status == 0, state.schema == "keepupdated/self-update/v1", state.id == expectedRecoveryID,
                      state.target == appURL.path, self.expectedRecoveryID == expectedRecoveryID,
                      ["not_replaced", "candidate_installed", "original_restored", "unknown", "busy"].contains(state.status) else {
                    throw ClientError.message("The app update response could not be verified.")
                }
                inspection = state
                // Keep the original lease unless an inactive, one-shot worker and
                // a stable result have both been verified by the read-only inspector.
                if phase == "waiting_for_exit", let lease = pendingLease, state.workerStopped,
                   ["not_replaced", "candidate_installed", "original_restored"].contains(state.status) {
                    phase = state.status == "candidate_installed" ? "idle" : "failed"
                    pendingLease = nil
                    inventory.releaseAppUpdate(lease)
                }
            } catch { inspectionFailed = true }
        }
    }
    func readRecovery(demo: Bool) {
        // Missing, malformed or stale records are not evidence that this worker stopped.
        recovery = nil
        guard !demo else { return }
        if expectedRecoveryID == nil {
            expectedRecoveryID = defaults.string(forKey: recoveryIDKey)
            if let path = defaults.string(forKey: recoveryKey) { recoveryURL = URL(fileURLWithPath: path) }
        }
        guard let expectedRecoveryID, let url = recoveryURL,
              let size = try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize, size <= 1_048_576,
              let data = try? Data(contentsOf: url),
              let state = try? JSONDecoder().decode(AppUpdateRecovery.self, from: data),
              state.schema == "keepupdated/self-update/v1", state.id == expectedRecoveryID else { return }
        recovery = state
    }
    var message: String {
        switch phase {
        case "checking": return L10n.text("Checking public app releases…")
        case "preparing": return L10n.text("Downloading and verifying the app update…")
        case "handoff", "waiting_for_exit": return L10n.text("Preparing to close, install and reopen KeepUpdated…")
        case "not_published": return L10n.text("No public stable release has been published yet.")
        case "available": return L10n.text("KeepUpdated %@ is available.", result?.targetVersion ?? "—")
        case "newer_than_release", "up_to_date":
            guard result?.evidence?.installedPayloadSignature == "verified" else {
                return L10n.text("The release check reports no newer version. App verification cannot be confirmed here.")
            }
            return phase == "up_to_date"
                ? L10n.text("This verified app is up to date with the public stable release.")
                : L10n.text("This verified app is newer than the latest public stable release.")
        case "demo": return L10n.text("Demo: synthetic app-update check. No network request, download or restart was performed.")
        case "failed": return L10n.text("App update checking or installation could not be verified.")
        default: return L10n.text("KeepUpdated checks once at launch. You can retry here; installation always needs confirmation.")
        }
    }
    var footerMessage: String {
        switch phase {
        case "up_to_date": return L10n.text("App is up to date")
        case "available": return L10n.text("Click to update")
        case "checking": return L10n.text("Checking app version…")
        case "not_published": return L10n.text("App not publicly released")
        case "newer_than_release": return L10n.text("Newer preview version")
        case "failed": return L10n.text("App update not verified")
        case "demo": return L10n.text("Demo app version")
        case "preparing", "handoff", "waiting_for_exit": return L10n.text("Updating KeepUpdated…")
        default: return L10n.text("App version not checked")
        }
    }
    var installationLimit: String? {
        guard result?.status == "available", result?.canInstall == false else { return nil }
        if result?.reason == "self_update_current_signature_unverified" {
            return L10n.text("Installation is unavailable because this running app has no verified Developer ID signature. A production update must retain the authenticated current developer identity.")
        }
        return L10n.text("This release cannot be installed because its source, signature or compatibility has not been verified.")
    }
    var recoveryMessage: String? {
        guard recoveryURL != nil else { return nil }
        if inspectingRecovery { return L10n.text("Inspecting app update files…") }
        if inspectionFailed { return L10n.text("The update files could not be inspected. The outcome is unknown.") }
        guard let inspection else { return L10n.text("The saved update record has not been checked against the current app files.") }
        switch inspection.status {
        case "not_replaced": return L10n.text("The original app is still installed. The candidate has not replaced it.")
        case "candidate_installed": return L10n.text("The candidate is installed and the retained original was verified.")
        case "original_restored": return L10n.text("The original app is restored. The candidate is retained for review.")
        case "busy": return L10n.text("An update worker is still active. Check again after it stops.")
        default: return L10n.text("The update outcome cannot be confirmed. Keep the app and recovery files for review.")
        }
    }
    var recoveryReopenMessage: String? {
        guard let inspection else { return nil }
        return inspection.reopenStatus == "recorded_reopened"
            ? L10n.text("The saved worker record reports a verified reopening. This inspection did not reopen the app.")
            : L10n.text("App reopening is not verified. This inspection did not reopen the app.")
    }
}
