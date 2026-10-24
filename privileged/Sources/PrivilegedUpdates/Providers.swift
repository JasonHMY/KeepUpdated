import Foundation

struct ComponentMetadata {
    var id: String
    var version: String
    var application: String
    var bundleID: String
}
struct PackageMetadata: Equatable {
    var id: String
    var version: String
    var restart: Bool
}
private final class PackageXML: NSObject, XMLParserDelegate {
    var root: [String: String]?
    var depth = 0
    var bundles: [[String: String]] = []
    func parser(_ parser: XMLParser, didStartElement name: String, namespaceURI: String?, qualifiedName: String?, attributes: [String: String]) {
        if depth == 0 && name == "pkg-info" { root = attributes }
        // Nested bundle-version/upgrade-bundle entries are references, not payload bundles.
        if name == "bundle" && depth == 1 { bundles.append(attributes) }
        depth += 1
    }
    func parser(_ parser: XMLParser, didEndElement name: String, namespaceURI: String?, qualifiedName: String?) { depth -= 1 }
}

struct Providers {
    var commands: any SystemCommands = NativeCommands()

    func packageTeam(_ path: String) throws -> String {
        let signature = try commands.run("/usr/sbin/pkgutil", ["--check-signature", path])
        let certificate = signature.text.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.filter { $0.hasPrefix("1.") }
        guard signature.status == 0, certificate.count == 1, certificate[0].hasPrefix("1. Developer ID Installer:"),
              let range = certificate[0].range(of: "\\([A-Z0-9]{10}\\)$", options: .regularExpression),
              try commands.run("/usr/sbin/spctl", ["--assess", "--type", "install", path]).status == 0 else { throw PrivilegeError.packageSignatureRejected }
        return String(certificate[0][range].dropFirst().dropLast())
    }

    func componentMetadata(_ path: String, directory: String) throws -> ComponentMetadata {
        let listing = try commands.run("/usr/bin/xar", ["-tf", path])
        let entries = listing.text.split(separator: "\n").map(String.init)
        guard listing.status == 0, entries.contains("PackageInfo"), entries.contains("Payload"),
              Set(entries).count == entries.count, entries.allSatisfy({ ["PackageInfo", "Payload", "Bom"].contains($0) }),
              try commands.run("/usr/bin/xar", ["-xf", path, "-C", directory, "PackageInfo"]).status == 0 else { throw PrivilegeError.packageMetadataUnsupported }
        let data = try readRegular(directory + "/PackageInfo")
        guard !String(decoding: data, as: UTF8.self).contains("<!DOCTYPE"), !String(decoding: data, as: UTF8.self).contains("<scripts") else { throw PrivilegeError.packageMetadataUnsupported }
        let delegate = PackageXML(), parser = XMLParser(data: data)
        parser.shouldResolveExternalEntities = false; parser.delegate = delegate
        guard parser.parse(), let metadata = delegate.root, delegate.bundles.count == 1,
              let id = metadata["identifier"], let version = metadata["version"], let location = metadata["install-location"],
              let path = delegate.bundles[0]["path"], let application = canonicalPath(location + "/" + path),
              let bundleID = delegate.bundles[0]["id"], delegate.bundles[0]["CFBundleShortVersionString"] == version else { throw PrivilegeError.packageMetadataUnsupported }
        return ComponentMetadata(id: id, version: version, application: application, bundleID: bundleID)
    }

    func packageRestart(_ path: String) throws -> Bool {
        let action = try commands.run("/usr/sbin/installer", ["-query", "RestartAction", "-pkg", path])
        let restart = action.text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard action.status == 0, ["None", "RequireRestart", "RecommendRestart"].contains(restart) else { throw PrivilegeError.packageMetadataUnsupported }
        return restart != "None"
    }

    func package(_ target: PackageTarget, stagedPath: String, directory: String) throws -> PackageMetadata {
        guard try packageTeam(stagedPath) == target.teamID else { throw PrivilegeError.packageSignatureRejected }
        let metadata = try componentMetadata(stagedPath, directory: directory)
        guard metadata.id == target.receiptID, metadata.version == target.version, metadata.application == target.installationPath,
              metadata.bundleID == target.bundleID, target.version.compare(target.fromVersion, options: .numeric) == .orderedDescending else { throw PrivilegeError.packageMetadataUnsupported }
        return PackageMetadata(id: target.receiptID, version: target.version, restart: try packageRestart(stagedPath))
    }

    func installedPackageApplication(_ target: PackageTarget) throws {
        guard canonicalPath(target.installationPath) == target.installationPath else {
            throw PrivilegeError.sourceChanged
        }
        try commands.verifyCode(target.installationPath, requirement: signingRequirement(team: target.teamID, identifier: target.bundleID))
        let info = try readRegular(target.installationPath + "/Contents/Info.plist")
        guard hash(info) == target.infoSHA256,
              hash(try readRegular(target.installationPath + "/Contents/_CodeSignature/CodeResources", limit: 16 << 20)) == target.codeResourcesSHA256 else { throw PrivilegeError.sourceChanged }
        guard let plist = try PropertyListSerialization.propertyList(from: info, format: nil) as? [String: Any],
              plist["CFBundleIdentifier"] as? String == target.bundleID,
              plist["CFBundleShortVersionString"] as? String == target.fromVersion else { throw PrivilegeError.installedVersionChanged }
        let installedReceipt = try receipt(target.receiptID)
        guard installedReceipt.version == target.fromVersion else { throw PrivilegeError.installedVersionChanged }
        let receiptData = installedReceipt.data
        guard let receiptInfo = try PropertyListSerialization.propertyList(from: receiptData, format: nil) as? [String: Any],
              receiptInfo["volume"] as? String == "/", let location = receiptInfo["install-location"] as? String else { throw PrivilegeError.packageMetadataUnsupported }
        let files = try commands.run("/usr/sbin/pkgutil", ["--files", target.receiptID])
        guard files.status == 0, files.text.split(separator: "\n").contains(where: {
            canonicalPath(location + "/" + String($0)) == target.installationPath + "/Contents/Info.plist"
        }) else { throw PrivilegeError.packageMetadataUnsupported }
        let processes = try commands.run("/bin/ps", ["-axo", "comm="])
        guard processes.status == 0 else { throw PrivilegeError.verificationUnknown }
        guard !processes.text.split(separator: "\n").contains(where: { $0.trimmingCharacters(in: .whitespaces).hasPrefix(target.installationPath + "/") }) else {
            throw PrivilegeError.appStoreRunning
        }
    }

    func backupApplication(_ path: String, directory: String, infoHash: String, resourcesHash: String, requirement: String) throws {
        let backup = directory + "/before.app"
        guard try commands.run("/usr/bin/ditto", ["--rsrc", "--extattr", path, backup]).status == 0 else { throw PrivilegeError.sourceChanged }
        for root in [path, backup] {
            guard hash(try readRegular(root + "/Contents/Info.plist")) == infoHash,
                  hash(try readRegular(root + "/Contents/_CodeSignature/CodeResources", limit: 16 << 20)) == resourcesHash else { throw PrivilegeError.sourceChanged }
            try commands.verifyCode(root, requirement: requirement)
        }
    }

    func receipt(_ id: String) throws -> (version: String, data: Data) {
        let result = try commands.run("/usr/sbin/pkgutil", ["--pkg-info-plist", id])
        guard result.status == 0,
              let dict = try PropertyListSerialization.propertyList(from: result.data, format: nil) as? [String: Any],
              dict["pkgid"] as? String == id, let version = dict["pkg-version"] as? String,
              validVersion(version) else { throw PrivilegeError.verificationUnknown }
        return (version, result.data)
    }

    func osVersion() throws -> (version: String, build: String) {
        let version = try commands.run("/usr/bin/sw_vers", ["-productVersion"])
        let build = try commands.run("/usr/bin/sw_vers", ["-buildVersion"])
        guard version.status == 0, build.status == 0 else { throw PrivilegeError.verificationUnknown }
        return (version.text.trimmingCharacters(in: .whitespacesAndNewlines), build.text.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    func system(_ target: SystemTarget, installing: Bool) throws {
        let current = try osVersion()
        guard current.version == target.fromVersion, current.build == target.fromBuild else {
            throw PrivilegeError.installedVersionChanged
        }
        let candidates = try commands.run("/usr/sbin/softwareupdate", ["--list"])
        guard candidates.status == 0, Self.hasCandidate(target, in: candidates.text) else { throw PrivilegeError.candidateUnavailable }
        if installing {
            let silicon = try commands.run("/usr/sbin/sysctl", ["-n", "hw.optional.arm64"])
            // No password/stdinpass, token manipulation, MDM enrollment or implicit fallback.
            guard silicon.status == 0, silicon.text.trimmingCharacters(in: .whitespacesAndNewlines) == "0" else {
                throw PrivilegeError.volumeOwnerAuthorizationRequired
            }
        }
    }

    static func hasCandidate(_ target: SystemTarget, in text: String) -> Bool {
        let lines = text.components(separatedBy: .newlines).map { $0.trimmingCharacters(in: .whitespaces) }
        var matches = 0
        var labels = 0
        for index in lines.indices where lines[index] == "* Label: " + target.label || lines[index] == "- Label: " + target.label {
            labels += 1
            guard index + 1 < lines.count else { return false }
            let fields = lines[index + 1].components(separatedBy: ",").map { $0.trimmingCharacters(in: .whitespaces) }
            if fields.contains("Version: " + target.version), fields.contains("Action: restart") { matches += 1 }
        }
        return labels == 1 && matches == 1
    }

    func verify(_ plan: SpecialPlan, exitStatus: Int32, restartRequired: Bool, output: String = "") -> OperationResult {
        var result = OperationResult(planID: plan.planID, status: "verification_unknown", reason: "requested_state_unverified",
                                     executed: true, commandSucceeded: exitStatus == 0)
        do {
            if let target = plan.request.package {
                result.observedVersion = try receipt(target.receiptID).version
                let data = try readRegular(target.installationPath + "/Contents/Info.plist")
                let info = try PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any]
                try commands.verifyCode(target.installationPath, requirement: signingRequirement(team: target.teamID, identifier: target.bundleID))
                if exitStatus == 0 && result.observedVersion == target.version && info?["CFBundleIdentifier"] as? String == target.bundleID && info?["CFBundleShortVersionString"] as? String == target.version {
                    result.status = restartRequired ? "awaiting_restart" : "installed"
                    result.reason = restartRequired ? "receipt_verified_restart_required" : "receipt_version_verified"
                }
            } else if let target = plan.request.system {
                let current = try osVersion()
                result.observedVersion = current.version; result.observedBuild = current.build
                if exitStatus == 0 && current.version == target.version && current.build == target.build {
                    result.status = "installed"; result.reason = "os_version_and_build_verified"
                } else if exitStatus == 0 && current.version == target.fromVersion && current.build == target.fromBuild &&
                            output.contains("Please restart your computer") {
                    result.status = "awaiting_restart"; result.reason = "system_tool_requested_restart_target_not_yet_verified"
                }
            }
        } catch { result.reason = "post_install_readback_unavailable" }
        if exitStatus != 0 { result.status = "failed"; result.reason = "native_installer_failed_inspect_recovery_record" }
        return result
    }
}

// Read-only preview; the root helper repeats these checks using its own private copy.
public func preview(_ request: Request, now: Date = Date()) throws -> SpecialPlan {
    try request.validate()
    let providers = Providers()
    if let target = request.package {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-preview-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: directory) }
        let path = try stagePackage(target, directory: directory.path, owner: getuid())
        _ = try providers.package(target, stagedPath: path, directory: directory.path)
        try providers.installedPackageApplication(target)
        guard try providers.receipt(target.receiptID).version == target.fromVersion else { throw PrivilegeError.installedVersionChanged }
    } else if let target = request.appStore {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-store-preview-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: directory) }
        let manager = try providers.stageStoreManager(target, directory: directory.path, owner: getuid())
        try providers.appStore(target, manager: manager, uid: getuid())
    } else if let target = request.system {
        try providers.system(target, installing: true)
    }
    return try SpecialPlan.make(request, now: now)
}
