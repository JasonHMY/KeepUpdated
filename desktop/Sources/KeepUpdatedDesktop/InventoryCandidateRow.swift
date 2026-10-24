import SwiftUI

/// One logical product per card; exact installations are chosen on its details page.
struct InventoryCandidateRow: View {
    let product: InventoryTreeNode
    @Binding var selected: Bool
    let selectable: Bool
    let open: () -> Void
    @Environment(\.colorScheme) private var colorScheme

    private var installations: [InventoryItem] { product.installations }
    private var category: ItemCategory { product.productCategory }

    var body: some View {
        Button(action: open) {
            VStack(alignment: .leading, spacing: 12) {
                HStack {
                    if !product.applicationIconPaths.isEmpty {
                        InventoryItemIcon(applicationPaths: product.applicationIconPaths, symbol: category.symbol, size: 48)
                    } else {
                        SculptedIcon(symbol: category == .skills ? "wand.and.stars" : category.symbol,
                                     tint: category.iconTint, size: 48)
                    }
                    Spacer(minLength: 30)
                }
                Text(product.productTitle)
                    .font(.system(size: 13, weight: colorScheme == .dark ? .regular : .medium))
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                summary
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxWidth: .infinity, minHeight: 140, alignment: .topLeading)
            .padding(16)
            .contentShape(RoundedRectangle(cornerRadius: 14))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(([product.productTitle] + summaryLines).joined(separator: ", "))
        .accessibilityHint(L10n.text("Open details and choose installations."))
        .accessibilityIdentifier("inventory-product-" + product.id)
        .overlay(alignment: .topTrailing) {
            if !installations.isEmpty {
                Toggle(L10n.text("Select %@", product.productTitle), isOn: $selected)
                    .labelsHidden().toggleStyle(.checkbox).disabled(!selectable)
                    .accessibilityLabel(L10n.text("Select %@", product.productTitle))
                    .padding(16)
            }
        }
        .nativeInventoryCardGlass()
        .accessibilityElement(children: .contain)
    }

    private var summaryLines: [String] {
        if installations.count == 1, let item = installations.first {
            return [item.displayVersion + (item.canUpdate ? " → " + (item.latestVersion ?? L10n.text("Unknown")) : ""),
                    item.statusLabel]
        }
        if installations.isEmpty { return [L10n.text("Read only")] }
        return [L10n.text("%@ installations", String(installations.count)),
                L10n.text("Updates: %@", String(installations.filter(\.canUpdate).count))]
    }

    @ViewBuilder private var summary: some View {
        if installations.count == 1 {
            VStack(alignment: .leading, spacing: 5) {
                Text(summaryLines[0])
                    .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                Text(summaryLines[1]).font(.system(size: 10)).foregroundStyle(.secondary)
            }
        } else if installations.isEmpty {
            Text(summaryLines[0]).font(.caption).foregroundStyle(.secondary)
        } else {
            VStack(alignment: .leading, spacing: 5) {
                Text(summaryLines[0])
                    .font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                Text(summaryLines[1])
                    .font(.system(size: 10).monospacedDigit()).foregroundStyle(.secondary)
            }
        }
    }
}
