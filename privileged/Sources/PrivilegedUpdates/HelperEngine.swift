import Foundation
import Darwin

struct Attempt: Codable {
    var plan: SpecialPlan
    var uid: UInt32
    var boot: String
    var restartRequired: Bool
    var result: OperationResult
}

// One process-wide engine serializes preparation and installation. Each XPC session
// owns its preparation, so another signed client cannot consume its authorization.
final class HelperEngine {
    let root: String
    let owner: uid_t
    let providers: Providers
    let now: () -> Date
    private(set) var pending: (session: UUID, plan: SpecialPlan, directory: String, restart: Bool, uid: uid_t)?
    private var lockFD: Int32 = -1

    init(root: String, owner: uid_t = 0, providers: Providers = Providers(), now: @escaping () -> Date = Date.init) throws {
        self.root = root; self.owner = owner; self.providers = providers; self.now = now
        try privateDirectory(root, owner: owner)
        lockFD = open(root + "/operation.lock", O_RDWR | O_CREAT | O_NOFOLLOW | O_CLOEXEC, 0o600)
        var info = stat()
        guard lockFD >= 0, fstat(lockFD, &info) == 0, info.st_uid == owner, info.st_mode & S_IFMT == S_IFREG,
              info.st_mode & 0o077 == 0, flock(lockFD, LOCK_EX | LOCK_NB) == 0 else { throw PrivilegeError.operationInProgress }
    }
    deinit { if lockFD >= 0 { close(lockFD) } }

    func prepare(_ plan: SpecialPlan, session: UUID, uid: uid_t) throws {
        try plan.validate(confirm: plan.planID, now: now())
        if let old = pending, now().timeIntervalSince1970 >= Double(old.plan.expiresAt) { cancel(session: old.session) }
        guard pending == nil else { throw PrivilegeError.operationInProgress }
        guard !FileManager.default.fileExists(atPath: attemptPath(plan.operationID)) else { throw PrivilegeError.alreadyAttempted }
        let directory = root + "/stage-" + UUID().uuidString
        try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        do {
            var restart = false
            if let target = plan.request.package {
                let path = try stagePackage(target, directory: directory, owner: owner)
                restart = try providers.package(target, stagedPath: path, directory: directory).restart
                try providers.installedPackageApplication(target)
                let receipt = try providers.receipt(target.receiptID)
                guard receipt.version == target.fromVersion else { throw PrivilegeError.installedVersionChanged }
                try receipt.data.write(to: URL(fileURLWithPath: directory + "/before-receipt.plist"), options: .withoutOverwriting)
            } else if let target = plan.request.appStore {
                let manager = try providers.stageStoreManager(target, directory: directory, owner: owner)
                try providers.appStore(target, manager: manager, uid: uid)
                // Receipt/metadata recovery evidence; this is not an app rollback backup.
                for file in ["Info.plist", "_MASReceipt/receipt", "_CodeSignature/CodeResources"] {
                    try readRegular(target.path + "/Contents/" + file, limit: 16 << 20).write(to: URL(fileURLWithPath: directory + "/before-" + file.replacingOccurrences(of: "/", with: "-")), options: .withoutOverwriting)
                }
            } else if let target = plan.request.system {
                try providers.system(target, installing: true)
                restart = true
            } else if let id = plan.request.restartOf {
                let old = try installationAttempt(id, uid: uid)
                guard old.result.status == "awaiting_restart", old.boot == (try boot()) else { throw PrivilegeError.noPendingRestart }
            }
            pending = (session, plan, directory, restart, uid)
        } catch {
            try? FileManager.default.removeItem(atPath: directory)
            throw error
        }
    }

    func cancel(session: UUID) {
        guard let pending, pending.session == session else { return }
        try? FileManager.default.removeItem(atPath: pending.directory)
        self.pending = nil
    }

    func execute(_ id: String, session: UUID, cancellation: OperationCancellation = OperationCancellation(), authorize: () throws -> Void) throws -> OperationResult {
        guard let pending, pending.session == session, pending.plan.planID == id else { throw PrivilegeError.invalidPlan }
        try cancellation.check()
        try pending.plan.validate(confirm: id, now: now())
        try authorize()
        try cancellation.check()
        let operationID = pending.plan.operationID
        defer {
            if !FileManager.default.fileExists(atPath: attemptPath(operationID)) { try? FileManager.default.removeItem(atPath: pending.directory) }
            self.pending = nil
        }
        let plan = pending.plan
        // Only private staged bytes are passed to installer. Recheck trust and current
        // receipt immediately after the user's authorization dialog.
        if let target = plan.request.package {
            try providers.installedPackageApplication(target)
            try FileManager.default.removeItem(atPath: pending.directory + "/PackageInfo")
            guard try providers.package(target, stagedPath: pending.directory + "/approved.pkg", directory: pending.directory).restart == pending.restart,
                  try providers.receipt(target.receiptID).version == target.fromVersion else {
                throw PrivilegeError.installedVersionChanged
            }
        } else if let target = plan.request.appStore {
            try providers.appStore(target, manager: pending.directory + "/mas", uid: pending.uid)
        } else if let target = plan.request.system {
            try providers.system(target, installing: true)
        } else if let prior = plan.request.restartOf {
            let old = try installationAttempt(prior, uid: pending.uid)
            guard old.result.status == "awaiting_restart", old.boot == (try boot()) else { throw PrivilegeError.noPendingRestart }
        }
        try cancellation.check()
        if let target = plan.request.package {
            try providers.backupApplication(target.installationPath, directory: pending.directory,
                infoHash: target.infoSHA256, resourcesHash: target.codeResourcesSHA256,
                requirement: signingRequirement(team: target.teamID, identifier: target.bundleID))
        } else if let target = plan.request.appStore {
            try providers.backupApplication(target.path, directory: pending.directory,
                infoHash: target.infoSHA256, resourcesHash: target.codeResourcesSHA256,
                requirement: "anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.9] exists and identifier \"\(target.bundleID)\"")
        }
        try cancellation.check()
        let bootIdentity = try boot()
        try pending.plan.validate(confirm: id, now: now())
        try cancellation.beginInstallation()
        var result = OperationResult(planID: operationID, status: "verification_unknown", reason: "execution_started_result_not_recorded", executed: true)
        result.recoveryPath = pending.directory
        if plan.request.package != nil || plan.request.appStore != nil { result.backupPath = pending.directory + "/before.app" }
        var attempt = Attempt(plan: plan, uid: pending.uid, boot: bootIdentity, restartRequired: pending.restart, result: result)
        // An immutable initial record prevents retry after a crash or lost response.
        try createDurable(try encode(attempt), at: attemptPath(operationID))
        do {
            if plan.request.package != nil {
                let output = try providers.commands.run("/usr/sbin/installer", ["-pkg", pending.directory + "/approved.pkg", "-target", "/"])
                result = providers.verify(plan, exitStatus: output.status, restartRequired: pending.restart)
            } else if let target = plan.request.appStore {
                let manager = pending.directory + "/mas"
                let output = try providers.commands.store(manager, ["update", "--inaccurate", "--check-min-os", target.adamID], uid: pending.uid)
                result = providers.verifyStore(plan, manager: manager, uid: pending.uid, exitStatus: output.status)
            } else if let target = plan.request.system {
                let output = try providers.commands.run("/usr/sbin/softwareupdate", ["--install", target.label])
                result = providers.verify(plan, exitStatus: output.status, restartRequired: true, output: output.text)
            } else {
                result = OperationResult(planID: operationID, status: "restart_authorized", reason: "show_native_restart_dialog")
            }
        } catch {
            result = OperationResult(planID: operationID, status: "verification_unknown", reason: "native_process_result_unavailable", executed: true)
        }
        result.planID = operationID
        result.recoveryPath = pending.directory
        if plan.request.package != nil || plan.request.appStore != nil { result.backupPath = pending.directory + "/before.app" }
        attempt.result = result
        try createDurable(try encode(attempt), at: resultPath(operationID))
        return result
    }

    func status(_ id: String, uid: uid_t) throws -> OperationResult {
        var attempt = try installationAttempt(id, uid: uid)
        if attempt.result.status == "awaiting_restart", attempt.boot != (try boot()) {
            let original = attempt.result
            var verified = providers.verify(attempt.plan, exitStatus: 0, restartRequired: false)
            verified.planID = id
            verified.backupPath = original.backupPath
            verified.recoveryPath = original.recoveryPath
            attempt.result = verified
        }
        attempt.result.planID = id
        return attempt.result
    }

    private func installationAttempt(_ id: String, uid: uid_t) throws -> Attempt {
        var key = id
        var seen = Set<String>()
        for _ in 0..<64 {
            guard seen.insert(key).inserted else { throw PrivilegeError.noPendingRestart }
            let attempt = try readAttempt(key, uid: uid)
            guard let parent = attempt.plan.request.restartOf else { return attempt }
            key = parent
        }
        throw PrivilegeError.noPendingRestart
    }

    private func boot() throws -> String {
        let value = try providers.commands.run("/usr/sbin/sysctl", ["-n", "kern.boottime"])
        guard value.status == 0, !value.text.isEmpty else { throw PrivilegeError.verificationUnknown }
        return value.text.trimmingCharacters(in: .whitespacesAndNewlines)
    }
    private func readAttempt(_ id: String, uid: uid_t) throws -> Attempt {
        guard matches(id, "^[0-9a-f]{64}$") else { throw PrivilegeError.invalidRequest }
        var attempted = try decode(Attempt.self, readRegular(attemptPath(id)))
        guard attempted.uid == uid, attempted.plan.operationID == id else { throw PrivilegeError.authorizationDenied }
        guard FileManager.default.fileExists(atPath: resultPath(id)) else { return attempted }
        // The final file may exist but be empty/truncated if the helper died
        // between O_EXCL creation and fsync. Keep the immutable recovery record.
        if let completed = try? decode(Attempt.self, readRegular(resultPath(id))),
           completed.uid == attempted.uid, completed.plan == attempted.plan,
           completed.boot == attempted.boot, completed.restartRequired == attempted.restartRequired,
           completed.result.planID == id,
           completed.result.backupPath == attempted.result.backupPath,
           completed.result.recoveryPath == attempted.result.recoveryPath {
            return completed
        }
        attempted.result.status = "verification_unknown"
        attempted.result.reason = "final_record_unreadable_recovery_available"
        return attempted
    }

    private func attemptPath(_ id: String) -> String { root + "/" + id + ".attempted" }
    private func resultPath(_ id: String) -> String { root + "/" + id + ".result" }
}

func createDurable(_ data: Data, at path: String) throws {
    let fd = open(path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
    guard fd >= 0 else { throw PrivilegeError.alreadyAttempted }
    let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    try handle.write(contentsOf: data)
    try handle.synchronize()
    let parent = open(URL(fileURLWithPath: path).deletingLastPathComponent().path, O_RDONLY | O_DIRECTORY | O_CLOEXEC)
    guard parent >= 0 else { throw PrivilegeError.insecureState }
    defer { close(parent) }
    guard fsync(parent) == 0 else { throw PrivilegeError.insecureState }
}
