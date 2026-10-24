import XCTest
import Security
@testable import PrivilegedUpdates

final class FixtureCommands: SystemCommands {
    var applicationPath = ""
    var calls: [(String, [String])] = []
    var installed = "1.0"
    var packageVersion = "2.0"
    var archiveListing = "PackageInfo\nPayload\nBom\n"
    var signatureAccepted = true
    var restart = "None"
    var exitStatus: Int32 = 0
    var readbackFails = false
    var corruptBackup = false
    var backupStarted: DispatchSemaphore?
    var backupResume: DispatchSemaphore?
    var installationStarted: (() -> Void)?
    var mutateReceiptOnInstall = true
    var boot = "boot-one"
    var silicon = "0"
    var systemVersion = "13.6.6"
    var systemBuild = "22G630"
    var systemOutput = "Please restart your computer"
    var labels = "* Label: macOS Ventura 13.6.7-22G720\nTitle: macOS Ventura, Version: 13.6.7, Size: 100K, Recommended: YES, Action: restart,\n"
    func run(_ executable: String, _ arguments: [String]) throws -> CommandOutput {
        calls.append((executable, arguments))
        func result(_ text: String, status: Int32 = 0) -> CommandOutput { CommandOutput(status: status, data: Data(text.utf8)) }
        switch (executable, arguments.first ?? "") {
        case ("/usr/sbin/pkgutil", "--check-signature"):
            return result("1. Developer ID Installer: Fixture (ABCDEFGHIJ)\n", status: signatureAccepted ? 0 : 1)
        case ("/usr/sbin/spctl", _): return result("", status: signatureAccepted ? 0 : 1)
        case ("/usr/bin/xar", "-tf"): return result(archiveListing)
        case ("/usr/bin/xar", "-xf"):
            let directory = arguments[3]
            try Data("<pkg-info identifier=\"com.example.fixture\" version=\"\(packageVersion)\" install-location=\"/\"><bundle id=\"com.example.fixture\" path=\"\(applicationPath.dropFirst())\" CFBundleShortVersionString=\"\(packageVersion)\"/></pkg-info>".utf8).write(to: URL(fileURLWithPath: directory + "/PackageInfo"))
            return result("")
        case ("/usr/sbin/pkgutil", "--pkg-info-plist"):
            if readbackFails { throw PrivilegeError.verificationUnknown }
            let data = try PropertyListSerialization.data(fromPropertyList: ["pkgid": "com.example.fixture", "pkg-version": installed, "volume": "/", "install-location": "/"], format: .xml, options: 0)
            return CommandOutput(status: 0, data: data)
        case ("/usr/sbin/pkgutil", "--files"): return result(String(applicationPath.dropFirst()) + "/Contents/Info.plist\n")
        case ("/usr/bin/ditto", _):
            backupStarted?.signal()
            if let backupResume, backupResume.wait(timeout: .now() + 5) != .success { throw PrivilegeError.verificationUnknown }
            try FileManager.default.copyItem(atPath: arguments[2], toPath: arguments[3])
            if corruptBackup { try Data("corrupt backup".utf8).write(to: URL(fileURLWithPath: arguments[3] + "/Contents/_CodeSignature/CodeResources")) }
            return result("")
        case ("/bin/ps", _): return result("")
        case ("/usr/sbin/installer", "-query"): return result(restart)
        case ("/usr/sbin/installer", "-pkg"):
            installationStarted?()
            if mutateReceiptOnInstall {
                installed = "2.0"
                let plist = try PropertyListSerialization.data(fromPropertyList: ["CFBundleIdentifier": "com.example.fixture", "CFBundleShortVersionString": "2.0"], format: .xml, options: 0)
                try plist.write(to: URL(fileURLWithPath: applicationPath + "/Contents/Info.plist"))
            }
            return result("", status: exitStatus)
        case ("/usr/sbin/sysctl", _): return result(arguments.last == "kern.boottime" ? boot : silicon)
        case ("/usr/bin/sw_vers", "-productVersion"): return result(systemVersion)
        case ("/usr/bin/sw_vers", "-buildVersion"): return result(systemBuild)
        case ("/usr/sbin/softwareupdate", "--list"): return result(labels)
        case ("/usr/sbin/softwareupdate", "--install"): return result(systemOutput, status: exitStatus)
        default: XCTFail("Unexpected fixture command \(executable) \(arguments)"); throw PrivilegeError.invalidRequest
        }
    }
    func store(_ executable: String, _ arguments: [String], uid: uid_t) throws -> CommandOutput { throw PrivilegeError.appStoreManagerNotBundled }
    func verifyCode(_ path: String, requirement: String) throws { if !signatureAccepted { throw PrivilegeError.packageSignatureRejected } }
    var installations: Int { calls.filter { $0.0 == "/usr/sbin/installer" && $0.1.first == "-pkg" || $0.0 == "/usr/sbin/softwareupdate" && $0.1.first == "--install" }.count }
}

final class PrivilegeTests: XCTestCase {
    let now = Date(timeIntervalSince1970: 1_800_000_000)
    var directories: [URL] = []
    override func tearDown() { for directory in directories { try? FileManager.default.removeItem(at: directory) } }
    func directory() throws -> String {
        let path = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-privilege-test-" + UUID().uuidString).resolvingSymlinksInPath()
        try FileManager.default.createDirectory(at: path, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        directories.append(path)
        return canonicalPath(path.path)!
    }
    func package() throws -> Request {
        let path = try directory() + "/fixture.pkg"
        let content = Data("synthetic package bytes; never an installer".utf8)
        try content.write(to: URL(fileURLWithPath: path))
        let app = try directory() + "/Fixture.app"
        try FileManager.default.createDirectory(atPath: app + "/Contents", withIntermediateDirectories: true)
        let plist = try PropertyListSerialization.data(fromPropertyList: ["CFBundleIdentifier": "com.example.fixture", "CFBundleShortVersionString": "1.0"], format: .xml, options: 0)
        try plist.write(to: URL(fileURLWithPath: app + "/Contents/Info.plist"))
        try FileManager.default.createDirectory(atPath: app + "/Contents/_CodeSignature", withIntermediateDirectories: true)
        let resources = Data("fixture signature metadata".utf8)
        try resources.write(to: URL(fileURLWithPath: app + "/Contents/_CodeSignature/CodeResources"))
        return Request(kind: "package", package: PackageTarget(infoSHA256: PrivilegedUpdates.hash(plist), codeResourcesSHA256: PrivilegedUpdates.hash(resources), installationPath: app, bundleID: "com.example.fixture", path: path, sha256: PrivilegedUpdates.hash(content), teamID: "ABCDEFGHIJ", receiptID: "com.example.fixture", fromVersion: "1.0", version: "2.0", source: "local-signed-package"))
    }
    func system() -> Request {
        Request(kind: "system", system: SystemTarget(label: "macOS Ventura 13.6.7-22G720", version: "13.6.7", build: "22G720", fromVersion: "13.6.6", fromBuild: "22G630"))
    }
    func engine(_ commands: FixtureCommands) throws -> HelperEngine {
        try HelperEngine(root: directory(), owner: getuid(), providers: Providers(commands: commands), now: { self.now })
    }
    func testExactPlanUnknownFieldsExpiryAndEffects() throws {
        var plan = try SpecialPlan.make(package(), now: now)
        try plan.validate(confirm: plan.planID, now: now)
        XCTAssertThrowsError(try plan.validate(confirm: "yes", now: now))
        XCTAssertThrowsError(try plan.validate(confirm: plan.planID, now: now.addingTimeInterval(601)))
        plan.effects.administrator = false
        XCTAssertThrowsError(try plan.validate(confirm: plan.planID, now: now))
        var object = try JSONSerialization.jsonObject(with: encode(package())) as! [String: Any]
        object["argv"] = ["/bin/sh", "-c", "id"]
        XCTAssertThrowsError(try decode(Request.self, JSONSerialization.data(withJSONObject: object)))
        var request = try package(); request.kind = "shell"
        XCTAssertThrowsError(try request.validate())
    }
    func testMetadataReadIsBoundedAndRejectsSymlinks() throws {
        let root = try directory(), path = root + "/metadata"
        try Data(repeating: 1, count: 128).write(to: URL(fileURLWithPath: path))
        XCTAssertEqual(try readRegular(path, limit: 128).count, 128)
        XCTAssertThrowsError(try readRegular(path, limit: 127))
        let link = root + "/link"
        try FileManager.default.createSymbolicLink(atPath: link, withDestinationPath: path)
        XCTAssertThrowsError(try readRegular(link, limit: 128))
    }
    func testStageRejectsChangedBytesAndSymlink() throws {
        var request = try package()
        try Data("changed".utf8).write(to: URL(fileURLWithPath: request.package!.path))
        XCTAssertThrowsError(try stagePackage(request.package!, directory: directory(), owner: getuid()))
        request = try package()
        let link = try directory() + "/link.pkg"
        try FileManager.default.createSymbolicLink(atPath: link, withDestinationPath: request.package!.path)
        request.package!.path = link
        XCTAssertThrowsError(try stagePackage(request.package!, directory: directory(), owner: getuid()))
    }
    func testExactAuthorizationSessionAndOneShotExecution() throws {
        let commands = FixtureCommands(), service = try engine(commands), session = UUID()
        let plan = try SpecialPlan.make(package(), now: now)
        commands.applicationPath = plan.request.package?.installationPath ?? ""
        try service.prepare(plan, session: session, uid: getuid())
        XCTAssertEqual(commands.installations, 0)
        XCTAssertThrowsError(try service.execute(plan.planID, session: UUID(), authorize: {}))
        XCTAssertThrowsError(try service.execute(plan.planID, session: session) { throw PrivilegeError.authorizationDenied })
        XCTAssertEqual(commands.installations, 0)
        let result = try service.execute(plan.planID, session: session, authorize: {})
        XCTAssertEqual(result.status, "installed")
        XCTAssertTrue(result.executed)
        XCTAssertEqual(commands.installations, 1)
        XCTAssertThrowsError(try service.prepare(plan, session: UUID(), uid: getuid()))
        XCTAssertThrowsError(try service.status(plan.planID, uid: getuid() + 1))
        XCTAssertEqual(try service.status(plan.planID, uid: getuid()).status, "installed")
    }
    func testSourceReplacementUsesApprovedPrivateCopy() throws {
        let commands = FixtureCommands(), service = try engine(commands), session = UUID()
        let request = try package(), plan = try SpecialPlan.make(request, now: now)
        commands.applicationPath = plan.request.package?.installationPath ?? ""
        try service.prepare(plan, session: session, uid: getuid())
        try Data("replacement".utf8).write(to: URL(fileURLWithPath: request.package!.path))
        _ = try service.execute(plan.planID, session: session, authorize: {})
        let call = commands.calls.first { $0.0 == "/usr/sbin/installer" && $0.1.first == "-pkg" }!
        XCTAssertNotEqual(call.1[1], request.package!.path)
        XCTAssertEqual(PrivilegedUpdates.hash(try readRegular(call.1[1])), request.package!.sha256)
        XCTAssertEqual(call.1, ["-pkg", call.1[1], "-target", "/"])
    }
    func testSignatureVersionDriftAndCancellationBlockExecution() throws {
        for failure in ["signature", "version", "scripts", "receipt", "cancel"] {
            let commands = FixtureCommands(), service = try engine(commands), session = UUID()
            let plan = try SpecialPlan.make(package(), now: now)
            commands.applicationPath = plan.request.package?.installationPath ?? ""
            if failure == "signature" { commands.signatureAccepted = false }
            if failure == "version" { commands.packageVersion = "3.0" }
            if failure == "scripts" { commands.archiveListing += "Scripts\n" }
            if failure == "signature" || failure == "version" || failure == "scripts" {
                XCTAssertThrowsError(try service.prepare(plan, session: session, uid: getuid()))
            } else {
                commands.applicationPath = plan.request.package?.installationPath ?? ""
                try service.prepare(plan, session: session, uid: getuid())
                if failure == "receipt" { commands.installed = "9.0" } else { service.cancel(session: session) }
                XCTAssertThrowsError(try service.execute(plan.planID, session: session, authorize: {}))
            }
            XCTAssertEqual(commands.installations, 0)
        }
    }
    func testCancelOrDisconnectDuringBackupDoesNotStartInstaller() throws {
        for disconnected in [false, true] {
            let commands = FixtureCommands(), service = try engine(commands)
            let queue = DispatchQueue(label: "fixture.backup")
            let session = Session(uid: getuid(), engine: service, queue: queue)
            let plan = try SpecialPlan.make(package(), now: now)
            commands.applicationPath = plan.request.package!.installationPath
            try service.prepare(plan, session: session.id, uid: getuid())
            let started = DispatchSemaphore(value: 0), resume = DispatchSemaphore(value: 0)
            commands.backupStarted = started; commands.backupResume = resume
            let finished = expectation(description: "backup finishes without install")
            queue.async {
                do {
                    _ = try service.execute(plan.planID, session: session.id, cancellation: session.cancellation, authorize: {})
                    XCTFail("cancelled backup started installation")
                } catch { XCTAssertEqual(error as? PrivilegeError, .cancelled) }
                finished.fulfill()
            }
            XCTAssertEqual(started.wait(timeout: .now() + 5), .success)
            let acknowledged = DispatchSemaphore(value: 0)
            // The listener routes both invalidation and interruption through this
            // same method. Its reply must not wait behind the blocked backup.
            if disconnected { session.connectionClosed(); acknowledged.signal() }
            else { session.cancel { acknowledged.signal() } }
            XCTAssertEqual(acknowledged.wait(timeout: .now() + 0.2), .success)
            resume.signal()
            wait(for: [finished], timeout: 5)
            XCTAssertEqual(commands.installations, 0)
        }
    }
    func testCancellationAfterInstallationTransitionPreservesReadback() throws {
        let cancellation = OperationCancellation()
        let commands = FixtureCommands(), service = try engine(commands), session = UUID()
        commands.installationStarted = { cancellation.cancel() }
        let plan = try SpecialPlan.make(package(), now: now)
        commands.applicationPath = plan.request.package!.installationPath
        try service.prepare(plan, session: session, uid: getuid())
        XCTAssertEqual(try service.execute(plan.planID, session: session, cancellation: cancellation, authorize: {}).status, "installed")
    }
    func testKnownOperationKeyAndAttemptOnlyRecoveryRetainBackup() throws {
        let commands = FixtureCommands(), service = try engine(commands), session = UUID()
        var request = try package()
        let operationID = String(repeating: "b", count: 64)
        request.operationID = operationID
        let plan = try SpecialPlan.make(request, now: now)
        XCTAssertNotEqual(plan.planID, operationID)
        commands.applicationPath = request.package!.installationPath
        try service.prepare(plan, session: session, uid: getuid())
        let result = try service.execute(plan.planID, session: session, authorize: {})
        XCTAssertEqual(result.planID, operationID)
        // A crash after mutation but before final journaling leaves this exact
        // durable attempted record. No response or completed record is needed.
        try FileManager.default.removeItem(atPath: service.root + "/" + operationID + ".result")
        let recovered = try service.status(operationID, uid: getuid())
        XCTAssertEqual(recovered.status, "verification_unknown")
        XCTAssertEqual(recovered.reason, "execution_started_result_not_recorded")
        XCTAssertEqual(recovered.backupPath, result.backupPath)
        XCTAssertEqual(recovered.recoveryPath, result.recoveryPath)
        XCTAssertTrue(FileManager.default.fileExists(atPath: recovered.backupPath! + "/Contents/Info.plist"))
        XCTAssertTrue(FileManager.default.fileExists(atPath: recovered.recoveryPath! + "/before-receipt.plist"))
        let attemptedBytes = try readRegular(service.root + "/" + operationID + ".attempted")
        for corrupt in [Data(), Data("{\"plan\":".utf8)] {
            try corrupt.write(to: URL(fileURLWithPath: service.root + "/" + operationID + ".result"))
            let partial = try service.status(operationID, uid: getuid())
            XCTAssertEqual(partial.status, "verification_unknown")
            XCTAssertEqual(partial.reason, "final_record_unreadable_recovery_available")
            XCTAssertEqual(partial.backupPath, result.backupPath)
            XCTAssertEqual(partial.recoveryPath, result.recoveryPath)
            XCTAssertEqual(try readRegular(service.root + "/" + operationID + ".attempted"), attemptedBytes)
            XCTAssertThrowsError(try service.prepare(plan, session: UUID(), uid: getuid()))
        }
    }
    func testUnverifiedBackupStopsBeforeInstallation() throws {
        let commands = FixtureCommands(), service = try engine(commands), session = UUID()
        let plan = try SpecialPlan.make(package(), now: now)
        commands.applicationPath = plan.request.package!.installationPath
        try service.prepare(plan, session: session, uid: getuid())
        commands.corruptBackup = true
        XCTAssertThrowsError(try service.execute(plan.planID, session: session, authorize: {}))
        XCTAssertEqual(commands.installations, 0)
    }
    func testFailureAndMissingReadbackAreNeverSuccess() throws {
        for failure in ["exit", "unchanged"] {
            let commands = FixtureCommands(), service = try engine(commands), session = UUID()
            let plan = try SpecialPlan.make(package(), now: now)
            commands.applicationPath = plan.request.package?.installationPath ?? ""
        try service.prepare(plan, session: session, uid: getuid())
            if failure == "exit" { commands.exitStatus = 1 } else { commands.mutateReceiptOnInstall = false }
            let result = try service.execute(plan.planID, session: session, authorize: {})
            XCTAssertEqual(result.status, failure == "exit" ? "failed" : "verification_unknown")
        }
    }
    func testPendingRestartRequiresSeparatePlanAndPostBootReadback() throws {
        let commands = FixtureCommands(), service = try engine(commands), session = UUID()
        commands.restart = "RequireRestart"
        let plan = try SpecialPlan.make(package(), now: now)
        commands.applicationPath = plan.request.package?.installationPath ?? ""
        try service.prepare(plan, session: session, uid: getuid())
        XCTAssertEqual(try service.execute(plan.planID, session: session, authorize: {}).status, "awaiting_restart")
        XCTAssertEqual(commands.installations, 1)
        let restart = try SpecialPlan.make(Request(kind: "restart", restartOf: plan.planID), now: now)
        let restartSession = UUID()
        try service.prepare(restart, session: restartSession, uid: getuid())
        XCTAssertEqual(try service.execute(restart.planID, session: restartSession, authorize: {}).status, "restart_authorized")
        XCTAssertEqual(commands.installations, 1)
        // Cancelling the native restart dialog leaves the installation pending.
        // Its latest returned operation key must support another explicit preview.
        let retry = try SpecialPlan.make(Request(kind: "restart", restartOf: restart.planID), now: now)
        let retrySession = UUID()
        try service.prepare(retry, session: retrySession, uid: getuid())
        XCTAssertEqual(try service.execute(retry.planID, session: retrySession, authorize: {}).status, "restart_authorized")
        XCTAssertEqual(try service.status(retry.planID, uid: getuid()).status, "awaiting_restart")
        commands.boot = "boot-two"
        XCTAssertEqual(try service.status(retry.planID, uid: getuid()).status, "installed")
        XCTAssertEqual(try service.status(plan.planID, uid: getuid()).status, "installed")
        commands.installed = "1.0"
        XCTAssertEqual(try service.status(plan.planID, uid: getuid()).status, "verification_unknown")
    }
    func testSystemExactLabelNoImplicitRestartAndVolumeOwnerGate() throws {
        let commands = FixtureCommands(), service = try engine(commands), session = UUID()
        let plan = try SpecialPlan.make(system(), now: now)
        commands.silicon = "1"
        XCTAssertThrowsError(try service.prepare(plan, session: session, uid: getuid())) { XCTAssertEqual($0 as? PrivilegeError, .volumeOwnerAuthorizationRequired) }
        XCTAssertEqual(commands.installations, 0)
        commands.silicon = "0"
        commands.applicationPath = plan.request.package?.installationPath ?? ""
        try service.prepare(plan, session: session, uid: getuid())
        XCTAssertEqual(try service.execute(plan.planID, session: session, authorize: {}).status, "awaiting_restart")
        XCTAssertTrue(commands.calls.contains { $0.0 == "/usr/sbin/softwareupdate" && $0.1 == ["--install", plan.request.system!.label] })
        XCTAssertFalse(commands.calls.contains { $0.1.contains("--all") || $0.1.contains("--restart") || $0.1.contains("--stdinpass") })
        commands.boot = "boot-two"; commands.systemVersion = "13.6.7"; commands.systemBuild = "22G720"
        XCTAssertEqual(try service.status(plan.planID, uid: getuid()).status, "installed")
    }
    func testAmbiguousSystemListAndStatePermissionsReject() throws {
        let commands = FixtureCommands(), service = try engine(commands)
        commands.labels += commands.labels
        let plan = try SpecialPlan.make(system(), now: now)
        XCTAssertThrowsError(try service.prepare(plan, session: UUID(), uid: getuid()))
        let path = try directory(); chmod(path, 0o755)
        XCTAssertThrowsError(try HelperEngine(root: path, owner: getuid(), providers: Providers(commands: commands)))
        XCTAssertThrowsError(try HelperEngine(root: service.root, owner: getuid(), providers: Providers(commands: commands)))
    }
    func testAppStoreRequiresSealedBundledManager() throws {
        let digest = String(repeating: "a", count: 64)
        let target = AppStoreTarget(adamID: "123456", bundleID: "com.example.fixture", path: "/Applications/Fixture.app", receiptSHA256: digest, infoSHA256: digest, codeResourcesSHA256: digest, fromVersion: "1.0", version: "2.0", region: "US", managerSHA256: digest, managerVersion: "7.0.0")
        let request = Request(kind: "app_store_update", appStore: target)
        try request.validate()
        let commands = FixtureCommands(), service = try engine(commands)
        let plan = try SpecialPlan.make(request, now: now)
        XCTAssertThrowsError(try service.prepare(plan, session: UUID(), uid: getuid())) {
            XCTAssertEqual($0 as? PrivilegeError, .appStoreManagerNotBundled)
        }
        XCTAssertEqual(commands.installations, 0)
        var object = try JSONSerialization.jsonObject(with: encode(request)) as! [String: Any]
        object["managerPath"] = "/tmp/mas"
        XCTAssertThrowsError(try decode(Request.self, JSONSerialization.data(withJSONObject: object)))
    }
    func testPackagePreparationDiscoversBindingWithoutInstalling() throws {
        let request = try package(), commands = FixtureCommands()
        let target = request.package!
        commands.applicationPath = target.installationPath
        let input = PreparationInput(targetID: "fixture-app", kind: "package", packagePath: target.path, installationPath: target.installationPath, bundleID: target.bundleID, fromVersion: target.fromVersion)
        let prepared = prepareCandidates(input, providers: Providers(commands: commands))
        XCTAssertEqual(prepared.status, "prepared")
        XCTAssertEqual(prepared.candidates, [request])
        XCTAssertEqual(prepared.executionBlocker, "developer_signature_required")
        XCTAssertEqual(commands.installations, 0)
        commands.signatureAccepted = false
        let rejected = prepareCandidates(input, providers: Providers(commands: commands))
        XCTAssertEqual(rejected.status, "blocked")
        XCTAssertTrue(rejected.candidates.isEmpty)
        XCTAssertEqual(commands.installations, 0)
    }
    func testSystemPreparationListsExactCandidatesAndHonestBlocker() throws {
        let commands = FixtureCommands()
        commands.silicon = "1"
        let input = PreparationInput(targetID: "fixture-os", kind: "system", installationPath: "/System/Library/CoreServices/SystemVersion.plist", bundleID: "", fromVersion: commands.systemVersion)
        let prepared = prepareCandidates(input, providers: Providers(commands: commands))
        XCTAssertEqual(prepared.status, "prepared")
        XCTAssertEqual(prepared.candidates, [system()])
        XCTAssertEqual(prepared.executionBlocker, "volume_owner_authorization_required")
        XCTAssertEqual(commands.installations, 0)
        commands.labels += commands.labels
        let ambiguous = prepareCandidates(input, providers: Providers(commands: commands))
        XCTAssertEqual(ambiguous.status, "blocked")
        XCTAssertTrue(ambiguous.candidates.isEmpty)
    }
    func testSigningRequirementParses() {
        var requirement: SecRequirement?
        XCTAssertEqual(SecRequirementCreateWithString(signingRequirement(team: "ABCDEFGHIJ", identifier: authorizationIdentifier) as CFString, [], &requirement), errSecSuccess)
        XCTAssertNotNil(requirement)
    }
    func testUnsignedDevelopmentBinaryCannotAuthorize() {
        XCTAssertThrowsError(try signingTeam())
        XCTAssertEqual(serviceStatus(), "developer_signature_required")
    }
}
