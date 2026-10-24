import Foundation

// The UI preference affects our copy only, never authorization or target identity.
public func authorizationLanguage(preference: String?, preferredLanguages: [String]) -> String {
    switch preference ?? "system" {
    case "en": return "en"
    case "zh-Hans": return "zh-Hans"
    case "system":
        let primary = preferredLanguages.first?.replacingOccurrences(of: "_", with: "-").lowercased() ?? "en"
        return primary == "zh" || primary.hasPrefix("zh-hans") || ["zh-cn", "zh-sg"].contains(primary) ? "zh-Hans" : "en"
    default: return "en"
    }
}
