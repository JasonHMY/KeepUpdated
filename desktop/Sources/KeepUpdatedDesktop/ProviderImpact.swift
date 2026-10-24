import Foundation

struct RuntimeReview: Codable {
    var owner: String
    var currentVersion: String
    var targetVersion: String
    var compatibility: String
    var consumers: [Consumer]
    var unknownImpact: [String]
    struct Consumer: Codable {
        var id: String?
        var name: String
        var version: String?
        var path: String?
        var fingerprint: String?
    }
    var compatibilityLabel: String {
        switch compatibility {
        case "same_major": return L10n.text("Same major version; dependency impact still needs review")
        case "major_change": return L10n.text("Major version change")
        case "pre_1_minor_change": return L10n.text("Pre-1.0 minor version change")
        default: return L10n.text("Compatibility unverified")
        }
    }
}

struct MarketplaceRefresh: Codable {
    var stateKnown: Bool
    var attempted: Bool
    var commandSucceeded: Bool
    var changed: Bool
    var beforeCommit: String
    var afterCommit: String?
    var reason: String
    var stateLabel: String {
        if !attempted { return L10n.text("Marketplace refresh not attempted") }
        if !stateKnown { return L10n.text("Marketplace refresh state unverified") }
        return changed ? L10n.text("Marketplace metadata changed") : L10n.text("Marketplace metadata unchanged")
    }
}
