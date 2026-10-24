import SwiftUI

struct InventoryFolderRow: View {
    let row: InventoryTreeNode.Row
    let busy: Bool
    let showSelection: Bool
    let toggle: () -> Void
    let uninstall: () -> Void
    var body: some View {
        if let version = row.node.version {
            HStack(spacing: 12) {
                Color.clear.frame(width: 12).accessibilityHidden(true)
                Image(systemName: "archivebox").frame(width: 30).accessibilityHidden(true)
                Text(L10n.text("Installed version %@", version))
                Spacer()
                Text(L10n.text("Read only")).foregroundStyle(.secondary)
            }.font(.callout).padding(.leading, indentation).padding(.trailing, 22).padding(.vertical, 12)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                Button(action: toggle) {
                    HStack(spacing: 12) {
                        Image(systemName: "chevron.right").rotationEffect(.degrees(row.expanded ? 90 : 0))
                            .font(.caption).frame(width: 12).foregroundStyle(.secondary).accessibilityHidden(true)
                        let applicationPaths = row.node.items.filter { $0.kind == "application" }.map(\.path)
                        if applicationPaths.isEmpty {
                            Image(systemName: row.node.isTechnicalDetail ? "info.circle" : row.node.items.first?.category.symbol ?? "folder")
                                .frame(width: 30).foregroundStyle(.secondary).accessibilityHidden(true)
                        } else {
                            InventoryItemIcon(applicationPaths: applicationPaths, symbol: "app")
                        }
                        VStack(alignment: .leading, spacing: 4) {
                            Text(row.node.displayTitle).font(.headline)
                            if row.expanded, let summary = row.node.summary { Text(summary).font(.caption).foregroundStyle(.secondary) }
                        }
                        Spacer()
                    }.contentShape(Rectangle())
                }.buttonStyle(MotionButtonStyle())
                    .accessibilityValue(L10n.text(row.expanded ? "Expanded" : "Collapsed"))
                    .accessibilityHint(L10n.text("Show or hide grouped installations."))
                if !row.node.uninstallIDs.isEmpty {
                    Button(L10n.text("Uninstall")) { uninstall() }
                        .nativeGlassButton().frame(maxWidth: .infinity, alignment: .trailing)
                        .disabled(busy)
                        .accessibilityLabel(L10n.text("Uninstall group %@ (%@ items)", row.node.displayTitle, String(row.node.uninstallIDs.count)))
                        .help(L10n.text("Review every item in this group, then confirm uninstall."))
                }
            }.padding(.leading, indentation).padding(.trailing, 22).padding(.vertical, 12)
        }
    }
    private var indentation: CGFloat { 12 + CGFloat(min(row.depth, 3)) * 12 }
}
