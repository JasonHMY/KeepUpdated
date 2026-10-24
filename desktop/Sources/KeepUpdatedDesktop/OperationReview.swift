import Foundation

// These fields describe the CLI contract. The original plan bytes remain the apply payload.
enum OperationConsent: String, CaseIterable {
    case runtimeUpdate = "runtime_update"
    case privilegedInstall = "privileged_install"
    case restartSystem = "restart_system"
    var title: String {
        switch self {
        case .runtimeUpdate: return L10n.text("Review shared runtime update and dependent tools")
        case .privilegedInstall: return L10n.text("Allow this installation to request administrator authorization")
        case .restartSystem: return L10n.text("Review a separate system restart operation")
        }
    }
}

struct ApplicationTarget: Codable, Identifiable {
    var id: String
    var path: String
    var identity: String
    var version: String?
    var reopen: Bool?
}

struct ApplicationLifecycleResult: Codable, Identifiable {
    var id: String
    var path: String
    var quitStatus: String
    var quitReason: String
    var reopenStatus: String
    var reopenReason: String
    static func label(_ status: String) -> String {
        switch status {
        case "not_requested": return L10n.text("Not requested")
        case "requested": return L10n.text("Requested; not yet verified")
        case "exited": return L10n.text("Exit verified")
        case "reopened": return L10n.text("Reopen verified")
        case "refused": return L10n.text("Exit declined or cancelled")
        case "skipped": return L10n.text("Skipped")
        case "failed": return L10n.text("Failed")
        default: return L10n.text("Result unverified")
        }
    }
}

struct MCPConfigurationPreview: Codable {
    var operation: String?
    var configPath: String?
    var serverKey: String?
    var packageReference: PackageReference?
    struct PackageReference: Codable {
        var launcher: String
        var before: String
        var after: String
    }
}

struct MCPLauncherDiscovery: Codable {
    var status: String
    var reason: String
    var configuredReference: String?
    var configuredVersion: String?
    var observedAt: String?
    var candidate: Candidate?
    var candidateId: String?
    struct Candidate: Codable {
        var ecosystem: String
        var package: String
        var version: String
        var reference: String
        var sourceIdentity: String
        var constraintResult: String
    }
    var selectable: Bool {
        status == "available" && candidate?.ecosystem == "npm" && candidate?.constraintResult == "exact_same_package" &&
        candidate?.sourceIdentity.hasPrefix("npm-default:") == true && candidateId?.count == 64
    }
}

struct ApplicationUpdateBinding: Codable {
    var resolvedTarget: ResolvedTarget?
    struct ResolvedTarget: Codable {
        var applicationUpdate: ApplicationUpdatePreview?
    }
}
struct ApplicationUpdatePreview: Codable {
    var bundleId: String
    var teamId: String
    var architecture: String
    var macos: String
    var archive: Archive
    struct Archive: Codable {
        var format: String?
        var version: String
        var build: String
        var length: Int64
    }
}

struct ReviewChoices: Equatable {
    var privilegedRequestData: Data?
    var consents: Set<String> = []
    var quitApps: Set<String> = []
    var reopenApps: Set<String> = []
    var mcpReference: String = "" {
        didSet { if oldValue != mcpReference { mcpReferenceApproved = false; mcpCandidate = "" } }
    }
    var mcpCandidate = ""
    var mcpReferenceApproved = false
    var special = false
    var specialForPreview: Bool { mcpReference.isEmpty ? special : mcpReferenceApproved }
    mutating func selectMCPCandidate(_ discovery: MCPLauncherDiscovery) {
        guard discovery.selectable, let candidate = discovery.candidate, let id = discovery.candidateId else { return }
        mcpReference = candidate.reference
        mcpCandidate = id
        mcpReferenceApproved = false
    }
    var arguments: [String] {
        consents.sorted().flatMap { ["--consent", $0] } +
        quitApps.sorted().flatMap { ["--quit-app", $0] } +
        reopenApps.sorted().flatMap { ["--reopen-app", $0] } +
        (mcpReference.isEmpty ? [] : ["--mcp-reference", mcpReference]) +
        (mcpCandidate.isEmpty ? [] : ["--mcp-candidate", mcpCandidate])
    }
}

extension PlanItem {
    var consentCodes: [String] { Array(Set(requiredConsents ?? [])).sorted() }
    var exitTargets: [ApplicationTarget] {
        if let applications, !applications.isEmpty { return applications }
        if let applicationLifecycle, !applicationLifecycle.isEmpty { return applicationLifecycle }
        if kind == "application", requiresQuit == true, let path = affectedPaths?.first {
            return [.init(id: id, path: path, identity: "", version: fromVersion)]
        }
        return []
    }
    var needsTypedReview: Bool { !consentCodes.isEmpty || requiresAdmin == true || requiresRestart == true || requiresQuit == true || kind == "mcp-server" }
    func isExecutable(in selection: PlanSelection?) -> Bool {
        guard status == "ready", Set(selection?.reopenApps ?? []).isSubset(of: Set(selection?.quitApps ?? [])) else { return false }
        let selected = Set(selection?.consents ?? [])
        guard consentCodes.allSatisfy({ OperationConsent(rawValue: $0) != nil && selected.contains($0) }) else { return false }
        if requiresAdmin == true && !(consentCodes.contains("privileged_install") && selected.contains("privileged_install")) { return false }
        if requiresRestart == true && !(consentCodes.contains("restart_system") && selected.contains("restart_system")) { return false }
        if requiresQuit == true {
            let targets = exitTargets
            guard !targets.isEmpty, targets.allSatisfy({ (selection?.quitApps ?? []).contains($0.id) }) else { return false }
        }
        if requiresScripts == true && specialApproved != true { return false }
        if reason == "mcp_configuration_may_reload_client_confirm_separately" && specialApproved != true { return false }
        if let reference = mcp?.packageReference {
            guard mcp?.operation == "replace-package-reference", selection?.mcpReference == reference.after,
                  selection?.special == true, specialApproved == true else { return false }
        }
        if !(selection?.mcpReference ?? "").isEmpty && mcp?.packageReference == nil { return false }
        if let candidateId = selection?.mcpCandidate, !candidateId.isEmpty {
            guard mcpDiscovery?.selectable == true, mcpDiscovery?.candidateId == candidateId,
                  mcpDiscovery?.candidate?.reference == mcp?.packageReference?.after else { return false }
        }
        return true
    }
}
