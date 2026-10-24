import SwiftUI
import Combine

struct PlanSheet: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @ObservedObject private var language = LanguageSettings.shared
    @ObservedObject var store: InventoryStore
    @State private var now = Date()
    private let timer = Timer.publish(every: 1, on: .main, in: .common).autoconnect()
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text(L10n.text("Confirm %@ Preview", String(describing: store.preview?.displayActionTitle ?? L10n.text("Operation")))).font(.title2.weight(.semibold))
                if store.demo { Text(L10n.text("Demo Mode")).foregroundStyle(.orange) }
                Spacer()
            }
            if let plan = store.preview {
                if plan.action == .update && !store.manualUpdates.isEmpty { ManualUpdatesView(store: store) }
                if let notice = store.previewNotice {
                    Text(notice).font(.callout).foregroundStyle(.orange)
                }
                Text(plan.selection?.hasSpecificReview == true ? L10n.text("This is a separate preview for special actions. Review installation scripts, permissions and conditions before confirming.") : (plan.action == .update ? L10n.text("Ordinary updates run first. Extra permissions, app exit, restart and installation scripts need separate confirmation.") : L10n.text("Uninstalling removes the contents listed below. Review the items and affected paths.")))
                    .foregroundStyle(.secondary)
                if let request = plan.selection?.privilegedRequest, plan.items.allSatisfy({ $0.privilegedRequest == nil }) {
                    PrivilegedRequestView(request: request)
                }
                if plan.action == .uninstall {
                    Text(L10n.text("Selected entries: %@ · Uninstall actions: %@. Review unavailable items below.",
                        String(plan.items.count), String(plan.executable.count)))
                        .font(.callout)
                    Toggle(L10n.text("Also clean configuration and personal data"), isOn: Binding(get: { store.cleanup }, set: { store.changeCleanup($0) }))
                        .disabled(store.busy)
                    Text(store.cleanup ? L10n.text("Only explicitly listed paths will be cleaned. Data with unknown ownership will be preserved.") : L10n.text("Configuration and personal data will be preserved."))
                        .font(.caption).foregroundStyle(.secondary)
                }
                if plan.items.contains(where: { $0.requiresAdmin == true }) {
                    Button(L10n.text("Update Permissions…")) { store.showPrivileges = true }.disabled(store.busy)
                }
                ScrollView {
                    PlanGroupsView(plan: plan, store: store).id(plan.planId)
                }.background(Color.primary.opacity(0.025), in: RoundedRectangle(cornerRadius: 8)).frame(minHeight: 180)
                if !plan.special.isEmpty {
                    Text(L10n.text("Items needing further review: %@. These items will not run yet. Review each item's requirements before creating a new preview.", String(describing: plan.special.count)))
                        .font(.callout).foregroundStyle(.orange)
                }
                if store.busy {
                    if let progress = store.progress { InventoryProgressView(progress: progress) }
                    else { ActivityProgressView(label: store.activity) }
                }
                HStack {
                    if !store.busy { Text(L10n.text("Ready: %@ · Expires at %@", String(describing: plan.executable.count), String(describing: plan.expires?.formatted(.dateTime.hour().minute().locale(L10n.locale)) ?? L10n.text("Unknown")))).font(.caption).foregroundStyle(.secondary) }
                    Spacer()
                    NativeGlassControlGroup(spacing: 8) {
                        HStack(spacing: 8) {
                            Button(store.busy ? L10n.text("Cancel Operation") : L10n.text("Back")) { if store.busy { store.cancel() } else { store.dismissPreview() } }
                                .nativeGlassButton().keyboardShortcut(.cancelAction).disabled(store.applying)
                            Button(store.demo ? L10n.text("Simulate %@", String(describing: plan.displayActionTitle)) : L10n.text("Confirm %@ (%@)", String(describing: plan.displayActionTitle), String(describing: plan.executable.count))) { store.execute() }
                                .nativeGlassButton(prominent: true)
                                .disabled(store.busy || !store.hasConfirmedPlan || plan.executable.isEmpty || (plan.expires ?? .distantPast) <= now)
                        }
                    }
                }
                if (plan.expires ?? .distantPast) <= now { Text(L10n.text("This preview has expired. Go back and create a new one.")).foregroundStyle(.orange).font(.caption) }
            }
        }.padding(24).frame(minWidth: 520, maxWidth: .infinity, minHeight: 400, maxHeight: .infinity, alignment: .topLeading)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: store.busy)
            .animation(reduceMotion ? nil : NativeMotion.selection, value: store.previewNotice)
            .transaction { if reduceMotion { $0.animation = nil; $0.disablesAnimations = true } }
            .onReceive(timer) { now = $0 }
    }
}


private struct PlanGroupsView: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @ObservedObject private var language = LanguageSettings.shared
    let plan: OperationPlan
    @ObservedObject var store: InventoryStore
    @State private var executableExpanded = true
    @State private var attentionExpanded: Bool
    @State private var noOperationExpanded: Bool

    init(plan: OperationPlan, store: InventoryStore) {
        self.plan = plan
        self.store = store
        _attentionExpanded = State(initialValue: plan.action == .uninstall || plan.executable.isEmpty || plan.items.count == 1)
        _noOperationExpanded = State(initialValue: plan.executable.isEmpty)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            DisclosureGroup(L10n.text("Ready (%@)", String(describing: plan.executable.count)), isExpanded: $executableExpanded) {
                rows(plan.executable)
            }
            if !plan.needsAttention.isEmpty {
                DisclosureGroup(L10n.text("Needs Attention (%@)", String(describing: plan.needsAttention.count)), isExpanded: $attentionExpanded) {
                    rows(plan.needsAttention)
                }
            }
            if !plan.sharedReferences.isEmpty {
                Text(L10n.text("Shared Installation References (%@)", String(plan.sharedReferences.count))).font(.subheadline)
                rows(plan.sharedReferences)
            }
            if !plan.noOperation.isEmpty {
                DisclosureGroup(L10n.text("No Action Needed (%@)", String(describing: plan.noOperation.count)), isExpanded: $noOperationExpanded) {
                    rows(plan.noOperation)
                }
            }
        }.padding(12)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: executableExpanded)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: attentionExpanded)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: noOperationExpanded)
    }

    private func rows(_ items: [PlanItem]) -> some View {
        LazyVStack(alignment: .leading, spacing: 0) {
            ForEach(items) { item in
                PlanItemRow(item: item, store: store)
                Divider()
            }
        }
    }
}

private struct PlanItemRow: View {
    @ObservedObject private var language = LanguageSettings.shared
    let item: PlanItem
    @ObservedObject var store: InventoryStore
    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack {
                Text(item.displayName).font(.headline)
                Spacer()
                Text(item.isExecutable(in: store.preview?.selection) ? L10n.text("Ready") : item.statusLabel).foregroundStyle(item.isExecutable(in: store.preview?.selection) ? Color.accentColor : Color.secondary)
            }
            if !["ready", "manual", "blocked", "skipped"].contains(item.status) {
                Text(L10n.text("Status code: %@", item.status)).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
            }
            if let version = item.fromVersion {
                Text(version + (item.targetVersion.map { " → " + $0 } ?? "")).font(.caption).foregroundStyle(.secondary)
            }
            if let reason = item.reason, !reason.isEmpty { ReasonMessage(code: reason) }
            if item.canonicalId != nil {
                if let canonical = store.preview?.canonicalAction(for: item) {
                    Text(L10n.text("Handled by: %@ (%@)", canonical.displayName, canonical.id)).font(.callout).textSelection(.enabled)
                    Text(L10n.text("One shared action is planned. This reference is not a separate update.")).font(.caption).foregroundStyle(.secondary)
                } else {
                    Text(L10n.text("The shared action is unavailable. Create a new preview.")).font(.callout).foregroundStyle(.orange)
                }
            }
            if let skillUpdate = item.skillUpdate {
                SkillChangesView(preview: skillUpdate, executable: item.isExecutable(in: store.preview?.selection))
            }
            if !item.requirements.isEmpty { Text(L10n.text("Requires: %@", item.requirements.joined(separator: L10n.listSeparator))).font(.caption).foregroundStyle(.orange) }
            if let paths = item.affectedPaths, !paths.isEmpty {
                DisclosureGroup(L10n.text("Affected Paths")) {
                    Text(paths.joined(separator: "\n")).font(.caption2).foregroundStyle(.secondary).textSelection(.enabled)
                }.font(.caption)
            }
            if let ids = item.affectedIds, !ids.isEmpty {
                Text(L10n.text("Also affected: %@", store.affectedNames(ids))).font(.caption).foregroundStyle(.orange).textSelection(.enabled)
            }
            if let paths = item.cleanupPaths, !paths.isEmpty {
                Text(L10n.text("Configuration and personal data to clean")).font(.caption).foregroundStyle(.orange)
                Text(paths.joined(separator: "\n")).font(.caption2).textSelection(.enabled)
            }
            if let review = item.runtimeReview { RuntimeReviewView(review: review) }
            if let request = item.privilegedRequest { PrivilegedRequestView(request: request) }
            if let plugin = item.pluginUpdate { PluginUpdatePreviewView(plugin: plugin) }
            if let update = item.binding?.resolvedTarget?.applicationUpdate {
                Text(L10n.text("Verified app candidate: %@ · Build %@", update.archive.version, update.archive.build)).font(.callout)
                Text(L10n.text("Developer: %@ · Architecture: %@", update.teamId, update.architecture)).font(.caption)
                Text(update.bundleId).font(.caption).textSelection(.enabled)
            }
            if let reference = item.mcp?.packageReference {
                Text(L10n.text("Configuration reference only; no server installation"))
                    .font(.callout.weight(.medium))
                Text(reference.before + " → " + reference.after).font(.callout.monospaced()).textSelection(.enabled)
                if let path = item.mcp?.configPath { Text(path).font(.caption).textSelection(.enabled) }
            }
            if let lifecycle = item.applicationLifecycle, !lifecycle.isEmpty {
                ForEach(lifecycle) { app in
                    Text(L10n.text("Approved exit: %@", app.path)).font(.caption)
                    Text(app.reopen == true ? L10n.text("Reopen after verified update: approved") : L10n.text("Reopen after update: not requested")).font(.caption)
                }
            }
            if !item.consentCodes.isEmpty {
                ForEach(item.consentCodes, id: \.self) { code in
                    Text((OperationConsent(rawValue: code)?.title ?? L10n.text("Unknown permission")) + " · " + ((store.preview?.selection?.consents ?? []).contains(code) ? L10n.text("Selected") : L10n.text("Not selected")))
                        .font(.caption).foregroundStyle(.orange)
                }
            }
            if let original = store.inventoryItem(for: item) {
                if original.isAppStoreApplication {
                    Button(original.appStoreActionTitle) { store.openAppStore(original) }
                } else if original.canOpenForManualUpdate {
                    Text(L10n.text("Open App to update yourself")).font(.caption).foregroundStyle(.secondary)
                    Button(L10n.text("Open App")) { store.openManualApplication(original) }.disabled(store.busy)
                }
            }
            if item.needsTypedReview {
                OperationReviewView(item: item, store: store)
            } else if item.requiresSpecialConfirmation && item.specialApproved != true {
                Button(L10n.text("Handle Separately…")) { store.planSpecial(item) }
                    .disabled(store.busy)
                    .accessibilityLabel(L10n.text("Handle %@ separately", String(describing: item.displayName)))
            } else if item.reason == "use_related_cask_target", !(item.affectedIds ?? []).isEmpty {
                Button(L10n.text("View Related Installation…")) { store.planRelated(item) }.disabled(store.busy)
            }
        }.padding(12)
    }
}

struct ResultsSheet: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @ObservedObject private var language = LanguageSettings.shared
    let report: ApplyReport
    @ObservedObject var store: InventoryStore
    private var demo: Bool { store.demo }
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(demo ? L10n.text("Demo Results") : L10n.text("Operation Results")).font(.title2.weight(.semibold))
            Text(demo ? L10n.text("Demo mode uses synthetic data. Updates and uninstalls never call the real CLI.") : L10n.text("Review each recorded result below. Unresolved items need a fresh check and preview."))
                .foregroundStyle(.secondary)
            Text(L10n.text("Completed: %@ · Failed or unverified: %@ · Not executed: %@", String(describing: report.completed.count), String(describing: report.failed.count), String(describing: report.notExecuted.count)))
                .font(.callout.weight(.medium))
            if report.isUnfinished {
                Text(L10n.text("This operation may still be running or may have been interrupted. The saved results do not confirm that its processes have stopped. Check the current installation state before creating a new preview."))
                    .font(.callout).foregroundStyle(.orange)
            }
            if !report.awaitingRestart.isEmpty {
                Text(L10n.text("Awaiting system restart: %@. Installation and restart are separate results.", String(report.awaitingRestart.count)))
                    .font(.callout).foregroundStyle(.orange)
            }
            if store.busy {
                if let progress = store.progress { InventoryProgressView(progress: progress) }
                else { ActivityProgressView(label: store.activity) }
            }
            if !demo {
                Button(L10n.text("Reload Saved Results")) { store.reloadLastResults() }.disabled(store.busy)
            }
            if !report.retryable.isEmpty {
                Button(L10n.text("Check & Preview Unresolved Items (%@)", String(describing: report.retryable.count))) { store.retryResults(ids: report.retryable.map(\.id)) }
                    .nativeGlassButton().disabled(store.busy)
            }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    ForEach(report.results) { result in
                        ResultItemRow(result: result, report: report, store: store)
                        Divider()
                    }
                }
            }.background(Color.primary.opacity(0.025), in: RoundedRectangle(cornerRadius: 8))
            HStack { Spacer(); Button(L10n.text("Done")) { if let onClose { onClose() } else { dismiss() } }.nativeGlassButton(prominent: true).keyboardShortcut(.defaultAction) }
        }.padding(24).frame(minWidth: 480, maxWidth: .infinity, minHeight: 360, maxHeight: .infinity, alignment: .topLeading)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: store.busy)
            .transaction { if reduceMotion { $0.animation = nil; $0.disablesAnimations = true } }
    }

}

private struct ResultItemRow: View {
    let result: ApplyReport.ApplyResult
    let report: ApplyReport
    @ObservedObject var store: InventoryStore
    @ObservedObject private var language = LanguageSettings.shared
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack { Text(result.displayName).font(.headline); Spacer(); Text(result.reason == "mcp_package_reference_changed" ? L10n.text("Configuration updated") : status(result.status)).foregroundStyle(.secondary) }
            if !["success", "updated", "uninstalled", "succeeded", "configured", "skipped", "error", "failed", "blocked", "manual", "verification_unknown", "cleanup_failed", "awaiting_restart"].contains(result.status) {
                Text(L10n.text("Status code: %@", result.status)).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
            }
            if let reason = result.reason { ReasonMessage(code: reason) }
            if result.canonicalId != nil {
                if let canonical = report.canonicalResult(for: result) {
                    Text(L10n.text("Shared action: %@ (%@) · %@", canonical.displayName, canonical.id, status(canonical.status))).font(.callout).textSelection(.enabled)
                    if let reason = canonical.reason { ReasonMessage(code: reason) }
                    Text(L10n.text("This reference was not executed or verified separately.")).font(.caption).foregroundStyle(.secondary)
                } else {
                    Text(L10n.text("The shared action result is unavailable. Check again before retrying.")).font(.callout).foregroundStyle(.orange)
                }
            }
            if let refresh = result.marketplaceRefresh { MarketplaceRefreshView(refresh: refresh) }
            if result.reloadRequired == true {
                Text(PluginUpdatePreview.reloadInstructions).font(.callout).foregroundStyle(.orange)
            }
            if let lifecycle = result.applicationLifecycle {
                ForEach(lifecycle) { app in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(app.path).font(.caption).textSelection(.enabled)
                        Text(L10n.text("App exit: %@", ApplicationLifecycleResult.label(app.quitStatus))).font(.callout)
                        ReasonMessage(code: app.quitReason)
                        Text(L10n.text("App reopen: %@", ApplicationLifecycleResult.label(app.reopenStatus))).font(.callout)
                        ReasonMessage(code: app.reopenReason)
                    }.padding(.top, 6)
                }
            }
            if let after = result.afterVersion { Text(L10n.text("Current version: %@", after)).font(.caption).foregroundStyle(.secondary) }
            if result.needsReview {
                Button(L10n.text("Check & Preview Again")) { store.retryResults(ids: [result.id]) }
                    .disabled(store.busy).accessibilityLabel(L10n.text("Check and preview %@ again", String(describing: result.displayName)))
            }
            if let changes = result.relatedChanges, !changes.isEmpty {
                Text(L10n.text("Actual Related Changes (%@)", String(describing: changes.count))).font(.subheadline.weight(.semibold)).padding(.top, 6)
                ForEach(changes) { change in
                    VStack(alignment: .leading, spacing: 3) {
                        Text(change.name + " · " + change.statusLabel).font(.callout)
                        if let version = change.versionChange { Text(version).font(.caption).foregroundStyle(.secondary) }
                        if !["added", "version_changed", "not_observed_after"].contains(change.status) {
                            Text(L10n.text("Status code: %@", change.status)).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }
                }
            }
            if let operation = result.privilegedOperationId {
                Text(L10n.text("Operation ID: %@", operation)).font(.caption.monospaced()).textSelection(.enabled)
                Text(L10n.text("Read the recovery status before trying this privileged operation again."))
                    .font(.caption).foregroundStyle(.orange)
                Button(L10n.text("Check Operation Status")) { store.checkPrivilegedOperation(operation) }
                    .disabled(store.busy || operation.range(of: "^[0-9a-f]{64}$", options: .regularExpression) == nil)
                if let status = store.operationStatuses[operation] {
                    Text(L10n.text("Queried recovery status")).font(.subheadline.weight(.semibold))
                    Text(status.title).font(.callout)
                    ReasonMessage(code: status.reasonCode)
                    if let version = status.observedVersion { Text(L10n.text("Observed version: %@", version)).font(.caption) }
                    if let build = status.observedBuild { Text(L10n.text("Observed build: %@", build)).font(.caption) }
                    if store.canPreviewRestart(operation) {
                        Button(L10n.text("Preview Restart for This Operation")) { store.previewRestart(operation) }
                            .disabled(store.busy)
                    }
                    if let backup = status.backupPath { Text(backup).font(.caption).textSelection(.enabled) }
                    if let recovery = status.recoveryPath { Text(recovery).font(.caption).textSelection(.enabled) }
                }
            }
            if let paths = result.recoveryPaths {
                ForEach(paths, id: \.self) { path in Text(L10n.text("Recovery location: %@", path)).font(.caption).textSelection(.enabled) }
            }
            if let recovery = result.recoveryPath {
                Text(L10n.text("Recovery record: %@", recovery)).font(.caption).textSelection(.enabled)
            }
            if let backup = result.backupPath, !backup.isEmpty {
                Text(L10n.text("Backup Location")).font(.subheadline.weight(.semibold)).padding(.top, 6)
                Text(backup).font(.caption).textSelection(.enabled)
            }
        }.padding(12)
    }
    private func status(_ value: String) -> String {
        switch value {
        case "success", "updated", "uninstalled", "succeeded": return L10n.text("Completed")
        case "skipped": return L10n.text("Skipped")
        case "error", "failed": return L10n.text("Failed")
        case "blocked": return L10n.text("Not executed")
        case "manual": return L10n.text("Manual action needed")
        case "verification_unknown": return L10n.text("Result unverified")
        case "configured": return L10n.text("Configuration updated")
        case "awaiting_restart": return L10n.text("Awaiting system restart")
        case "cleanup_failed": return L10n.text("Post-uninstall cleanup incomplete")
        default: return L10n.text("Status unverified")
        }
    }
}

private struct SkillChangesView: View {
    @ObservedObject private var language = LanguageSettings.shared
    let preview: SkillUpdatePreview
    let executable: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(executable ? L10n.text("After confirmation, the fixed version in this preview will be installed with a backup saved first.") : L10n.text("Review the differences below first. This item will not run; existing content will be preserved."))
                .font(.caption).foregroundStyle(.secondary)
            if preview.changes.isEmpty {
                Text(L10n.text("No file differences are available. Read the check explanation and create a new preview if needed."))
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                Text(L10n.text("File Changes (%@)", String(describing: preview.changes.count))).font(.callout.weight(.medium))
                Text(preview.changeSummary).font(.caption).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(preview.changes) { change in
                        SkillFileChangeView(change: change)
                    }
                }.frame(maxWidth: .infinity, alignment: .leading).padding(.top, 4)
            }
        }.padding(.vertical, 5)
    }
}

private struct SkillFileChangeView: View {
    @ObservedObject private var language = LanguageSettings.shared
    let change: SkillFileChange
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(change.kindLabel).font(.caption.weight(.medium)).foregroundStyle(change.kind == "deleted" ? Color.orange : Color.secondary)
                Text(change.path).font(.callout.monospaced()).textSelection(.enabled)
            }
            if let size = change.sizeChange { Text(size).font(.caption).foregroundStyle(.secondary) }
            if let mode = change.modeChange { Text(L10n.text("Permissions: %@", mode)).font(.caption).foregroundStyle(.secondary) }
            if change.binary == true {
                Text(L10n.text("Binary file; a line-by-line diff is unavailable.")).font(.caption).foregroundStyle(.secondary)
                if let sha = change.oldSha { Text(L10n.text("Original file digest: %@", sha)).font(.caption.monospaced()).textSelection(.enabled) }
                if let sha = change.newSha { Text(L10n.text("New file digest: %@", sha)).font(.caption.monospaced()).textSelection(.enabled) }
            } else if let diff = change.diff, !diff.isEmpty {
                DisclosureGroup(L10n.text("Line-by-line Diff")) {
                    ScrollView(.horizontal) {
                        Text(diff).font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                            .fixedSize(horizontal: true, vertical: false).padding(8)
                    }.background(Color(nsColor: .textBackgroundColor), in: RoundedRectangle(cornerRadius: 6))
                }.font(.caption)
            } else if change.kind != "mode_changed" {
                Text(L10n.text("No line-by-line diff was received. Read the check explanation.")).font(.caption).foregroundStyle(.secondary)
            }
            if change.truncated == true {
                Text(L10n.text("The diff is too large to show completely. This item will not run; review the source and files again."))
                    .font(.caption).foregroundStyle(.orange)
            }
        }.frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct ReasonMessage: View {
    @ObservedObject private var language = LanguageSettings.shared
    let code: String
    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(ReasonText.describe(code)).font(.callout).textSelection(.enabled)
            if let detail = ReasonText.technicalDetail(code) {
                DisclosureGroup(L10n.text("Technical Details")) { Text(detail).font(.caption).textSelection(.enabled) }
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
    }
}

struct ItemDetailSheet: View {
    @ObservedObject private var language = LanguageSettings.shared
    let item: InventoryItem
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(item.displayName).font(.title2.weight(.semibold)).textSelection(.enabled)
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    detail(L10n.text("Category"), item.category.title)
                    if let plugin = item.plugin { PluginOwnerView(plugin: plugin) }
                    detail(L10n.text("Version"), item.displayVersion)
                    if let build = item.installedBuild { detail(L10n.text("Build version"), build) }
                    if let latest = item.latestVersion { detail(L10n.text("Source candidate version"), latest) }
                    if let build = item.latestBuild { detail(L10n.text("Source build version"), build) }
                    detail(L10n.text("Update status"), item.statusLabel)
                    detail(L10n.text("Check explanation"), item.statusExplanation)
                    if item.owner == "volta" {
                        detail(L10n.text("Volta selection"), VoltaPresentation.selection(item))
                    } else if item.activityKnown == true { detail(L10n.text("Active state"), item.active == true ? L10n.text("Confirmed active") : L10n.text("No active entry found")) }
                    if let explanation = item.healthExplanation { detail(L10n.text("Local check explanation"), explanation) }
                    DisclosureGroup(L10n.text("Source & Location")) {
                        VStack(alignment: .leading, spacing: 10) {
                            if let identity = item.identity { detail(L10n.text("Product identity"), identity) }
                            detail(L10n.text("Owner"), item.owner)
                            detail(L10n.text("Source"), item.source)
                            if let manager = item.skillManager {
                                detail(L10n.text("Skill manager"), manager.manager)
                                if let repository = manager.repository { detail(L10n.text("Recorded repository"), repository) }
                                if let subdirectory = manager.subdirectory { detail(L10n.text("Recorded subdirectory"), subdirectory) }
                                if let commit = manager.commit { detail(L10n.text("Recorded commit"), commit) }
                                detail(L10n.text("Canonical Skill directory"), manager.root)
                                detail(L10n.text("Manager metadata"), manager.metadataPath)
                                detail(L10n.text("Local baseline check"), ReasonText.describe(manager.code))
                            }
                            detail(L10n.text("Channel"), item.channel)
                            detail(L10n.text("Path"), item.path)
                            if let realPath = item.realPath, realPath != item.path { detail(L10n.text("Actual path"), realPath) }
                            if let architectures = item.architecture, !architectures.isEmpty { detail(L10n.text("Architecture"), architectures.joined(separator: L10n.listSeparator)) }
                        }.frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                    }
                    DisclosureGroup(L10n.text("Check Evidence")) {
                        VStack(alignment: .leading, spacing: 10) {
                            detail(L10n.text("Update diagnostics"), item.update.status + " · " + item.update.code)
                            detail(L10n.text("Local diagnostics"), item.health.status + " · " + (item.health.detail ?? item.health.code))
                            if !item.evidence.isEmpty {
                                Text(item.evidence.map { item.id.hasPrefix("demo-") ? L10n.text($0) : $0 }.joined(separator: "\n")).font(.caption).textSelection(.enabled)
                            }
                        }.frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                    }
                    if let dependencies = item.dependencies, !dependencies.isEmpty {
                        DisclosureGroup(L10n.text("Dependency Checks (%@)", String(describing: dependencies.count))) {
                            VStack(alignment: .leading, spacing: 10) {
                                Text(L10n.text("Only declared static dependencies are checked. Unverified does not mean broken.")).font(.caption).foregroundStyle(.secondary)
                                Text(dependencies.map(\.display).joined(separator: "\n\n")).font(.caption).textSelection(.enabled)
                            }.frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                        }
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack { Spacer(); Button(L10n.text("Done")) { if let onClose { onClose() } else { dismiss() } }.nativeGlassButton(prominent: true).keyboardShortcut(.defaultAction) }
        }.padding(24).frame(minWidth: 480, maxWidth: .infinity, minHeight: 400, maxHeight: .infinity, alignment: .topLeading)
    }
    private func detail(_ title: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 3) { Text(title).font(.caption).foregroundStyle(.secondary); Text(value).textSelection(.enabled) }
    }
}

struct DiagnosticsSheet: View {
    @ObservedObject private var language = LanguageSettings.shared
    @ObservedObject var store: InventoryStore
    private var report: InventoryReport? { store.report }
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(L10n.text("Diagnostics & Coverage")).font(.title2.weight(.semibold))
            Text(L10n.text("Unknown, unsupported or unreadable items are not treated as up to date. Unlisted installations may not have been discovered."))
                .font(.callout).foregroundStyle(.secondary)
            Button(L10n.text("Update Permissions…")) { store.showPrivileges = true }
                .disabled(store.busy)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let report {
                        Text(L10n.text("Generated: %@", String(describing: report.generatedAt))).font(.caption).foregroundStyle(.secondary)
                        Text(L10n.text("Read Diagnostics (%@)", String(describing: report.diagnostics.count))).font(.headline)
                        if report.diagnostics.isEmpty { Text(L10n.text("No discovery read errors were found in this scan.")).font(.callout) }
                        ForEach(report.diagnostics) { value in
                            VStack(alignment: .leading, spacing: 3) {
                                if ["skill_update_interrupted", "skill_update_record_unreadable"].contains(value.code) {
                                    Text(L10n.text("The previous Skill update did not complete. Review the original folder and backup. The recovery record is listed below; this folder will not update until resolved."))
                                        .font(.callout).foregroundStyle(.orange)
                                }
                                Text(value.adapter + " · " + value.code).textSelection(.enabled)
                                if let path = value.path { Text(path).font(.caption).foregroundStyle(.secondary).textSelection(.enabled) }
                            }
                        }
                        Divider()
                        Text(L10n.text("Adapter Coverage")).font(.headline)
                        ForEach(report.coverage) { value in
                            VStack(alignment: .leading, spacing: 3) {
                                Text(value.adapter + " · " + CoverageText.level(value.level)).font(.headline)
                                Text(value.adapter == "demo" ? L10n.text(value.detail) : CoverageText.describe(value)).font(.callout).textSelection(.enabled)
                                if L10n.catalog(.english)[value.detail] == nil {
                                    DisclosureGroup(L10n.text("Original Diagnostic Record")) {
                                        Text(value.detail).font(.caption).textSelection(.enabled)
                                    }
                                }
                            }
                        }
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack { Spacer(); Button(L10n.text("Done")) { if let onClose { onClose() } else { dismiss() } }.nativeGlassButton(prominent: true).keyboardShortcut(.defaultAction) }
        }.padding(24).frame(minWidth: 480, maxWidth: .infinity, minHeight: 400, maxHeight: .infinity, alignment: .topLeading)
    }
}


struct AboutSheet: View {
    @ObservedObject private var language = LanguageSettings.shared
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    private let info = AboutInfo()
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("KeepUpdated").font(.title.weight(.semibold))
            VStack(alignment: .leading, spacing: 4) {
                Text(info.versionText)
                Text(info.sourceText).font(.caption.monospaced())
                if let source = info.source, source.dirty {
                    Text(L10n.text("Source tree: %@", source.treeSHA256)).font(.caption.monospaced())
                }
            }.foregroundStyle(.secondary).textSelection(.enabled)
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text(L10n.text("View local apps, packages, Skills and MCP installations in one place."))
                    Text(L10n.text("Scan first, then check for updates. Select items to preview, or use Update All to preview every category."))
                    Text(L10n.text("Updates found means the source reports new content. The preview determines whether it can be installed directly. Supported installation sources can also be checked and previewed when their version is unknown."))
                    Text(L10n.text("Previewing does not change installations. Execution starts only after confirmation; special conditions such as permissions, app exit and installation scripts are shown separately."))
                    Text(L10n.text("Some items only offer update instructions, and unknown states remain visible. Follow the actual conditions in the preview."))
                        .font(.callout).foregroundStyle(.secondary)
                    VStack(alignment: .leading, spacing: 8) {
                        Link(L10n.text("User Guide"), destination: AboutInfo.guideURL)
                        Link(L10n.text("Support & Feedback"), destination: AboutInfo.supportURL)
                        Link(L10n.text("Privacy & Local Data"), destination: AboutInfo.privacyURL)
                        Link(L10n.text("Report a Security Issue Privately"), destination: AboutInfo.securityURL)
                        Text(L10n.text("Reports and saved results can contain private paths. Share only a minimal, redacted example when asking for help."))
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack { Spacer(); Button(L10n.text("OK")) { if let onClose { onClose() } else { dismiss() } }.nativeGlassButton(prominent: true).keyboardShortcut(.defaultAction) }
        }.padding(24).frame(minWidth: 400, maxWidth: .infinity, minHeight: 380, maxHeight: .infinity, alignment: .topLeading)
    }
}

struct ManualUpdatesView: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var expanded = true
    @ObservedObject var store: InventoryStore
    @ObservedObject private var language = LanguageSettings.shared
    var body: some View {
        DisclosureGroup(L10n.text("Manual App Review (%@)", String(store.manualUpdates.count)), isExpanded: $expanded) {
            Text(L10n.text("Review each app’s check status. Use App Store or the app’s updater if an update is available. Opening a link or app does not count as a completed update."))
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if store.manualUpdates.contains(where: \.isAppStoreApplication) {
                HStack {
                    Spacer()
                    Button(L10n.text("Open App Store Updates")) { store.openAppStoreUpdates() }
                        .frame(width: 270, alignment: .trailing)
                }.padding(.top, 4)
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(store.manualUpdates) { item in
                        HStack(alignment: .firstTextBaseline) {
                            VStack(alignment: .leading, spacing: 3) {
                                Text(item.displayName).lineLimit(2)
                                Text(item.statusLabel).font(.caption).foregroundStyle(.secondary)
                                Text(item.statusExplanation).font(.caption).foregroundStyle(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                            Spacer(minLength: 12)
                            Group {
                                if item.isAppStoreApplication {
                                    Button(item.appStoreActionTitle) { store.openAppStore(item) }
                                } else if item.canOpenForManualUpdate {
                                    Button(L10n.text("Open App")) { store.openManualApplication(item) }.disabled(store.busy)
                                } else {
                                    Text(item.canUpdate ? L10n.text("Update this app manually") : L10n.text("Check in the app")).font(.caption).foregroundStyle(.secondary)
                                }
                            }.frame(width: 270, alignment: .trailing)
                        }
                    }
                }.padding(.top, 6)
            }.frame(maxHeight: 120)
        }.font(.callout).accessibilityIdentifier("manual-app-updates")
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: expanded)
    }
}
