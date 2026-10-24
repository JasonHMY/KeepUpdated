import Foundation
import Combine

enum AppLanguage: String, CaseIterable, Identifiable {
    case system, english = "en", simplifiedChinese = "zh-Hans"
    var id: String { rawValue }
    var title: String {
        switch self {
        case .system: return L10n.text("Follow System")
        case .english: return L10n.text("English")
        case .simplifiedChinese: return L10n.text("Simplified Chinese")
        }
    }

    func resolved(preferredLanguages: [String] = Locale.preferredLanguages) -> AppLanguage {
        guard self == .system else { return self }
        // Use the primary system language. Unsupported languages fall back to English.
        guard let primary = preferredLanguages.first?.replacingOccurrences(of: "_", with: "-").lowercased() else { return .english }
        if primary == "zh" || primary.hasPrefix("zh-hans") || ["zh-cn", "zh-sg"].contains(primary) { return .simplifiedChinese }
        return .english
    }
}

final class LanguageSettings: ObservableObject {
    static let shared = LanguageSettings(defaults: L10n.defaults)
    private let defaults: UserDefaults
    @Published var selection: AppLanguage {
        didSet { defaults.set(selection.rawValue, forKey: L10n.preferenceKey) }
    }
    init(defaults: UserDefaults) {
        self.defaults = defaults
        selection = AppLanguage(rawValue: defaults.string(forKey: L10n.preferenceKey) ?? "system") ?? .english
    }
    var locale: Locale { Locale(identifier: selection.resolved().rawValue) }
}

enum L10n {
    static let preferenceKey = "interfaceLanguage"
    // Demo preferences are isolated from the installed app, including language persistence.
    static var defaults: UserDefaults = ProcessInfo.processInfo.arguments.contains("--demo")
        ? UserDefaults(suiteName: (Bundle.main.bundleIdentifier ?? "com.keepupdated.desktop") + ".demo")! : .standard
    static var language: AppLanguage {
        (AppLanguage(rawValue: defaults.string(forKey: preferenceKey) ?? "system") ?? .english).resolved()
    }
    static var locale: Locale { Locale(identifier: language.rawValue) }
    static let resources: Bundle = {
        // SwiftPM's generated accessor also supports command-line tests. Packaged apps
        // keep the bundle in Contents/Resources so it is covered by the app signature.
        if let url = Bundle.main.url(forResource: "KeepUpdatedDesktop_KeepUpdatedDesktop", withExtension: "bundle"), let bundle = Bundle(url: url) { return bundle }
        return .module
    }()
    private static let tables: [AppLanguage: [String: String]] = [
        .english: load(.english), .simplifiedChinese: load(.simplifiedChinese)
    ]
    private static func load(_ language: AppLanguage) -> [String: String] {
        guard let url = resources.url(forResource: "Localizable", withExtension: "strings", subdirectory: nil, localization: language.rawValue),
              let data = try? Data(contentsOf: url),
              let table = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: String] else { return [:] }
        return table
    }
    static func catalog(_ language: AppLanguage) -> [String: String] { tables[language] ?? [:] }
    static func text(_ key: String, _ arguments: String...) -> String {
        format(key, arguments: arguments, language: language)
    }
    static func format(_ key: String, arguments: [String] = [], language: AppLanguage) -> String {
        let resolved = language.resolved()
        let template = translation(key, selected: tables[resolved] ?? [:], english: tables[.english] ?? [:])
        guard !arguments.isEmpty else { return template }
        return String(format: template, locale: Locale(identifier: resolved.rawValue), arguments: arguments)
    }
    static func translation(_ key: String, selected: [String: String], english: [String: String]) -> String {
        selected[key] ?? english[key] ?? key
    }
    static var listSeparator: String { language == .simplifiedChinese ? "、" : ", " }
}

// Keep notices as keys and arguments so changing language also updates existing notices.
struct LocalizedMessage: ExpressibleByStringLiteral {
    private let key: String
    private let arguments: [LocalizedMessage]
    private let verbatim: Bool
    init(_ key: String, _ arguments: String...) {
        self.key = key
        self.arguments = arguments.map { LocalizedMessage(verbatim: $0) }
        verbatim = false
    }
    init(_ key: String, localizedArgument: String) {
        self.key = key
        arguments = [LocalizedMessage(localizedArgument)]
        verbatim = false
    }
    private init(verbatim text: String) { key = text; arguments = []; verbatim = true }
    init(stringLiteral value: String) { self.init(value) }
    var text: String { verbatim ? key : L10n.format(key, arguments: arguments.map(\.text), language: L10n.language) }
}
