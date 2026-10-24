import SwiftUI

/// The selected installation keeps its exact update and removal targets.
struct InventoryInspector: View {
    @ObservedObject private var language = LanguageSettings.shared
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let item: InventoryItem
    @ObservedObject var store: InventoryStore
    let actionsEnabled: Bool
    let details: () -> Void
    let choosePackage: () -> Void

    private var supporting: Bool { InventoryTree.isSupporting(item) }
    private var previewTitle: String? { store.canPreviewUpdate(item) ? item.updatePreviewTitle : nil }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(item.statusExplanation).font(.callout).foregroundStyle(.secondary)
            if let summary = store.rowSummary(item, group: nil) { Text(summary).font(.callout).foregroundStyle(.secondary) }
            if let explanation = item.healthExplanation { Text(explanation).font(.callout).foregroundStyle(.orange) }
            if !supporting { actions.disabled(store.busy || !actionsEnabled) }
            Button(L10n.text("Source & Evidence"), action: details)
                .buttonStyle(.link)
                .accessibilityLabel(L10n.text("Source & Evidence") + ": " + item.displayName)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("inventory-inspector-" + item.id)
    }

    private var actions: some View {
        VStack(alignment: .leading, spacing: 12) {
            updateAction
            if item.canPreparePackage || item.canPrepareSystem {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 10) { specialActions }
                    VStack(alignment: .leading, spacing: 10) { specialActions }
                }
            }
        }
    }

    @ViewBuilder private var updateAction: some View {
        if item.appStoreURL != nil {
            Button(item.appStoreActionTitle) { store.openAppStore(item) }
                .nativeGlassButton(prominent: true).disabled(store.busy)
                .help(L10n.text("Open this app’s App Store page, or the Updates page when its product is unknown. Nothing is installed by KeepUpdated."))
                .accessibilityLabel(L10n.text("%@ %@", item.appStoreActionTitle, item.displayName))
        } else if item.needsManualUpdate && item.canOpenForManualUpdate {
            Button(L10n.text("Open App")) { store.openManualApplication(item) }
                .nativeGlassButton(prominent: true).disabled(store.busy)
                .help(L10n.text("Open the original app to update it yourself."))
                .accessibilityLabel(L10n.text("Open %@ to update yourself", item.displayName))
        } else if let previewTitle {
            Button(previewTitle) { store.plan(action: .update, ids: [item.id]) }
                .nativeGlassButton(prominent: true).disabled(store.busy)
                .help(L10n.text("Verify the source and create a preview. Installed content will not change before confirmation."))
                .accessibilityLabel(L10n.text("%@ %@", previewTitle, item.displayName))
        } else if item.kind == "mcp-server" {
            Button(L10n.text("Review MCP")) { store.plan(action: .update, ids: [item.id]) }
                .nativeGlassButton(prominent: true).disabled(store.busy)
        }
    }

    @ViewBuilder private var specialActions: some View {
        if item.canPreparePackage {
            Button(L10n.text("Choose Installer…"), action: choosePackage)
                .nativeGlassButton().disabled(store.busy)
                .help(L10n.text("Select a local signed .pkg to read its exact target and prerequisites. Nothing is installed."))
        }
        if item.canPrepareSystem {
            Button(L10n.text("System Update Options")) { store.preparePrivileged(item) }
                .nativeGlassButton().disabled(store.busy)
        }
    }

}
