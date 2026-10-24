import Foundation
import Security
import ServiceManagement

public let helperServiceName = "com.keepupdated.privileged"
public let helperPlistName = helperServiceName + ".plist"
public let authorizationIdentifier = "com.keepupdated.authorization"

public func signingTeam() throws -> String {
    var code: SecCode?
    var details: CFDictionary?
    var staticCode: SecStaticCode?
    guard SecCodeCopySelf([], &code) == errSecSuccess, let code,
          SecCodeCopyStaticCode(code, [], &staticCode) == errSecSuccess, let staticCode,
          SecCodeCopySigningInformation(staticCode, SecCSFlags(rawValue: kSecCSSigningInformation), &details) == errSecSuccess,
          let info = details as? [String: Any], let team = info[kSecCodeInfoTeamIdentifier as String] as? String,
          matches(team, "^[A-Z0-9]{10}$"), let flags = info[kSecCodeInfoFlags as String] as? UInt32,
          flags & 0x10000 != 0, flags & 0x2 == 0 else { throw PrivilegeError.developerSignatureRequired }
    var requirement: SecRequirement?
    guard SecRequirementCreateWithString(signingRequirement(team: team, identifier: nil) as CFString, [], &requirement) == errSecSuccess,
          SecCodeCheckValidity(code, SecCSFlags(rawValue: kSecCSStrictValidate), requirement) == errSecSuccess else {
        throw PrivilegeError.developerSignatureRequired
    }
    return team
}

public func signingRequirement(team: String, identifier: String?) -> String {
    let id = identifier.map { " and identifier \"\($0)\"" } ?? ""
    return "anchor apple generic and certificate leaf[subject.OU] = \"\(team)\" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists" + id +
        " and ! entitlement[\"com.apple.security.get-task-allow\"] exists" +
        " and ! entitlement[\"com.apple.security.cs.disable-library-validation\"] exists" +
        " and ! entitlement[\"com.apple.security.cs.allow-dyld-environment-variables\"] exists"
}

func rightName(_ planID: String) -> String { "com.keepupdated.update." + planID }

// Only the root helper creates a short-lived, digest-specific administrator right.
func createRight(_ id: String) throws {
    var authorization: AuthorizationRef?
    guard AuthorizationCreate(nil, nil, [], &authorization) == errAuthorizationSuccess, let authorization else {
        throw PrivilegeError.authorizationDenied
    }
    defer { AuthorizationFree(authorization, []) }
    let definition: [String: Any] = ["class": "user", "group": "admin", "shared": false,
                                   "timeout": 0, "allow-root": false, "authenticate-user": true,
                                   "version": 1]
    guard AuthorizationRightSet(authorization, rightName(id), definition as CFDictionary,
                                 "Approve this exact KeepUpdated operation" as CFString, nil, nil) == errAuthorizationSuccess else {
        throw PrivilegeError.authorizationDenied
    }
}
func removeRight(_ id: String) {
    var authorization: AuthorizationRef?
    if AuthorizationCreate(nil, nil, [], &authorization) == errAuthorizationSuccess, let authorization {
        _ = AuthorizationRightRemove(authorization, rightName(id))
        AuthorizationFree(authorization, [])
    }
}

public final class OperationAuthorization {
    private var reference: AuthorizationRef?
    public init(planID: String) throws {
        guard matches(planID, "^[0-9a-f]{64}$") else { throw PrivilegeError.invalidPlan }
        guard AuthorizationCreate(nil, nil, [], &reference) == errAuthorizationSuccess, let reference else {
            throw PrivilegeError.authorizationDenied
        }
        let status = rightName(planID).withCString { name in
            var item = AuthorizationItem(name: name, valueLength: 0, value: nil, flags: 0)
            return withUnsafeMutablePointer(to: &item) { pointer in
                var rights = AuthorizationRights(count: 1, items: pointer)
                return AuthorizationCopyRights(reference, &rights, nil, [.interactionAllowed, .extendRights], nil)
            }
        }
        guard status == errAuthorizationSuccess else {
            AuthorizationFree(reference, [.destroyRights]); self.reference = nil
            throw status == errAuthorizationCanceled ? PrivilegeError.cancelled : PrivilegeError.authorizationDenied
        }
    }
    deinit { if let reference { AuthorizationFree(reference, [.destroyRights]) } }
    public func externalForm() throws -> Data {
        guard let reference else { throw PrivilegeError.authorizationDenied }
        var external = AuthorizationExternalForm()
        guard AuthorizationMakeExternalForm(reference, &external) == errAuthorizationSuccess else { throw PrivilegeError.authorizationDenied }
        return withUnsafeBytes(of: external) { Data($0) }
    }
}

func validateAuthorization(_ data: Data, planID: String) throws {
    guard data.count == MemoryLayout<AuthorizationExternalForm>.size else { throw PrivilegeError.authorizationDenied }
    var external = AuthorizationExternalForm()
    _ = withUnsafeMutableBytes(of: &external) { data.copyBytes(to: $0) }
    var reference: AuthorizationRef?
    guard AuthorizationCreateFromExternalForm(&external, &reference) == errAuthorizationSuccess, let reference else {
        throw PrivilegeError.authorizationDenied
    }
    defer { AuthorizationFree(reference, [.destroyRights]) }
    let status = rightName(planID).withCString { name in
        var item = AuthorizationItem(name: name, valueLength: 0, value: nil, flags: 0)
        return withUnsafeMutablePointer(to: &item) { pointer in
            var rights = AuthorizationRights(count: 1, items: pointer)
            // Never extend rights or prompt inside the privileged process.
            return AuthorizationCopyRights(reference, &rights, nil, [], nil)
        }
    }
    guard status == errAuthorizationSuccess else { throw PrivilegeError.authorizationDenied }
}

public func serviceStatus() -> String {
    guard (try? signingTeam()) != nil else { return "developer_signature_required" }
    switch SMAppService.daemon(plistName: helperPlistName).status {
    case .enabled: return "enabled"
    case .requiresApproval: return "system_settings_approval_required"
    case .notRegistered: return "helper_not_registered"
    case .notFound: return "helper_not_packaged"
    @unknown default: return "helper_status_unknown"
    }
}

public func registerHelper() throws {
    _ = try signingTeam()
    // The caller presents a separate explicit service-registration confirmation.
    try SMAppService.daemon(plistName: helperPlistName).register()
}
