import SwiftUI

struct OperationReviewView: View {
    let item: PlanItem
    @ObservedObject var store: InventoryStore
    @ObservedObject private var language = LanguageSettings.shared
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var expanded = false
    @State private var choices = ReviewChoices()
    var body: some View {
        DisclosureGroup(L10n.text("Review Specific Permissions"), isExpanded: $expanded) {
            VStack(alignment: .leading, spacing: 10) {
                Text(L10n.text("These choices create a new preview for this item only. Nothing runs until you confirm that preview."))
                    .font(.caption).foregroundStyle(.secondary)
                ForEach(item.consentCodes, id: \.self) { code in
                    if let consent = OperationConsent(rawValue: code) {
                        Toggle(consent.title, isOn: selected(code, in: \.consents))
                    } else {
                        Text(L10n.text("This permission is not supported by this app version: %@", code)).foregroundStyle(.orange)
                    }
                }
                if (item.requiresAdmin == true && !item.consentCodes.contains("privileged_install")) || (item.requiresRestart == true && !item.consentCodes.contains("restart_system")) {
                    Text(L10n.text("This preview lacks the specific permission contract for elevation or restart. It cannot run in this app."))
                        .font(.caption).foregroundStyle(.orange)
                }
                if item.requiresRestart == true {
                    Text(L10n.text("Installing an update does not restart this Mac. Restart requires its own preview and confirmation."))
                        .font(.caption).foregroundStyle(.secondary)
                }
                if choices.mcpReference.isEmpty && (item.requiresScripts == true || item.reason == "mcp_configuration_may_reload_client_confirm_separately") {
                    Toggle(item.requiresScripts == true ? L10n.text("Allow the listed installation scripts") : L10n.text("Allow the owning agent to reload this configuration"), isOn: $choices.special)
                }
                if item.requiresQuit == true && store.preview?.action == .update {
                    Text(L10n.text("Quit requests are graceful. Unsaved work or a cancelled exit must stop the update."))
                        .font(.caption).foregroundStyle(.secondary)
                    ForEach(item.exitTargets) { app in
                        VStack(alignment: .leading, spacing: 5) {
                            Toggle(L10n.text("Quit %@ before updating", app.identity.isEmpty ? app.path : app.identity), isOn: Binding(
                                get: { choices.quitApps.contains(app.id) },
                                set: { selected in
                                    if selected { choices.quitApps.insert(app.id) }
                                    else { choices.quitApps.remove(app.id); choices.reopenApps.remove(app.id) }
                                }))
                            Text(app.path).font(.caption).textSelection(.enabled)
                            Toggle(L10n.text("Reopen this app after a verified update"), isOn: selected(app.id, in: \.reopenApps))
                                .disabled(!choices.quitApps.contains(app.id))
                        }
                    }
                    if item.exitTargets.isEmpty {
                        Text(L10n.text("No exact application identity is available for an exit request. No automatic exit is allowed."))
                            .font(.caption).foregroundStyle(.orange)
                    }
                }
                if item.kind == "mcp-server" && store.preview?.action == .update {
                    referenceControls
                }
                Button(L10n.text("Create Separate Preview")) { store.planReviewed(item, choices: choices) }
                    .disabled(store.busy || (!choices.mcpReference.isEmpty && !choices.mcpReferenceApproved) || !item.consentCodes.allSatisfy { OperationConsent(rawValue: $0) != nil })
            }.padding(.top, 8)
        }.font(.callout).disabled(store.busy)
            .animation(reduceMotion ? nil : NativeMotion.disclosure, value: expanded)
            .animation(reduceMotion ? nil : NativeMotion.selection, value: choices.mcpReference.isEmpty)
    }
    private var referenceControls: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let discovery = item.mcpDiscovery {
                if let configured = discovery.configuredReference {
                    Text(L10n.text("Configured reference: %@", configured)).textSelection(.enabled)
                }
                Text(L10n.text("Installed and running versions were not checked."))
                    .font(.caption).foregroundStyle(.secondary)
                if discovery.selectable, let candidate = discovery.candidate {
                    Text(L10n.text("Registry candidate: %@", candidate.reference)).textSelection(.enabled)
                    Text(L10n.text("Verified default npm source; exact same-package replacement."))
                        .font(.caption).foregroundStyle(.secondary)
                    if let observation = discovery.observedAt {
                        Text(L10n.text("Observed in preview: %@", observation)).font(.caption).foregroundStyle(.secondary)
                    }
                    Button(L10n.text("Select This Registry Candidate")) { choices.selectMCPCandidate(discovery) }
                } else {
                    Text(ReasonText.describe(discovery.reason)).font(.caption).foregroundStyle(.secondary)
                }
            }
            TextField(L10n.text("Exact replacement package reference"), text: $choices.mcpReference)
                .textFieldStyle(.roundedBorder)
            Text(L10n.text("Enter the exact package and version. This changes the selected MCP configuration reference only; it does not install or start the server."))
                .font(.caption).foregroundStyle(.secondary)
            if !choices.mcpReference.isEmpty {
                Text(L10n.text(choices.mcpCandidate.isEmpty ? "Manual reference: registry availability and source are not verified." : "Selected registry candidate is bound to this configuration and source."))
                    .font(.caption).foregroundStyle(.secondary)
            }
            Toggle(L10n.text("Allow this exact configuration change and possible client reload"), isOn: $choices.mcpReferenceApproved)
                .disabled(choices.mcpReference.isEmpty)
        }
    }
    private func selected(_ value: String, in keyPath: WritableKeyPath<ReviewChoices, Set<String>>) -> Binding<Bool> {
        Binding(get: { choices[keyPath: keyPath].contains(value) }, set: { selected in
            if selected { choices[keyPath: keyPath].insert(value) }
            else { choices[keyPath: keyPath].remove(value) }
        })
    }
}
