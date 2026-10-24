import SwiftUI

struct ProductDetailsView: View {
    @ObservedObject private var language = LanguageSettings.shared
    @Environment(\.colorScheme) private var colorScheme
    let product: InventoryTreeNode
    @ObservedObject var store: InventoryStore
    @Binding var selection: ProductInstallationSelection
    let back: () -> Void
    let details: (InventoryItem) -> Void
    let choosePackage: (InventoryItem) -> Void

    private var selected: [InventoryItem] { product.installations.filter { selection.selectedIDs.contains($0.id) } }
    private var updateIDs: [String] { selected.filter { store.canPreviewUpdate($0) }.map(\.id) }
    private var removalTitle: String {
        L10n.text(product.productCategory == .mcp ? "Remove Connections (%@)" : "Uninstall Selected (%@)", String(selected.count))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 14) {
                Button(action: back) { Label(L10n.text("Back"), systemImage: "chevron.left") }
                    .buttonStyle(.plain).disabled(store.busy).keyboardShortcut(.cancelAction)
                Spacer()
                Text(product.productCategory.title).font(.caption).foregroundStyle(.secondary)
            }
            HStack(spacing: 12) {
                if !product.applicationIconPaths.isEmpty {
                    InventoryItemIcon(applicationPaths: product.applicationIconPaths, symbol: product.productCategory.symbol, size: 42)
                } else {
                    SculptedIcon(symbol: product.productCategory == .skills ? "wand.and.stars" : product.productCategory.symbol,
                                 tint: product.productCategory.iconTint, size: 42)
                }
                Text(product.productTitle).font(.title2.weight(colorScheme == .dark ? .regular : .medium)).textSelection(.enabled)
            }
            if !product.installations.isEmpty {
                Text(L10n.text("Choose the installations to include. Updating or removing still requires a preview and confirmation."))
                    .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    ForEach(product.installations) { item in
                        installation(item)
                    }
                    if !product.supportingItems.isEmpty {
                        Text(L10n.text("Supporting records · Read only")).font(.headline.weight(colorScheme == .dark ? .medium : .semibold)).padding(.top, 8)
                        Text(L10n.text("Components and dependencies are shown for reference. They are not separate installation choices."))
                            .font(.caption).foregroundStyle(.secondary)
                        ForEach(product.supportingItems) { item in
                            supportingRecord(item)
                        }
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }
            if store.busy {
                HStack {
                    if let progress = store.progress { InventoryProgressView(progress: progress) }
                    else { ActivityProgressView(label: store.activity) }
                    Spacer()
                    Button(L10n.text("Cancel")) { store.cancel() }
                        .nativeGlassButton().disabled(!store.canCancelCurrentOperation)
                }
            }
            if !product.installations.isEmpty {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 12) {
                        selectionCount
                        Spacer()
                        operationButtons
                    }
                    VStack(alignment: .leading, spacing: 10) {
                        selectionCount
                        HStack(spacing: 12) { operationButtons }
                    }
                }
            }
        }.padding(24)
            .frame(minWidth: 520, maxWidth: .infinity, minHeight: 400, maxHeight: .infinity, alignment: .topLeading)
            .accessibilityIdentifier("product-details-" + product.id)
    }

    private var selectionCount: some View {
        Text(L10n.text("Selected installations: %@", String(selected.count))).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
    }

    @ViewBuilder private var operationButtons: some View {
        Button(removalTitle) { store.plan(action: .uninstall, ids: selected.map(\.id)) }
            .nativeGlassButton().disabled(store.busy || selected.isEmpty)
            .help(L10n.text("Review only the selected installations before confirming removal."))
        Button(L10n.text("Preview Updates (%@)", String(updateIDs.count))) { store.plan(action: .update, ids: updateIDs) }
            .nativeGlassButton(prominent: true).disabled(store.busy || updateIDs.isEmpty)
    }

    private func installation(_ item: InventoryItem) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Toggle(isOn: Binding(get: { selection.selectedIDs.contains(item.id) },
                                 set: { selection.setSelected(item.id, selected: $0) })) {
                HStack(alignment: .firstTextBaseline, spacing: 12) {
                    Text(item.clientName ?? item.owner).font(.headline.weight(colorScheme == .dark ? .medium : .semibold))
                    Spacer()
                    Text(item.displayVersion).font(.callout.monospacedDigit()).foregroundStyle(.secondary)
                }
            }.toggleStyle(.checkbox).disabled(store.busy)
                .accessibilityLabel(L10n.text("Select %@", (item.clientName ?? item.owner) + ": " + item.path))
                .accessibilityIdentifier("installation-choice-" + item.id)
            Text(item.path).font(.caption.monospaced()).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
            if let versions = item.installedVersions, versions.count > 1 {
                Text(L10n.text("Installed versions: %@", versions.joined(separator: L10n.listSeparator)))
                    .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
            }
            if item.source != item.owner { Text(item.source).font(.caption).foregroundStyle(.secondary) }
            InventoryInspector(item: item, store: store, actionsEnabled: selection.selectedIDs.contains(item.id),
                details: { details(item) }, choosePackage: { choosePackage(item) })
        }.padding(16).background(Color.primary.opacity(0.025), in: RoundedRectangle(cornerRadius: 12))
    }

    private func supportingRecord(_ item: InventoryItem) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(item.displayName).font(.callout)
                Spacer()
                Text(item.displayVersion).font(.caption.monospacedDigit()).foregroundStyle(.secondary)
            }
            Text(item.path).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
            Text(item.statusLabel).font(.caption).foregroundStyle(.secondary)
            Button(L10n.text("Source & Evidence")) { details(item) }.buttonStyle(.link)
                .accessibilityLabel(L10n.text("Source & Evidence") + ": " + item.displayName)
        }.padding(16).background(Color.primary.opacity(0.02), in: RoundedRectangle(cornerRadius: 12))
    }
}
