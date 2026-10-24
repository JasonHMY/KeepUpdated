import Foundation

struct PluginOwner: Codable, Hashable {
    var client: String
    var id: String
    var scope: String
    var root: String
    var version: String
    var commit: String?
}

struct PluginUpdatePreview: Codable {
    var owner: PluginOwner
    var marketplace: String
    var entry: String
    var commit: String
    var sourceDigest: String?
    var currentTree: String?
    var targetTree: String?
    var fingerprint: String?
    var skills: [String]?
    var reloadRequired: Bool?
    var remoteVerified: Bool?
    var refreshMarketplace: Bool?
    var marketplaceBeforeCommit: String?

    var provenance: String {
        if refreshMarketplace == true {
            return remoteVerified == true
                ? L10n.text("Verified remote snapshot. This preview also includes refreshing the selected marketplace to the approved commit.")
                : L10n.text("Marketplace refresh is included, but remote verification is not established by this preview.")
        }
        if remoteVerified == true { return L10n.text("The remote target was verified when this preview was created.") }
        return L10n.text("Cached marketplace snapshot. Remote freshness was not checked.")
    }
    static var reloadInstructions: String {
        L10n.text("Existing Claude sessions need plugin reload. Run /reload-plugins in Claude or start a new session. The loaded session version has not been verified.")
    }
}
