import AppKit

@MainActor
enum MenuLocalization {
    private static var observers: [NSObjectProtocol] = []
    private static var isRefreshing = false
    // AppKit creates these standard menus outside the SwiftUI locale environment.
    private static let titles = [
        "File", "Edit", "View", "Window", "Help",
        "Undo", "Redo", "Cut", "Copy", "Paste",
        "Select All", "Close Window", "Minimize", "Zoom", "Bring All to Front",
        "Enter Full Screen", "Exit Full Screen", "Services", "Hide KeepUpdated", "Hide Others",
        "Show All", "Show Toolbar", "Hide Toolbar", "Customize Toolbar…", "KeepUpdated Help",
        "Delete", "AutoFill", "Contact…", "Passwords…", "Credit Card…",
        "Start Dictation", "Emoji & Symbols", "Services Settings…", "Show Tab Bar", "Hide Tab Bar",
        "Show All Tabs", "Exit Tab Overview", "New Tab", "Move Tab to New Window", "Merge All Windows",
        "Show Previous Tab", "Show Next Tab", "Minimize All", "Zoom All", "Fill",
        "Center", "Move & Resize", "Halves", "Left", "Right",
        "Top", "Bottom", "Quarters", "Top Left", "Top Right",
        "Bottom Left", "Bottom Right", "Arrange", "Left & Right", "Left & Quarters",
        "Right & Left", "Right & Quarters", "Top & Bottom", "Top & Quarters", "Bottom & Top",
        "Bottom & Quarters", "Return to Previous Size", "Full Screen Tile", "Left of Screen", "Right of Screen",
        "Arrange in Front", "Remove Window from Set"
    ]
    static func start() {
        guard observers.isEmpty else { return }
        // SwiftUI may rebuild native menu items after a view update. Refresh after
        // AppKit updates its windows as well as when a menu begins tracking.
        for notification in [NSApplication.didUpdateNotification, NSMenu.didBeginTrackingNotification, NSMenu.didChangeItemNotification, NSMenu.didAddItemNotification] {
            observers.append(NotificationCenter.default.addObserver(forName: notification, object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { refresh() }
            })
        }
        refresh()
    }
    static func refresh() {
        guard !isRefreshing, let menu = NSApplication.shared.mainMenu else { return }
        isRefreshing = true
        defer { isRefreshing = false }
        translate(menu)
    }
    static func translate(_ menu: NSMenu) {
        if let title = translatedTitle(menu.title) {
            if menu.title != title { menu.title = title }
        }
        for item in menu.items {
            if let title = translatedTitle(item.title) {
                if item.title != title { item.title = title }
                if item.submenu == nil, item.action != NSSelectorFromString("menuAction:") {
                    // Standard AppKit validation may reset title; attributedTitle keeps
                    // its displayed label without replacing the native action or validator.
                    if item.attributedTitle?.string != title { item.attributedTitle = NSAttributedString(string: title) }
                    item.setAccessibilityLabel(title)
                    item.setAccessibilityTitle(title)
                }
            }
            if let submenu = item.submenu { translate(submenu) }
        }
    }
    private static func translatedTitle(_ original: String) -> String? {
        if let key = titles.contains(original) ? original : titles.first(where: { L10n.format($0, language: .simplifiedChinese) == original }) {
            return L10n.text(key)
        }
        for (english, chinese, key) in [("Move All to ", "将所有窗口移到 ", "Move All to %@"), ("Move to ", "移到 ", "Move to %@"), ("Undo ", "撤销 ", "Undo %@"), ("Redo ", "重做 ", "Redo %@")] {
            for prefix in [english, chinese] where original.hasPrefix(prefix) {
                let name = String(original.dropFirst(prefix.count))
                return L10n.text(key, name)
            }
        }
        return nil
    }

}
