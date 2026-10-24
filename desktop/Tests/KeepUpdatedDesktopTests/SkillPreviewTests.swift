import XCTest
@testable import KeepUpdatedDesktop

final class SkillPreviewTests: XCTestCase {
    private func skill(_ code: String = "online_query_required") -> InventoryItem {
        var item = DemoData.item("skill", "Example Skill", "skill", "codex", "unknown", code: code)
        item.source = "local"
        item.evidence = []
        return item
    }

    private var receiptEvidence: [String] {
        ["skill-source:github:example/skills", "skill-path:writing/SKILL.md", "skill-manager:skills", "skill-receipt:/Example/skills-lock.json"]
    }

    func testUnknownLocalSkillDoesNotAdvertiseAnUpdatePreview() {
        let item = skill("skill_upstream_unknown")
        XCTAssertFalse(item.hasManagedUpdatePreview)
        XCTAssertFalse(item.canReviewSkillChanges)
        XCTAssertFalse(item.canUpdate)
        XCTAssertFalse(skill().hasManagedUpdatePreview)
    }

    func testVerifiedCopiedSkillReceiptRetainsPreviewBeforeAndAfterChecking() {
        for code in ["online_query_required", "skill_content_current", "skill_update_available", "timeout"] {
            var item = skill(code)
            item.evidence = receiptEvidence
            XCTAssertTrue(item.hasManagedUpdatePreview, code)
        }
        var ownReceipt = skill()
        ownReceipt.evidence = ["skill-manager:keepupdated", "skill-receipt:/Example/skill-receipts/receipt.json"]
        XCTAssertTrue(ownReceipt.hasManagedUpdatePreview)
        for evidence in [["skill-source:github:example/skills"], ["skill-receipt:/Example/skills-lock.json"], ["skill-manager:keepupdated"], ["skill-source:github:", "skill-receipt:"], ["skill-source:github:example/skills", "skill-receipt:"]] {
            var incomplete = skill()
            incomplete.evidence = evidence
            XCTAssertFalse(incomplete.hasManagedUpdatePreview, evidence.description)
        }
    }

    func testConflictingUnverifiedAndManagerOwnedSourcesRemainExcluded() {
        for code in ["skill_upstream_unknown", "skill_source_conflict", "skill_source_unverified"] {
            var item = skill(code)
            item.evidence = receiptEvidence
            XCTAssertFalse(item.hasManagedUpdatePreview, code)
        }
        var managed = skill()
        managed.evidence = receiptEvidence
        managed.skillManager = .init(manager: "skillshare", root: "/Example/shared", metadataPath: "/Example/shared/.skillshare.json", format: "skillshare", code: "skillshare_managed")
        XCTAssertFalse(managed.hasManagedUpdatePreview)
        var system = skill()
        system.owner = "system"
        system.evidence = receiptEvidence
        XCTAssertFalse(system.hasManagedUpdatePreview)
    }

    func testGitAndRegisteredClaudePluginRetainTheirExistingPreviewRoutes() {
        var git = skill("git_remote_not_fetched")
        git.source = "git"
        git.evidence = ["git-head:0123456789012345678901234567890123456789", "git-branch:main", "git-worktree-clean"]
        XCTAssertTrue(git.hasManagedUpdatePreview)

        var plugin = skill("plugin_owner_candidate_required")
        plugin.source = "plugin-cache"
        XCTAssertFalse(plugin.hasManagedUpdatePreview)
        plugin.plugin = .init(client: "claude", id: "example@market", scope: "user", root: "/Example/plugin", version: "1")
        XCTAssertTrue(plugin.hasManagedUpdatePreview)
        plugin.plugin?.client = "codex"
        XCTAssertFalse(plugin.hasManagedUpdatePreview)
        plugin.plugin?.client = "claude"
        plugin.plugin?.scope = "project"
        XCTAssertFalse(plugin.hasManagedUpdatePreview)
        plugin.plugin = nil
        plugin.pluginCache = .init(client: "claude", root: "/Example/cache", id: "example@market", version: "1", registration: "unregistered")
        XCTAssertFalse(plugin.hasManagedUpdatePreview)
    }

    func testVerifiedLocalChangesRemainReviewableWithoutClaimingAnAvailableUpdate() {
        for code in ["skill_upstream_changed_local_modified", "skill_local_modified_or_baseline_unknown", "skill_local_modified"] {
            var item = skill(code)
            item.evidence = receiptEvidence
            XCTAssertTrue(item.hasManagedUpdatePreview)
            XCTAssertTrue(item.canReviewSkillChanges)
            XCTAssertFalse(item.canUpdate)
        }
    }
}
