import SwiftUI

struct LanguageSettingsSheet: View {
    @ObservedObject var store: InventoryStore
    @ObservedObject var updater: AppUpdateStore
    @ObservedObject private var language = LanguageSettings.shared
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(L10n.text("Settings")).font(.title2.weight(.medium)).frame(maxWidth: .infinity)
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    VStack(alignment: .leading, spacing: 10) {
                        Picker(L10n.text("App Language"), selection: $language.selection) {
                            ForEach(AppLanguage.allCases) { option in
                                Text(option.title).tag(option)
                            }
                        }
                        .pickerStyle(.radioGroup)
                        Text(L10n.text("Language changes apply immediately and are saved. Unsupported system languages use English."))
                            .font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                    VStack(alignment: .leading, spacing: 12) {
                        HStack(spacing: 12) {
                            Button(L10n.text("Check for Updates")) { if let onClose { onClose() } else { dismiss() }; store.refresh(online: true) }
                                .disabled(store.busy)
                            Button(L10n.text("Start Scan")) { if let onClose { onClose() } else { dismiss() }; store.refresh(online: false) }
                                .disabled(store.busy)
                        }
                        HStack(spacing: 12) {
                            Button(L10n.text("Scan Locations")) { store.showLocations = true }
                                .disabled(store.busy)
                            Button(L10n.text("Update Permissions")) { store.showPrivileges = true }
                                .disabled(store.busy)
                        }
                    }
                    AppUpdateSection(inventory: store, updater: updater)
                    DisclosureGroup(L10n.text("Advanced")) {
                        Button(L10n.text("Diagnostics & Coverage")) { store.showDiagnostics = true }
                            .padding(.top, 8).disabled(store.busy)
                    }
                    Button(L10n.text("About KeepUpdated")) { store.showHelp = true }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack {
                Spacer()
                Button(L10n.text("Done")) { if let onClose { onClose() } else { dismiss() } }
                    .nativeGlassButton(prominent: true).keyboardShortcut(.defaultAction).disabled(updater.installationInProgress)
            }
        }.padding(24).frame(minWidth: 400, maxWidth: .infinity, minHeight: 360, maxHeight: .infinity, alignment: .topLeading)
            .environment(\.locale, language.locale)
            .interactiveDismissDisabled(updater.installationInProgress)
            .background {
                Button(L10n.text("Done")) { if let onClose { onClose() } else { dismiss() } }
                    .keyboardShortcut(.cancelAction).hidden().disabled(updater.installationInProgress)
            }
    }
}
