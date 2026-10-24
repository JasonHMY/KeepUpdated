import Foundation

// Presentation-only DTOs. Requests sent back to the CLI retain every original JSON field.
struct PrivilegedRequestPreview: Codable {
    var kind: String
    var package: Package?
    var system: System?
    var appStore: AppStore?
    var restartOf: String?
    var operationID: String?
    struct Package: Codable {
        var path: String
        var installationPath: String
        var bundleID: String
        var version: String
        var fromVersion: String
        var teamID: String
        var receiptID: String
        var sha256: String
    }
    struct System: Codable {
        var label: String
        var version: String
        var build: String
        var fromVersion: String
        var fromBuild: String
    }
    struct AppStore: Codable {
        var adamID: String
        var bundleID: String
        var path: String
        var version: String
        var fromVersion: String
        var region: String
        var managerVersion: String
    }
    var supportedPreparation: Bool {
        guard appStore == nil, restartOf == nil, (operationID ?? "").isEmpty else { return false }
        return (kind == "package" && package != nil && system == nil) || (kind == "system" && system != nil && package == nil)
    }
}

struct PrivilegedPreparation: Identifiable {
    let id = UUID()
    let targetID: String
    let targetName: String
    let status: String
    let reason: String
    let executionBlocker: String
    let candidates: [Candidate]
    struct Candidate: Identifiable {
        let id: Int
        let request: PrivilegedRequestPreview
        let data: Data
        var title: String {
            if let package = request.package { return package.bundleID + " " + package.version }
            if let system = request.system { return system.label }
            return request.kind
        }
    }
    static func decode(_ data: Data, targetID: String, targetName: String) throws -> PrivilegedPreparation {
        struct Envelope: Decodable {
            var targetID: String
            var status: String
            var reason: String
            var executionBlocker: String
            var candidates: [PrivilegedRequestPreview]
        }
        let envelope = try JSONDecoder().decode(Envelope.self, from: data)
        guard envelope.targetID == targetID,
              let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let requests = object["candidates"] as? [[String: Any]], requests.count == envelope.candidates.count else {
            throw ClientError.message("Candidate preparation did not match the selected installation. Scan again.")
        }
        let candidates = try envelope.candidates.enumerated().map { index, request in
            Candidate(id: index, request: request, data: try JSONSerialization.data(withJSONObject: requests[index], options: [.sortedKeys]))
        }
        return .init(targetID: targetID, targetName: targetName, status: envelope.status, reason: envelope.reason,
                     executionBlocker: envelope.executionBlocker, candidates: candidates)
    }
    var canPreview: Bool { status == "prepared" && candidates.contains { $0.request.supportedPreparation } }
}

struct HelperStatus: Decodable {
    var status: String
    var reason: String?
    var planID: String?
    var backupPath: String?
    var recoveryPath: String?
    var observedVersion: String?
    var observedBuild: String?
    var canRequestRegistration: Bool { status == "helper_not_registered" || (status == "blocked" && reason == "helper_not_registered") }
    var reasonCode: String { (reason?.isEmpty == false ? reason : nil) ?? status }
    var title: String {
        switch status {
        case "enabled": return L10n.text("Helper enabled")
        case "helper_not_registered": return L10n.text("Helper not enabled")
        case "system_settings_approval_required": return L10n.text("System Settings approval required")
        case "demo": return L10n.text("Demo helper status")
        case "installed": return L10n.text("Installation recorded")
        case "awaiting_restart": return L10n.text("Awaiting system restart")
        case "cancelled": return L10n.text("Cancelled")
        case "blocked", "developer_signature_required", "helper_not_packaged", "native_bridge_not_packaged": return L10n.text("Blocked")
        default: return L10n.text("Status unverified")
        }
    }
}

extension InventoryItem {
    var canPreparePackage: Bool { kind == "application" && !["system", "homebrew", "app-store", "app-component"].contains(owner) }
    var canPrepareSystem: Bool { kind == "os" && owner == "system" && source == "apple" }
}
