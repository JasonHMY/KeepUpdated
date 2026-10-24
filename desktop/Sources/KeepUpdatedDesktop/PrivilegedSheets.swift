import SwiftUI

struct PrivilegedPreparationSheet: View {
    let preparation: PrivilegedPreparation
    @ObservedObject var store: InventoryStore
    @ObservedObject private var language = LanguageSettings.shared
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    @State private var selected: Int?
    init(preparation: PrivilegedPreparation, store: InventoryStore, onClose: (() -> Void)? = nil) {
        self.preparation = preparation
        self.store = store
        self.onClose = onClose
        let supported = preparation.candidates.filter { $0.request.supportedPreparation }
        _selected = State(initialValue: supported.count == 1 ? supported.first?.id : nil)
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(L10n.text("Installation Candidates")).font(.title2.weight(.semibold))
            Text(preparation.targetName).font(.headline)
            if store.demo { Text(L10n.text("Demo Mode")).foregroundStyle(.orange) }
            Text(L10n.text("Preparation only reads metadata. Choose one exact candidate to create an operation preview; no installation has been authorized."))
                .font(.callout).foregroundStyle(.secondary)
            if preparation.reason != preparation.executionBlocker || preparation.executionBlocker.isEmpty {
                ReasonMessage(code: preparation.reason)
            }
            if !preparation.executionBlocker.isEmpty {
                Text(L10n.text("Execution is currently blocked")).font(.headline).foregroundStyle(.orange)
                ReasonMessage(code: preparation.executionBlocker)
            }
            Button(L10n.text("Update Permissions…")) { store.showPrivileges = true }.disabled(store.busy)
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if preparation.candidates.isEmpty { Text(L10n.text("No prepared candidates are available. This does not mean the installation is up to date.")) }
                    ForEach(preparation.candidates) { candidate in
                        VStack(alignment: .leading, spacing: 8) {
                            Button {
                                selected = candidate.id
                            } label: {
                                Label(selected == candidate.id ? L10n.text("Selected candidate") : L10n.text("Select candidate"), systemImage: selected == candidate.id ? "largecircle.fill.circle" : "circle")
                            }.disabled(!preparation.canPreview || !candidate.request.supportedPreparation)
                            .accessibilityLabel(L10n.text("Candidate %@", candidate.title))
                            .accessibilityAddTraits(selected == candidate.id ? .isSelected : [])
                            PrivilegedRequestView(request: candidate.request)
                        }.frame(maxWidth: .infinity, alignment: .leading)
                        Divider()
                    }
                }
            }
            HStack {
                Button(L10n.text("Cancel")) { if let onClose { onClose() } else { dismiss() } }.keyboardShortcut(.cancelAction)
                Spacer()
                Button(L10n.text("Preview Selected Candidate")) { if let selected { store.selectPrivilegedCandidate(selected) } }
                    .nativeGlassButton(prominent: true).disabled(store.busy || !preparation.canPreview || selected == nil)
            }
        }.padding(24).frame(minWidth: 480, maxWidth: .infinity, minHeight: 400, maxHeight: .infinity, alignment: .topLeading)
    }
}

struct PrivilegedRequestView: View {
    let request: PrivilegedRequestPreview
    @ObservedObject private var language = LanguageSettings.shared
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let package = request.package {
                packageSummary(package)
            } else if let system = request.system {
                systemSummary(system)
            } else if let app = request.appStore {
                appStoreSummary(app)
            } else if let operation = request.restartOf {
                Text(L10n.text("Separate restart request for operation %@", operation)).font(.callout).textSelection(.enabled)
            } else {
                Text(L10n.text("Unsupported candidate kind: %@", request.kind)).foregroundStyle(.orange)
            }
        }.fixedSize(horizontal: false, vertical: true)
    }

    @ViewBuilder private func packageSummary(_ package: PrivilegedRequestPreview.Package) -> some View {
        let versionLine: String = "\(package.bundleID) · \(package.fromVersion) → \(package.version)"
        Text(L10n.text("Selected installer candidate")).font(.headline)
        Text(versionLine)
        Text(package.path).font(.caption).textSelection(.enabled)
        Text(L10n.text("Installation path: %@", package.installationPath)).font(.caption).textSelection(.enabled)
        Text(L10n.text("Developer team: %@ · Receipt: %@", package.teamID, package.receiptID)).font(.caption)
        Text("SHA-256: " + package.sha256).font(.caption.monospaced()).textSelection(.enabled)
    }

    @ViewBuilder private func systemSummary(_ system: PrivilegedRequestPreview.System) -> some View {
        let versionLine: String = "\(system.fromVersion) (\(system.fromBuild)) → \(system.version) (\(system.build))"
        Text(system.label).font(.headline)
        Text(versionLine)
        Text(L10n.text("System scripts require their own approval. Installation does not authorize a restart."))
            .font(.caption).foregroundStyle(.secondary)
    }

    @ViewBuilder private func appStoreSummary(_ app: PrivilegedRequestPreview.AppStore) -> some View {
        let identityLine: String = "\(app.bundleID) · \(app.adamID)"
        let versionLine: String = "\(app.fromVersion) → \(app.version) · \(app.region)"
        Text(L10n.text("Exact App Store target")).font(.headline)
        Text(identityLine)
        Text(versionLine)
        Text(app.path).font(.caption).textSelection(.enabled)
    }
}

struct PrivilegeStatusSheet: View {
    @ObservedObject var store: InventoryStore
    @ObservedObject private var language = LanguageSettings.shared
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(L10n.text("Update Permissions")).font(.title2.weight(.semibold))
            Text(L10n.text("Allow KeepUpdated to request administrator permission when an update needs it. Every installation still requires its own preview and confirmation."))
                .font(.callout).foregroundStyle(.secondary)
            if let status = store.helperStatus {
                Text(permissionTitle(status)).font(.headline)
                if status.status == "demo" {
                    Text(L10n.text("Demo mode does not check or change update permissions."))
                } else if status.status == "enabled" {
                    Text(L10n.text("Update permissions are enabled. Each installation still needs your approval."))
                } else { ReasonMessage(code: status.reasonCode) }
            } else { Text(L10n.text("Update permissions have not been checked.")) }
            if let status = store.helperStatus {
                DisclosureGroup(L10n.text("Technical Details")) {
                    Text(status.title + " · " + status.reasonCode).font(.caption).textSelection(.enabled)
                }
            }
            if store.busy { ActivityProgressView(label: store.activity) }
            HStack {
                Button(L10n.text("Check Permissions")) { store.refreshHelperStatus() }.disabled(store.busy)
                Button(L10n.text("Enable Update Permissions…")) { store.registerHelper() }
                    .disabled(store.busy || store.demo || store.helperStatus?.canRequestRegistration != true)
            }
            HStack { Spacer(); Button(L10n.text("Done")) { if let onClose { onClose() } else { dismiss() } }.keyboardShortcut(.defaultAction).disabled(store.busy) }
        }.padding(24).frame(minWidth: 400, maxWidth: .infinity, minHeight: 360, maxHeight: .infinity, alignment: .topLeading)
            .task { store.refreshHelperStatus() }
            // The native bridge presents the separate registration confirmation.
    }

    private func permissionTitle(_ status: HelperStatus) -> String {
        switch status.status {
        case "enabled": return L10n.text("Update permissions enabled")
        case "helper_not_registered": return L10n.text("Update permissions not enabled")
        case "system_settings_approval_required": return L10n.text("System Settings approval required")
        case "demo": return L10n.text("Demo Mode")
        default: return L10n.text("Update permissions unavailable")
        }
    }
}
