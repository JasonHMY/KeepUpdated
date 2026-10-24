import Foundation
import Darwin

func enclosingApp() throws -> URL {
    var url = URL(fileURLWithPath: CommandLine.arguments[0]).resolvingSymlinksInPath().deletingLastPathComponent()
    for _ in 0..<6 {
        if url.pathExtension == "app" { return url }
        url.deleteLastPathComponent()
    }
    throw PrivilegeError.appStoreManagerNotBundled
}

extension Providers {
    func stageStoreManager(_ target: AppStoreTarget, directory: String, owner: uid_t) throws -> String {
        let source = try enclosingApp().appendingPathComponent("Contents/Library/Tools/mas").path
        guard FileManager.default.fileExists(atPath: source) else { throw PrivilegeError.appStoreManagerNotBundled }
        let path = try stageFile(source, sha256: target.managerSHA256, directory: directory, filename: "mas", owner: owner)
        let requirement = signingRequirement(team: try signingTeam(), identifier: "com.keepupdated.mas")
        try commands.verifyCode(path, requirement: requirement)
        let dependencies = try commands.run("/usr/bin/otool", ["-L", path])
        let lines = dependencies.text.split(separator: "\n").dropFirst()
        guard dependencies.status == 0, !lines.isEmpty,
              lines.allSatisfy({ line in
                  let path = line.trimmingCharacters(in: .whitespaces).components(separatedBy: " (compatibility version:")[0]
                  return path.hasPrefix("/System/Library/") || path.hasPrefix("/usr/lib/")
              }), chmod(path, 0o700) == 0 else { throw PrivilegeError.appStoreManagerNotBundled }
        return path
    }

    func storeObject(_ path: String, _ args: [String], uid: uid_t) throws -> [String: Any] {
        let output = try commands.store(path, args, uid: uid)
        guard output.status == 0, let object = try JSONSerialization.jsonObject(with: output.data) as? [String: Any] else {
            throw PrivilegeError.appStoreCandidateChanged
        }
        return object
    }

    // Spotlight and the sealed manager must independently identify one physical app.
    func installedStoreIdentity(_ target: AppStoreTarget, version: String, manager: String, uid: uid_t) throws -> String {
        guard sameStoreDirectory(target.path, target.path) else { throw PrivilegeError.appStoreIdentityChanged }
        try commands.verifyCode(target.path, requirement: "anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.9] exists and identifier \"\(target.bundleID)\"")
        let info = try readRegular(target.path + "/Contents/Info.plist")
        guard let plist = try PropertyListSerialization.propertyList(from: info, format: nil) as? [String: Any],
              plist["CFBundleIdentifier"] as? String == target.bundleID,
              plist["CFBundleShortVersionString"] as? String == version else { throw PrivilegeError.appStoreIdentityChanged }
        for (field, expected) in [("kMDItemCFBundleIdentifier", target.bundleID), ("kMDItemVersion", version), ("kMDItemAppStoreAdamID", target.adamID)] {
            let output = try commands.run("/usr/bin/mdls", ["-raw", "-name", field, target.path])
            guard output.status == 0, output.text.trimmingCharacters(in: .whitespacesAndNewlines) == expected else { throw PrivilegeError.appStoreIdentityChanged }
        }
        for query in ["kMDItemAppStoreAdamID == " + target.adamID, "kMDItemCFBundleIdentifier == \"\(target.bundleID)\""] {
            let output = try commands.run("/usr/bin/mdfind", ["-0", query])
            let paths = output.text.components(separatedBy: "\0")
            guard output.status == 0, paths.count == 2, paths[1].isEmpty,
                  sameStoreDirectory(paths[0], target.path) else { throw PrivilegeError.appStoreIdentityChanged }
        }
        // Native mas 7 emits the identifying fields without --json; --json selects
        // a different Spotlight projection that can contain only display names.
        let installed = try storeObject(manager, ["list", target.adamID], uid: uid)
        guard (installed["adamID"] as? NSNumber)?.stringValue == target.adamID,
              installed["bundleID"] as? String == target.bundleID,
              let path = installed["path"] as? String, sameStoreDirectory(path, target.path),
              installed["version"] as? String == version else { throw PrivilegeError.appStoreIdentityChanged }
        return version
    }

    func appStore(_ target: AppStoreTarget, manager: String, uid: uid_t) throws {
        _ = try installedStoreIdentity(target, version: target.fromVersion, manager: manager, uid: uid)
        for (file, digest) in [("Contents/Info.plist", target.infoSHA256), ("Contents/_MASReceipt/receipt", target.receiptSHA256),
                               ("Contents/_CodeSignature/CodeResources", target.codeResourcesSHA256)] {
            let path = target.path + "/" + file
            guard storePathInfo(path) != nil, hash(try readRegular(path, limit: 16 << 20)) == digest else { throw PrivilegeError.appStoreIdentityChanged }
        }
        let config = try storeObject(manager, ["config", "--json"], uid: uid)
        guard config["mas"] as? String == "7.0.0", config["store"] as? String == target.region else { throw PrivilegeError.appStoreCandidateChanged }
        let catalog = try storeObject(manager, ["lookup", "--json", target.adamID], uid: uid)
        let macProduct = catalog["kind"] as? String == "mac-software" ||
            catalog["kind"] as? String == "software" && (catalog["supportedDevices"] as? [String])?.contains("MacDesktop-MacDesktop") == true
        guard (catalog["adamID"] as? NSNumber)?.stringValue == target.adamID,
              catalog["bundleID"] as? String == target.bundleID, macProduct,
              catalog["version"] as? String == target.version,
              let minimum = catalog["minimumOSVersion"] as? String, matches(minimum, "^[0-9]+(\\.[0-9]+)*$"),
              let current = config["macos"] as? String, let numeric = storeOSVersion(current),
              numeric.compare(minimum, options: .numeric) != .orderedAscending else { throw PrivilegeError.appStoreCandidateChanged }
        let processes = try commands.run("/bin/ps", ["-axo", "comm="])
        guard processes.status == 0 else { throw PrivilegeError.verificationUnknown }
        let appPaths = [target.path, storeDataPath(target.path)]
        guard !processes.text.split(separator: "\n").contains(where: {
            let path = $0.trimmingCharacters(in: .whitespaces)
            return appPaths.contains { path == $0 || path.hasPrefix($0 + "/") }
        }) else { throw PrivilegeError.appStoreRunning }
    }

    func verifyStore(_ plan: SpecialPlan, manager: String, uid: uid_t, exitStatus: Int32) -> OperationResult {
        var result = OperationResult(planID: plan.planID, status: "verification_unknown", reason: "app_store_target_unverified", executed: true, commandSucceeded: exitStatus == 0)
        guard let target = plan.request.appStore else { return result }
        do {
            let version = try installedStoreIdentity(target, version: target.version, manager: manager, uid: uid)
            result.observedVersion = version
            let path = target.path + "/Contents/_MASReceipt/receipt"
            guard storePathInfo(path) != nil else { throw PrivilegeError.appStoreIdentityChanged }
            let receipt = try readRegular(path, limit: 16 << 20)
            if exitStatus == 0, !receipt.isEmpty {
                result.status = "installed"; result.reason = "app_store_identity_and_version_verified"
            }
        } catch { result.reason = "app_store_post_install_readback_unavailable" }
        if exitStatus != 0 { result.status = "failed"; result.reason = "app_store_update_failed_account_may_require_attention" }
        return result
    }
}

func storeOSVersion(_ text: String) -> String? {
    guard matches(text, "^[0-9]+(\\.[0-9]+)*( \\([A-Za-z0-9]+\\))?$") else { return nil }
    return String(text.split(separator: " ")[0])
}

private func storeDataPath(_ path: String) -> String {
    if path == "/Applications" || path.hasPrefix("/Applications/") { return "/System/Volumes/Data" + path }
    return path
}

// lstat every component: realpath alone cannot distinguish an accepted firmlink
// from a symbolic link into another installation.
func storePathInfo(_ path: String) -> stat? {
    guard cleanAbsolutePath(path) else { return nil }
    var info = stat(), current = ""
    for part in path.split(separator: "/") {
        current += "/" + part
        guard lstat(current, &info) == 0, info.st_mode & S_IFMT != S_IFLNK else { return nil }
    }
    return info
}

func sameStoreDirectory(_ actual: String, _ expected: String) -> Bool {
    guard storeDataPath(actual) == storeDataPath(expected),
          let a = storePathInfo(actual), let b = storePathInfo(expected),
          a.st_mode & S_IFMT == S_IFDIR, b.st_mode & S_IFMT == S_IFDIR else { return false }
    return a.st_dev == b.st_dev && a.st_ino == b.st_ino
}
