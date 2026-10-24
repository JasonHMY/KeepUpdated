import Foundation

enum CoverageText {
    static func describe(_ coverage: InventoryReport.Coverage) -> String {
        guard L10n.catalog(.english)[coverage.detail] != nil else {
            return L10n.text("This adapter returned coverage details that this app version cannot translate. Read the original diagnostic record below.")
        }
        return L10n.text(coverage.detail)
    }
    static func level(_ value: String) -> String {
        switch value {
        case "partial": return L10n.text("Partial coverage")
        case "full": return L10n.text("Full coverage")
        case "none": return L10n.text("No coverage")
        default: return L10n.text("Coverage unverified")
        }
    }
}
