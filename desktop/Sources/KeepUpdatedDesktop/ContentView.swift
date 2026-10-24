import SwiftUI
import UniformTypeIdentifiers

struct ContentView: View {
    @ObservedObject private var language = LanguageSettings.shared
    @ObservedObject var store: InventoryStore
    @ObservedObject var updater: AppUpdateStore
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @FocusState private var searchFocused: Bool
    @State private var searchExpanded = false
    @State private var detail: InventoryItem?
    @State private var productID: String?
    @State private var installationSelections: [String: ProductInstallationSelection] = [:]
    @State private var packageTarget: InventoryItem?
    @State private var choosingPackage = false
    @State private var page: Page = .home
    private enum Page: Int { case home, summary, manager }

    var body: some View {
        standardPresentation
            .fileImporter(isPresented: $choosingPackage, allowedContentTypes: [UTType(filenameExtension: "pkg") ?? .item], allowsMultipleSelection: false) { result in
                defer { packageTarget = nil }
                switch result {
                case .success(let urls):
                    if let target = packageTarget, let url = urls.first { store.preparePrivileged(target, packagePath: url.path) }
                case .failure: store.packageSelectionFailed()
                }
            }
    }

    private var standardPresentation: some View {
        mainContent
        .environment(\.inventoryIconRevision, store.iconRevision)
        .transaction { if reduceMotion { $0.animation = nil; $0.disablesAnimations = true } }
        .task {
            if store.report == nil { await store.restoreLastResults() }
            updater.checkAtLaunch(using: store)
            if store.report == nil { store.refresh(online: false) }
        }
        .onChange(of: page) { if $0 != .manager { searchFocused = false } }
        .onChange(of: store.showSettings) { if $0 { searchFocused = false } }
        .onChange(of: store.report?.items) { _ in reconcileInstallationSelections() }
        .onChange(of: store.completedCheckID) { _ in page = .summary }
        .onChange(of: store.preview != nil) { if !$0 { store.presentPendingResults() } }
        .onChange(of: store.results != nil) { if !$0 { store.resultsSheetDismissed() } }
        .onChange(of: store.privilegedPreparation != nil) { if !$0 { store.privilegedPreparationDismissed() } }
        .onExitCommand {
            if settingsVisible {
                if !updater.installationInProgress { store.showSettings = false }
            } else if store.canCancelCurrentOperation { store.cancel() }
        }
        .background {
            Button(L10n.text("Search")) { if !settingsVisible && inlinePage == nil { page = .manager; searchExpanded = true; searchFocused = true } }.keyboardShortcut("f", modifiers: .command).hidden()
        }
    }

    private var errorText: String {
        guard let detail = store.errorDetail else { return store.error ?? "" }
        return (store.error ?? "") + "\n\n" + L10n.text("Technical Details") + ":\n" + detail
    }

    private var mainContent: some View {
        GeometryReader { geometry in
        ZStack {
            UpdateBackdrop(categoryPosition: categoryPosition)
            VStack(spacing: 0) {
                windowHeader
                if inlinePage == nil { categories }
                ZStack {
                    if let inlinePage {
                        inlineContent
                            .id(inlinePage)
                            .frame(width: geometry.size.width * 0.8,
                                   height: min(geometry.size.height * 0.8, geometry.size.height - 76))
                            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 17))
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                            .transition(NativeMotion.reveal(reduceMotion: reduceMotion))
                    } else {
                        ZStack {
                            overviewPage(summary: false)
                                .modifier(NativePageMotion(position: Page.home.rawValue, selection: page.rawValue))
                            overviewPage(summary: true)
                                .modifier(NativePageMotion(position: Page.summary.rawValue, selection: page.rawValue))
                            manager
                                .modifier(NativePageMotion(position: Page.manager.rawValue, selection: page.rawValue))
                        }
                        .transition(NativeMotion.reveal(reduceMotion: reduceMotion))
                    }
                }.clipped()
                .animation(reduceMotion ? nil : NativeMotion.navigation, value: inlinePage)
            }
            .disabled(settingsVisible)
            .accessibilityHidden(settingsVisible)
            if settingsVisible {
                Color.black.opacity(0.12).ignoresSafeArea()
                LanguageSettingsSheet(store: store, updater: updater, onClose: { store.showSettings = false })
                    .frame(width: min(560, geometry.size.width * 0.8), height: min(600, geometry.size.height * 0.8))
                    .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 20))
                    .shadow(color: .black.opacity(0.16), radius: 24, y: 12)
                    .transition(NativeMotion.reveal(reduceMotion: reduceMotion))
            }
        }
        .overlay(alignment: .bottomTrailing) {
            Text(updater.currentVersion).font(.caption.monospacedDigit()).foregroundStyle(.white.opacity(0.65))
                .padding(14).accessibilityLabel(L10n.text("Current app version: %@", updater.currentVersion))
        }
        .animation(reduceMotion ? nil : NativeMotion.navigation, value: settingsVisible)
        .tint(.blue)
        }.ignoresSafeArea(.container, edges: .top)
    }

    private var settingsVisible: Bool { store.showSettings && inlinePage == nil }

    // Existing operation state remains authoritative; only its presentation changes.
    private var inlinePage: String? {
        if store.error != nil { return "error" }
        if store.showPrivileges { return "privileges" }
        if store.privilegedPreparation != nil { return "preparation" }
        if store.preview != nil { return "preview" }
        if store.results != nil { return "results" }
        if detail != nil { return "detail" }
        if let productID { return "product:" + productID }
        if store.showLocations { return "locations" }
        if store.showDiagnostics { return "diagnostics" }
        if store.showHelp { return "help" }
        return nil
    }

    @ViewBuilder private var inlineContent: some View {
        switch inlinePage {
        case "error":
            VStack(alignment: .leading, spacing: 20) {
                Text(L10n.text("Operation Incomplete")).font(.title2)
                ScrollView { Text(errorText).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading) }
                Button(L10n.text("OK")) { store.dismissError() }.nativeGlassButton(prominent: true)
            }.padding(24)
        case "privileges": PrivilegeStatusSheet(store: store, onClose: { store.showPrivileges = false })
        case "preparation":
            if let preparation = store.privilegedPreparation {
                PrivilegedPreparationSheet(preparation: preparation, store: store, onClose: { store.privilegedPreparation = nil })
            }
        case "preview": PlanSheet(store: store)
        case "results":
            if let report = store.results { ResultsSheet(report: report, store: store, onClose: { store.results = nil }) }
        case "detail":
            if let item = detail { ItemDetailSheet(item: item, onClose: { detail = nil }) }
        case "locations": ScanLocationsSheet(store: store, onClose: { store.showLocations = false })
        case "diagnostics": DiagnosticsSheet(store: store, onClose: { store.showDiagnostics = false })
        case "help": AboutSheet(onClose: { store.showHelp = false })
        default:
            if let productID {
                if let product = store.product(id: productID) {
                    ProductDetailsView(product: product, store: store,
                        selection: installationSelection(for: product),
                        back: { self.productID = nil },
                        details: { detail = $0 },
                        choosePackage: { packageTarget = $0; choosingPackage = true })
                } else {
                    VStack(spacing: 20) {
                        Text(L10n.text("This item is no longer in the current scan."))
                        Button(L10n.text("Back")) { self.productID = nil }.nativeGlassButton()
                    }
                }
            }
        }
    }

    private func overviewPage(summary: Bool) -> some View {
        VStack(spacing: 0) {
            if summary {
                OverviewSummaryView(overview: store.overview, manage: openManager)
            } else {
                WelcomeView(busy: store.busy && page == .home, check: check)
            }
            if summary || store.lastNotice != L10n.text("Some items could not be read. You can check the details in Settings.") {
                notice.foregroundStyle(.white.opacity(0.85))
            }
            footer(active: page == (summary ? .summary : .home)).foregroundStyle(.white).environment(\.colorScheme, .dark)
        }
    }

    private var notice: some View {
        VStack(spacing: 0) {
            if let notice = store.lastNotice {
                MotionText(text: notice).font(.caption).padding(.horizontal, 22).padding(.vertical, 8)
                    .fixedSize(horizontal: false, vertical: true)
                    .transition(NativeMotion.reveal(reduceMotion: reduceMotion))
            }
        }.animation(reduceMotion ? nil : NativeMotion.disclosure, value: store.lastNotice)
    }

    private func check() {
        store.refresh(online: true)
    }

    private func openManager(_ status: InventoryStatusFilter) {
        store.statusFilter = status
        store.onlyUpdates = false
        store.query = ""
        store.category = .all
        page = .manager
    }

    private var windowHeader: some View {
        HStack(spacing: 12) {
            if page == .summary && inlinePage == nil {
                Button { page = .home } label: { Image(systemName: "house") }
                    .buttonStyle(.plain).accessibilityLabel(L10n.text("Home"))
            }
            Spacer()
            if store.demo { inventoryTitle }
            if store.lastResults != nil || store.recoveryReadFailed {
                Button { store.showLastResults() } label: { Label(L10n.text("Last Results"), systemImage: "clock") }
                    .disabled(store.busy).buttonStyle(.plain)
            }
            Button { store.showSettings = true } label: { Image(systemName: "gearshape").frame(width: 28, height: 28) }
                .buttonStyle(.plain).accessibilityLabel(L10n.text("Settings"))
                .disabled(inlinePage != nil)
        }.font(.caption).foregroundStyle(.white).padding(.leading, 78).padding(.trailing, 8).padding(.top, 8).frame(height: 36)
    }

    private var manager: some View {
        let presentation = store.presentation()
        return VStack(spacing: 0) {
            ManagerToolbar(category: store.category.title, displayCount: presentation.displayCount,
                language: language.selection.resolved(),
                query: $store.query, searchExpanded: $searchExpanded, searchFocused: $searchFocused,
                onlyUpdates: $store.onlyUpdates, statusFilter: $store.statusFilter,
                canSelectAll: !store.busy && presentation.products.contains { !previewIDs(for: $0).isEmpty },
                home: { page = .home }, back: { page = .summary }, selectAll: {
                    for product in presentation.products {
                        previewIDs(for: product).forEach { store.setSelected($0, selected: true) }
                    }
                })
            inventory(presentation)
            .clipped()
            .animation(nil, value: store.category)
            notice
            if !store.manualUpdates.isEmpty { ManualUpdatesView(store: store).padding(12) }
            Divider()
            selectionBar(previewCount: presentation.previewCount)
            footer(active: page == .manager)
        }
        .background(.regularMaterial)
        .clipShape(RoundedRectangle(cornerRadius: 17))
        .padding(.horizontal, 12).padding(.bottom, 12)
    }


    @ViewBuilder private var inventoryTitle: some View {
        if store.demo {
            Text(L10n.text("Demo Mode")).font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .help(L10n.text("Synthetic examples; your computer will not be read or changed"))
        } else {
            Text(L10n.text("Local installations")).foregroundStyle(.secondary)
        }
    }

    private func selectionBar(previewCount: Int) -> some View {
        HStack(spacing: 16) {
            Button(L10n.text("Clear Selection")) { store.clearSelection() }.buttonStyle(.link).disabled(store.busy || store.selectedIDs.isEmpty)
            Spacer()
            Button(L10n.text("Update All")) { page = .manager; store.plan(action: .update, all: true) }
                .disabled(store.busy || store.report == nil)
            Button(L10n.text("Preview Selected (%@)", String(store.selectedIDs.count))) { store.previewSelected() }
                .nativeGlassButton(prominent: true).disabled(store.busy || store.selectedIDs.isEmpty)
        }
        .overlay {
            MotionText(text: L10n.text("Selected: %@", String(store.selectedIDs.count))).font(.callout.weight(.semibold)).monospacedDigit()
                .help(L10n.text("Selection is kept across categories and searches. Preview includes all selected items."))
        }
        .padding(.horizontal, 22).padding(.vertical, 14)
        .background(.thinMaterial)
    }

    private var categoryPosition: Double {
        let categories = store.visibleCategories
        return Double(categories.firstIndex(of: store.category) ?? 0) / Double(max(1, categories.count - 1))
    }

    private func selectCategory(_ category: ItemCategory) {
        store.category = category
        page = .manager
    }

    private var categories: some View {
        GeometryReader { geometry in
        ScrollView(.horizontal, showsIndicators: false) {
            NativeGlassControlGroup(spacing: 8) {
            HStack(spacing: 8) {
                ForEach(store.visibleCategories) { category in
                    InventoryCategoryButton(category: category, count: store.count(category),
                                            selected: store.category == category) {
                        selectCategory(category)
                    }
                }
            }
            .animation(reduceMotion ? nil : NativeMotion.selection, value: store.category)
            }.padding(.horizontal, 26).padding(.vertical, 10)
                .frame(minWidth: geometry.size.width)
        }
        }.frame(height: 64)
        .environment(\.colorScheme, .dark)
        .accessibilityElement(children: .contain).accessibilityLabel(L10n.text("Installation categories"))
    }

    @ViewBuilder private func inventory(_ presentation: InventoryStore.Presentation) -> some View {
        if store.report == nil {
            Spacer()
            VStack(spacing: 12) {
                if store.busy && store.progress == nil { ActivityProgressView(label: store.activity).frame(width: 360) }
                Image(systemName: "desktopcomputer").font(.largeTitle).foregroundStyle(.secondary)
                Text(store.busy ? store.activity : L10n.text("Scan your computer to see installations here."))
                if !store.busy { Button(L10n.text("Start Scan")) { store.refresh(online: false) }.nativeGlassButton(prominent: true) }
            }
            Spacer()
        } else if presentation.items.isEmpty {
            Spacer()
            VStack(spacing: 10) {
                Image(systemName: "magnifyingglass").font(.largeTitle).foregroundStyle(.secondary)
                Text(L10n.text("No matching items")).font(.headline)
                Text(L10n.text("Change the category or search, or turn off Updates only.")).foregroundStyle(.secondary)
                Button(L10n.text("Show All")) { selectCategory(.all); store.query = ""; store.onlyUpdates = false; store.statusFilter = .all }
            }
            Spacer()
        } else {
            InventoryProductGrid(products: presentation.products,
                                 active: page == .manager && inlinePage == nil && !settingsVisible) { product in
                let ids = previewIDs(for: product)
                InventoryCandidateRow(product: product,
                    selected: Binding(get: { !ids.isEmpty && ids.allSatisfy { store.selectedIDs.contains($0) } },
                                      set: { selected in ids.forEach { store.setSelected($0, selected: selected) } }),
                    selectable: !store.busy && !ids.isEmpty,
                    open: { openProduct(product) })
            }
            .id(InventoryGridScope(category: store.category, query: store.query, onlyUpdates: store.onlyUpdates,
                                   status: store.statusFilter, reportRevision: store.iconRevision,
                                   language: language.selection.resolved()))
        }
    }

    private func openProduct(_ product: InventoryTreeNode) {
        if installationSelections[product.id] == nil {
            installationSelections[product.id] = ProductInstallationSelection(ids: product.installations.map(\.id))
        } else {
            installationSelections[product.id]?.reconcile(ids: product.installations.map(\.id))
        }
        searchFocused = false
        productID = product.id
    }

    private func previewIDs(for product: InventoryTreeNode) -> [String] {
        let selection = installationSelections[product.id]
        return product.installations.filter { item in
            store.canPreviewUpdate(item) && (selection?.selectedIDs.contains(item.id) ?? true)
        }.map(\.id)
    }

    private func installationSelection(for product: InventoryTreeNode) -> Binding<ProductInstallationSelection> {
        Binding(get: {
            installationSelections[product.id] ?? ProductInstallationSelection(ids: [])
        }, set: { selection in
            installationSelections[product.id] = selection
            for item in product.installations where !selection.selectedIDs.contains(item.id) {
                store.setSelected(item.id, selected: false)
            }
        })
    }

    private func reconcileInstallationSelections() {
        let previous = installationSelections
        let products = store.products
        installationSelections = ProductInstallationSelection.reconciling(previous,
            products: Dictionary(uniqueKeysWithValues: products.map { ($0.id, $0.installations.map(\.id)) }))
        if let productID, store.product(id: productID) == nil, let selection = previous[productID] {
            let replacements = products.filter { !selection.availableIDs.isDisjoint(with: Set($0.installations.map(\.id))) }
            if replacements.count == 1 { self.productID = replacements.first?.id }
        }
    }

    private func footer(active: Bool) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if active {
                if let progress = store.progress { InventoryProgressView(progress: progress) }
                else if store.busy { ActivityProgressView(label: store.activity) }
                else if updater.busy { ActivityProgressView(label: updater.message) }
            }
            if let recovery = store.recoveryNotice {
                Text(recovery).font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if store.busy {
                    Button(L10n.text("Cancel")) { store.cancel() }.nativeGlassButton().disabled(!store.canCancelCurrentOperation)
                } else {
                    Text(L10n.text("Total: %@ · Updates found: %@", String(describing: store.count(.all)), String(describing: store.updateCount))).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()

            }
        }.padding(.horizontal, 22).padding(.vertical, 12)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: store.busy)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: updater.busy)
    }
}
