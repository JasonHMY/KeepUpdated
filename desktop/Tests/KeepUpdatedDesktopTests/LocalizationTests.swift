import XCTest
import AppKit
@testable import KeepUpdatedDesktop

final class LocalizationTests: XCTestCase {
    private var previousDefaults: UserDefaults!
    private var suite: String!
    override func setUp() {
        super.setUp()
        previousDefaults = L10n.defaults
        suite = "KeepUpdatedLocalizationTests." + UUID().uuidString
        L10n.defaults = UserDefaults(suiteName: suite)!
        L10n.defaults.set("en", forKey: L10n.preferenceKey)
    }
    override func tearDown() {
        L10n.defaults.removePersistentDomain(forName: suite)
        L10n.defaults = previousDefaults
        super.tearDown()
    }

    @MainActor
    func testManualReviewKeepsDistinctStatusAndNeutralStoreNavigationInBothLanguages() {
        let store = InventoryStore(demo: true)
        var report = DemoData.report
        report.items = ["available", "current", "unknown", "error"].map { status in
            var item = DemoData.appStoreItem
            item.id = status
            item.update.status = status
            if status == "unknown" { item.update.code = "mac_store_not_queried" }
            if status == "error" { item.update.code = "timeout" }
            return item
        }
        store.replaceReport(report)
        store.plan(action: .update, all: true)
        for language in ["en", "zh-Hans"] {
            L10n.defaults.set(language, forKey: L10n.preferenceKey)
            XCTAssertEqual(Set(store.manualUpdates.map(\.statusLabel)).count, 4)
            for item in store.manualUpdates {
                XCTAssertEqual(item.appStoreActionTitle, item.canUpdate ? L10n.text("Open App Store to Update") : L10n.text("Open in App Store"))
                XCTAssertNotNil(item.appStoreURL)
                XCTAssertFalse(item.statusExplanation.isEmpty)
            }
            XCTAssertEqual(store.lastNotice, L10n.text("Review these apps below. Any available updates must be completed in App Store or the app."))
            XCTAssertNil(store.preview)
            XCTAssertNil(store.results)
            XCTAssertFalse(store.busy)
        }
    }

    func testAboutIdentityChangesLanguageWithoutInventingBuildValues() {
        let info = AboutInfo(info: ["CFBundleShortVersionString": "0.1.0", "CFBundleVersion": "8"],
            metadata: Data(#"{"source_commit":"abc123","source_tree_sha256":"tree","working_tree_dirty":true}"#.utf8))
        XCTAssertEqual(info.versionText, "Version 0.1.0 · Build 8")
        XCTAssertEqual(info.sourceText, "Source: abc123 (local changes)")
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        XCTAssertEqual(info.versionText, "版本 0.1.0 · 构建 8")
        XCTAssertEqual(info.sourceText, "源码：abc123（含本地修改）")
        XCTAssertEqual(AboutInfo(info: [:], metadata: nil).sourceText, "源码信息不可用")
    }

    func testCurrentScannerCoverageDetailsHaveBothTranslations() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let scanner = try String(contentsOf: root.appendingPathComponent("internal/audit/scanner.go"))
        let pattern = try NSRegularExpression(pattern: #"\{"([a-z-]+)", "partial", "([^"]+)"\}"#)
        let matches = pattern.matches(in: scanner, range: NSRange(scanner.startIndex..., in: scanner))
        XCTAssertFalse(matches.isEmpty)
        for match in matches {
            let adapter = (scanner as NSString).substring(with: match.range(at: 1))
            let detail = (scanner as NSString).substring(with: match.range(at: 2))
            for language in [AppLanguage.english, .simplifiedChinese] {
                XCTAssertNotNil(L10n.catalog(language)[detail], adapter)
                L10n.defaults.set(language.rawValue, forKey: L10n.preferenceKey)
                XCTAssertEqual(CoverageText.describe(.init(adapter: adapter, level: "partial", detail: detail)), L10n.catalog(language)[detail])
            }
        }
    }

    func testLanguageResolutionAndUnsupportedLanguageFallback() {
        for language in ["zh-Hans", "zh-Hans-CN", "zh_CN", "zh-SG", "zh"] {
            XCTAssertEqual(AppLanguage.system.resolved(preferredLanguages: [language]), .simplifiedChinese)
        }
        for language in ["en-AU", "fr-FR", "ja-JP", "zh-Hant", "zh-TW", "unknown"] {
            XCTAssertEqual(AppLanguage.system.resolved(preferredLanguages: [language, "zh-Hans"]), .english)
        }
        XCTAssertEqual(AppLanguage.system.resolved(preferredLanguages: []), .english)
        XCTAssertEqual(AppLanguage.english.resolved(preferredLanguages: ["zh-Hans"]), .english)
        XCTAssertEqual(AppLanguage.simplifiedChinese.resolved(preferredLanguages: ["fr"]), .simplifiedChinese)
        L10n.defaults.set("unrecognized", forKey: L10n.preferenceKey)
        XCTAssertEqual(L10n.language, .english)
    }

    func testPythonReceiptReasonsAreLocalized() {
        let codes = ["python_receipt_schema_unsupported", "python_backend_unsupported", "python_injected_tools_require_review", "python_locked_tool_requires_review", "python_entrypoints_require_review", "python_interpreter_unverified", "python_requirement_constraints_require_review", "python_receipt_constraints_require_review", "python_requirement_installation_mismatch", "python_manager_capability_unsupported", "python_policy_requires_new_preview", "python_target_outside_constraints", "python_receipt_backup_unavailable", "python_receipt_restore_unverified", "python_configuration_requires_review"]
        for code in codes {
            L10n.defaults.set("en", forKey: L10n.preferenceKey)
            let english = ReasonText.describe(code)
            XCTAssertFalse(english.contains(code), code)
            XCTAssertTrue(english.contains("Python") || english.contains("pipx") || english.contains("tool") || english.contains("environment"), code)
            L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
            let chinese = ReasonText.describe(code)
            XCTAssertNotEqual(chinese, english, code)
            XCTAssertFalse(chinese.contains(code), code)
        }
    }

    func testSelectionPersistsAndSystemRemainsAnExplicitChoice() {
        let settings = LanguageSettings(defaults: L10n.defaults)
        XCTAssertEqual(settings.selection, .english)
        settings.selection = .simplifiedChinese
        let reopened = LanguageSettings(defaults: UserDefaults(suiteName: suite)!)
        XCTAssertEqual(reopened.selection, .simplifiedChinese)
        XCTAssertEqual(L10n.text("Check for Updates"), "检查更新")
        reopened.selection = .system
        XCTAssertEqual(LanguageSettings(defaults: L10n.defaults).selection, .system)
    }

    func testCatalogsHaveMatchingKeysAndFormatArguments() throws {
        let english = L10n.catalog(.english), chinese = L10n.catalog(.simplifiedChinese)
        let resources = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("Sources/KeepUpdatedDesktop/Resources")
        let keyPattern = try NSRegularExpression(pattern: #"^"((?:[^"\\]|\\.)*)"\s*="# , options: .anchorsMatchLines)
        for language in ["en", "zh-Hans"] {
            let source = try String(contentsOf: resources.appendingPathComponent(language + ".lproj/Localizable.strings"))
            let keys = keyPattern.matches(in: source, range: NSRange(source.startIndex..., in: source)).map { (source as NSString).substring(with: $0.range(at: 1)) }
            XCTAssertGreaterThan(keys.count, 600)
            XCTAssertEqual(keys.count, Set(keys).count, "Duplicate keys silently hide translations")
        }
        XCTAssertGreaterThan(english.count, 600)
        XCTAssertEqual(Set(english.keys), Set(chinese.keys))
        let placeholders = try NSRegularExpression(pattern: "%([0-9]+\\$)?@")
        for (key, value) in english {
            XCTAssertFalse(value.isEmpty, key)
            XCTAssertEqual(key, value, "English keys are readable fallback text")
            let translated = try XCTUnwrap(chinese[key])
            XCTAssertFalse(translated.isEmpty, key)
            func count(_ text: String) -> Int { placeholders.numberOfMatches(in: text, range: NSRange(text.startIndex..., in: text)) }
            XCTAssertEqual(count(value), count(translated), key)
            let args = (0..<count(value)).map { "ARG\($0)" }
            let output = L10n.format(key, arguments: args, language: .simplifiedChinese)
            for arg in args { XCTAssertTrue(output.contains(arg), key) }
            XCTAssertFalse(output.contains("%@"), key)
        }
        XCTAssertEqual(L10n.format("Selected: %@", arguments: ["0"], language: .english), "Selected: 0")
        XCTAssertEqual(L10n.format("Select %@", arguments: ["工具 100%"], language: .simplifiedChinese), "选择 工具 100%")
    }

    func testCacheFolderKeepsStatusInItemDetails() throws {
        let cache = try XCTUnwrap(DemoData.report.items.first { $0.pluginCache != nil })
        var old = cache; old.id = "old-cache"; old.pluginCache?.registration = "unregistered"
        let unknownDetails = try XCTUnwrap(InventoryTree.build([cache]).first)
        XCTAssertTrue(unknownDetails.isTechnicalDetail)
        let unknown = try XCTUnwrap(unknownDetails.children.first)
        let unregistered = try XCTUnwrap(InventoryTree.build([old]).first?.children.first)
        XCTAssertFalse(unknown.displayTitle.contains("registration"))
        XCTAssertEqual(unknown.items.first?.statusLabel, "Registration unverified")
        XCTAssertFalse(unregistered.displayTitle.contains("Unregistered"))
        XCTAssertEqual(unregistered.items.first?.statusLabel, "Unregistered cache")
        let originalID = unknown.id
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        XCTAssertFalse(unknown.displayTitle.contains("注册"))
        XCTAssertEqual(unknown.items.first?.statusLabel, "注册状态未验证")
        XCTAssertFalse(unregistered.displayTitle.contains("未注册"))
        XCTAssertEqual(unregistered.items.first?.statusLabel, "未注册缓存")
        XCTAssertEqual(unknown.id, originalID)
    }

    func testMissingTranslationFallsBackToEnglishThenReadableKey() {
        XCTAssertEqual(L10n.translation("key", selected: [:], english: ["key": "English fallback"]), "English fallback")
        XCTAssertEqual(L10n.translation("Readable key", selected: [:], english: [:]), "Readable key")
        XCTAssertEqual(L10n.translation("key", selected: ["key": "中文"], english: ["key": "English"]), "中文")
    }

    func testEveryPresentationKeyHasBothResources() throws {
        let directory = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("Sources/KeepUpdatedDesktop")
        let pattern = try NSRegularExpression(pattern: #"(?:L10n\.text|LocalizedMessage|ClientError\.message)\("((?:[^"\\]|\\.)*)""#)
        let english = L10n.catalog(.english), chinese = L10n.catalog(.simplifiedChinese)
        for url in try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) where url.pathExtension == "swift" {
            let source = try String(contentsOf: url)
            for match in pattern.matches(in: source, range: NSRange(source.startIndex..., in: source)) {
                let literal = (source as NSString).substring(with: match.range(at: 1))
                let key = try JSONDecoder().decode(String.self, from: Data(("\"" + literal + "\"").utf8))
                if key.isEmpty { continue }
                XCTAssertNotNil(english[key], "\(url.lastPathComponent): \(key)")
                XCTAssertNotNil(chinese[key], "\(url.lastPathComponent): \(key)")
            }
        }
        for category in ItemCategory.allCases {
            if ![.skills, .mcp, .homebrew, .npm, .pnpm, .bun, .python].contains(category) { XCTAssertNotNil(chinese[category.rawValue]) }
        }
    }

    @MainActor func testExistingNoticesAndModelReasonsChangeLanguageWithoutChangingCodes() async throws {
        let store = InventoryStore(demo: true)
        store.refresh(online: false)
        for _ in 0..<200 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertFalse(store.busy)
        XCTAssertEqual(store.lastNotice, "Demo data refreshed. Your computer was not read or changed.")
        let item = DemoData.item("sample", "Example", "application", "standalone", "unknown", code: "online_query_required")
        XCTAssertEqual(item.statusLabel, "Not checked for updates")
        let message = LocalizedMessage("Preparing %@ preview…", localizedArgument: "Update")
        XCTAssertEqual(message.text, "Preparing Update preview…")
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        XCTAssertEqual(store.lastNotice, "演示数据已刷新，未读取或修改电脑。")
        XCTAssertEqual(item.statusLabel, "未检查更新")
        XCTAssertEqual(message.text, "正在生成更新清单…")
        XCTAssertEqual(item.update.code, "online_query_required")
        XCTAssertEqual(ReasonText.technicalDetail("future_machine_code"), "future_machine_code")
        XCTAssertFalse(ReasonText.describe("future_machine_code").contains("future_machine_code"))
        let rawDiagnostic = "Unknown backend message: /Example/data"
        XCTAssertEqual(ReasonText.technicalDetail(rawDiagnostic), rawDiagnostic)
        XCTAssertFalse(ReasonText.describe(rawDiagnostic).contains("Unknown backend"))
    }
    @MainActor func testNativeMenuLabelsPreserveActionsAndSurviveTitleValidation() {
        let menu = NSMenu(title: "Edit")
        let action = NSSelectorFromString("copy:")
        let item = NSMenuItem(title: "Copy", action: action, keyEquivalent: "c")
        menu.addItem(item)
        let display = NSMenuItem(title: "Move to Example Display", action: nil, keyEquivalent: "")
        menu.addItem(display)
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        MenuLocalization.translate(menu)
        XCTAssertEqual(menu.title, "编辑")
        XCTAssertEqual(item.attributedTitle?.string, "拷贝")
        XCTAssertEqual(item.accessibilityTitle(), "拷贝")
        XCTAssertEqual(item.action, action)
        XCTAssertEqual(item.keyEquivalent, "c")
        XCTAssertEqual(display.attributedTitle?.string, "移到 Example Display")
        item.title = "Copy" // AppKit may reset the plain title while validating the native action.
        XCTAssertEqual(item.attributedTitle?.string, "拷贝")
        L10n.defaults.set("en", forKey: L10n.preferenceKey)
        MenuLocalization.translate(menu)
        XCTAssertEqual(menu.title, "Edit")
        XCTAssertEqual(item.attributedTitle?.string, "Copy")
        XCTAssertEqual(item.accessibilityTitle(), "Copy")
        XCTAssertEqual(display.attributedTitle?.string, "Move to Example Display")
    }

    func testRegisteredPluginPreviewAndProvenanceDoNotClaimLoadedOrLatestVersion() throws {
        let data = Data(#"{"schema":"keepupdated/v1","command":"scan","generated_at":"","items":[{"id":"plugin-skill","kind":"skill","name":"Example Skill","path":"/Example/plugin/skills/demo/SKILL.md","owner":"claude-plugin","source":"plugin-cache","channel":"plugin","installed_version":"1.0.0","evidence":[],"health":{"status":"ok","code":"readable"},"update":{"status":"unsupported","code":"plugin_owner_candidate_required"},"plugin":{"client":"claude","id":"example@market","scope":"user","root":"/Example/plugin","version":"1.0.0","commit":"old-commit"}}],"diagnostics":[],"coverage":[]}"#.utf8)
        let report = try ReportJSON.decode(InventoryReport.self, from: data, schema: "keepupdated/v1")
        let item = try XCTUnwrap(report.items.first)
        XCTAssertTrue(item.hasManagedUpdatePreview)
        XCTAssertFalse(item.canUpdate)
        XCTAssertNotEqual(item.activityKnown, true)
        XCTAssertEqual(item.plugin?.version, "1.0.0")
        XCTAssertEqual(item.statusLabel, "Managed by plugin")
        var preview = PluginUpdatePreview(owner: try XCTUnwrap(item.plugin), marketplace: "/Example/market", entry: "example", commit: "approved-commit", skills: ["skills/demo/SKILL.md"], reloadRequired: true)
        XCTAssertTrue(preview.provenance.contains("Cached marketplace snapshot"))
        XCTAssertFalse(preview.provenance.lowercased().contains("latest"))
        preview.remoteVerified = true
        preview.refreshMarketplace = true
        XCTAssertTrue(preview.provenance.contains("approved commit"))
        XCTAssertFalse(preview.provenance.lowercased().contains("latest"))
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        XCTAssertTrue(preview.provenance.contains("已验证远端快照"))
        XCTAssertTrue(PluginUpdatePreview.reloadInstructions.contains("/reload-plugins"))
        XCTAssertTrue(PluginUpdatePreview.reloadInstructions.contains("尚未确认"))
        let result = try ReportJSON.decode(ApplyReport.self, from: Data(#"{"schema":"keepupdated/apply/v1","plan_id":"p","started_at":"","finished_at":"","results":[{"id":"plugin-skill","name":"Example Skill","status":"updated","reason":"plugin_snapshot_installed_reload_required","after_version":"1.1.0","reload_required":true}]}"#.utf8), schema: "keepupdated/apply/v1")
        XCTAssertEqual(result.completed.first?.afterVersion, "1.1.0")
        XCTAssertEqual(result.completed.first?.reloadRequired, true)
    }

    @MainActor func testRegisteredPluginSkillEntersPreviewWithoutBecomingAnAvailableUpdate() {
        var item = DemoData.item("plugin-skill", "Example Skill", "skill", "claude-plugin", "unsupported", code: "plugin_owner_candidate_required")
        item.source = "plugin-cache"
        item.plugin = .init(client: "claude", id: "example@market", scope: "user", root: "/Example/plugin", version: "1.0.0")
        let store = InventoryStore(demo: true)
        store.replaceReport(.init(schema: "keepupdated/v1", command: "scan", generatedAt: "", items: [item], diagnostics: [], coverage: []))
        XCTAssertTrue(store.canPreviewUpdate(item))
        store.setSelected(item.id, selected: true)
        XCTAssertEqual(store.selectedIDs, [item.id])
        XCTAssertEqual(store.updateCount, 0)
        XCTAssertFalse(item.canUpdate)
    }

    func testRuntimeImpactAndMarketplaceSideEffectsRemainDistinct() throws {
        let planData = Data(#"{"schema":"keepupdated/plan/v1","plan_id":"p","created_at":"","expires_at":"","action":"update","items":[{"id":"runtime","name":"node","status":"manual","required_consents":["runtime_update"],"requires_scripts":true,"runtime_review":{"owner":"homebrew","current_version":"24.1.0","target_version":"24.2.0","compatibility":"same_major","consumers":[{"id":"tool","name":"Example Tool","version":"1.2.0","path":"/Example/tool","fingerprint":"opaque"},{"name":"unresolved-consumer"}],"unknown_impact":["Project scripts, external applications and undiscovered consumers cannot be fully determined.","Installed consumers are listed for review; they are not authorized for update or rebuild."]}}]}"#.utf8)
        let plan = try ReportJSON.decode(OperationPlan.self, from: planData, schema: "keepupdated/plan/v1")
        let review = try XCTUnwrap(plan.items.first?.runtimeReview)
        XCTAssertEqual(review.currentVersion, "24.1.0")
        XCTAssertEqual(review.targetVersion, "24.2.0")
        XCTAssertEqual(review.consumers.first?.path, "/Example/tool")
        XCTAssertEqual(review.consumers.first?.version, "1.2.0")
        XCTAssertNil(review.consumers.last?.version)
        XCTAssertEqual(review.unknownImpact.count, 2)
        XCTAssertTrue(review.compatibilityLabel.contains("still needs review"))
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        XCTAssertTrue(review.compatibilityLabel.contains("仍需核对"))
        for note in review.unknownImpact { XCTAssertNotEqual(L10n.text(note), note) }
        L10n.defaults.set("en", forKey: L10n.preferenceKey)
        let data = Data(#"{"schema":"keepupdated/apply/v1","plan_id":"p","started_at":"","finished_at":"","results":[{"id":"plugin","name":"Example","status":"failed","changed":false,"marketplace_refresh":{"attempted":true,"command_succeeded":true,"state_known":true,"changed":true,"before_commit":"before","after_commit":"after","reason":"plugin_marketplace_refresh_stopped_install"}}]}"#.utf8)
        let result = try ReportJSON.decode(ApplyReport.self, from: data, schema: "keepupdated/apply/v1")
        XCTAssertTrue(result.completed.isEmpty)
        XCTAssertEqual(result.failed.count, 1)
        var refresh = try XCTUnwrap(result.failed.first?.marketplaceRefresh)
        XCTAssertFalse(result.failed.first?.changed == true)
        XCTAssertTrue(refresh.changed)
        XCTAssertEqual(refresh.afterCommit, "after")
        XCTAssertEqual(refresh.stateLabel, "Marketplace metadata changed")
        refresh.stateKnown = false; refresh.changed = false
        XCTAssertEqual(refresh.stateLabel, "Marketplace refresh state unverified")
        refresh.attempted = false
        XCTAssertEqual(refresh.stateLabel, "Marketplace refresh not attempted")
    }

    func testNodePolicyReasonsHaveReadableBilingualMappings() {
        let codes = ["node_runtime_selection_requires_review", "node_manager_layout_unsupported", "node_selector_requires_review", "node_selector_installation_mismatch", "node_selector_target_mismatch", "node_policy_requires_new_preview"]
        for code in codes {
            L10n.defaults.set("en", forKey: L10n.preferenceKey)
            let english = ReasonText.describe(code)
            XCTAssertNil(ReasonText.technicalDetail(code), code)
            L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
            XCTAssertNotEqual(ReasonText.describe(code), english, code)
        }
    }

    func testNormalProviderReasonsHaveReadableBilingualMappings() {
        let codes = ["application_update_cancelled", "signed_application_dmg_ready", "signed_application_dmg_verified", "application_archive_format_changed", "application_dmg_mount_unverified", "application_dmg_detach_failed", "application_dmg_multiple_volumes", "application_dmg_payload_unavailable", "application_dmg_single_bundle_required", "application_dmg_unknown_payload", "application_dmg_package_unsupported", "application_dmg_copy_mismatch", "application_dmg_limit", "application_dmg_duplicate_path", "application_dmg_unsafe_path", "application_dmg_escaping_symlink", "application_dmg_special_file", "signed_application_zip_ready", "signed_application_bundle_verified", "application_feed_unavailable", "application_signed_full_zip_required", "application_archive_signature_invalid", "runtime_breaking_version_requires_manual_review", "native_cli_original_source_verified", "native_cli_failed_original_restored", "owner_unavailable", "owner_query_timeout", "owner_network_unavailable", "owner_permission_denied", "app_store_mas_missing", "app_store_mas_version_unsupported", "app_store_requested_version_unverified", "app_store_scripts_consent_required"]
        for code in codes {
            L10n.defaults.set("en", forKey: L10n.preferenceKey)
            let english = ReasonText.describe(code)
            XCTAssertNil(ReasonText.technicalDetail(code), code)
            XCTAssertNotEqual(english, code)
            L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
            XCTAssertNotEqual(ReasonText.describe(code), english, code)
        }
        L10n.defaults.set("en", forKey: L10n.preferenceKey)
        XCTAssertTrue(ReasonText.describe("native_cli_original_source_verified").contains("Confirm to download and verify"))
    }

    func testNativeRestartPurposeHasBothLocalizedResources() throws {
        for language in ["en", "zh-Hans"] {
            let url = try XCTUnwrap(L10n.resources.url(forResource: "InfoPlist", withExtension: "strings", subdirectory: nil, localization: language))
            let table = try XCTUnwrap(PropertyListSerialization.propertyList(from: Data(contentsOf: url), format: nil) as? [String: String])
            let purpose = try XCTUnwrap(table["NSAppleEventsUsageDescription"])
            XCTAssertTrue(purpose.contains(language == "en" ? "separate restart operation" : "单独批准重启"))
        }
    }

}

extension LocalizationTests {
    func testSkillshareProvenanceIsReadOnlyAndLocalized() throws {
        let examples = DemoData.skillshareExamples
        XCTAssertEqual(examples.count, 4)
        for item in examples {
            XCTAssertEqual(item.statusLabel, item.update.code == "skillshare_metadata_unverified" ? "Skillshare record needs review" : "Managed by Skillshare")
            XCTAssertFalse(item.canUpdate)
            XCTAssertFalse(item.statusExplanation.contains("Up to date"))
            XCTAssertTrue(item.statusExplanation.contains("Skillshare"))
            XCTAssertEqual(item.skillManager?.repository, "example/skills")
        }
        var conflict = examples[0]
        conflict.skillManager?.code = "skill_source_conflict"
        conflict.update.code = "skill_source_conflict"
        XCTAssertEqual(conflict.statusExplanation, "This Skill has conflicting source records. Review its original installation records first.")
        let preview = DemoData.plan(action: .update, items: examples, cleanup: false, special: false)
        XCTAssertTrue(preview.items.allSatisfy { $0.status == "blocked" && $0.targetVersion == nil })
        let encoder = JSONEncoder(); encoder.keyEncodingStrategy = .convertToSnakeCase
        let decoder = JSONDecoder(); decoder.keyDecodingStrategy = .convertFromSnakeCase
        let encoded = try encoder.encode(examples[0])
        XCTAssertTrue(String(decoding: encoded, as: UTF8.self).contains("skill_manager"))
        let decoded = try decoder.decode(InventoryItem.self, from: encoded)
        XCTAssertEqual(decoded.skillManager, examples[0].skillManager)
        var oldReportItem = examples[0]; oldReportItem.skillManager = nil
        XCTAssertNil(try decoder.decode(InventoryItem.self, from: encoder.encode(oldReportItem)).skillManager)
        L10n.defaults.set("zh-Hans", forKey: L10n.preferenceKey)
        for item in examples {
            XCTAssertEqual(item.statusLabel, item.update.code == "skillshare_metadata_unverified" ? "Skillshare 记录需核对" : "由 Skillshare 管理")
            XCTAssertTrue(item.statusExplanation.contains("Skillshare"))
            XCTAssertNotEqual(item.statusExplanation, L10n.catalog(.english)[item.statusExplanation])
        }
        XCTAssertEqual(conflict.statusExplanation, "同一个 Skill 对应多份不同来源记录，需要先核对原安装记录。")
        XCTAssertTrue(examples[0].statusExplanation.contains("尚未检查远端"))
        XCTAssertTrue(examples[1].statusExplanation.contains("新增、删除或修改"))
        XCTAssertTrue(examples[2].statusExplanation.contains("缺少完整的文件基线"))
        XCTAssertTrue(examples[3].statusExplanation.contains("无法验证"))
        XCTAssertEqual(L10n.text("Canonical Skill directory"), "Skill 实际目录")
    }
}
