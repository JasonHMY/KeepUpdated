import SwiftUI

@main
struct KeepUpdatedApp: App {
    @StateObject private var language = LanguageSettings.shared
    @StateObject private var store = InventoryStore()
    @StateObject private var updater = AppUpdateStore()
    var body: some Scene {
        Window("KeepUpdated", id: "main") {
            ContentView(store: store, updater: updater)
                .frame(minWidth: 960, minHeight: 520)
                .environment(\.locale, language.locale)
                .onAppear { MenuLocalization.start() }
                .onChange(of: language.selection) { _ in MenuLocalization.refresh() }
        }
        .defaultSize(width: 1120, height: 760)
        .windowResizability(.contentMinSize)
        .windowStyle(.hiddenTitleBar)
        .commands {
            CommandGroup(replacing: .appSettings) {
                Button(L10n.text("Settings…")) { store.showSettings = true }
                    .keyboardShortcut(",", modifiers: .command)
            }
            CommandGroup(replacing: .appInfo) {
                Button(L10n.text("About KeepUpdated & Help")) { store.showHelp = true }
            }
            CommandGroup(replacing: .help) {
                Button(L10n.text("KeepUpdated Help")) { store.showHelp = true }
            }
            CommandGroup(replacing: .appTermination) {
                Button(L10n.text("Quit KeepUpdated")) { NSApplication.shared.terminate(nil) }
                    .keyboardShortcut("q", modifiers: .command).disabled(store.busy)
            }
            CommandGroup(after: .newItem) {
                Button(L10n.text("Check for Updates")) { store.refresh(online: true) }
                    .keyboardShortcut("r", modifiers: .command)
                    .disabled(store.busy)
            }
        }
    }
}
