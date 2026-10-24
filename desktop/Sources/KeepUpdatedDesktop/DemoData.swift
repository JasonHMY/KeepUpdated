import Foundation

enum DemoData {
    static func item(_ id: String, _ name: String, _ kind: String, _ owner: String, _ status: String = "available", code: String? = nil) -> InventoryItem {
        InventoryItem(id: id, kind: kind, name: name, path: "/Example/" + name, owner: owner, source: owner, channel: "stable", installedVersion: "1.0.0", latestVersion: status == "available" ? "1.1.0" : nil, evidence: ["Synthetic example; no real installation data"], health: .init(status: "ok", code: "demo"), update: .init(status: status, code: code ?? (status == "available" ? "newer_release" : (status == "current" ? "exact_source_version_match" : "no_update_source"))))
    }
    static func pythonTool(_ id: String, _ name: String, _ owner: String, _ status: String = "available") -> InventoryItem {
        var value = item(id, name, "python-package", owner, status)
        let environment = "/Example/Tools/" + name.lowercased().replacingOccurrences(of: "_", with: "-").replacingOccurrences(of: ".", with: "-")
        value.path = environment + "/lib/python3/site-packages/" + name + ".dist-info"
        value.evidence.append("environment:" + environment)
        return value
    }
    static let report = InventoryReport(schema: "keepupdated/v1", command: "check", generatedAt: "2026-01-01T00:00:00Z", items: [
        item("demo-app", "Example Editor", "application", "homebrew"),
        item("demo-long", "Example Application with a Long Name for Shared Project Installations", "application", "standalone", "unknown", code: "public_registry_candidate_unverified_origin"),
        item("demo-skill", "writing-helper", "skill", "git"),
        item("demo-mcp", "Example MCP", "mcp-server", "configuration", "unsupported", code: "server_source_not_verified"),
        item("demo-formula", "example-tool", "formula", "homebrew"),
        item("demo-npm", "@example/agent", "node-package", "npm"),
        item("demo-pnpm", "example-pnpm-tool", "node-package", "pnpm", "current"),
        item("demo-bun", "example-bun-tool", "node-package", "bun", "current"),
        pythonTool("demo-python", "example-python", "pipx"),
        item("demo-runtime", "Example Runtime", "runtime", "version-manager", "unsupported", code: "use_installation_owner"),
        item("demo-service", "Example Background Service", "service", "user-launchagent", "unsupported", code: "service_not_update_target"),
        systemItem,
        item("demo-unchecked", "Unchecked Application", "application", "standalone", "unknown", code: "online_query_required"),
        item("demo-timeout", "Timed-out Application", "application", "standalone", "error", code: "timeout"),
        unverifiedCandidate,
        item("demo-copied-skill", "copied-writing-helper", "skill", "skills-lock", code: "skill_update_available"),
        item("demo-local-skill", "locally-edited-helper", "skill", "skills-lock", "unknown", code: "skill_upstream_changed_local_modified"),
        item("demo-native-unknown", "Unchecked Package", "node-package", "npm", "unknown", code: "online_query_required"),
        manualAppItem,
        pythonTool("demo-retry", "retryable-example-tool", "uv"),
        linkedMCP,
        registeredPlugin,
        reviewedRuntime,
        appStoreItem
    ] + skillshareExamples + treeExamples, diagnostics: [], coverage: [.init(adapter: "demo", level: "partial", detail: "Demo mode uses synthetic data. Updates and uninstalls never call the real CLI.")])

    static var skillshareExamples: [InventoryItem] {
        ["skillshare_managed", "skillshare_local_changes", "skillshare_baseline_missing", "skillshare_metadata_unverified"].enumerated().map { index, code in
            var value = item("demo-skillshare-\(index)", "Skillshare Example \(index + 1)", "skill", ".agents", "unknown", code: code)
            value.source = "local"
            value.skillManager = .init(manager: "skillshare", repository: "example/skills", subdirectory: "skills/example", commit: String(repeating: "a", count: 40), root: "/Example/Skillshare/example", metadataPath: "/Example/Skillshare/.metadata.json", format: "central-v1", code: code)
            return value
        }
    }

    static var manualAppItem: InventoryItem {
        var value = item("demo-vendor-app", "Standalone Application with Update", "application", "standalone")
        value.path = "/Example/Standalone Application.app"
        value.realPath = value.path
        value.identity = "com.example.manual"
        return value
    }

    static var appStoreItem: InventoryItem {
        var value = item("demo-app-store", "Example Store App", "application", "app-store")
        value.identity = "com.example.store"
        value.appStore = .init(id: 123456789, bundleId: "com.example.store", country: "au")
        return value
    }

    private static var treeExamples: [InventoryItem] {
        var host = item("demo-tree-host", "Example Studio", "application", "standalone", "current")
        host.path = "/Example/Studio.app"
        var helper = item("demo-tree-helper", "Studio Crash Helper", "application", "app-component")
        helper.path = host.path + "/Contents/Helpers/Crash.app"
        helper.related = [host.id]
        helper.health = .init(status: "error", code: "bundle_metadata_unreadable")
        var skill = item("demo-tree-skill", "Shared Writing Skill", "skill", "git")
        skill.path = "/Example/ClientA/writing"; skill.realPath = "/Example/Shared/writing"
        var alias = skill; alias.id = "demo-tree-alias"; alias.path = "/Example/ClientB/writing"
        var copy = skill; copy.id = "demo-tree-copy"; copy.path = "/Example/Independent/writing"; copy.realPath = copy.path
        copy.installedVersion = "0.9.0"
        var cache = skill; cache.id = "demo-cache"; cache.name = "Cached Review Skill"
        cache.path = "/Example/Cache/market/tools/1/skills/review/SKILL.md"; cache.realPath = cache.path
        cache.source = "plugin-cache"; cache.owner = "plugin-cache"; cache.update = .init(status: "unsupported", code: "codex_plugin_update_in_client")
        cache.pluginCache = .init(client: "codex", root: "/Example/Cache/market/tools/1", id: "tools@market", version: "1", registration: "unverified")
        var pluginSkill = registeredPlugin
        pluginSkill.id = "demo-tree-plugin"; pluginSkill.path = "/Example/plugin/skills/review"
        var environment = item("demo-tree-env", "Example Python Environment", "python-environment", "uv", "unsupported")
        environment.path = "/Example/Environments/tool"
        var package = item("demo-tree-package", "Environment Package", "python-package", "uv")
        package.path = environment.path + "/lib/python3/site-packages/example.dist-info"
        package.evidence.append("environment:" + environment.path)
        return [host, helper, skill, alias, copy, cache, pluginSkill, environment, package]
    }

    private static var unverifiedCandidate: InventoryItem {
        var candidate = item("demo-unverified", "Package with Unknown Source", "node-package", "npm", "unknown", code: "public_registry_candidate_unverified_origin")
        candidate.source = "npm-public-candidate"
        candidate.latestVersion = "2.0.0"
        return candidate
    }
    private static var linkedMCP: InventoryItem {
        var item = item("demo-linked-mcp", "MCP with Linked Installation", "mcp-server", "configuration", "unknown", code: "server_source_not_verified")
        item.related = ["demo-python"]
        return item
    }

    private static var reviewedRuntime: InventoryItem {
        var value = item("demo-reviewed-runtime", "node", "formula", "homebrew", "unknown", code: "online_query_required")
        value.installedVersion = "24.1.0"
        value.installedVersions = ["22.0.0", "24.1.0"]
        return value
    }

    private static var systemItem: InventoryItem {
        var value = item("demo-system", "macOS", "os", "system", "unknown", code: "system_update_query_not_requested")
        value.source = "apple"
        value.path = "/System/Library/CoreServices/SystemVersion.plist"
        value.installedVersion = "26.0"
        return value
    }

    private static var registeredPlugin: InventoryItem {
        var entry = item("demo-plugin", "Example Plugin Skill", "skill", "claude-plugin", "unsupported", code: "plugin_owner_candidate_required")
        entry.path = "/Example/plugin/skills/helper"
        entry.source = "plugin-cache"
        entry.plugin = .init(client: "claude", id: "example@demo-market", scope: "user", root: "/Example/plugin", version: "1.0.0", commit: "demo-old-commit")
        return entry
    }

    static func plan(action: OperationAction, items: [InventoryItem], cleanup: Bool, special: Bool = false, review: ReviewChoices = ReviewChoices(), all: Bool = false) -> OperationPlan {
        let now = Date()
        let formatter = ISO8601DateFormatter()
        return OperationPlan(schema: "keepupdated/plan/v1", planId: "demo-" + UUID().uuidString, createdAt: formatter.string(from: now), expiresAt: formatter.string(from: now.addingTimeInterval(900)), action: action, items: items.map { item in
            let supported = ["application", "formula", "node-package", "python-package", "skill"].contains(item.kind) || item.id == "demo-linked-mcp"
            let needsQuit = item.kind == "application"
            let scripts = item.id == "demo-npm" && action == .update
            let isCurrent = action == .update && item.update.status == "current"
            let status = isCurrent ? "skipped" : (supported && !needsQuit && (!scripts || special) ? "ready" : "manual")
            let reason = isCurrent ? "Already up to date; no update needed." : (needsQuit ? "Quit this app, then create a new preview. Demo mode will not quit apps." : (scripts ? "This update includes installation scripts and needs separate confirmation. Demo mode will not run scripts." : (supported ? "Demo preview; your computer will not change." : "This source needs manual handling. Check again after completing the external steps.")))
            var entry = PlanItem(id: item.id, name: item.name, kind: item.kind, provider: item.owner, fromVersion: item.displayVersion, targetVersion: action == .update ? item.latestVersion : nil, status: status, reason: reason, requiresAdmin: false, requiresQuit: needsQuit, requiresRestart: false, cleanup: cleanup, affectedPaths: [item.path], affectedIds: [], cleanupPaths: cleanup ? ["/Example/Configuration/" + item.name] : [], requiresScripts: scripts, specialApproved: special && scripts)
            if action == .update, let manager = item.skillManager {
                entry.status = "blocked"
                entry.reason = manager.code
                entry.targetVersion = nil
                return entry
            }
            if action == .update && needsQuit {
                let app = ApplicationTarget(id: item.id, path: item.path, identity: "org.example.editor", version: "1.0.0", reopen: review.reopenApps.contains(item.id))
                entry.applications = [app]
                if review.quitApps.contains(item.id) {
                    entry.applicationLifecycle = [app]
                    entry.status = "ready"
                    entry.reason = "Demo preview; your computer will not change."
                }
            }
            if action == .update && item.id == "demo-reviewed-runtime" {
                entry.requiredConsents = ["runtime_update"]
                entry.requiresScripts = true
                entry.specialApproved = special
                entry.targetVersion = "24.2.0"
                entry.runtimeReview = .init(owner: "homebrew", currentVersion: "24.1.0", targetVersion: "24.2.0", compatibility: "same_major", consumers: [.init(id: "demo-npm", name: "Example Agent", version: "1.0.0", path: "/Example/agent")], unknownImpact: ["Project scripts, external applications and undiscovered consumers cannot be fully determined.", "Installed consumers are listed for review; they are not authorized for update or rebuild."])
                entry.status = review.consents.contains("runtime_update") && special ? "ready" : "manual"
                entry.reason = entry.status == "ready" ? "Demo preview; your computer will not change." : "shared_runtime_requires_separate_review"
            }
            if action == .update && item.kind == "mcp-server" && !review.mcpReference.isEmpty {
                entry.status = special ? "ready" : "blocked"
                entry.specialApproved = special
                entry.fromVersion = nil
                entry.targetVersion = nil
                entry.reason = special ? "mcp_package_reference_change_only_not_installation" : "mcp_configuration_may_reload_client_confirm_separately"
                entry.mcp = .init(operation: "replace-package-reference", configPath: "/Example/mcp.json", serverKey: "example", packageReference: .init(launcher: "npx", before: "example@1.0.0", after: review.mcpReference))
            }
            if action == .update, supported, !isCurrent, entry.mcp?.packageReference == nil, entry.targetVersion == nil { entry.targetVersion = "1.1.0" }
            if action == .update, item.kind == "application", item.owner != "homebrew" {
                entry.status = "manual"
                entry.reason = "application_update_requires_vendor_adapter"
                entry.requiresQuit = false
            }
            if action == .update && ["demo-copied-skill", "demo-local-skill"].contains(item.id) {
                entry.skillUpdate = copiedSkillPreview
                entry.targetVersion = "abcdef1234567890"
                if item.id == "demo-local-skill" {
                    entry.status = "blocked"
                    entry.reason = "skill_local_modifications_present"
                }
            }
            if let plugin = item.plugin, action == .update {
                entry.pluginUpdate = .init(owner: plugin, marketplace: "/Example/marketplace", entry: "example", commit: "demo-approved-commit", skills: ["skills/example/SKILL.md", "skills/related/SKILL.md"], reloadRequired: true)
                entry.reason = "plugin_cached_snapshot_requires_reload"
                entry.affectedPaths = [plugin.root]
            }
            return entry
        }, selection: PlanSelection(special: special, consents: review.consents.sorted(), quitApps: review.quitApps.sorted(), reopenApps: review.reopenApps.sorted(), mcpReference: review.mcpReference.isEmpty ? nil : review.mcpReference, ids: all ? [] : items.map(\.id), all: all, cleanup: cleanup))
    }
    static let copiedSkillPreview = SkillUpdatePreview(fileChanges: [
        .init(path: "SKILL.md", kind: "modified", oldSize: 94, newSize: 128, oldMode: "100644", newMode: "100644", diff: "--- a/SKILL.md\n+++ b/SKILL.md\n@@ -1,2 +1,2 @@\n # Writing helper\n-Use the previous steps.\n+Use the revised steps and check the result."),
        .init(path: "references/checks.md", kind: "added", newSize: 36, newMode: "100644", diff: "--- /dev/null\n+++ b/references/checks.md\n@@ -0,0 +1 @@\n+Check the saved output before sharing."),
        .init(path: "references/old-notes.md", kind: "deleted", oldSize: 24, oldMode: "100644", diff: "--- a/references/old-notes.md\n+++ /dev/null\n@@ -1 +0,0 @@\n-Previous notes removed."),
        .init(path: "scripts/check.sh", kind: "mode_changed", oldSize: 32, newSize: 32, oldMode: "100644", newMode: "100755"),
        .init(path: "assets/example.png", kind: "modified", oldSize: 1024, newSize: 2048, oldMode: "100644", newMode: "100644", binary: true)
    ])
    static func privilegedPreparation(item: InventoryItem, packagePath: String?) -> Data {
        let request: [String: Any]
        if let packagePath {
            request = ["kind": "package", "package": ["path": packagePath, "installationPath": item.path,
                "bundleID": item.identity ?? "org.example.app", "fromVersion": item.displayVersion, "version": "2.0.0",
                "teamID": "EXAMPLETEA", "receiptID": "org.example.receipt", "sha256": String(repeating: "0", count: 64),
                "infoSHA256": String(repeating: "1", count: 64), "codeResourcesSHA256": String(repeating: "2", count: 64), "source": "local-signed-package"]]
        } else {
            request = ["kind": "system", "system": ["label": "macOS Example 26.1-25B100", "version": "26.1", "build": "25B100", "fromVersion": "26.0", "fromBuild": "25A100"]]
        }
        return try! JSONSerialization.data(withJSONObject: ["targetID": item.id, "status": "prepared", "reason": "demo_privileged_actions_disabled", "executionBlocker": "demo_privileged_actions_disabled", "candidates": [request]], options: [.sortedKeys])
    }
    static func planData(_ plan: OperationPlan, privilegedRequest: Data?) throws -> Data {
        guard let privilegedRequest else { return try ReportJSON.encode(plan) }
        let request = try JSONSerialization.jsonObject(with: privilegedRequest)
        let kind = (request as? [String: Any])?["kind"] as? String
        var object = try JSONSerialization.jsonObject(with: ReportJSON.encode(plan)) as! [String: Any]
        var selection = object["selection"] as! [String: Any]
        selection["privileged_request"] = request
        object["selection"] = selection
        object["items"] = (object["items"] as! [[String: Any]]).map { original -> [String: Any] in
            var item = original
            item["status"] = "blocked"
            item["reason"] = "demo_privileged_actions_disabled"
            item["requires_admin"] = true
            item["required_consents"] = kind == "restart" ? ["privileged_install", "restart_system"] : ["privileged_install"]
            item["requires_restart"] = kind == "restart"
            item["requires_scripts"] = kind == "system"
            item["special_approved"] = plan.selection?.special == true
            item["privileged_request"] = request
            if let target = ((request as? [String: Any])?["package"] ?? (request as? [String: Any])?["system"]) as? [String: Any] {
                item["from_version"] = target["fromVersion"]
                item["target_version"] = target["version"]
            }
            return item
        }
        return try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])
    }
    static func apply(_ plan: OperationPlan) -> ApplyReport {
        let date = ISO8601DateFormatter().string(from: Date())
        let results: [ApplyReport.ApplyResult] = plan.items.map { item in
            var result = ApplyReport.ApplyResult(id: item.id, name: item.name, status: item.id == "demo-retry" ? "failed" : "skipped", reason: item.id == "demo-retry" ? "Demo failure: check again to create a new preview. No installation was changed." : "Demo mode: no real update or uninstall was performed.", beforeVersion: item.fromVersion, afterVersion: item.fromVersion, changed: false)
            if plan.action == .update && item.id == "demo-plugin" {
                result.status = "failed"
                result.reason = "Demo failure: check again to create a new preview. No installation was changed."
                result.marketplaceRefresh = .init(stateKnown: true, attempted: true, commandSucceeded: true, changed: true, beforeCommit: "demo-old-commit", afterCommit: "demo-other-commit", reason: "plugin_marketplace_refresh_stopped_install")
            }
            return result
        }
        return ApplyReport(schema: "keepupdated/apply/v1", planId: plan.planId, startedAt: date, finishedAt: date, results: results)
    }
}
