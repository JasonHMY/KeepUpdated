import AppKit
import SwiftUI

struct InventoryRow: View {
    @ObservedObject private var language = LanguageSettings.shared
    let item: InventoryItem
    let busy: Bool
    let previewTitle: String?
    @Binding var selected: Bool
    let details: () -> Void
    let update: () -> Void
    let openAppStore: () -> Void
    let openApplication: () -> Void
    let uninstall: () -> Void
    let choosePackage: () -> Void
    let prepareSystem: () -> Void
    var showSelection: Bool = false
    var displayTitle: String? = nil
    var showsActions = true
    var depth = 0
    var groupSummary: String? = nil
    var groupExpanded = false
    var toggleGroup: (() -> Void)? = nil
    @State private var expanded = false
    private var isExpanded: Bool { toggleGroup == nil ? expanded : groupExpanded }
    private func toggleExpansion() {
        if let toggleGroup { toggleGroup() } else { expanded.toggle() }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 0) {
                if showSelection {
                    Group {
                        if showsActions {
                            Toggle(L10n.text("Select %@", item.displayName), isOn: $selected)
                                .labelsHidden().toggleStyle(.checkbox)
                                .disabled(busy || previewTitle == nil)
                        } else {
                            Color.clear.accessibilityHidden(true)
                        }
                    }.frame(width: 16).padding(.trailing, 12)
                }
                Button { toggleExpansion() } label: {
                    summary
                }.buttonStyle(.plain)
                    .accessibilityLabel(summaryAccessibilityLabel)
                    .accessibilityValue(L10n.text(isExpanded ? "Expanded" : "Collapsed"))
                    .accessibilityHint(L10n.text(toggleGroup == nil ? "Show or hide installation details" : "Show or hide details and grouped installations"))
                    .accessibilityIdentifier("inventory-summary-" + item.id)
                Group {
                    if showsActions { actions }
                    else { Color.clear.frame(height: 1).accessibilityHidden(true) }
                }.frame(width: 270, alignment: .trailing).padding(.trailing, 22)
            }.padding(.leading, 22 + CGFloat(min(depth, 4)) * 24)
            if isExpanded {
                VStack(alignment: .leading, spacing: 8) {
                    if let groupSummary {
                        Text(groupSummary).font(.caption).foregroundStyle(.secondary)
                    }
                    Text(versionSummary).font(.caption).foregroundStyle(.secondary)
                    Label(item.statusLabel, systemImage: item.statusSymbol)
                        .font(.callout).foregroundStyle(item.canUpdate && !InventoryTree.isSupporting(item) ? Color.accentColor : Color.secondary)
                    Text(item.statusExplanation).font(.callout).foregroundStyle(.secondary)
                    if let explanation = item.healthExplanation {
                        Label(L10n.text("Installation issue"), systemImage: "exclamationmark.triangle")
                            .font(.caption).foregroundStyle(.orange)
                        Text(explanation).font(.caption).foregroundStyle(.orange)
                    }
                    Text(item.path).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                    if showsActions && (showsSecondaryUpdate || !showsPrimaryRemoval) {
                        HStack(spacing: 8) {
                            if showsSecondaryUpdate { updateAction }
                            if !showsPrimaryRemoval { removalAction }
                        }.buttonStyle(.bordered)
                    }
                    HStack(spacing: 12) {
                        Button(L10n.text("Source & Evidence"), action: details).buttonStyle(.link)
                        if item.canPreparePackage {
                            Button(L10n.text("Choose Installer…"), action: choosePackage).disabled(busy)
                                .help(L10n.text("Select a local signed .pkg to read its exact target and prerequisites. Nothing is installed."))
                        }
                        if item.canPrepareSystem {
                            Button(L10n.text("System Update Options"), action: prepareSystem).disabled(busy)
                        }
                    }
                }
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.leading, 88 + CGFloat(min(depth, 4)) * 24 + (showSelection ? 28 : 0))
                .padding(.trailing, 22).padding(.bottom, 12)
            }
        }
        // Catch row whitespace without wrapping selection, links or operation controls.
        .background {
            Button { toggleExpansion() } label: { Color.clear.contentShape(Rectangle()) }
                .buttonStyle(.plain).accessibilityHidden(true).focusable(false)
        }
        .accessibilityElement(children: .contain)
    }

    private var summary: some View {
        HStack(spacing: 12) {
            Image(systemName: isExpanded ? "chevron.down" : "chevron.right")
                .font(.caption).frame(width: 12).foregroundStyle(.secondary)
                .opacity(toggleGroup == nil ? 0 : 1).accessibilityHidden(true)
            InventoryItemIcon(applicationPaths: item.kind == "application" ? [item.path] : [],
                              symbol: item.category.symbol)
            VStack(alignment: .leading, spacing: 4) {
                Text(displayTitle ?? item.displayName).font(.headline)
                if let summaryVersion {
                    Text(summaryVersion).font(.caption).foregroundStyle(.secondary)
                }
            }.frame(maxWidth: .infinity, alignment: .leading)
        }
        .fixedSize(horizontal: false, vertical: true)
        .padding(.vertical, 12).padding(.trailing, 12)
        .contentShape(Rectangle())
    }

    private var actions: some View {
        HStack(spacing: 8) {
            if showsPrimaryUpdate { updateAction }
            if showsPrimaryRemoval { removalAction }
        }.buttonStyle(.bordered).fixedSize()
    }

    private var showsPrimaryUpdate: Bool {
        item.appStoreURL != nil || (item.needsManualUpdate && item.canOpenForManualUpdate) ||
            (item.canUpdate && previewTitle != nil)
    }

    private var showsPrimaryRemoval: Bool {
        ![.commandLine, .runtimes, .services, .system, .other].contains(item.category)
    }

    private var showsSecondaryUpdate: Bool {
        !showsPrimaryUpdate && (previewTitle != nil || item.kind == "mcp-server")
    }

    @ViewBuilder private var updateAction: some View {
        if item.appStoreURL != nil {
            Button(item.appStoreActionTitle, action: openAppStore).disabled(busy)
                .help(L10n.text("Open this app’s App Store page, or the Updates page when its product is unknown. Nothing is installed by KeepUpdated."))
                .accessibilityLabel(L10n.text("%@ %@", item.appStoreActionTitle, item.displayName))
        } else if item.needsManualUpdate && item.canOpenForManualUpdate {
            Button(L10n.text("Open App"), action: openApplication).disabled(busy)
                .help(L10n.text("Open the original app to update it yourself."))
                .accessibilityLabel(L10n.text("Open %@ to update yourself", item.displayName))
        } else if let previewTitle {
            Button(previewTitle, action: update).disabled(busy)
                .help(L10n.text("Verify the source and create a preview. Installed content will not change before confirmation."))
                .accessibilityLabel(L10n.text("%@ %@", previewTitle, item.displayName))
        } else if item.kind == "mcp-server" {
            Button(L10n.text("Review MCP"), action: update).disabled(busy)
        }
    }

    private var removalAction: some View {
        Button(L10n.text(item.kind == "mcp-server" ? "Remove Connection" : "Uninstall"), action: uninstall).disabled(busy)
            .accessibilityLabel(L10n.text(item.kind == "mcp-server" ? "Remove connection %@" : "Uninstall %@", item.displayName))
            .help(L10n.text(item.kind == "mcp-server" ? "Review removal of this client connection. The service or installed program is not removed." : "Review uninstall effects and choose whether to clean configuration and personal data"))
    }

    private var summaryAccessibilityLabel: String {
        var parts = [displayTitle ?? item.displayName]
        if let summaryVersion { parts.append(summaryVersion) }
        return parts.joined(separator: L10n.listSeparator)
    }

    private var summaryVersion: String? {
        guard let version = item.activeVersion ?? item.installedVersion, !version.isEmpty,
              !(item.kind == "executable" && InventoryTree.isSupporting(item)) else { return nil }
        return item.displayVersion
    }

    private var versionSummary: String {
        let origin = item.source == item.owner ? item.owner : "\(item.owner) · \(item.source)"
        if item.kind == "executable", InventoryTree.isSupporting(item) {
            return L10n.text("Command entry. Check the owning package for its version.")
        }
        let installed = "\(origin) · \(item.displayVersion)"
        guard item.canUpdate, let latest = item.latestVersion else { return installed }
        if latest == item.displayVersion, let build = item.latestBuild {
            return L10n.text("%@ · New build %@", installed, build)
        }
        return installed + " → " + latest
    }
}

private struct InventoryIconRevisionKey: EnvironmentKey {
    static let defaultValue: UUID? = nil
}

extension EnvironmentValues {
    var inventoryIconRevision: UUID? {
        get { self[InventoryIconRevisionKey.self] }
        set { self[InventoryIconRevisionKey.self] = newValue }
    }
}

struct InventoryItemIcon: View {
    let applicationPaths: [String]
    let symbol: String
    var size: CGFloat = 30
    @State private var appIcon: NSImage?
    @Environment(\.inventoryIconRevision) private var revision

    private struct Request: Hashable {
        let paths: [String]
        let revision: UUID?
    }

    var body: some View {
        Group {
            if let appIcon {
                Image(nsImage: appIcon).resizable().scaledToFit()
            } else {
                Image(systemName: symbol).font(.title3).foregroundStyle(.secondary)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
        .task(id: Request(paths: applicationPaths, revision: revision)) {
            appIcon = nil
            let image = await ApplicationIconLoader.shared.icon(for: applicationPaths, revision: revision)
            guard !Task.isCancelled else { return }
            appIcon = image
        }
    }
}
