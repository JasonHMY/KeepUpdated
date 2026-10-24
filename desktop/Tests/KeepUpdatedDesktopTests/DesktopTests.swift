import XCTest
import Combine
@testable import KeepUpdatedDesktop

final class DesktopTests: XCTestCase {
    private var previousDefaults: UserDefaults!
    private var languageSuite: String!
    override func setUp() {
        super.setUp()
        previousDefaults = L10n.defaults
        languageSuite = "KeepUpdatedDesktopTests." + UUID().uuidString
        L10n.defaults = UserDefaults(suiteName: languageSuite)!
        // Preserve the existing Chinese copy assertions without depending on this Mac's language.
        L10n.defaults.set(AppLanguage.simplifiedChinese.rawValue, forKey: L10n.preferenceKey)
    }
    override func tearDown() {
        L10n.defaults.removePersistentDomain(forName: languageSuite)
        L10n.defaults = previousDefaults
        super.tearDown()
    }

    func testInventoryJSONAndUnknownFields() throws {
        let data = Data(#"{"schema":"keepupdated/v1","command":"scan","generated_at":"2026-01-01T00:00:00Z","items":[{"id":"one","kind":"skill","name":"Example","path":"/Example/Skill","owner":"git","source":"git","channel":"stable","identity":"org.example.skill","installed_version":"1.0","active_version":"1.1","evidence":[],"health":{"status":"ok","code":"readable"},"update":{"status":"future-status","code":"future-code"},"dependencies":[{"name":"example-runtime","installed_version":"2.0","status":"unknown","code":"runtime_binding_unverified"}],"new_field":"ignored"}],"diagnostics":[],"coverage":[]}"#.utf8)
        let report = try ReportJSON.decode(InventoryReport.self, from: data, schema: "keepupdated/v1")
        XCTAssertEqual(report.items.count, 1)
        XCTAssertEqual(report.items[0].displayVersion, "1.1")
        XCTAssertEqual(report.items[0].update.status, "future-status")
        XCTAssertEqual(report.items[0].statusLabel, "状态格式不支持")
        XCTAssertEqual(report.items[0].identity, "org.example.skill")
        XCTAssertEqual(report.items[0].dependencies?.first?.installedVersion, "2.0")
    }

    func testRejectsUnexpectedSchemaAndMalformedJSON() {
        XCTAssertThrowsError(try ReportJSON.decode(InventoryReport.self, from: Data(#"{"schema":"other/v1"}"#.utf8), schema: "keepupdated/v1"))
        XCTAssertThrowsError(try ReportJSON.decode(InventoryReport.self, from: Data("not json".utf8), schema: "keepupdated/v1"))
    }

    func testCategoriesPreserveAllItemsAndUnknownKind() {
        let items = DemoData.report.items
        XCTAssertEqual(Set(items.map(\.category)).count, 11)
        XCTAssertEqual(items.count, ItemCategory.allCases.filter { $0 != .all }.reduce(0) { total, category in total + items.filter { $0.category == category }.count })
        XCTAssertEqual(DemoData.item("other", "Other", "future-kind", "unknown").category, .other)
    }

    func testSearchMatchesNameSourceAndPath() {
        let item = DemoData.item("npm", "@example/agent", "node-package", "npm")
        XCTAssertTrue(item.matches(" AGENT "))
        XCTAssertTrue(item.matches("npm"))
        XCTAssertTrue(item.matches("/Example"))
        XCTAssertTrue(item.matches(""))
        XCTAssertFalse(item.matches("absent"))
    }

    func testInventoryFiltersPreserveNaturalOrderAndUnfilteredCounts() async {
        await MainActor.run {
            let store = InventoryStore(demo: true)
            XCTAssertEqual(store.count(.all), 29)
            XCTAssertEqual(Set(store.filtered.map(\.id)), Set(DemoData.report.items.map(\.id)))

            var report = DemoData.report
            report.items = [
                DemoData.item("skill10", "Tool 10", "skill", "git", "current"),
                DemoData.item("app", "Tool 1", "application", "standalone"),
                DemoData.item("skill2-first", "Tool 2", "skill", "git"),
                DemoData.item("skill2-second", "Tool 2", "skill", "git")
            ]
            store.replaceReport(report)
            XCTAssertEqual(store.filtered.map(\.id), ["app", "skill2-first", "skill2-second", "skill10"])
            store.category = .skills
            XCTAssertEqual(store.filtered.map(\.id), ["skill2-first", "skill2-second", "skill10"])
            store.onlyUpdates = true
            XCTAssertEqual(store.filtered.map(\.id), ["skill2-first", "skill2-second"])
            store.query = " Tool 2 "
            XCTAssertEqual(store.filtered.map(\.id), ["skill2-first", "skill2-second"])
            store.query = "absent"
            XCTAssertTrue(store.filtered.isEmpty)
            XCTAssertEqual(store.count(.all), 3)
            XCTAssertEqual(store.count(.skills), 2)
            XCTAssertEqual(store.count(.applications), 1)
            XCTAssertEqual(store.count(.mcp), 0)
            XCTAssertEqual(store.updateCount, 2)
            XCTAssertEqual(store.report?.items.map(\.id), report.items.map(\.id))
        }
    }

    func testReplacingInventoryRemovesStaleItemsAndRefreshesCounts() async {
        await MainActor.run {
            let store = InventoryStore(demo: true)
            store.category = .skills
            store.onlyUpdates = true
            store.query = "replacement"
            var report = DemoData.report
            report.items = [DemoData.item("new", "Replacement", "skill", "git", "current")]
            store.replaceReport(report)
            XCTAssertTrue(store.filtered.isEmpty)
            XCTAssertEqual(store.updateCount, 0)
            store.onlyUpdates = false
            XCTAssertEqual(store.filtered.map(\.id), ["new"])
            XCTAssertEqual(store.count(.all), 1)
            XCTAssertEqual(store.count(.applications), 0)
            report.items = []
            store.replaceReport(report)
            XCTAssertTrue(store.filtered.isEmpty)
            XCTAssertTrue(ItemCategory.allCases.allSatisfy { store.count($0) == 0 })
            XCTAssertEqual(store.updateCount, 0)
        }
    }

    func testOnlyConfirmedAvailableItemsOfferUpdate() {
        for state in ["current", "unknown", "unsupported", "error", "future-status"] {
            var item = DemoData.item(state, "Example", "node-package", "npm", state)
            item.latestVersion = "99.0.0"
            XCTAssertFalse(item.canUpdate, "A candidate version must not turn \(state) into an update action")
        }
        XCTAssertTrue(DemoData.item("new", "Example", "node-package", "npm", "available").canUpdate)
        XCTAssertFalse(DemoData.report.items.first { $0.id == "demo-unverified" }!.canUpdate)
    }

    func testStatusExplainsDifferentUnknownCausesAndNextSteps() {
        let codes = ["online_query_required", "skill_upstream_unknown", "public_registry_candidate_unverified_origin", "timeout", "version_scheme_or_active_version_unresolved", "no_matching_mac_product"]
        let items = codes.map { DemoData.item($0, "Example", "application", "standalone", $0 == "timeout" ? "error" : "unknown", code: $0) }
        XCTAssertEqual(Set(items.map(\.statusLabel)).count, codes.count)
        for item in items {
            XCTAssertFalse(item.statusLabel.contains("尚未确认"))
            XCTAssertFalse(item.statusExplanation.isEmpty)
            XCTAssertFalse(item.statusExplanation.contains(item.update.code))
            XCTAssertFalse(item.canUpdate)
        }
        XCTAssertTrue(items[0].statusExplanation.contains("检查更新"))
        XCTAssertTrue(items[2].statusExplanation.contains("同一来源"))
        XCTAssertTrue(items[3].statusExplanation.contains("时间"))
        XCTAssertTrue(items[3].matches("超时"))
    }

    func testMissingMetadataAndManagedComponentsHaveSpecificExplanations() {
        var component = DemoData.item("component", "Example Helper", "application", "app-component", "unknown", code: "no_update_source")
        XCTAssertEqual(component.statusLabel, "跟随主应用更新")
        component.health = .init(status: "error", code: "bundle_metadata_unreadable")
        XCTAssertEqual(component.statusLabel, "安装信息有异常")
        XCTAssertTrue(component.statusExplanation.contains("信息文件无法读取"))
        let plugin = DemoData.item("plugin", "Example Skill", "skill", "plugin-cache", "unsupported", code: "managed_by_plugin_owner")
        XCTAssertEqual(plugin.statusLabel, "注册状态未验证")
        XCTAssertTrue(plugin.statusExplanation.contains("所属客户端"))
    }

    func testStableSourceAndSkillContentStatesStayDistinct() {
        var item = DemoData.item("vendor", "Example", "application", "standalone", "current")
        item.channel = "public-stable"
        XCTAssertEqual(item.statusLabel, "与稳定版一致")
        item.update.status = "available"
        XCTAssertEqual(item.statusLabel, "有稳定版更新")
        item.kind = "skill"
        item.channel = "unknown"
        item.update.code = "skill_update_available"
        XCTAssertTrue(item.statusExplanation.contains("具体文件变化"))
        item.update = .init(status: "unknown", code: "skill_upstream_changed_local_modified")
        XCTAssertFalse(item.canUpdate)
        XCTAssertTrue(item.statusExplanation.contains("安装记录不同"))
        item.installedVersion = "abcdef123456"
        XCTAssertEqual(item.displayVersion, "内容 abcdef123456")
    }

    func testInterruptedSkillUpdateIsVisibleWithoutAnInventoryRow() {
        var report = DemoData.report
        report.items = []
        report.diagnostics = [.init(adapter: "skills", path: "/Example/transaction.json", code: "skill_update_interrupted")]
        XCTAssertTrue(report.hasInterruptedSkillUpdate)
        report.diagnostics[0].code = "skill_update_record_unreadable"
        XCTAssertTrue(report.hasInterruptedSkillUpdate)
        report.diagnostics = []
        XCTAssertFalse(report.hasInterruptedSkillUpdate)
    }

    func testPlanRoundTripAndSpecialItemsExcludedFromOrdinaryExecution() throws {
        var plan = DemoData.plan(action: .update, items: DemoData.report.items, cleanup: false)
        plan.items[0].requiresAdmin = true
        let data = try ReportJSON.encode(plan)
        let decoded = try ReportJSON.decode(OperationPlan.self, from: data, schema: "keepupdated/plan/v1")
        XCTAssertEqual(decoded.planId, plan.planId)
        XCTAssertFalse(decoded.executable.contains { $0.id == plan.items[0].id })
        XCTAssertTrue(decoded.special.contains { $0.id == plan.items[0].id })
        XCTAssertNotNil(decoded.expires)
        XCTAssertTrue(decoded.executable.allSatisfy { $0.status == "ready" })
    }

    func testCopiedSkillPreviewJSONPreservesFileChanges() throws {
        let data = Data(#"{"schema":"keepupdated/plan/v1","plan_id":"copied-skill-plan","created_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-01T00:15:00Z","action":"update","items":[{"id":"skill","name":"Example Skill","status":"blocked","reason":"skill_content_differs_from_receipt","skill_update":{"root":"/Example/Skills","local_modified":true,"file_changes":[{"path":"SKILL.md","kind":"modified","old_size":20,"new_size":35,"old_mode":"100644","new_mode":"100755","binary":false,"diff":"-Old\n+New","truncated":true},{"path":"assets/example.png","kind":"added","new_size":1024,"binary":true}]}}]}"#.utf8)
        let plan = try ReportJSON.decode(OperationPlan.self, from: data, schema: "keepupdated/plan/v1")
        let preview = try XCTUnwrap(plan.items.first?.skillUpdate)
        XCTAssertEqual(preview.changes.count, 2)
        XCTAssertEqual(preview.changeSummary, "新增 1 · 修改 1")
        XCTAssertEqual(preview.changes.first?.modeChange, "100644 → 100755")
        XCTAssertEqual(preview.changes.first?.diff, "-Old\n+New")
        XCTAssertEqual(preview.changes.first?.truncated, true)
        XCTAssertEqual(preview.changes.last?.binary, true)
        XCTAssertTrue(plan.executable.isEmpty)
        let roundTrip = try ReportJSON.decode(OperationPlan.self, from: ReportJSON.encode(plan), schema: "keepupdated/plan/v1")
        XCTAssertEqual(roundTrip.items.first?.skillUpdate?.changes.first?.diff, "-Old\n+New")
    }

    func testCopiedSkillDemoSeparatesPreviewFromBlockedLocalChanges() throws {
        let items = DemoData.report.items.filter { ["demo-copied-skill", "demo-local-skill"].contains($0.id) }
        let plan = DemoData.plan(action: .update, items: items, cleanup: false)
        XCTAssertEqual(plan.executable.map(\.id), ["demo-copied-skill"])
        XCTAssertEqual(plan.items.last?.status, "blocked")
        XCTAssertEqual(plan.items.last?.reason, "skill_local_modifications_present")
        let preview = try XCTUnwrap(plan.items.first?.skillUpdate)
        XCTAssertEqual(preview.changes.count, 5)
        XCTAssertEqual(preview.changeSummary, "新增 1 · 修改 2 · 删除 1 · 权限变化 1")
        XCTAssertTrue(preview.changes.contains { $0.kind == "mode_changed" && $0.modeChange != nil })
        XCTAssertTrue(preview.changes.contains { $0.binary == true })
        XCTAssertFalse(SkillUpdatePreview(fileChanges: nil).changeSummary.isEmpty)
        XCTAssertFalse(SkillUpdatePreview(fileChanges: []).changeSummary.isEmpty)
        XCTAssertTrue(DemoData.apply(plan).results.allSatisfy { $0.changed == false && $0.status == "skipped" })
    }

    func testLocalSkillReviewDoesNotBecomeAnAvailableUpdate() {
        for code in ["skill_upstream_changed_local_modified", "skill_local_modified_or_baseline_unknown", "skill_local_modified"] {
            var item = DemoData.item("local", "Edited Skill", "skill", "skills-lock", "unknown", code: code)
            XCTAssertTrue(item.canReviewSkillChanges)
            XCTAssertFalse(item.canUpdate)
            item.kind = "application"
            XCTAssertFalse(item.canReviewSkillChanges)
        }
        XCTAssertFalse(DemoData.item("unknown", "Unknown Skill", "skill", "unknown", "unknown", code: "skill_upstream_unknown").canReviewSkillChanges)
    }

    func testPrivatePlanFilePermissionsAndExactBytes() throws {
        let data = Data(#"{"schema":"example","unknown":"preserve exact bytes"}"#.utf8)
        let file = try PrivatePlanFile(data: data)
        defer { file.remove() }
        XCTAssertEqual(try Data(contentsOf: file.url), data)
        let folderAttributes = try FileManager.default.attributesOfItem(atPath: file.directory.path)
        let fileAttributes = try FileManager.default.attributesOfItem(atPath: file.url.path)
        XCTAssertEqual((folderAttributes[.posixPermissions] as? NSNumber)?.intValue, 0o700)
        XCTAssertEqual((fileAttributes[.posixPermissions] as? NSNumber)?.intValue, 0o600)
        file.remove()
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.url.path))
    }

    func testDemoApplyNeverClaimsRealChanges() throws {
        let plan = DemoData.plan(action: .uninstall, items: DemoData.report.items, cleanup: true)
        let result = DemoData.apply(plan)
        XCTAssertEqual(result.results.count, plan.items.count)
        XCTAssertTrue(result.results.allSatisfy { $0.changed == false && ["skipped", "failed"].contains($0.status) })
        XCTAssertEqual(try ReportJSON.decode(ApplyReport.self, from: ReportJSON.encode(result), schema: "keepupdated/apply/v1").planId, plan.planId)
    }

    func testScanLocationsKeepArgumentsSeparatedAndPersistOnlyPaths() throws {
        let defaults = UserDefaults(suiteName: "KeepUpdatedTests-" + UUID().uuidString)!
        var locations = ScanLocations()
        locations[.skillsRoot] = ["/Example/My Agent/skills"]
        locations[.mcpConfig] = ["/Example/Agent/config.json"]
        locations.save(defaults: defaults)
        XCTAssertEqual(ScanLocations.load(defaults: defaults), locations)
        XCTAssertEqual(locations.arguments, ["--mcp-config", "/Example/Agent/config.json", "--skills-root", "/Example/My Agent/skills"])
        defaults.removeObject(forKey: "scanLocations")
    }

    func testFractionalPlanDatesAndSeparateScriptConfirmation() throws {
        XCTAssertNotNil(ReportJSON.date("2026-10-19T04:12:19.123456789Z"))
        XCTAssertNotNil(ReportJSON.date("2026-10-19T04:12:19Z"))
        let item = DemoData.report.items.first { $0.id == "demo-npm" }!
        let ordinary = DemoData.plan(action: .update, items: [item], cleanup: false)
        let special = DemoData.plan(action: .update, items: [item], cleanup: false, special: true)
        XCTAssertTrue(ordinary.executable.isEmpty)
        XCTAssertEqual(ordinary.special.count, 1)
        XCTAssertEqual(special.executable.count, 1)
        XCTAssertTrue(special.selection?.special == true)
        XCTAssertTrue(special.items[0].specialApproved == true)
    }

    func testPlanPresentationPrioritizesChangesWithoutLosingOrMutatingItems() throws {
        var plan = DemoData.plan(action: .update, items: [DemoData.report.items[3]], cleanup: false)
        func row(_ id: String, _ status: String, scripts: Bool = false, approved: Bool = false) -> PlanItem {
            var item = plan.items[0]
            item.id = id
            item.status = status
            item.requiresScripts = scripts
            item.specialApproved = approved
            return item
        }
        plan.items = [row("skipped", "skipped"), row("special", "manual", scripts: true), row("blocked", "blocked"), row("ready", "ready"), row("manual", "manual"), row("approved", "ready", scripts: true, approved: true), row("future", "future-status")]
        let original = try ReportJSON.encode(plan)
        XCTAssertEqual(plan.displayItems.map(\.id), ["ready", "approved", "special", "blocked", "manual", "future", "skipped"])
        XCTAssertEqual(plan.displayItems.count, plan.items.count)
        XCTAssertEqual(try ReportJSON.encode(plan), original)
        var mcp = row("mcp", "manual")
        mcp.reason = "mcp_configuration_may_reload_client_confirm_separately"
        XCTAssertTrue(mcp.requiresSpecialConfirmation)
        XCTAssertEqual(mcp.displayPriority, 1)
    }

    func testApplyJSONPreservesRelatedChangesAndBackupPath() throws {
        let data = Data(#"{"schema":"keepupdated/apply/v1","plan_id":"example-plan","started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:01:00Z","results":[{"id":"mcp","name":"Example MCP","status":"uninstalled","reason":"configuration_removed_shared_software_preserved","changed":true,"related_changes":[{"id":"dependency","name":"Example Dependency","before_version":"1.0","after_version":"1.1","status":"version_changed"},{"id":"added","name":"New Dependency","after_version":"2.0","status":"added"},{"id":"absent","name":"Unobserved Item","before_version":"3.0","status":"not_observed_after"}],"backup_path":"/Example/Private Backups/config.json"}]}"#.utf8)
        let report = try ReportJSON.decode(ApplyReport.self, from: data, schema: "keepupdated/apply/v1")
        let result = try XCTUnwrap(report.results.first)
        XCTAssertEqual(result.backupPath, "/Example/Private Backups/config.json")
        XCTAssertEqual(result.relatedChanges?.count, 3)
        XCTAssertEqual(result.relatedChanges?.first?.versionChange, "1.0 → 1.1")
        XCTAssertEqual(result.relatedChanges?.last?.statusLabel, "检查后未观察到")
        XCTAssertEqual(result.relatedChanges?.last?.status, "not_observed_after")
    }

    func testReasonTextUsesNaturalCopyAndKeepsUnknownCodeInDetails() {
        for code in ["close_application_then_verify_all_bundle_processes", "mcp_configuration_may_reload_client_confirm_separately", "mcp_configuration_still_uses_installation", "approved_git_commit_verified", "configuration_removed_but_inventory_still_contains_definition", "skill_changes_require_confirmation", "skill_local_modifications_present", "skill_preview_too_large", "skill_recovery_required", "approved_skill_content_verified", "skill_update_verification_failed"] {
            XCTAssertNotEqual(ReasonText.describe(code), code)
            XCTAssertNil(ReasonText.technicalDetail(code))
        }
        let unknown = "future_backend_reason"
        XCTAssertNotEqual(ReasonText.describe(unknown), unknown)
        XCTAssertEqual(ReasonText.technicalDetail(unknown), unknown)
        XCTAssertEqual(ReasonText.describe("演示计划，不会改动电脑"), "演示计划，不会改动电脑")
    }

    func testProcessDrainsBothPipesAndAcceptsExitOneReport() async throws {
        let fixture = try executableFixture("""
        #!/bin/sh
        i=0
        while [ "$i" -lt 3000 ]; do
            printf '                                        '
            printf 'fixture diagnostic larger than a pipe buffer\\n' >&2
            i=$((i+1))
        done
        printf '{"schema":"keepupdated/v1","command":"scan","generated_at":"2026-01-01T00:00:00Z","items":[],"diagnostics":[],"coverage":[]}'
        exit 1
        """)
        defer { fixture.remove() }
        let result = try await CLIClient(testExecutable: fixture.url).run([])
        XCTAssertEqual(result.status, 1)
        XCTAssertGreaterThan(result.data.count, 100_000)
        XCTAssertTrue(try ReportJSON.decode(InventoryReport.self, from: result.data, schema: "keepupdated/v1").items.isEmpty)
    }

    func testProcessExitTwoIsFailure() async throws {
        let fixture = try executableFixture("#!/bin/sh\nprintf 'invalid arguments' >&2\nexit 2\n")
        defer { fixture.remove() }
        do {
            _ = try await CLIClient(testExecutable: fixture.url).run([])
            XCTFail("exit 2 must fail even if a command printed output")
        } catch {
            XCTAssertTrue((error as? ClientError)?.technicalDetail?.contains("invalid arguments") == true)
            XCTAssertTrue(error.localizedDescription.contains("退出码 2"))
        }
    }

    func testProcessCancellationTerminatesFixture() async throws {
        let fixture = try executableFixture("#!/bin/sh\nwhile :; do :; done\n")
        defer { fixture.remove() }
        let client = CLIClient(testExecutable: fixture.url)
        let task = Task { try await client.run([]) }
        try await Task.sleep(nanoseconds: 100_000_000)
        task.cancel()
        do { _ = try await task.value; XCTFail("cancelled process must not return a success") }
        catch is CancellationError {} catch { XCTFail("expected cancellation, got \(error)") }
    }

    private func executableFixture(_ text: String) throws -> PrivatePlanFile {
        let fixture = try PrivatePlanFile(data: Data(text.utf8))
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: fixture.url.path)
        return fixture
    }

    func testPreviewCapabilityDoesNotPromoteUnknownVersions() async {
        await MainActor.run {
            let store = InventoryStore(demo: true)
            var report = DemoData.report
            let node = DemoData.item("node", "Node", "node-package", "npm", "unknown")
            let python = DemoData.pythonTool("python", "Python", "uv", "unknown")
            let brew = DemoData.item("brew", "Brew", "formula", "homebrew", "unknown")
            let git = DemoData.item("git", "Git Skill", "skill", "git", "unknown")
            var copied = DemoData.item("copied", "Copied Skill", "skill", "skills-lock", "unknown")
            copied.source = "local"
            copied.evidence = ["skill-source:github:example/skills", "skill-receipt:/Example/receipt.json"]
            var linked = DemoData.item("linked", "MCP", "mcp-server", "configuration", "unknown")
            linked.related = [node.id]
            var unlinked = linked
            unlinked.id = "unlinked"
            unlinked.related = ["missing"]
            let unsupported = DemoData.item("pip", "System Python", "python-package", "pip", "unknown")
            let plugin = DemoData.item("plugin", "Plugin Skill", "skill", "plugin-cache", "unknown")
            let app = DemoData.item("app", "Application", "application", "standalone")
            report.items = [node, python, brew, git, copied, linked, unlinked, unsupported, plugin, app]
            store.replaceReport(report)
            for item in [node, python, brew, git, copied, linked] {
                XCTAssertFalse(item.canUpdate)
                XCTAssertTrue(store.canPreviewUpdate(item), item.id)
                XCTAssertEqual(item.updatePreviewTitle, "检查并预览")
            }
            for item in [unlinked, unsupported, plugin] { XCTAssertFalse(store.canPreviewUpdate(item), item.id) }
            XCTAssertTrue(app.canUpdate)
            XCTAssertEqual(app.updatePreviewTitle, "查看更新方式")
            store.onlyUpdates = true
            XCTAssertEqual(store.filtered.map(\.id), [app.id])
        }
    }

    func testSelectionSurvivesFiltersAndPrunesAfterInventoryReplacement() async {
        await MainActor.run {
            let store = InventoryStore(demo: true)
            store.category = .skills
            store.selectVisible()
            let skills = store.selectedIDs
            XCTAssertFalse(skills.isEmpty)
            store.category = .mcp
            store.query = "已关联"
            store.selectVisible()
            XCTAssertTrue(store.selectedIDs.isSuperset(of: skills))
            XCTAssertTrue(store.selectedIDs.contains("demo-linked-mcp"))
            store.setSelected("demo-system", selected: true)
            XCTAssertFalse(store.selectedIDs.contains("demo-system"))
            var report = DemoData.report
            report.items = report.items.filter { $0.id == "demo-linked-mcp" }
            store.replaceReport(report)
            XCTAssertTrue(store.selectedIDs.isEmpty, "A remaining MCP without its installation owner is no longer selectable")
            store.replaceReport(DemoData.report)
            store.query = ""
            store.selectVisible()
            store.clearSelection()
            XCTAssertTrue(store.selectedIDs.isEmpty)
        }
    }

    func testSelectedPreviewAndGlobalPlanKeepTheirSeparateScopes() async {
        let store = await MainActor.run { InventoryStore(demo: true) }
        await MainActor.run {
            store.setSelected("demo-native-unknown", selected: true)
            store.setSelected("demo-copied-skill", selected: true)
            store.category = .applications
            store.query = "absent"
            store.previewSelected()
        }
        await waitForIdle(store)
        await MainActor.run {
            XCTAssertEqual(Set(store.preview?.items.map(\.id) ?? []), ["demo-native-unknown", "demo-copied-skill"])
            XCTAssertTrue(store.preview?.items.contains { $0.id == "demo-native-unknown" && $0.isExecutable } == true)
            store.dismissPreview()
            store.plan(action: .update, all: true)
        }
        await waitForIdle(store)
        await MainActor.run {
            XCTAssertEqual(store.preview?.items.count, DemoData.report.items.filter { !$0.needsManualUpdate }.count)
            XCTAssertTrue(store.preview?.items.contains { $0.id == "demo-system" } == true)
            XCTAssertTrue(store.preview?.items.contains { $0.id == "demo-local-skill" && $0.status == "blocked" } == true)
        }
    }

    func testPlanGroupsPartitionEveryItemAndRetainSpecialGuards() {
        var plan = DemoData.plan(action: .update, items: DemoData.report.items, cleanup: false)
        var future = plan.items[0]
        future.id = "future"
        future.status = "future-status"
        plan.items.append(future)
        let groups = plan.executable + plan.needsAttention + plan.noOperation
        XCTAssertEqual(groups.count, plan.items.count)
        XCTAssertEqual(Set(groups.map(\.id)), Set(plan.items.map(\.id)))
        XCTAssertTrue(plan.special.allSatisfy { item in plan.needsAttention.contains { $0.id == item.id } })
        XCTAssertTrue(plan.needsAttention.contains { $0.id == "future" })
        XCTAssertTrue(plan.noOperation.allSatisfy { $0.status == "skipped" })
    }

    func testResultsSummaryIncludesFailuresUnknownAndNotExecutedStates() {
        let statuses = ["updated", "uninstalled", "failed", "verification_unknown", "cleanup_failed", "future-status", "skipped", "blocked", "manual"]
        let report = ApplyReport(schema: "keepupdated/apply/v1", planId: "summary", startedAt: "", finishedAt: "", results: statuses.map { .init(id: $0, name: $0, status: $0) })
        XCTAssertEqual(report.completed.count, 2)
        XCTAssertEqual(report.failed.count, 4)
        XCTAssertEqual(report.notExecuted.count, 3)
        XCTAssertEqual(report.completed.count + report.failed.count + report.notExecuted.count, statuses.count)
        XCTAssertTrue(report.failed.allSatisfy(\.needsReview))
    }

    func testFailedRetryWaitsForSheetDismissalAndCreatesFreshOriginalActionPlan() async {
        for action in [OperationAction.update, .uninstall] {
            let store = await MainActor.run { InventoryStore(demo: true) }
            await MainActor.run { store.plan(action: action, ids: ["demo-retry"]) }
            await waitForIdle(store)
            let originalID = await MainActor.run { store.preview?.planId }
            await MainActor.run { store.execute() }
            await waitForIdle(store)
            await MainActor.run {
                XCTAssertNil(store.preview)
                XCTAssertNil(store.results)
                XCTAssertEqual(store.lastResults?.failed.map(\.id), ["demo-retry"])
                store.presentPendingResults()
                store.results = nil
                store.showLastResults()
                XCTAssertEqual(store.results?.planId, originalID)
                store.retryResults(ids: ["demo-retry", "not-a-failure"])
                XCTAssertNil(store.results)
                XCTAssertNil(store.preview, "No new sheet is presented during dismissal")
                store.resultsSheetDismissed()
            }
            await waitForIdle(store)
            await MainActor.run {
                XCTAssertEqual(store.preview?.action, action)
                XCTAssertEqual(store.preview?.items.map(\.id), ["demo-retry"])
                XCTAssertNotEqual(store.preview?.planId, originalID)
                XCTAssertEqual(store.lastResults?.planId, originalID, "Retry only plans; it must not execute again")
                XCTAssertTrue(store.lastResults?.results.allSatisfy { $0.changed == false } == true)
            }
        }
    }

    private func waitForIdle(_ store: InventoryStore) async {
        let done = expectation(description: "operation completed")
        let subscription = await MainActor.run {
            store.$busy.filter { !$0 }.prefix(1).sink { _ in done.fulfill() }
        }
        await fulfillment(of: [done], timeout: 3)
        withExtendedLifetime(subscription) {}
    }

    func testRetryFreshScanKeepsPresentIDsAndOriginalActionInMixedBatch() async throws {
        for action in [OperationAction.update, .uninstall] {
            let fixture = try retryScanFixture(action: action, remainingIDs: ["retry-present"])
            defer { fixture.remove() }
            let store = await MainActor.run { InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.url)) }
            await MainActor.run { store.plan(action: action, ids: ["retry-present", "retry-removed"]) }
            await waitForIdle(store)
            let oldPlanID = await MainActor.run { store.preview?.planId }
            await MainActor.run { store.execute() }
            await waitForIdle(store)
            await MainActor.run {
                XCTAssertEqual(store.report?.items.count, 2, "The cached post-apply inventory still contains the old IDs")
                store.presentPendingResults()
                store.retryResults(ids: ["retry-present", "retry-removed"])
                store.resultsSheetDismissed()
            }
            await waitForIdle(store)
            await MainActor.run {
                XCTAssertNil(store.error)
                XCTAssertEqual(store.report?.items.map(\.id), ["retry-present", "replacement-record"])
                XCTAssertEqual(store.preview?.action, action)
                XCTAssertEqual(store.preview?.items.map(\.id), ["retry-present"])
                XCTAssertNotEqual(store.preview?.planId, oldPlanID)
                XCTAssertTrue(store.previewNotice?.contains("1 项未在本次重新扫描中找到") == true)
                XCTAssertEqual(store.lastResults?.planId, oldPlanID)
            }
            let calls = try String(contentsOf: fixture.directory.appendingPathComponent("calls"), encoding: .utf8).split(separator: "\n").map(String.init)
            XCTAssertEqual(calls.filter { $0.hasPrefix("apply ") }.count, 1, "Retry must never replay apply")
            XCTAssertEqual(calls.filter { $0.hasPrefix("scan") }.count, 2)
            XCTAssertEqual(calls.filter { $0.hasPrefix("plan ") }.count, 2)
            XCTAssertTrue(calls.last?.contains("--action \(action.rawValue)") == true)
            XCTAssertFalse(calls.last?.contains("retry-removed") == true)
            XCTAssertFalse(calls.last?.contains("replacement-record") == true, "Matching names must not select a new installation ID")
        }
    }

    func testRetryWithAllIDsMissingDoesNotPlanAndRetainsPriorResult() async throws {
        let fixture = try retryScanFixture(action: .uninstall, remainingIDs: [])
        defer { fixture.remove() }
        let store = await MainActor.run { InventoryStore(demo: false, client: CLIClient(testExecutable: fixture.url)) }
        await MainActor.run { store.plan(action: .uninstall, ids: ["retry-present", "retry-removed"]) }
        await waitForIdle(store)
        let oldPlanID = await MainActor.run { store.preview?.planId }
        await MainActor.run { store.execute() }
        await waitForIdle(store)
        await MainActor.run {
            store.presentPendingResults()
            store.retryResults(ids: ["retry-present", "retry-removed"])
            store.resultsSheetDismissed()
        }
        await waitForIdle(store)
        await MainActor.run {
            XCTAssertNil(store.error)
            XCTAssertNil(store.preview)
            XCTAssertEqual(store.report?.items.map(\.id), ["replacement-record"])
            XCTAssertTrue(store.lastNotice?.contains("未找到这 2 项") == true)
            XCTAssertTrue(store.lastNotice?.contains("上次结果") == true)
            XCTAssertEqual(store.lastResults?.planId, oldPlanID)
            store.showLastResults()
            XCTAssertEqual(store.results?.planId, oldPlanID)
        }
        let calls = try String(contentsOf: fixture.directory.appendingPathComponent("calls"), encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertEqual(calls.filter { $0.hasPrefix("apply ") }.count, 1)
        XCTAssertEqual(calls.filter { $0.hasPrefix("scan") }.count, 2)
        XCTAssertEqual(calls.filter { $0.hasPrefix("plan ") }.count, 1)
    }

    private func retryScanFixture(action: OperationAction, remainingIDs: [String]) throws -> PrivatePlanFile {
        var initial = DemoData.report
        initial.items = [DemoData.pythonTool("retry-present", "present-tool", "uv"), DemoData.pythonTool("retry-removed", "removed-tool", "uv")]
        var fresh = initial
        fresh.items = initial.items.filter { remainingIDs.contains($0.id) }
        var replacement = initial.items[1]
        replacement.id = "replacement-record"
        replacement.path += "-new-record"
        fresh.items.append(replacement)
        let firstPlan = DemoData.plan(action: action, items: initial.items, cleanup: false)
        let nextPlan = DemoData.plan(action: action, items: fresh.items.filter { remainingIDs.contains($0.id) }, cleanup: false)
        let result = ApplyReport(schema: "keepupdated/apply/v1", planId: firstPlan.planId, startedAt: "", finishedAt: "", results: [
            .init(id: "retry-present", name: "Present Tool", status: "failed", changed: false),
            .init(id: "retry-removed", name: "Removed Tool", status: action == .uninstall ? "cleanup_failed" : "verification_unknown", changed: true)
        ])
        func quoted<T: Encodable>(_ value: T) throws -> String {
            "'" + String(decoding: try ReportJSON.encode(value), as: UTF8.self).replacingOccurrences(of: "'", with: "'\\''") + "'"
        }
        return try executableFixture("""
        #!/bin/sh
        fixture_dir="$(dirname "$0")"
        printf '%s\\n' "$*" >> "$fixture_dir/calls"
        case "$1" in
        plan)
            if [ -e "$fixture_dir/applied" ]; then
                for argument in "$@"; do
                    if [ "$argument" = retry-removed ]; then printf 'selected ID is absent' >&2; exit 2; fi
                done
                printf '%s' \(try quoted(nextPlan))
            else
                printf '%s' \(try quoted(firstPlan))
            fi
            ;;
        apply)
            touch "$fixture_dir/applied"
            printf '%s' \(try quoted(result))
            ;;
        scan)
            if [ -e "$fixture_dir/scanned" ]; then
                printf '%s' \(try quoted(fresh))
            else
                touch "$fixture_dir/scanned"
                printf '%s' \(try quoted(initial))
            fi
            ;;
        *) exit 2 ;;
        esac
        """)
    }
}
