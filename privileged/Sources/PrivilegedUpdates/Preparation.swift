import Foundation

public struct PreparationInput: Codable {
    public var targetID: String
    public var kind: String
    public var packagePath: String?
    public var installationPath: String
    public var bundleID: String
    public var fromVersion: String
}
public struct Preparation: Codable {
    public var targetID: String
    public var status: String
    public var reason: String
    public var candidates: [Request]
    public var executionBlocker: String
}

public func prepareCandidates(_ input: PreparationInput) -> Preparation {
    prepareCandidates(input, providers: Providers())
}
func prepareCandidates(_ input: PreparationInput, providers: Providers) -> Preparation {
    var output = Preparation(targetID: input.targetID, status: "blocked", reason: "invalid_preparation_target", candidates: [], executionBlocker: "")
    do {
        guard !input.targetID.isEmpty, input.targetID.count <= 128, validVersion(input.fromVersion) else { throw PrivilegeError.invalidRequest }
        if input.kind == "package" {
            guard let path = input.packagePath, cleanAbsolutePath(path), path.hasSuffix(".pkg"),
                  cleanAbsolutePath(input.installationPath), input.installationPath.hasSuffix(".app") else { throw PrivilegeError.invalidRequest }
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-prepare-" + UUID().uuidString)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
            defer { try? FileManager.default.removeItem(at: directory) }
            let digest = try packageSHA256(path)
            let staged = try stageFile(path, sha256: digest, directory: directory.path, filename: "approved.pkg", owner: getuid())
            let team = try providers.packageTeam(staged)
            let metadata = try providers.componentMetadata(staged, directory: directory.path)
            guard metadata.application == input.installationPath, metadata.bundleID == input.bundleID,
                  metadata.version.compare(input.fromVersion, options: .numeric) == .orderedDescending else { throw PrivilegeError.packageMetadataUnsupported }
            let target = PackageTarget(infoSHA256: hash(try readRegular(input.installationPath + "/Contents/Info.plist")),
                codeResourcesSHA256: hash(try readRegular(input.installationPath + "/Contents/_CodeSignature/CodeResources", limit: 16 << 20)),
                installationPath: input.installationPath, bundleID: input.bundleID, path: path, sha256: digest,
                teamID: team, receiptID: metadata.id, fromVersion: input.fromVersion, version: metadata.version, source: "local-signed-package")
            let request = Request(kind: "package", package: target)
            try request.validate()
            try providers.installedPackageApplication(target)
            _ = try providers.packageRestart(staged)
            output.candidates = [request]
            output.reason = "signed_package_bound_to_installed_app"
        } else if input.kind == "system", input.packagePath == nil,
                  input.installationPath == "/System/Library/CoreServices/SystemVersion.plist" {
            let current = try providers.osVersion()
            guard current.version == input.fromVersion else { throw PrivilegeError.installedVersionChanged }
            let listing = try providers.commands.run("/usr/sbin/softwareupdate", ["--list"])
            guard listing.status == 0 else { throw PrivilegeError.candidateUnavailable }
            output.candidates = try systemCandidates(listing.text, current: current)
            guard !output.candidates.isEmpty else { throw PrivilegeError.candidateUnavailable }
            let silicon = try providers.commands.run("/usr/sbin/sysctl", ["-n", "hw.optional.arm64"])
            if silicon.status != 0 || silicon.text.trimmingCharacters(in: .whitespacesAndNewlines) != "0" {
                output.executionBlocker = PrivilegeError.volumeOwnerAuthorizationRequired.rawValue
            }
            output.reason = "exact_system_update_candidates"
        } else { throw PrivilegeError.invalidRequest }
        output.status = "prepared"
        if output.executionBlocker.isEmpty {
            let gate = serviceStatus()
            output.executionBlocker = gate == "enabled" ? "" : gate
        }
    } catch {
        output.candidates = []
        output.reason = (error as? PrivilegeError)?.rawValue ?? "preparation_metadata_unavailable"
    }
    return output
}

func systemCandidates(_ text: String, current: (version: String, build: String)) throws -> [Request] {
    let lines = text.components(separatedBy: .newlines).map { $0.trimmingCharacters(in: .whitespaces) }
    var result: [Request] = [], labels = Set<String>()
    for index in lines.indices {
        let line = lines[index]
        guard line.hasPrefix("* Label: macOS ") || line.hasPrefix("- Label: macOS ") else { continue }
        let label = String(line.dropFirst(9))
        guard labels.insert(label).inserted, result.count < 32, index + 1 < lines.count else { throw PrivilegeError.candidateUnavailable }
        let versions = lines[index + 1].components(separatedBy: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { $0.hasPrefix("Version: ") }
        guard versions.count == 1, let build = label.split(separator: "-").last else { throw PrivilegeError.candidateUnavailable }
        let target = SystemTarget(label: label, version: String(versions[0].dropFirst(9)), build: String(build), fromVersion: current.version, fromBuild: current.build)
        let request = Request(kind: "system", system: target)
        try request.validate()
        guard Providers.hasCandidate(target, in: text) else { throw PrivilegeError.candidateUnavailable }
        result.append(request)
    }
    return result
}
