import Foundation

/// Presentation-only buckets. Unknown and failed checks never imply current.
enum InventoryStatusFilter: String, CaseIterable, Identifiable {
    case all = "All statuses", updates = "Updates to preview", manual = "Manual action", unknown = "Not confirmed", current = "Checked current"
    var id: String { rawValue }
    var title: String { L10n.text(rawValue) }

    func countTitle(_ count: Int) -> String {
        switch self {
        case .all: return L10n.text("Found %@ installation records", String(count))
        case .updates: return L10n.text("%@ updates to preview", String(count))
        case .manual: return L10n.text("%@ need manual handling", String(count))
        case .unknown: return L10n.text("%@ not confirmed", String(count))
        case .current: return L10n.text("%@ checked current", String(count))
        }
    }

    static func status(of item: InventoryItem) -> Self {
        if item.update.status == "current" { return .current }
        if item.update.status == "error" { return .unknown }
        if item.needsManualUpdate || item.kind == "os" || item.owner == "volta" || item.skillManager != nil { return .manual }
        if item.canUpdate { return .updates }
        return .unknown
    }
    func matches(_ item: InventoryItem) -> Bool { self == .all || Self.status(of: item) == self }
}

struct InventoryOverview {
    let items: [InventoryItem]
    let unconfirmedVersionCount: Int
    private let statusCounts: [InventoryStatusFilter: Int]

    init(items: [InventoryItem]) {
        self.items = items.filter { !InventoryTree.isSupporting($0) }
        unconfirmedVersionCount = self.items.filter { !["available", "current"].contains($0.update.status) }.count
        var counts: [InventoryStatusFilter: Int] = [.all: self.items.count]
        for item in self.items { counts[InventoryStatusFilter.status(of: item), default: 0] += 1 }
        statusCounts = counts
    }
    func count(_ status: InventoryStatusFilter) -> Int { statusCounts[status, default: 0] }
}
