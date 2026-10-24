import SwiftUI

struct PluginOwnerView: View {
    let plugin: PluginOwner
    @ObservedObject private var language = LanguageSettings.shared
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(L10n.text("Registered on-disk plugin")).font(.subheadline.weight(.semibold))
            Text(plugin.id + " · " + plugin.client).textSelection(.enabled)
            Text(L10n.text("Registered version: %@", plugin.version)).font(.caption)
            Text(L10n.text("Installation scope: %@", plugin.scope)).font(.caption)
            Text(L10n.text("This registration does not establish which version a running session has loaded."))
                .font(.caption).foregroundStyle(.secondary)
            Text(plugin.root).font(.caption).textSelection(.enabled)
        }
    }
}

struct PluginUpdatePreviewView: View {
    let plugin: PluginUpdatePreview
    @ObservedObject private var language = LanguageSettings.shared
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(L10n.text("Whole-plugin update: %@", plugin.owner.id)).font(.callout.weight(.medium))
            Text(L10n.text("This updates the owning plugin and its included Skills together, not just the selected Skill file."))
                .font(.caption).foregroundStyle(.secondary)
            Text(plugin.provenance).font(.callout)
            Text(L10n.text("Approved commit: %@", plugin.commit)).font(.caption.monospaced()).textSelection(.enabled)
            if let before = plugin.marketplaceBeforeCommit, plugin.refreshMarketplace == true {
                Text(L10n.text("Marketplace commit before refresh: %@", before)).font(.caption.monospaced()).textSelection(.enabled)
            }
            if let skills = plugin.skills, !skills.isEmpty {
                DisclosureGroup(L10n.text("Included Skills (%@)", String(skills.count))) {
                    Text(skills.joined(separator: "\n")).font(.caption).textSelection(.enabled)
                }
            }
            if plugin.reloadRequired == true {
                Text(PluginUpdatePreview.reloadInstructions).font(.caption).foregroundStyle(.orange)
            }
        }.padding(.vertical, 5)
    }
}
