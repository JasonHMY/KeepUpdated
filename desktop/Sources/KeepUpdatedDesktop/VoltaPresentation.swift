import Foundation

enum VoltaPresentation {
    static func selection(_ item: InventoryItem) -> String {
        if item.kind == "runtime", item.activityKnown == true {
            return item.active == true
                ? L10n.text("Selected in Volta's stored default platform. The running version has not been verified.")
                : L10n.text("Retained image, not selected in Volta's stored default platform. Other consumers may still use it.")
        }
        if item.kind == "runtime-pin" {
            return L10n.text("Project pin read from an explicitly selected project. It does not install or activate a runtime.")
        }
        return L10n.text("Runtime activation is unknown. A PATH shim does not prove which platform will run.")
    }
}
