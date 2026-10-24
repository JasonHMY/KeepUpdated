import Foundation
import CryptoKit

public enum PrivilegeError: String, Error {
    case invalidRequest = "invalid_request"
    case invalidPlan = "invalid_plan"
    case expiredPlan = "expired_plan"
    case confirmationMismatch = "confirmation_mismatch"
    case sourceChanged = "source_changed"
    case packageSignatureRejected = "package_signature_rejected"
    case packageMetadataUnsupported = "package_metadata_unsupported"
    case installedVersionChanged = "installed_version_changed"
    case candidateUnavailable = "candidate_unavailable"
    case volumeOwnerAuthorizationRequired = "volume_owner_authorization_required"
    case helperUnavailable = "helper_unavailable"
    case appStoreManagerNotBundled = "app_store_manager_not_bundled"
    case appStoreIdentityChanged = "app_store_identity_changed"
    case appStoreRunning = "app_store_running"
    case appStoreCandidateChanged = "app_store_candidate_changed"
    case developerSignatureRequired = "developer_signature_required"
    case authorizationDenied = "authorization_denied"
    case cancelled = "cancelled"
    case alreadyAttempted = "already_attempted"
    case insecureState = "insecure_state"
    case verificationUnknown = "verification_unknown"
    case operationInProgress = "operation_in_progress"
    case noPendingRestart = "no_pending_restart"
}

public struct PackageTarget: Codable, Equatable {
    public var infoSHA256: String
    public var codeResourcesSHA256: String
    public var installationPath: String
    public var bundleID: String
    public var path: String
    public var sha256: String
    public var teamID: String
    public var receiptID: String
    public var fromVersion: String
    public var version: String
    // A local, explicitly selected signed artifact; no inferred web provenance.
    public var source: String
}

public struct SystemTarget: Codable, Equatable {
    public var label: String
    public var version: String
    public var build: String
    public var fromVersion: String
    public var fromBuild: String
}

public struct AppStoreTarget: Codable, Equatable {
    public var adamID: String
    public var bundleID: String
    public var path: String
    public var receiptSHA256: String
    public var infoSHA256: String
    public var codeResourcesSHA256: String
    public var fromVersion: String
    public var version: String
    public var region: String
    public var managerSHA256: String
    public var managerVersion: String
}

public struct Request: Codable, Equatable {
    public var operationID: String?
    public var kind: String
    public var package: PackageTarget?
    public var system: SystemTarget?
    public var appStore: AppStoreTarget?
    public var restartOf: String?

    public func validate() throws {
        if let operationID, !matches(operationID, "^[0-9a-f]{64}$") { throw PrivilegeError.invalidRequest }
        switch kind {
        case "package":
            guard let p = package, system == nil, appStore == nil, restartOf == nil,
                  p.path.hasPrefix("/"), p.path.hasSuffix(".pkg"),
                  p.installationPath.hasPrefix("/"), p.installationPath.hasSuffix(".app"), !p.installationPath.contains(".app/"),
                  cleanAbsolutePath(p.installationPath),
                  matches(p.bundleID, "^[A-Za-z0-9][A-Za-z0-9.-]{2,200}$"),
                  cleanAbsolutePath(p.path),
                  [p.sha256, p.infoSHA256, p.codeResourcesSHA256].allSatisfy({ matches($0, "^[0-9a-f]{64}$") }), matches(p.teamID, "^[A-Z0-9]{10}$"),
                  matches(p.receiptID, "^[A-Za-z0-9][A-Za-z0-9._-]{2,200}$"),
                  validVersion(p.fromVersion), validVersion(p.version), p.fromVersion != p.version,
                  p.source == "local-signed-package" else { throw PrivilegeError.invalidRequest }
        case "system":
            guard let s = system, package == nil, appStore == nil, restartOf == nil,
                  matches(s.label, "^macOS [A-Za-z0-9 .()_-]{1,160}$"),
                  validVersion(s.version), validVersion(s.fromVersion),
                  matches(s.build, "^[0-9]{2}[A-Z][0-9]{2,6}[a-z]?$"),
                  matches(s.fromBuild, "^[0-9]{2}[A-Z][0-9]{2,6}[a-z]?$"),
                  s.label.hasSuffix("-" + s.build), s.build != s.fromBuild else { throw PrivilegeError.invalidRequest }
        case "app_store_update":
            guard let a = appStore, package == nil, system == nil, restartOf == nil,
                  matches(a.adamID, "^[1-9][0-9]{0,17}$"), matches(a.bundleID, "^[A-Za-z0-9][A-Za-z0-9.-]{2,200}$"),
                  a.path.hasPrefix("/Applications/"), a.path.hasSuffix(".app"),
                  cleanAbsolutePath(a.path),
                  [a.receiptSHA256, a.infoSHA256, a.codeResourcesSHA256, a.managerSHA256].allSatisfy({ matches($0, "^[0-9a-f]{64}$") }),
                  matches(a.region, "^[A-Z]{2}$"), a.managerVersion == "7.0.0",
                  validVersion(a.fromVersion), validVersion(a.version),
                  a.version.compare(a.fromVersion, options: .numeric) == .orderedDescending else { throw PrivilegeError.invalidRequest }
        case "restart":
            guard package == nil, system == nil, appStore == nil, let id = restartOf,
                  matches(id, "^[0-9a-f]{64}$") else { throw PrivilegeError.invalidRequest }
        default: throw PrivilegeError.invalidRequest
        }
    }
}

public struct Effects: Codable, Equatable {
    public var administrator: Bool
    public var scripts: Bool
    public var servicesMayChange: Bool
    public var restart: Bool
    public var automaticRollback: Bool
    public var cancellation: String

    public static func required(for request: Request) -> Effects {
        Effects(administrator: true, scripts: request.kind == "system" || request.kind == "app_store_update",
                servicesMayChange: request.kind != "restart", restart: request.kind == "restart",
                automaticRollback: false, cancellation: "before_execution_only")
    }
}

public struct SpecialPlan: Codable, Equatable {
    public var planID: String
    public var createdAt: Int64
    public var expiresAt: Int64
    public var request: Request
    public var effects: Effects
    public var operationID: String { request.operationID ?? planID }

    public static func make(_ request: Request, now: Date = Date()) throws -> SpecialPlan {
        try request.validate()
        let start = Int64(now.timeIntervalSince1970)
        var plan = SpecialPlan(planID: "", createdAt: start, expiresAt: start + 600,
                               request: request, effects: .required(for: request))
        plan.planID = try plan.digest()
        return plan
    }

    public func digest() throws -> String {
        var copy = self
        copy.planID = ""
        return hash(try PrivilegedUpdates.encode(copy))
    }

    public func validate(confirm: String, now: Date = Date()) throws {
        try request.validate()
        guard effects == .required(for: request), planID == (try digest()) else { throw PrivilegeError.invalidPlan }
        guard confirm == planID else { throw PrivilegeError.confirmationMismatch }
        let current = Int64(now.timeIntervalSince1970)
        guard expiresAt > createdAt, expiresAt - createdAt <= 600,
              current >= createdAt, current < expiresAt else { throw PrivilegeError.expiredPlan }
    }
}

public struct OperationResult: Codable, Equatable {
    public var planID: String
    public var status: String
    public var reason: String
    public var executed: Bool
    public var commandSucceeded: Bool
    public var observedVersion: String?
    public var observedBuild: String?
    public var backupPath: String?
    public var recoveryPath: String?

    public init(planID: String, status: String, reason: String, executed: Bool = false,
                commandSucceeded: Bool = false, observedVersion: String? = nil, observedBuild: String? = nil, backupPath: String? = nil, recoveryPath: String? = nil) {
        self.planID = planID; self.status = status; self.reason = reason
        self.executed = executed; self.commandSucceeded = commandSucceeded
        self.observedVersion = observedVersion; self.observedBuild = observedBuild; self.backupPath = backupPath; self.recoveryPath = recoveryPath
    }
}

public func encode<T: Encodable>(_ value: T) throws -> Data {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
    return try encoder.encode(value)
}

public func decode<T: Codable>(_ type: T.Type, _ data: Data) throws -> T {
    guard data.count <= 64 * 1024 else { throw PrivilegeError.invalidRequest }
    let value = try JSONDecoder().decode(type, from: data)
    let original = try JSONSerialization.jsonObject(with: data) as? NSDictionary
    let roundtrip = try JSONSerialization.jsonObject(with: encode(value)) as? NSDictionary
    guard let original, let roundtrip, original == roundtrip else { throw PrivilegeError.invalidRequest }
    return value
}

func hash(_ data: Data) -> String { SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined() }
func matches(_ value: String, _ expression: String) -> Bool {
    value.range(of: expression, options: .regularExpression) != nil && !value.contains("\n") && !value.contains("\r")
}
func validVersion(_ value: String) -> Bool { matches(value, "^[0-9][A-Za-z0-9._+-]{0,63}$") }

func cleanAbsolutePath(_ path: String) -> Bool {
    path.hasPrefix("/") && !path.contains("\0") && !path.contains("\n") && !path.contains("\r") &&
        path.split(separator: "/", omittingEmptySubsequences: false).dropFirst().allSatisfy { !$0.isEmpty && $0 != "." && $0 != ".." }
}
