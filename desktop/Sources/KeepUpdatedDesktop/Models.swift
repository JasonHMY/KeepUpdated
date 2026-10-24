import Foundation

struct CheckResult: Codable, Hashable {
    var status: String
    var code: String
    var detail: String?
}

struct InventoryItem: Codable, Identifiable, Hashable {
    var id: String
    var kind: String
    var name: String
    var path: String
    var owner: String
    var source: String
    var channel: String
    var installedVersion: String?
    var installedVersions: [String]?
	var installedBuild: String?
    var activeVersion: String?
    var latestVersion: String?
	var latestBuild: String?
    var evidence: [String]
    var health: CheckResult
    var update: CheckResult
    var identity: String?
    var realPath: String?
    var active: Bool?
    var activityKnown: Bool?
    var architecture: [String]?
    var dependencies: [Dependency]?
    var related: [String]?
    var plugin: PluginOwner?
    var appStore: AppStoreProduct?
    var skillManager: SkillManagerSource?
    struct SkillManagerSource: Codable, Hashable {
        var manager: String
        var repository: String?
        var subdirectory: String?
        var commit: String?
        var root: String
        var metadataPath: String
        var format: String
        var code: String
    }
    var pluginCache: PluginCache?
    struct PluginCache: Codable, Hashable {
        var client: String
        var root: String
        var id: String
        var version: String
        var registration: String
    }
    var isUnregisteredSkillCache: Bool {
        kind == "skill" && pluginCache?.registration == "unregistered" &&
        path.hasPrefix((pluginCache?.root ?? "") + "/")
    }
    var isUnverifiedSkillCache: Bool {
        kind == "skill" && source == "plugin-cache" && plugin == nil && !isUnregisteredSkillCache
    }

    struct AppStoreProduct: Codable, Hashable {
        var id: Int64
        var bundleId: String
        var country: String
    }
    var isAppStoreApplication: Bool { kind == "application" && source == "app-store" && owner == "app-store" }
    var isTopLevelApplication: Bool {
        kind == "application" && path.hasPrefix("/") && path.hasSuffix(".app") &&
        !URL(fileURLWithPath: path).deletingLastPathComponent().pathComponents.contains(where: { $0.lowercased().hasSuffix(".app") })
    }
    var needsManualUpdate: Bool {
        isAppStoreApplication || (isTopLevelApplication && owner == "standalone" && source != "sparkle")
    }
    var canOpenForManualUpdate: Bool {
        isTopLevelApplication && canUpdate && !isAppStoreApplication &&
        !["system", "app-component"].contains(owner) && !(identity ?? "").isEmpty
    }
    var appStoreActionTitle: String {
        canUpdate ? L10n.text("Open App Store to Update") : L10n.text("Open in App Store")
    }
    var appStoreURL: URL? {
        guard isAppStoreApplication else { return nil }
        if let product = appStore, product.id > 0, !product.bundleId.isEmpty,
           identity == product.bundleId,
           product.country.range(of: "^[A-Za-z]{2}$", options: .regularExpression) != nil {
            return URL(string: "macappstore://apps.apple.com/\(product.country.lowercased())/app/id\(product.id)")
        }
        return URL(string: "macappstore://showUpdatesPage")
    }

    struct Dependency: Codable, Hashable {
        var name: String
        var relationship: String?
        var constraint: String?
        var installedVersion: String?
        var status: String
        var code: String
        var display: String {
            let label: String
            switch status {
            case "ok": label = L10n.text("Meets requirement")
            case "error": label = L10n.text("Issue found")
            case "skipped": label = L10n.text("Skipped")
            default: label = L10n.text("Unverified")
            }
            return L10n.text("%@%@ · %@\nRequired: %@ · Installed: %@\n%@", String(describing: name), String(describing: relationship.map { " · " + $0 } ?? ""), String(describing: label), String(describing: constraint ?? L10n.text("Not declared")), String(describing: installedVersion ?? L10n.text("Unknown")), String(describing: code))
        }
    }

    var displayName: String {
        if kind == "runtime-pin", owner == "volta" { return L10n.text("Volta project pin") }
        return id.hasPrefix("demo-") ? L10n.text(name) : name
    }
    var displayVersion: String {
        if let version = activeVersion ?? installedVersion, !version.isEmpty {
            if kind == "skill", version.count == 12, version.allSatisfy({ $0.isHexDigit }) { return L10n.text("Content %@", version) }
            return version
        }
        if ["mcp-server", "mcp-config", "service", "runtime-pin"].contains(kind) { return L10n.text("Configuration item") }
        return L10n.text("Version not recorded")
    }
    var category: ItemCategory {
        switch kind {
        case "skill": return .skills
        case "mcp-server", "mcp-config": return .mcp
        case "application": return .applications
        case "formula", "cask", "tap": return .homebrew
        case "node-package":
            if owner.lowercased().contains("pnpm") { return .pnpm }
            if owner.lowercased().contains("bun") { return .bun }
            return .npm
        case "python-package", "python-environment": return .python
        case "runtime", "runtime-pin": return .runtimes
        case "executable":
            return ["node", "python", "python3", "java", "bun", "go"].contains(name.lowercased()) ? .runtimes : .commandLine
        case "service": return .services
        case "os": return .system
        default: return .other
        }
    }
    var canUpdate: Bool { update.status == "available" }
    var isDirectPythonTool: Bool {
        guard kind == "python-package", ["pipx", "uv"].contains(owner),
              name.range(of: #"^[A-Za-z0-9]+(?:[-_.]+[A-Za-z0-9]+)*$"#, options: .regularExpression) != nil else { return false }
        let environments = Set(evidence.filter { $0.hasPrefix("environment:") }.map { String($0.dropFirst("environment:".count)) })
        func canonical(_ value: String) -> String? {
            guard value.hasPrefix("/"), value != "/", !value.contains("//"), !value.contains("\0"),
                  !value.split(separator: "/").contains(where: { $0 == "." || $0 == ".." }) else { return nil }
            return value.hasSuffix("/") ? String(value.dropLast()) : value
        }
        guard environments.count == 1, let rawEnvironment = environments.first,
              let environment = canonical(rawEnvironment), let packagePath = canonical(path),
              packagePath.hasPrefix(environment + "/") else { return false }
        let packageName = name.lowercased().replacingOccurrences(of: "_", with: "-").replacingOccurrences(of: ".", with: "-")
        guard URL(fileURLWithPath: environment).lastPathComponent == packageName else { return false }
        if let realPath {
            guard let resolved = canonical(realPath), resolved.hasPrefix(environment + "/") else { return false }
        }
        return true
    }
    var hasManagedUpdatePreview: Bool {
        switch kind {
        case "formula", "cask": return owner == "homebrew"
        case "node-package": return ["npm", "pnpm", "bun"].contains(owner)
        case "python-package": return isDirectPythonTool
        case "skill":
            guard owner != "system", skillManager == nil,
                  !["skill_upstream_unknown", "skill_source_conflict", "skill_source_unverified"].contains(update.code) else { return false }
            switch source {
            case "git": return true
            case "plugin-cache": return plugin?.client == "claude" && plugin?.scope == "user"
            case "local":
                let receipt = evidence.contains { $0.hasPrefix("skill-receipt:") && $0 != "skill-receipt:" }
                return receipt && (evidence.contains { $0.hasPrefix("skill-source:github:") && $0 != "skill-source:github:" } || evidence.contains("skill-manager:keepupdated"))
            default: return false
            }
        default: return false
        }
    }
    var updatePreviewTitle: String {
        if canReviewSkillChanges { return L10n.text("Review Changes") }
        if kind == "application", canUpdate, owner != "homebrew" { return L10n.text("Update Options") }
        return canUpdate ? L10n.text("Update") : L10n.text("Check & Preview")
    }
    var canReviewSkillChanges: Bool {
        kind == "skill" && ["skill_upstream_changed_local_modified", "skill_local_modified_or_baseline_unknown", "skill_local_modified"].contains(update.code)
    }
    var statusLabel: String { InventoryStatusText.describe(self).title }
    var statusExplanation: String { InventoryStatusText.describe(self).explanation }
    var statusSymbol: String { InventoryStatusText.describe(self).symbol }
    var healthExplanation: String? { InventoryStatusText.healthExplanation(self) }
    func matches(_ query: String) -> Bool {
        let value = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return true }
        let fields = [displayName, name, source, owner, path, kind]
            + [plugin?.id, plugin?.client, pluginCache?.id, pluginCache?.client].compactMap { $0 }
        if fields.contains(where: { $0.localizedStandardContains(value) }) { return true }
        let status = InventoryStatusText.describe(self)
        return status.title.localizedStandardContains(value) || status.explanation.localizedStandardContains(value)
    }
}

enum ItemCategory: String, CaseIterable, Identifiable {
    case all = "All", applications = "Apps", skills = "Skills", mcp = "MCP"
    case homebrew = "Homebrew", npm = "npm", pnpm = "pnpm", bun = "Bun"
    case python = "Python", commandLine = "Command-line Tools", runtimes = "Runtimes", services = "Services", system = "System", other = "Other"
    var id: String { rawValue }
    var title: String {
        switch self {
        case .python: return L10n.text("Python Tools")
        case .services: return L10n.text("Background Items")
        default: return L10n.text(rawValue)
        }
    }
    var symbol: String {
        switch self {
        case .all: return "square.grid.2x2"
        case .applications: return "app"
        case .skills: return "wand.and.stars"
        case .mcp: return "point.3.connected.trianglepath.dotted"
        case .homebrew: return "shippingbox"
        case .npm, .pnpm, .bun: return "shippingbox"
        case .python: return "curlybraces"
        case .commandLine, .runtimes: return "terminal"
        case .services: return "gearshape.2"
        case .system: return "desktopcomputer"
        case .other: return "tray"
        }
    }
}

struct InventoryReport: Codable {
    var schema: String
    var command: String
    var generatedAt: String
    var items: [InventoryItem]
    var diagnostics: [Diagnostic]
    var coverage: [Coverage]
    var hasInterruptedSkillUpdate: Bool {
        diagnostics.contains { ["skill_update_interrupted", "skill_update_record_unreadable"].contains($0.code) }
    }
    struct Diagnostic: Codable, Identifiable {
        var adapter: String
        var path: String?
        var code: String
        var id: String { adapter + (path ?? "") + code }
    }
    struct Coverage: Codable, Identifiable {
        var adapter: String
        var level: String
        var detail: String
        var id: String { adapter }
    }
}

enum OperationAction: String, Codable {
    case update, uninstall
    var titleKey: String { self == .update ? "Update" : "Uninstall" }
    var title: String { L10n.text(titleKey) }
}

struct OperationPlan: Codable, Identifiable {
    var schema: String
    var planId: String
    var createdAt: String
    var expiresAt: String
    var action: OperationAction
    var items: [PlanItem]
    var selection: PlanSelection?
    var id: String { planId }
    var displayActionKey: String { selection?.privilegedRequest?.kind == "restart" ? "Restart" : action.titleKey }
    var displayActionTitle: String { L10n.text(displayActionKey) }
    var executable: [PlanItem] { items.filter { $0.isExecutable(in: selection) } }
    var displayItems: [PlanItem] {
        items.enumerated().sorted { left, right in
            if left.element.displayPriority != right.element.displayPriority {
                return left.element.displayPriority < right.element.displayPriority
            }
            return left.offset < right.offset
        }.map(\.element)
    }
    var special: [PlanItem] { items.filter { $0.requiresSpecialConfirmation && !$0.isExecutable(in: selection) && $0.status != "skipped" } }
    var needsAttention: [PlanItem] { displayItems.filter { !$0.isExecutable(in: selection) && $0.status != "skipped" } }
    var sharedReferences: [PlanItem] { items.filter { $0.status == "skipped" && $0.canonicalId != nil } }
    var noOperation: [PlanItem] { items.filter { $0.status == "skipped" && $0.canonicalId == nil } }
    func canonicalAction(for item: PlanItem) -> PlanItem? {
        guard item.status == "skipped", item.reason == "duplicate_installation_target",
              let id = item.canonicalId, id != item.id else { return nil }
        let matches = items.filter { $0.id == id }
        guard matches.count == 1, let action = matches.first,
              action.canonicalId == nil, action.isExecutable(in: selection) else { return nil }
        return action
    }
    var expires: Date? { ReportJSON.date(expiresAt) }
}

struct PlanSelection: Codable {
    var special: Bool?
    var consents: [String]?
    var quitApps: [String]?
    var reopenApps: [String]?
    var mcpReference: String?
    var mcpCandidate: String?
    var privilegedRequest: PrivilegedRequestPreview?
    var ids: [String]?
    var all: Bool?
    var cleanup: Bool?
    var hasSpecificReview: Bool {
        privilegedRequest != nil || special == true || !(consents ?? []).isEmpty || !(quitApps ?? []).isEmpty || !(mcpReference ?? "").isEmpty
    }
}

struct PlanItem: Codable, Identifiable {
    var canonicalId: String?
    var id: String
    var name: String
    var kind: String?
    var provider: String?
    var fromVersion: String?
    var targetVersion: String?
    var status: String
    var reason: String?
    var requiresAdmin: Bool?
    var requiresQuit: Bool?
    var requiresRestart: Bool?
    var cleanup: Bool?
    var affectedPaths: [String]?
    var affectedIds: [String]?
    var cleanupPaths: [String]?
    var requiresScripts: Bool?
    var specialApproved: Bool?
    var skillUpdate: SkillUpdatePreview?
    var requiredConsents: [String]?
    var applications: [ApplicationTarget]?
    var applicationLifecycle: [ApplicationTarget]?
    var mcp: MCPConfigurationPreview?
    var mcpDiscovery: MCPLauncherDiscovery?
    var binding: ApplicationUpdateBinding?
    var runtimeReview: RuntimeReview?
    var pluginUpdate: PluginUpdatePreview?
    var privilegedRequest: PrivilegedRequestPreview?
    var displayName: String { id.hasPrefix("demo-") ? L10n.text(name) : name }
    var requiresSpecialConfirmation: Bool {
        mcp?.packageReference != nil || !consentCodes.isEmpty || requiresAdmin == true || requiresQuit == true || requiresRestart == true || requiresScripts == true || reason == "mcp_configuration_may_reload_client_confirm_separately"
    }
    var isExecutable: Bool { isExecutable(in: nil) }
    var displayPriority: Int {
        if status == "skipped" { return 3 }
        if isExecutable { return 0 }
        if requiresSpecialConfirmation && specialApproved != true { return 1 }
        return 2
    }
    var statusLabel: String {
        switch status {
        case "ready": return requiresSpecialConfirmation && !isExecutable ? L10n.text("Separate confirmation needed") : L10n.text("Ready")
        case "manual": return L10n.text("Manual action needed")
        case "blocked": return L10n.text("Blocked")
        case "skipped": return L10n.text("Skipped")
        default: return L10n.text("Status unverified")
        }
    }
    var requirements: [String] {
        var values = [(requiresAdmin == true ? L10n.text("Administrator permission") : nil), (requiresQuit == true ? L10n.text("Quit application") : nil), (requiresRestart == true ? L10n.text("Restart") : nil), (requiresScripts == true ? L10n.text("Run installation scripts") : nil)].compactMap { $0 }
        if reason == "mcp_configuration_may_reload_client_confirm_separately" { values.append(L10n.text("May reload the owning agent")) }
        return values
    }
}

struct SkillUpdatePreview: Codable {
    var fileChanges: [SkillFileChange]?
    var changes: [SkillFileChange] { fileChanges ?? [] }
    var changeSummary: String {
        let kinds = [("added", L10n.text("Added")), ("modified", L10n.text("Modified")), ("deleted", L10n.text("Deleted")), ("mode_changed", L10n.text("Permissions changed"))]
        let counts = kinds.compactMap { kind, label -> String? in
            let count = changes.filter { $0.kind == kind }.count
            return count == 0 ? nil : "\(label) \(count)"
        }
        return counts.isEmpty ? L10n.text("No file differences to show") : counts.joined(separator: " · ")
    }
}

struct SkillFileChange: Codable, Identifiable {
    var path: String
    var kind: String
    var oldSize: Int?
    var newSize: Int?
    var oldMode: String?
    var newMode: String?
    var binary: Bool?
    var oldSha: String?
    var newSha: String?
    var diff: String?
    var truncated: Bool?
    var id: String { path }
    var kindLabel: String {
        switch kind {
        case "added": return L10n.text("Added")
        case "modified": return L10n.text("Modified")
        case "deleted": return L10n.text("Deleted")
        case "mode_changed": return L10n.text("Permissions changed")
        default: return L10n.text("Other change")
        }
    }
    var modeChange: String? {
        guard oldMode != newMode, oldMode != nil || newMode != nil else { return nil }
        return (oldMode ?? L10n.text("None")) + " → " + (newMode ?? L10n.text("None"))
    }
    var sizeChange: String? {
        guard oldSize != nil || newSize != nil else { return nil }
        func size(_ value: Int?) -> String { value.map { Int64($0).formatted(.byteCount(style: .file).locale(L10n.locale)) } ?? L10n.text("None") }
        return size(oldSize) + " → " + size(newSize)
    }
}

struct RecentOperation: Decodable {
    var schema: String
    var operation: ApplyReport?

    static func decode(_ data: Data) throws -> RecentOperation {
        guard data.count <= 8 * 1024 * 1024 + 1 else { throw ClientError.message("Saved operation results exceed the size limit.") }
        let record = try ReportJSON.decode(Self.self, from: data, schema: "keepupdated/recent-operation/v1")
        if let report = record.operation {
            guard report.schema == "keepupdated/apply/v1", report.planId.range(of: "^[0-9a-f]{64}$", options: .regularExpression) != nil,
                  report.action != nil, ["in_progress", "finished"].contains(report.state ?? ""),
                  let ids = report.originalIds, !ids.isEmpty, ids.count <= 10000, Set(ids).count == ids.count,
                  ids.allSatisfy({ !$0.isEmpty }), ids == report.results.map(\.id),
                  let started = ReportJSON.date(report.startedAt),
                  report.state == "in_progress" ? report.finishedAt.isEmpty : (ReportJSON.date(report.finishedAt).map { $0 >= started } ?? false)
            else { throw ClientError.message("Saved operation results are invalid. Review the local record before trying again.") }
        }
        return record
    }
}

struct ApplyReport: Codable, Identifiable {
    var schema: String
    var planId: String
    var startedAt: String
    var finishedAt: String
    var results: [ApplyResult]
    var action: OperationAction?
    var state: String?
    var originalIds: [String]?
    var isUnfinished: Bool { state == "in_progress" }
    var id: String { planId }
    var completed: [ApplyResult] { results.filter(\.isCompleted) }
    var failed: [ApplyResult] { results.filter { !$0.isCompleted && !$0.wasNotExecuted && !$0.awaitsRestart } }
    var retryable: [ApplyResult] { results.filter(\.needsReview) }
    var awaitingRestart: [ApplyResult] { results.filter(\.awaitsRestart) }
    var notExecuted: [ApplyResult] { results.filter(\.wasNotExecuted) }
    func canonicalResult(for item: ApplyResult) -> ApplyResult? {
        guard item.status == "skipped", let id = item.canonicalId, id != item.id else { return nil }
        let matches = results.filter { $0.id == id }
        guard matches.count == 1, let result = matches.first, result.canonicalId == nil else { return nil }
        return result
    }
    struct ApplyResult: Codable, Identifiable {
        var canonicalId: String?
        var id: String
        var name: String
        var status: String
        var reason: String?
        var beforeVersion: String?
        var afterVersion: String?
        var changed: Bool?
        var relatedChanges: [RelatedChange]?
        var backupPath: String?
        var applicationLifecycle: [ApplicationLifecycleResult]?
        var marketplaceRefresh: MarketplaceRefresh?
        var reloadRequired: Bool?
        var privilegedOperationId: String?
        var recoveryPath: String?
        var recoveryPaths: [String]?
        var awaitsRestart: Bool { status == "awaiting_restart" }
        var displayName: String { id.hasPrefix("demo-") ? L10n.text(name) : name }
        var isCompleted: Bool { ["success", "updated", "uninstalled", "succeeded", "configured"].contains(status) }
        var wasNotExecuted: Bool { ["skipped", "blocked", "manual"].contains(status) }
        var needsReview: Bool { privilegedOperationId == nil && ((!isCompleted && !wasNotExecuted && !awaitsRestart) || reason == "operation_not_started") }
    }
    struct RelatedChange: Codable, Identifiable {
        var id: String
        var name: String
        var beforeVersion: String?
        var afterVersion: String?
        var status: String
        var statusLabel: String {
            switch status {
            case "added": return L10n.text("Found after checking")
            case "version_changed": return L10n.text("Version changed")
            case "not_observed_after": return L10n.text("Not observed afterward")
            default: return L10n.text("Status unverified")
            }
        }
        var versionChange: String? {
            if let beforeVersion, let afterVersion { return beforeVersion + " → " + afterVersion }
            if let afterVersion { return L10n.text("Version after: %@", afterVersion) }
            if let beforeVersion { return L10n.text("Version before: %@", beforeVersion) }
            return nil
        }
    }
}

enum ReportJSON {
    static func date(_ text: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: text) { return date }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: text)
    }
    static func decode<T: Decodable>(_ type: T.Type, from data: Data, schema: String) throws -> T {
        let envelope = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        guard envelope?["schema"] as? String == schema else { throw ClientError.message("The CLI returned an incompatible format. Use the matching CLI version.") }
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(type, from: data)
    }
    static func encode<T: Encodable>(_ value: T) throws -> Data {
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        encoder.outputFormatting = [.sortedKeys]
        return try encoder.encode(value)
    }
}

enum ClientError: LocalizedError {
    case message(LocalizedMessage)
    case commandFailed(Int32, String)
    var errorDescription: String? {
        switch self {
        case .message(let value): return value.text
        case .commandFailed(let status, _): return L10n.text("The CLI could not complete the operation (exit code %@).", String(status))
        }
    }
    var technicalDetail: String? {
        if case .commandFailed(_, let detail) = self, !detail.isEmpty { return detail }
        return nil
    }
}
