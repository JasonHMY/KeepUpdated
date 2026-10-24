import SwiftUI

struct InventoryGridScope: Hashable {
    let category: ItemCategory
    let query: String
    let onlyUpdates: Bool
    let status: InventoryStatusFilter
    let reportRevision: UUID
    let language: AppLanguage
}

struct InventoryRenderWindow {
    private(set) var limit = 100

    mutating func extend(approaching index: Int, total: Int, active: Bool) -> Bool {
        guard active, limit < total, index >= limit - 12, index < limit else { return false }
        limit = min(total, limit + 100)
        return true
    }
}

/// Keep entrance history without invalidating the grid for every visible card.
final class InventoryCardEntrances {
    private var entered: Set<String> = []
    private var startedAt: TimeInterval?

    func contains(_ id: String) -> Bool { entered.contains(id) }

    func delay(for id: String, index: Int, now: TimeInterval) -> TimeInterval? {
        guard entered.insert(id).inserted else { return nil }
        let start = startedAt ?? now
        startedAt = start
        // The first screen forms a short wave; later scrolling never waits in a long queue.
        return max(0, min(Double(index) * 0.018, 0.3) - max(0, now - start))
    }
}

struct InventoryProductGrid<Card: View>: View {
    let products: [InventoryTreeNode]
    let active: Bool
    @ViewBuilder let card: (InventoryTreeNode) -> Card
    @State private var entrances = InventoryCardEntrances()

    var body: some View {
        NativeInventoryCollection(products: products, active: active, entrances: entrances, card: card)
    }
}

struct InventoryEnteringCard<Card: View>: View {
    let id: String
    let index: Int
    let active: Bool
    let entrances: InventoryCardEntrances
    @ViewBuilder let card: () -> Card
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var revealed = false
    @State private var animation: Animation?

    var body: some View {
        let visible = revealed || reduceMotion || entrances.contains(id)
        card()
            .modifier(NativeInventoryEntrance(visible: visible))
            .animation(animation, value: revealed)
            .onAppear { reveal(active: active, reduceMotion: reduceMotion) }
            .onChange(of: active) { reveal(active: $0, reduceMotion: reduceMotion) }
            .onChange(of: reduceMotion) { if $0 { reveal(active: active, reduceMotion: $0) } }
    }

    private func reveal(active: Bool, reduceMotion: Bool) {
        guard active, !revealed else { return }
        let delay = entrances.delay(for: id, index: index, now: Date.timeIntervalSinceReferenceDate)
        animation = reduceMotion ? nil : delay.map { NativeMotion.inventoryEntrance.delay($0) }
        revealed = true
    }
}
