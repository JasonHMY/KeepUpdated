import Foundation

/// View-owned scope: default to every installation once; a rescan never grants new selections.
struct ProductInstallationSelection: Equatable {
    private(set) var selectedIDs: Set<String>
    private(set) var availableIDs: Set<String>

    init(ids: [String]) {
        availableIDs = Set(ids)
        selectedIDs = availableIDs
    }

    mutating func setSelected(_ id: String, selected: Bool) {
        guard availableIDs.contains(id) else { return }
        if selected { selectedIDs.insert(id) }
        else { selectedIDs.remove(id) }
    }

    mutating func reconcile(ids: [String]) {
        availableIDs = Set(ids)
        selectedIDs.formIntersection(availableIDs)
    }

    /// A single row can become a group after a scan. Carry choices by actual installation IDs.
    static func restoring(ids: [String], previous: [Self]) -> Self? {
        let available = Set(ids)
        let related = previous.filter { !$0.availableIDs.isDisjoint(with: available) }
        guard !related.isEmpty else { return nil }
        var selection = Self(ids: ids)
        selection.selectedIDs = Set(related.flatMap(\.selectedIDs)).intersection(available)
        return selection
    }

    static func reconciling(_ previous: [String: Self], products: [String: [String]]) -> [String: Self] {
        let currentIDs = Set(products.values.flatMap { $0 })
        // Keep wholly absent products as tombstones. Overlapping old groups are replaced below,
        // so a stale group cannot resurrect choices changed in its split installation rows.
        var refreshed = previous.filter { $0.value.availableIDs.isDisjoint(with: currentIDs) }
        for (productID, ids) in products {
            if var selection = previous[productID] {
                selection.reconcile(ids: ids)
                refreshed[productID] = selection
            } else if let selection = restoring(ids: ids, previous: Array(previous.values)) {
                refreshed[productID] = selection
            }
        }
        return refreshed
    }
}
