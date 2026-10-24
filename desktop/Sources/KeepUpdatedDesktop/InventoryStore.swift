import Foundation
import Combine
import AppKit

@MainActor
final class InventoryStore: ObservableObject {
    @Published private(set) var report: InventoryReport?
    @Published private(set) var completedCheckID: UUID?
    @Published var category: ItemCategory = .all
    @Published var query = ""
    @Published var onlyUpdates = false
    @Published var statusFilter: InventoryStatusFilter = .all
    @Published private(set) var busy = false
    @Published private(set) var applying = false
    @Published private var activityMessage: LocalizedMessage = ""
    var activity: String { progress?.phaseLabel ?? activityMessage.text }
    @Published private(set) var progress: InventoryProgress?
    private var progressOperation: UUID?
    @Published private var storedError: Error?
    var error: String? { storedError.map { ($0 as? ClientError)?.localizedDescription ?? L10n.text("The operation could not be completed. See technical details.") } }
    var errorDetail: String? {
        if let client = storedError as? ClientError { return client.technicalDetail }
        return storedError?.localizedDescription
    }
    func dismissError() { storedError = nil }
    @Published private(set) var manualUpdates: [InventoryItem] = []
    @Published var preview: OperationPlan?
    @Published private var previewNoticeMessage: LocalizedMessage?
    var previewNotice: String? { previewNoticeMessage?.text }
    @Published var results: ApplyReport?
    @Published private(set) var lastResults: ApplyReport?
    @Published private(set) var recoveryReadFailed = false
    var recoveryNotice: String? {
        if recoveryReadFailed { return L10n.text("Saved operation results could not be read. Open Last Results for details.") }
        if lastResults?.isUnfinished == true { return L10n.text("The last operation has no confirmed final result. Open Last Results before trying again.") }
        return nil
    }
    @Published private(set) var selectedIDs: Set<String> = []
    @Published var cleanup = false
    @Published var showDiagnostics = false
    @Published var showLocations = false
    @Published var showHelp = false
    @Published var showSettings = false
    @Published var showPrivileges = false
    @Published var privilegedPreparation: PrivilegedPreparation?
    @Published private(set) var helperStatus: HelperStatus?
    @Published private(set) var operationStatuses: [String: HelperStatus] = [:]
    private var pendingRestart: (targetID: String, data: Data)?
    private var pendingPrivilegedCandidate: (targetID: String, data: Data)?
    @Published private(set) var locations: ScanLocations
    @Published private var lastNoticeMessage: LocalizedMessage?
    var lastNotice: String? { lastNoticeMessage?.text }
    let demo: Bool
    private let client: CLIClient
    private var task: Task<Void, Never>?
    private var exactPlan: Data?
    private var exactPlanScope: [String] = []
    private var pendingResults: ApplyReport?
    private var lastResultAction: OperationAction?
    private var pendingRetry: (action: OperationAction, ids: [String])?
    private var requestedIDs: [String] = []
    private var requestedAll = false
    private var requestedSpecial = false
    private var requestedReview = ReviewChoices()
    private var sortedItems: [InventoryItem] = []
    private(set) var overview = InventoryOverview(items: [])
    private(set) var iconRevision = UUID()
    private(set) var tree: [InventoryTreeNode] = []
    private var productNodes: [InventoryTreeNode] = []
    private var productLanguage: AppLanguage?
    private var categoryCounts: [ItemCategory: Int] = [:]
    private(set) var updateCount = 0
    private var previewableIDs: Set<String> = []

    init(demo: Bool = ProcessInfo.processInfo.arguments.contains("--demo"), client: CLIClient = CLIClient()) {
        self.demo = demo
        self.client = client
        locations = demo ? ScanLocations() : ScanLocations.load()
        if demo { replaceReport(DemoData.report) }
    }

    // A matching owner token prevents a stale callback from clearing another operation.
    private var appUpdateLease: UUID?
    var canCancelCurrentOperation: Bool { busy && !applying && appUpdateLease == nil }
    func acquireAppUpdate(_ message: LocalizedMessage = "Checking public app releases…") -> UUID? {
        guard !busy else { return nil }
        let lease = UUID()
        appUpdateLease = lease
        progressOperation = nil
        progress = nil
        activityMessage = message
        busy = true
        return lease
    }
    func updateAppUpdateActivity(_ lease: UUID, _ message: LocalizedMessage) {
        guard appUpdateLease == lease else { return }
        activityMessage = message
    }
    func releaseAppUpdate(_ lease: UUID) {
        guard appUpdateLease == lease else { return }
        appUpdateLease = nil
        busy = false
        activityMessage = ""
    }

    var filtered: [InventoryItem] {
        sortedItems.filter {
            (category == .all || $0.category == category) && (!onlyUpdates || $0.canUpdate) && statusFilter.matches($0) && $0.matches(query)
        }
    }
    struct Presentation {
        let items: [InventoryItem]
        let products: [InventoryTreeNode]
        let displayCount: Int
        let previewCount: Int
        fileprivate let makeRows: () -> [InventoryTreeNode.Row]
        var rows: [InventoryTreeNode.Row] { makeRows() }
    }

    // Product topology changes with a report or language, not with scrolling or selection.
    var products: [InventoryTreeNode] {
        if productLanguage != L10n.language {
            tree = InventoryTree.build(sortedItems)
            productNodes = InventoryTree.products(tree)
            productLanguage = L10n.language
        }
        return productNodes
    }

    func presentation(expanded: Set<String> = [], collapsedWhileFiltering: Set<String> = []) -> Presentation {
        let items = filtered
        let matching = Set(items.map(\.id))
        let filtering = !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || onlyUpdates || statusFilter != .all
        let visibleProducts = products.filter { node in node.items.contains { matching.contains($0.id) } }
        let tree = tree
        return Presentation(items: items,
            products: visibleProducts,
            displayCount: visibleProducts.filter { !$0.installations.isEmpty }.count,
            previewCount: items.filter { previewableIDs.contains($0.id) }.count,
            makeRows: {
                InventoryTreeNode.rows(tree, matching: matching, expanded: expanded,
                    filtering: filtering, collapsedWhileFiltering: collapsedWhileFiltering)
            })
    }
    var hasConfirmedPlan: Bool { exactPlan != nil }
    func product(id: String) -> InventoryTreeNode? { products.first { $0.id == id } }
    func count(_ category: ItemCategory) -> Int {
        categoryCounts[category, default: 0]
    }
    var visibleCategories: [ItemCategory] {
        ItemCategory.allCases.filter { $0 == .all || $0 == category || count($0) > 0 }
    }
    var visibleDisplayCount: Int {
        displayCount(matching: Set(filtered.map(\.id)))
    }
    private func displayCount(matching: Set<String>) -> Int {
        products.filter { node in node.items.contains { matching.contains($0.id) } && !node.installations.isEmpty }.count
    }
    func rowSummary(_ item: InventoryItem, group: String?) -> String? {
        var parts = [item.installationSummary, group].compactMap { $0 }
        if item.kind == "mcp-server", item.update.code == "use_installation_owner" {
            let owners = Set(item.related ?? []).compactMap { id in
                report?.items.first { $0.id == id && ["node-package", "python-package", "formula", "cask"].contains($0.kind) }
            }
            if owners.count == 1, let owner = owners.first {
                parts.append(L10n.text("Update via %@: %@", owner.owner, owner.displayName))
            }
        }
        return parts.isEmpty ? nil : parts.joined(separator: "\n")
    }
    func replaceReport(_ report: InventoryReport) {
        iconRevision = UUID()
        overview = InventoryOverview(items: report.items)
        sortedItems = report.items.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
        tree = InventoryTree.build(sortedItems)
        productNodes = InventoryTree.products(tree)
        productLanguage = L10n.language
        categoryCounts = [:]
        updateCount = displayCount(matching: Set(report.items.filter { $0.canUpdate && !InventoryTree.isSupporting($0) }.map(\.id)))
        for category in ItemCategory.allCases where category != .all {
            categoryCounts[category] = InventoryTree.displayCount(report.items, category: category)
        }
        categoryCounts[.all] = displayCount(matching: Set(report.items.map(\.id)))
        let managedIDs = Set(report.items.filter { $0.kind != "skill" && $0.hasManagedUpdatePreview }.map(\.id))
        previewableIDs = Set(report.items.filter { item in
            !InventoryTree.isSupporting(item) && (item.canUpdate || item.canReviewSkillChanges || item.hasManagedUpdatePreview ||
            (item.kind == "mcp-server" && (item.related ?? []).contains { managedIDs.contains($0) }))
        }.map(\.id))
        selectedIDs.formIntersection(previewableIDs)
        let manualIDs = Set(manualUpdates.map(\.id))
        manualUpdates = report.items.filter { manualIDs.contains($0.id) && $0.needsManualUpdate }
        self.report = report
    }
    func openAppStore(_ item: InventoryItem, open: (URL) -> Bool = { NSWorkspace.shared.open($0) }) {
        guard let current = report?.items.first(where: { $0.id == item.id }),
              current == item, let url = current.appStoreURL else { return }
        openStoreURL(url, open: open)
    }
    func openAppStoreUpdates(open: (URL) -> Bool = { NSWorkspace.shared.open($0) }) {
        guard report?.items.contains(where: \.isAppStoreApplication) == true,
              let url = URL(string: "macappstore://showUpdatesPage") else { return }
        openStoreURL(url, open: open)
    }
    private func openStoreURL(_ url: URL, open: (URL) -> Bool) {
        if demo {
            lastNoticeMessage = LocalizedMessage("Demo: App Store link %@. No page was opened and nothing was installed.", url.absoluteString)
        } else if open(url) {
            lastNoticeMessage = "App Store opened. Complete any update there, then check again."
        } else {
            storedError = ClientError.message("The App Store page could not be opened. Try again or open App Store manually.")
        }
    }

    func canPreviewUpdate(_ item: InventoryItem) -> Bool { previewableIDs.contains(item.id) }
    var visiblePreviewCount: Int { filtered.filter { previewableIDs.contains($0.id) }.count }
    func setSelected(_ id: String, selected: Bool) {
        guard !busy, previewableIDs.contains(id) else { return }
        if selected { selectedIDs.insert(id) } else { selectedIDs.remove(id) }
    }
    func selectVisible() {
        guard !busy else { return }
        selectedIDs.formUnion(filtered.filter { previewableIDs.contains($0.id) }.map(\.id))
    }
    func clearSelection() { guard !busy else { return }; selectedIDs.removeAll() }
    func previewSelected() {
        guard !selectedIDs.isEmpty else { return }
        plan(action: .update, ids: sortedItems.filter { selectedIDs.contains($0.id) }.map(\.id))
    }
    func affectedNames(_ ids: [String]) -> String {
        let lookup = Dictionary((report?.items ?? []).map { ($0.id, $0.displayName) }, uniquingKeysWith: { first, _ in first })
        return ids.map { lookup[$0] ?? $0 }.joined(separator: L10n.listSeparator)
    }

    func refresh(online: Bool) {
        guard !busy else { return }
        run(online ? LocalizedMessage("Checking updates and dependencies…") : LocalizedMessage("Scanning local installations…")) { [self] in
            try Task.checkCancellation()
            if self.demo {
                self.replaceReport(DemoData.report)
                self.lastNoticeMessage = LocalizedMessage("Demo data refreshed. Your computer was not read or changed.")
                if online { self.completedCheckID = UUID() }
                return
            }
            let arguments = online ? ["check", "--online", "--store-country", Locale.current.region?.identifier ?? "US"] : ["scan"]
            let output = try await self.runWithProgress(arguments + self.locations.arguments)
            try Task.checkCancellation()
            self.replaceReport(try ReportJSON.decode(InventoryReport.self, from: output.data, schema: "keepupdated/v1"))
            self.lastNoticeMessage = output.status == 1 ? LocalizedMessage("Some items could not be read. You can check the details in Settings.") : (online ? LocalizedMessage("Check completed. Unknown items and those needing manual handling remain listed.") : LocalizedMessage("Scan completed. Choose Check for Updates to look for new versions."))
            if online { self.completedCheckID = UUID() }
        }
    }

    func plan(action: OperationAction, ids: [String] = [], all: Bool = false, special: Bool = false, keepCleanup: Bool = false, review: ReviewChoices = ReviewChoices()) {
        guard !busy else { return }
        var targetIDs = ids
        var targetAll = all
        if action == .update {
            let candidates = (report?.items ?? []).filter { all || ids.contains($0.id) }
            manualUpdates = candidates.filter { $0.isAppStoreApplication || (review.privilegedRequestData == nil && $0.needsManualUpdate) }
            if !manualUpdates.isEmpty {
                let manualIDs = Set(manualUpdates.map(\.id))
                targetIDs = candidates.filter { !manualIDs.contains($0.id) }.map(\.id)
                targetAll = false
                if targetIDs.isEmpty {
                    preparePlan(ids: [], keepCleanup: keepCleanup)
                    preview = nil
                    lastNoticeMessage = "Review these apps below. Any available updates must be completed in App Store or the app."
                    return
                }
            }
        } else { manualUpdates = [] }
        preparePlan(ids: targetIDs, all: targetAll, special: special, keepCleanup: keepCleanup)
        requestedReview = review
        let scope = locations.arguments
        let requestKind = review.privilegedRequestData.flatMap { try? JSONDecoder().decode(PrivilegedRequestPreview.self, from: $0).kind }
        run(LocalizedMessage("Preparing %@ preview…", localizedArgument: requestKind == "restart" ? "Restart" : action.titleKey)) {
            try await self.generatePlan(action: action, ids: targetIDs, all: targetAll, special: special, scope: scope, review: review)
        }
    }

    private func preparePlan(ids: [String], all: Bool = false, special: Bool = false, keepCleanup: Bool = false) {
        if !keepCleanup { cleanup = false }
        requestedIDs = ids
        requestedAll = all
        requestedSpecial = special
        requestedReview = ReviewChoices()
        exactPlan = nil
        previewNoticeMessage = nil
    }

    private func generatePlan(action: OperationAction, ids: [String], all: Bool = false, special: Bool = false, scope: [String], review: ReviewChoices = ReviewChoices()) async throws {
        let data: Data
        if demo {
            let items = report?.items ?? []
            let plan = DemoData.plan(action: action, items: all ? items : items.filter { ids.contains($0.id) }, cleanup: cleanup, special: special, review: review, all: all)
            data = try DemoData.planData(plan, privilegedRequest: review.privilegedRequestData)
        } else {
            var args = ["plan", "--action", action.rawValue]
            if all { args.append("--all") }
            let selectionFile = ids.count > 1 ? try PrivatePlanFile(data: JSONEncoder().encode(ids)) : nil
            defer { selectionFile?.remove() }
            if let selectionFile { args += ["--ids-file", selectionFile.url.path] }
            else { for id in ids { args += ["--id", id] } }
            if cleanup && action == .uninstall { args.append("--cleanup") }
            if special { args.append("--special") }
            args += review.arguments
            if let request = review.privilegedRequestData {
                guard action == .update, !all, ids.count == 1 else { throw ClientError.message("Select one installation for the prepared candidate.") }
                let file = try PrivatePlanFile(data: request)
                defer { file.remove() }
                args += ["--privileged-request", file.url.path]
                data = try await runWithProgress(args + scope).data
            } else {
                data = try await runWithProgress(args + scope).data
            }
        }
        let plan = try ReportJSON.decode(OperationPlan.self, from: data, schema: "keepupdated/plan/v1")
        guard plan.action == action else { throw ClientError.message(LocalizedMessage("The preview action does not match. Create a new preview.")) }
        let returned = plan.selection
        let returnedIDs = plan.items.map(\.id)
        guard returned?.all == all, returned?.cleanup == (cleanup && action == .uninstall),
              Set(returnedIDs).count == returnedIDs.count,
              all ? (returned?.ids ?? []).isEmpty :
                (returnedIDs.sorted() == ids.sorted() && returned?.ids?.sorted() == ids.sorted()) else {
            throw ClientError.message("The preview items do not match your selection. Create a new preview.")
        }
        guard Set(returned?.consents ?? []) == review.consents,
              Set(returned?.quitApps ?? []) == review.quitApps,
              Set(returned?.reopenApps ?? []) == review.reopenApps,
              (returned?.mcpReference ?? "") == review.mcpReference,
              (returned?.mcpCandidate ?? "") == review.mcpCandidate,
              (returned?.special ?? false) == special else {
            throw ClientError.message("The preview permissions do not match your choices. Create a new preview.")
        }
        let object = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        let selectionObject = object?["selection"] as? [String: Any]
        let returnedRequest: Data?
        if let value = selectionObject?["privileged_request"] {
            returnedRequest = try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
        } else { returnedRequest = nil }
        guard returnedRequest == review.privilegedRequestData else {
            throw ClientError.message("The prepared request changed in the preview. Prepare it again.")
        }
        if let request = review.privilegedRequestData {
            let entries = object?["items"] as? [[String: Any]] ?? []
            for entry in entries where entry["status"] as? String == "ready" {
                guard entry["id"] as? String == ids.first, let value = entry["privileged_request"],
                      try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]) == request else {
                    throw ClientError.message("The prepared request changed in the preview. Prepare it again.")
                }
            }
        }
        exactPlan = data
        exactPlanScope = scope
        preview = plan
    }

    func changeCleanup(_ value: Bool) {
        guard let action = preview?.action, !busy else { return }
        cleanup = value
        plan(action: action, ids: requestedIDs, all: requestedAll, special: requestedSpecial, keepCleanup: true, review: requestedReview)
    }

    func planReviewed(_ item: PlanItem, choices: ReviewChoices) {
        guard let action = preview?.action, choices.reopenApps.isSubset(of: choices.quitApps),
              choices.consents.allSatisfy({ OperationConsent(rawValue: $0) != nil }),
              choices.quitApps.isSubset(of: Set(item.exitTargets.map(\.id))),
              choices.mcpReference.isEmpty || (action == .update && item.kind == "mcp-server" && choices.mcpReferenceApproved && !cleanup) else { return }
        var reviewed = choices
        if requestedIDs == [item.id] { reviewed.privilegedRequestData = requestedReview.privilegedRequestData }
        plan(action: action, ids: [item.id], special: reviewed.specialForPreview, keepCleanup: true, review: reviewed)
    }

    func planSpecial(_ item: PlanItem) {
        guard let action = preview?.action else { return }
        plan(action: action, ids: [item.id], special: true, keepCleanup: true)
    }

    func planRelated(_ item: PlanItem) {
        guard let action = preview?.action, let ids = item.affectedIds, !ids.isEmpty else { return }
        plan(action: action, ids: ids, keepCleanup: true)
    }

    func dismissPreview() {
        guard !busy else { return }
        preview = nil
        exactPlan = nil
        exactPlanScope = []
    }

    func addLocations(_ paths: [String], kind: ScanLocationKind) {
        guard !busy, preview == nil else { return }
        for path in paths where path.hasPrefix("/") && !locations[kind].contains(path) { locations[kind].append(path) }
        if !demo { locations.save() }
        lastNoticeMessage = LocalizedMessage("Scan locations saved. Scan again to use them.")
    }

    func removeLocation(_ path: String, kind: ScanLocationKind) {
        guard !busy, preview == nil else { return }
        locations[kind].removeAll { $0 == path }
        if !demo { locations.save() }
        lastNoticeMessage = LocalizedMessage("Scan locations saved. Scan again to use them.")
    }

    func presentPendingResults() {
        if let pendingResults { results = pendingResults; self.pendingResults = nil }
    }
    // Startup restores observations only. exactPlan and all execution consents stay empty.
    func restoreLastResults() async {
        guard !busy, !demo else { return }
        busy = true
        defer { busy = false }
        do { try await readRecentResults() } catch { /* Keep the recovery notice visible alongside the scan. */ }
    }
    private func readRecentResults() async throws {
        do {
            let response = try await client.run(["results"])
            let record = try RecentOperation.decode(response.data)
            lastResults = record.operation
            lastResultAction = record.operation?.action
            recoveryReadFailed = false
        } catch {
            recoveryReadFailed = true
            throw error
        }
    }
    func showLastResults() {
        guard !busy, preview == nil else { return }
        if let lastResults, !recoveryReadFailed { results = lastResults; return }
        reloadLastResults()
    }
    func reloadLastResults() {
        guard !busy, !demo, preview == nil else { return }
        run("Reading saved operation results…") {
            try await self.readRecentResults()
            self.results = self.lastResults
            if self.lastResults == nil { self.lastNoticeMessage = "No saved operation results were found." }
        }
    }
    func retryResults(ids: [String]) {
        guard !busy, let report = results, let action = lastResultAction else { return }
        let failedIDs = Set(report.retryable.map(\.id))
        let retryIDs = ids.filter { failedIDs.contains($0) }
        guard !retryIDs.isEmpty else { return }
        pendingRetry = (action, retryIDs)
        results = nil
    }
    func resultsSheetDismissed() {
        if let restart = pendingRestart {
            pendingRestart = nil
            plan(action: .update, ids: [restart.targetID], review: ReviewChoices(privilegedRequestData: restart.data))
            return
        }
        guard let retry = pendingRetry else { return }
        pendingRetry = nil
        let scope = locations.arguments
        run(LocalizedMessage("Scanning again and checking failed items…")) {
            let report: InventoryReport
            if self.demo {
                report = self.report ?? DemoData.report
            } else {
                let response = try await self.runWithProgress(["scan"] + scope)
                report = try ReportJSON.decode(InventoryReport.self, from: response.data, schema: "keepupdated/v1")
            }
            self.replaceReport(report)
            let present = Set(report.items.map(\.id))
            let ids = retry.ids.filter { present.contains($0) }
            let missingCount = retry.ids.count - ids.count
            guard !ids.isEmpty else {
                self.lastNoticeMessage = LocalizedMessage("Items missing from this scan: %@. Review Last Results; no new preview was created.", String(describing: missingCount))
                return
            }
            self.preparePlan(ids: ids)
            if missingCount > 0 {
                let notice = LocalizedMessage("Missing from this scan: %@. Items included in this preview: %@. Review missing items in Last Results.", String(describing: missingCount), String(describing: ids.count))
                self.previewNoticeMessage = notice
                self.lastNoticeMessage = notice
            }
            self.activityMessage = LocalizedMessage("Checking again and preparing %@ preview…", localizedArgument: retry.action.titleKey)
            try await self.generatePlan(action: retry.action, ids: ids, scope: scope)
        }
    }

    func execute() {
        guard !busy, let plan = preview, let data = exactPlan, !plan.executable.isEmpty else { return }
        guard let expires = plan.expires, expires > Date() else {
            storedError = ClientError.message("This preview has expired. Close it and create a new one.")
            return
        }
        run(LocalizedMessage("Running %@… Please wait for verification.", localizedArgument: plan.displayActionKey)) {
            self.applying = true
            defer { self.applying = false }
            let output: ApplyReport
            if self.demo {
                output = DemoData.apply(plan)
            } else {
                let file = try PrivatePlanFile(data: data)
                defer { file.remove() }
                do {
                    let response = try await self.runWithProgress(["apply", "--plan", file.url.path, "--confirm", plan.planId] + self.exactPlanScope)
                    output = try ReportJSON.decode(ApplyReport.self, from: response.data, schema: "keepupdated/apply/v1")
                    guard output.planId == plan.planId else { throw ClientError.message(LocalizedMessage("The result does not match the confirmed preview. Check local state before continuing.")) }
                } catch {
                    self.exactPlan = nil
                    self.exactPlanScope = []
                    try? await self.readRecentResults()
                    if self.lastResults?.planId == plan.planId { self.pendingResults = self.lastResults }
                    self.preview = nil
                    throw error
                }
            }
            self.pendingResults = output
            self.lastResults = output
            self.lastResultAction = plan.action
            self.preview = nil
            self.exactPlan = nil
            if !self.demo {
                self.activityMessage = LocalizedMessage("Scanning again to verify installations…")
                let refreshed = try await self.runWithProgress(["scan"] + self.locations.arguments)
                self.replaceReport(try ReportJSON.decode(InventoryReport.self, from: refreshed.data, schema: "keepupdated/v1"))
                self.lastNoticeMessage = output.state == "finished"
                    ? LocalizedMessage("Results saved and installations scanned again. Check for Updates to verify version sources.")
                    : LocalizedMessage("Installations scanned again. This result is available in the current session.")
            }
        }
    }

    func preparePrivileged(_ item: InventoryItem, packagePath: String? = nil) {
        guard !busy, (packagePath != nil && item.canPreparePackage) || (packagePath == nil && item.canPrepareSystem) else { return }
        privilegedPreparation = nil
        pendingPrivilegedCandidate = nil
        run(LocalizedMessage("Preparing exact installation candidates…")) {
            let data: Data
            if self.demo {
                data = DemoData.privilegedPreparation(item: item, packagePath: packagePath)
            } else {
                var args = ["special", "prepare", "--id", item.id]
                if let packagePath { args += ["--package", packagePath] }
                args += self.locations.appRoot.flatMap { ["--app-root", $0] }
                data = try await self.client.run(args).data
            }
            self.privilegedPreparation = try PrivilegedPreparation.decode(data, targetID: item.id, targetName: item.displayName)
        }
    }
    func selectPrivilegedCandidate(_ id: Int) {
        guard !busy, let preparation = privilegedPreparation, preparation.status == "prepared",
              let candidate = preparation.candidates.first(where: { $0.id == id }), candidate.request.supportedPreparation else { return }
        pendingPrivilegedCandidate = (preparation.targetID, candidate.data)
        privilegedPreparation = nil
    }
    func privilegedPreparationDismissed() {
        guard let selected = pendingPrivilegedCandidate else { return }
        pendingPrivilegedCandidate = nil
        plan(action: .update, ids: [selected.targetID], review: ReviewChoices(privilegedRequestData: selected.data))
    }
    func packageSelectionFailed() {
        storedError = ClientError.message("The selected installer could not be read. Choose the local package again.")
    }
    func refreshHelperStatus() {
        guard !busy else { return }
        run(LocalizedMessage("Checking the privileged helper…")) {
            if self.demo { self.helperStatus = HelperStatus(status: "demo"); return }
            self.helperStatus = try await JSONDecoder().decode(HelperStatus.self, from: self.client.run(["special", "status"]).data)
        }
    }
    func registerHelper() {
        guard !busy, !demo, helperStatus?.canRequestRegistration == true else { return }
        helperStatus = nil
        run(LocalizedMessage("Requesting helper registration…")) {
            let output = try await self.client.run(["special", "register-helper"])
            let registration = try JSONDecoder().decode(HelperStatus.self, from: output.data)
            if output.status == 0 {
                self.helperStatus = try await JSONDecoder().decode(HelperStatus.self, from: self.client.run(["special", "status"]).data)
            } else { self.helperStatus = registration }
        }
    }
    func checkPrivilegedOperation(_ id: String) {
        guard !busy, id.range(of: "^[0-9a-f]{64}$", options: .regularExpression) != nil else { return }
        operationStatuses[id] = nil
        run(LocalizedMessage("Reading the operation recovery status…")) {
            if self.demo {
                let waiting = self.results?.results.contains { $0.privilegedOperationId == id && $0.awaitsRestart } == true
                self.operationStatuses[id] = HelperStatus(status: waiting ? "awaiting_restart" : "demo", reason: "demo_privileged_actions_disabled", planID: id)
                return
            }
            let response = try await self.client.run(["special", "operation-status", id])
            let status = try JSONDecoder().decode(HelperStatus.self, from: response.data)
            guard status.planID == id || (status.status == "blocked" && (status.planID ?? "").isEmpty) else {
                throw ClientError.message("The recovery status did not match this operation.")
            }
            self.operationStatuses[id] = status
        }
    }

    func canPreviewRestart(_ operationID: String) -> Bool {
        guard !busy, let status = operationStatuses[operationID], status.planID == operationID, status.status == "awaiting_restart",
              results?.results.contains(where: { $0.privilegedOperationId == operationID }) == true else { return false }
        return (report?.items.filter(\.canPrepareSystem).count ?? 0) == 1
    }
    func previewRestart(_ operationID: String) {
        guard canPreviewRestart(operationID), let system = report?.items.first(where: \.canPrepareSystem),
              let data = try? JSONSerialization.data(withJSONObject: ["kind": "restart", "restartOf": operationID], options: [.sortedKeys]) else { return }
        pendingRestart = (system.id, data)
        pendingRetry = nil
        results = nil
    }

    func cancel() {
        guard canCancelCurrentOperation else { return }
        progressOperation = nil
        progress = nil
        task?.cancel()
        client.cancel()
    }

    private func runWithProgress(_ arguments: [String]) async throws -> CommandOutput {
        let operation = UUID()
        progressOperation = operation
        progress = nil
        defer { if progressOperation == operation { progressOperation = nil; progress = nil } }
        let output = try await client.run(arguments + ["--progress"]) { [weak self] event in
            Task { @MainActor in
                guard let self, self.progressOperation == operation, event.follows(self.progress) else { return }
                self.progress = event
            }
        }
        try Task.checkCancellation()
        return output
    }

    func inventoryItem(for preview: PlanItem) -> InventoryItem? {
        guard let item = report?.items.first(where: { $0.id == preview.id }),
              self.preview?.action == .update, ["manual", "blocked"].contains(preview.status) else { return nil }
        return item
    }

    func openManualApplication(_ item: InventoryItem,
                               open: @escaping @MainActor (URL) async throws -> Void = { url in
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.activates = true
        configuration.allowsRunningApplicationSubstitution = false
        let application = try await NSWorkspace.shared.openApplication(at: url, configuration: configuration)
        guard application.bundleURL?.resolvingSymlinksInPath() == url.resolvingSymlinksInPath() else {
            throw ClientError.message("The opened app could not be matched to the selected installation. Scan again.")
        }
    }) {
        guard !busy, let current = report?.items.first(where: { $0.id == item.id }), current == item,
              current.canOpenForManualUpdate else { return }
        if demo {
            lastNoticeMessage = "Demo: the original app would open for you to update. Nothing was opened or installed."
            return
        }
        run("Opening the original app…") {
            let url = try Self.verifiedApplicationURL(current)
            try Task.checkCancellation()
            try await open(url)
            self.lastNoticeMessage = "App opened. Complete the update in the app, then check again."
        }
    }

    static func verifiedApplicationURL(_ item: InventoryItem) throws -> URL {
        let invalid = ClientError.message("This app changed or is no longer available. Scan again before opening it.")
        guard item.canOpenForManualUpdate else { throw invalid }
        let original = URL(fileURLWithPath: item.path).standardizedFileURL
        let resolved = original.resolvingSymlinksInPath()
        let expected = URL(fileURLWithPath: item.realPath ?? item.path).standardizedFileURL
        guard resolved.path == expected.path,
              !resolved.deletingLastPathComponent().pathComponents.contains(where: { $0.lowercased().hasSuffix(".app") }),
              let values = try? resolved.resourceValues(forKeys: [.isDirectoryKey]), values.isDirectory == true else { throw invalid }
        let metadata = resolved.appendingPathComponent("Contents/Info.plist")
        guard let size = try? metadata.resourceValues(forKeys: [.fileSizeKey]).fileSize, size <= 1_048_576,
              let data = try? Data(contentsOf: metadata),
              let info = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
              info["CFBundleIdentifier"] as? String == item.identity,
              info["CFBundlePackageType"] as? String == "APPL",
              let executable = info["CFBundleExecutable"] as? String, !executable.isEmpty,
              executable != ".", executable != "..", !executable.contains("/"),
              FileManager.default.isExecutableFile(atPath: resolved.appendingPathComponent("Contents/MacOS/" + executable).path),
              item.installedVersion == nil || ((info["CFBundleShortVersionString"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? info["CFBundleVersion"] as? String) == item.installedVersion,
              item.installedBuild == nil || info["CFBundleVersion"] as? String == item.installedBuild else { throw invalid }
        return original
    }

    private func run(_ label: LocalizedMessage, operation: @escaping @MainActor () async throws -> Void) {
        busy = true
        progressOperation = nil
        progress = nil
        activityMessage = label
        storedError = nil
        task = Task {
            defer { busy = false; activityMessage = ""; task = nil }
            do { try await operation() }
            catch is CancellationError { lastNoticeMessage = LocalizedMessage("Operation cancelled. If an update or uninstall already started, scan again to check the actual state.") }
            catch { self.storedError = error }
        }
    }
}
