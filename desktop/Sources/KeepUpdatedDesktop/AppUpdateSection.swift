import SwiftUI

struct AppUpdateSection: View {
    @ObservedObject private var language = LanguageSettings.shared
    @ObservedObject var inventory: InventoryStore
    @ObservedObject var updater: AppUpdateStore
    @State private var confirmInstallation = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private func evidenceDetails(_ evidence: AppUpdateEvidence?, title: String, operation: Bool) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(L10n.text(title)).fontWeight(.semibold)
            ForEach(AppUpdateEvidence.rows(evidence, operation: operation), id: \.0) { row in
                Text("\(row.0): \(row.1)").fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(L10n.text("KeepUpdated App Updates")).font(.headline)
            Text(L10n.text("Current app version: %@", updater.currentVersion))
            Text(updater.message).fixedSize(horizontal: false, vertical: true)
            if updater.busy { ActivityProgressView(label: updater.message) }
            if let limit = updater.installationLimit {
                Text(limit).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if let notes = updater.result?.release?.body, !notes.isEmpty {
                DisclosureGroup(L10n.text("Release Notes")) {
                    ScrollView { Text(verbatim: notes).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading) }
                        .frame(maxHeight: 150)
                }
            }
            HStack {
                Button(L10n.text("Check for App Updates")) { updater.check(using: inventory) }
                    .disabled(updater.busy)
                if updater.result?.canInstall == true {
                    Button(L10n.text("Download, Install and Reopen…")) { confirmInstallation = true }
                        .disabled(inventory.busy || updater.busy || inventory.demo)
                }
            }
            if confirmInstallation {
                VStack(alignment: .leading, spacing: 12) {
                    Text(L10n.text("Install and reopen KeepUpdated?")).font(.headline)
                    Text(L10n.text("The update will be downloaded and verified first. KeepUpdated will then close, replace this app, verify the installation and reopen. Your original app will be retained for recovery."))
                        .fixedSize(horizontal: false, vertical: true)
                    HStack {
                        Button(L10n.text("Cancel")) { confirmInstallation = false }
                        Button(L10n.text("Download, Install and Reopen")) {
                            confirmInstallation = false
                            updater.install(using: inventory)
                        }.nativeGlassButton(prominent: true)
                            .disabled(inventory.busy || updater.busy || inventory.demo || updater.result?.canInstall != true)
                    }
                }.padding(16).background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
                    .transition(NativeMotion.reveal(reduceMotion: reduceMotion))
            }
            if let message = updater.recoveryMessage {
                Text(message).font(.callout).fixedSize(horizontal: false, vertical: true)
            }
            if let message = updater.recoveryReopenMessage {
                Text(message).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if let url = updater.recoveryURL {
                Button(L10n.text("Show App Update Recovery Files")) { NSWorkspace.shared.activateFileViewerSelecting([url]) }
                Button(L10n.text("Inspect App Update Files")) { updater.refreshRecovery(using: inventory) }
                    .disabled(updater.inspectingRecovery || ["checking", "preparing", "handoff"].contains(updater.phase) || inventory.demo)
            }
            if updater.result != nil || updater.recovery != nil || updater.detail != nil {
                DisclosureGroup(L10n.text("Technical Details")) {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 8) {
                            if let check = updater.result {
                                evidenceDetails(check.evidence, title: "App update check evidence", operation: false)
                            }
                            if let evidence = updater.preparedEvidence {
                                evidenceDetails(evidence, title: "Download preparation evidence", operation: false)
                            }
                            if let recovery = updater.recovery {
                                evidenceDetails(recovery.evidence, title: "Saved app update evidence", operation: true)
                            }
                            if let reason = updater.inspection?.reason {
                                Text(verbatim: reason).textSelection(.enabled)
                            }
                            if let reason = updater.detail ?? updater.result?.reason {
                                Text(verbatim: reason).textSelection(.enabled)
                            }
                            Text(L10n.text("SHA-256 checks compare downloaded bytes with GitHub release declarations. No independent archive publisher signature is available. Metadata freshness and rollback protection are not verified by the release date or local plan expiry."))
                                .foregroundStyle(.secondary)
                        }.font(.caption).frame(maxWidth: .infinity, alignment: .leading)
                    }.frame(maxHeight: 240)
                }
            }
        }
        .onAppear { updater.readRecovery(demo: inventory.demo) }
        .onChange(of: updater.phase) { if $0 == "checking" { confirmInstallation = false } }
        .animation(reduceMotion ? nil : NativeMotion.disclosure, value: confirmInstallation)
    }
}
