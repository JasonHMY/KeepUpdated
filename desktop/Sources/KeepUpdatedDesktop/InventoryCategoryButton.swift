import SwiftUI

struct InventoryCategoryButton: View {
    let category: ItemCategory
    let count: Int
    let selected: Bool
    let action: () -> Void

    var body: some View {
        let scale = selected ? 1.12 : 1.0
        CategoryScaleLayout(scale: scale) {
            Button(action: action) {
                HStack(spacing: 5) {
                    SculptedIcon(symbol: category.symbol, tint: category.iconTint, size: 17)
                    Text(category.title)
                    Text("\(count)").monospacedDigit()
                }
                .padding(.horizontal, 12).padding(.vertical, 9)
                .contentShape(Capsule())
            }
            .buttonStyle(.plain).font(.system(size: 12))
            .nativeCategoryGlass(selected: false)
            .scaleEffect(scale)
            .foregroundStyle(.white)
            .help(L10n.text("Open an item to choose its installations and review its details."))
            .accessibilityLabel(L10n.text("%@, count %@", String(describing: category.title), String(describing: count)))
            .accessibilityAddTraits(selected ? .isSelected : [])
        }
    }
}

/// Reserve the scaled control's real footprint while keeping its label centered.
struct CategoryScaleLayout: Layout {
    var scale: CGFloat
    var animatableData: CGFloat {
        get { scale }
        set { scale = newValue }
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let control = subviews.first else { return .zero }
        let size = control.sizeThatFits(.unspecified)
        return CGSize(width: size.width * scale, height: size.height * scale)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: CGPoint(x: bounds.midX, y: bounds.midY), anchor: .center,
                             proposal: .unspecified)
    }
}
