import XCTest
@testable import PrivilegedUpdates

private final class StoreFixture: SystemCommands {
    var path = ""
    var version = "1.0"
    var adamID = "123456"
    var bundleID = "com.example.fixture"
    var managerPath: String?
    var findPaths: String?
    var missingMDLS = false
    var os = "27.0.1 (26A434)"
    var minimum = "27.0.1"
    var kind = "software"
    var devices = ["MacDesktop-MacDesktop"]
    var signatureValid = true
    var namesOnly = false
    var duplicateJSON = false
    var catalogVersion = "2.0"
    var region = "US"
    var processes = ""
    var calls: [(String, [String])] = []
    func run(_ executable: String, _ args: [String]) throws -> CommandOutput {
        calls.append((executable, args))
        let text: String
        switch executable {
        case "/usr/bin/mdls":
            guard args.count == 4, args[0] == "-raw", args[1] == "-name", args[3] == path else { throw PrivilegeError.invalidRequest }
            switch args[2] {
            case "kMDItemCFBundleIdentifier": text = bundleID
            case "kMDItemVersion": text = version
            case "kMDItemAppStoreAdamID": text = adamID
            default: throw PrivilegeError.invalidRequest
            }
            return CommandOutput(status: 0, data: Data((missingMDLS ? "(null)" : text).utf8))
        case "/usr/bin/mdfind":
            guard args.count == 2, args[0] == "-0", ["kMDItemAppStoreAdamID == 123456", "kMDItemCFBundleIdentifier == \"com.example.fixture\""].contains(args[1]) else { throw PrivilegeError.invalidRequest }
            text = findPaths ?? path + "\0"
        case "/bin/ps":
            guard args == ["-axo", "comm="] else { throw PrivilegeError.invalidRequest }
            text = processes
        default: XCTFail("Unexpected system command \(executable) \(args)"); throw PrivilegeError.invalidRequest
        }
        return CommandOutput(status: 0, data: Data(text.utf8))
    }
    func store(_ executable: String, _ args: [String], uid: uid_t) throws -> CommandOutput {
        calls.append((executable, args))
        XCTAssertEqual(executable, "/fixture/sealed/mas")
        XCTAssertEqual(uid, getuid())
        let object: [String: Any]
        switch args {
        case ["list", "123456"]:
            object = namesOnly ? ["name": "Fixture"] : ["adamID": Int(adamID)!, "bundleID": bundleID, "path": managerPath ?? path, "version": version]
        case ["config", "--json"]: object = ["mas": "7.0.0", "store": region, "macos": os]
        case ["lookup", "--json", "123456"]:
            object = ["adamID": 123456, "bundleID": bundleID, "kind": kind, "version": catalogVersion, "minimumOSVersion": minimum, "supportedDevices": devices]
        default: XCTFail("Unexpected manager command \(args)"); throw PrivilegeError.invalidRequest
        }
        var data = try JSONSerialization.data(withJSONObject: object)
        if duplicateJSON && args.first == "list" { data.append(data) }
        return CommandOutput(status: 0, data: data)
    }
    func verifyCode(_ path: String, requirement: String) throws {
        XCTAssertEqual(path, self.path)
        XCTAssertTrue(requirement.contains("com.example.fixture"))
        XCTAssertTrue(requirement.contains("1.2.840.113635.100.6.1.9"))
        if !signatureValid { throw PrivilegeError.appStoreIdentityChanged }
    }
}

final class AppStoreTests: XCTestCase {
    private var roots: [String] = []
    override func tearDown() { for root in roots { try? FileManager.default.removeItem(atPath: root) } }
    private func fixture() throws -> (StoreFixture, AppStoreTarget) {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-store-test-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        let root = canonicalPath(url.path)!
        roots.append(root)
        let commands = StoreFixture(); commands.path = root + "/Fixture.app"
        for part in ["Contents/_MASReceipt", "Contents/_CodeSignature"] {
            try FileManager.default.createDirectory(atPath: commands.path + "/" + part, withIntermediateDirectories: true)
        }
        try writeVersion("1.0", path: commands.path)
        try Data("synthetic receipt".utf8).write(to: URL(fileURLWithPath: commands.path + "/Contents/_MASReceipt/receipt"))
        try Data("synthetic code resources".utf8).write(to: URL(fileURLWithPath: commands.path + "/Contents/_CodeSignature/CodeResources"))
        let target = AppStoreTarget(adamID: commands.adamID, bundleID: commands.bundleID, path: commands.path,
            receiptSHA256: PrivilegedUpdates.hash(try readRegular(commands.path + "/Contents/_MASReceipt/receipt")),
            infoSHA256: PrivilegedUpdates.hash(try readRegular(commands.path + "/Contents/Info.plist")),
            codeResourcesSHA256: PrivilegedUpdates.hash(try readRegular(commands.path + "/Contents/_CodeSignature/CodeResources")),
            fromVersion: "1.0", version: "2.0", region: "US", managerSHA256: String(repeating: "a", count: 64), managerVersion: "7.0.0")
        return (commands, target)
    }
    private func writeVersion(_ version: String, path: String) throws {
        let info = ["CFBundleIdentifier": "com.example.fixture", "CFBundleShortVersionString": version]
        try PropertyListSerialization.data(fromPropertyList: info, format: .xml, options: 0).write(to: URL(fileURLWithPath: path + "/Contents/Info.plist"))
    }
    private func plan(_ target: AppStoreTarget) -> SpecialPlan {
        let request = Request(kind: "app_store_update", appStore: target)
        // Only the provider is under test; the public request still requires /Applications.
        return SpecialPlan(planID: "fixture", createdAt: 1, expiresAt: 2, request: request, effects: .required(for: request))
    }
    func testNativeMinimalJSONAndSpotlightShapePassPreflight() throws {
        let (commands, target) = try fixture()
        try Providers(commands: commands).appStore(target, manager: "/fixture/sealed/mas", uid: getuid())
        XCTAssertTrue(commands.calls.contains { $0.1 == ["list", "123456"] })
        XCTAssertFalse(commands.calls.contains { $0.1.first == "list" && $0.1.contains("--json") })
        XCTAssertEqual(commands.calls.filter { $0.0 == "/usr/bin/mdfind" }.count, 2)
        XCTAssertEqual(commands.calls.filter { $0.0 == "/usr/bin/mdls" }.count, 3)
        XCTAssertFalse(commands.calls.contains { $0.1.first == "update" })
        commands.kind = "mac-software"; commands.devices = []
        XCTAssertNoThrow(try Providers(commands: commands).appStore(target, manager: "/fixture/sealed/mas", uid: getuid()))
    }
    func testAmbiguousMissingOrChangedIdentityIsBlocked() throws {
        for scenario in ["names-only", "multiple-json", "duplicate-path", "missing-nul", "different-directory", "missing-mdls", "wrong-id", "wrong-bundle", "wrong-version", "signature", "receipt"] {
            let (commands, target) = try fixture()
            switch scenario {
            case "names-only": commands.namesOnly = true
            case "multiple-json": commands.duplicateJSON = true
            case "duplicate-path": commands.findPaths = commands.path + "\0" + commands.path + "\0"
            case "missing-nul": commands.findPaths = commands.path
            case "different-directory": commands.managerPath = roots.last!
            case "missing-mdls": commands.missingMDLS = true
            case "wrong-id": commands.adamID = "654321"
            case "wrong-bundle": commands.bundleID = "com.example.other"
            case "wrong-version": commands.version = "9.0"
            case "signature": commands.signatureValid = false
            case "receipt": try Data("changed".utf8).write(to: URL(fileURLWithPath: target.path + "/Contents/_MASReceipt/receipt"))
            default: XCTFail(scenario)
            }
            XCTAssertThrowsError(try Providers(commands: commands).appStore(target, manager: "/fixture/sealed/mas", uid: getuid()), scenario)
        }
    }
    func testIOSCatalogMalformedOSAndNewerMinimumStayBlocked() throws {
        for scenario in ["ios", "wrong-marker", "os-suffix", "os-missing", "too-new", "minimum-suffix", "candidate", "region", "running"] {
            let (commands, target) = try fixture()
            switch scenario {
            case "ios": commands.devices = ["iPhone17,1"]
            case "wrong-marker": commands.devices = ["MacDesktop"]
            case "os-suffix": commands.os = "27.0.1 (26A434) extra"
            case "os-missing": commands.os = "(26A434)"
            case "too-new": commands.minimum = "27.0.2"
            case "minimum-suffix": commands.minimum = "27.0.1 beta"
            case "candidate": commands.catalogVersion = "2.1"
            case "region": commands.region = "AU"
            case "running": commands.processes = target.path + "/Contents/MacOS/fixture\n"
            default: XCTFail(scenario)
            }
            XCTAssertThrowsError(try Providers(commands: commands).appStore(target, manager: "/fixture/sealed/mas", uid: getuid()), scenario)
        }
        XCTAssertEqual(storeOSVersion("27.0.1 (26A434)"), "27.0.1")
        XCTAssertEqual(storeOSVersion("13.0"), "13.0")
        XCTAssertNil(storeOSVersion("27.0.1\n"))
    }
    func testReadbackUsesTheSameIdentityAndUniquenessChecks() throws {
        let (commands, target) = try fixture()
        commands.version = "2.0"; try writeVersion("2.0", path: target.path)
        let provider = Providers(commands: commands)
        XCTAssertEqual(provider.verifyStore(plan(target), manager: "/fixture/sealed/mas", uid: getuid(), exitStatus: 0).status, "installed")
        commands.findPaths = commands.path + "\0" + commands.path + "\0"
        XCTAssertEqual(provider.verifyStore(plan(target), manager: "/fixture/sealed/mas", uid: getuid(), exitStatus: 0).status, "verification_unknown")
        commands.findPaths = nil
        try writeVersion("1.0", path: target.path)
        XCTAssertEqual(provider.verifyStore(plan(target), manager: "/fixture/sealed/mas", uid: getuid(), exitStatus: 0).status, "verification_unknown")
        XCTAssertEqual(provider.verifyStore(plan(target), manager: "/fixture/sealed/mas", uid: getuid(), exitStatus: 1).status, "failed")
    }
    func testPhysicalIdentityRejectsSymlinksAndDifferentDirectories() throws {
        let (_, target) = try fixture()
        XCTAssertTrue(sameStoreDirectory(target.path, target.path))
        XCTAssertFalse(sameStoreDirectory(roots.last!, target.path))
        let link = roots.last! + "/Alias.app"
        try FileManager.default.createSymbolicLink(atPath: link, withDestinationPath: target.path)
        XCTAssertFalse(sameStoreDirectory(link, target.path))
        XCTAssertFalse(sameStoreDirectory(link, link))
        let parent = roots.last! + "/parent"
        try FileManager.default.createSymbolicLink(atPath: parent, withDestinationPath: roots.last!)
        XCTAssertNil(storePathInfo(parent + "/Fixture.app"))
        XCTAssertFalse(sameStoreDirectory(target.path + "/../Fixture.app", target.path))
    }
    func testApplicationsFirmlinkIsOnePhysicalDirectory() {
        // Read-only stat of the two OS directory aliases; no app inventory or Store calls.
        XCTAssertTrue(sameStoreDirectory("/Applications", "/System/Volumes/Data/Applications"))
        XCTAssertFalse(sameStoreDirectory("/Applications", "/System/Volumes/Data/Applications-other"))
    }
    func testStorePackageEffectsIncludeScripts() throws {
        let (_, target) = try fixture()
        XCTAssertTrue(plan(target).effects.scripts)
        XCTAssertTrue(plan(target).effects.servicesMayChange)
        XCTAssertFalse(plan(target).effects.restart)
    }
}
