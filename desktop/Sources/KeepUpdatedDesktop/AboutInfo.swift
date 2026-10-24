import Foundation

struct AboutInfo {
    let version: String?
    let build: String?
    let source: Source?

    struct Source: Decodable {
        let commit: String
        let treeSHA256: String
        let dirty: Bool
        enum CodingKeys: String, CodingKey {
            case commit = "source_commit", treeSHA256 = "source_tree_sha256", dirty = "working_tree_dirty"
        }
    }

    init(bundle: Bundle = .main) {
        let metadata = bundle.url(forResource: "DESKTOP_BUILDINFO", withExtension: "json")
            .flatMap { try? Data(contentsOf: $0) }
        self.init(info: bundle.infoDictionary ?? [:], metadata: metadata)
    }

    init(info: [String: Any], metadata: Data?) {
        version = (info["CFBundleShortVersionString"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        build = (info["CFBundleVersion"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        source = metadata.flatMap { try? JSONDecoder().decode(Source.self, from: $0) }
    }

    var versionText: String {
        L10n.text("Version %@ · Build %@", version ?? L10n.text("Unknown"), build ?? L10n.text("Unknown"))
    }
    var sourceText: String {
        guard let source, !source.commit.isEmpty else { return L10n.text("Source information unavailable") }
        return source.dirty ? L10n.text("Source: %@ (local changes)", source.commit) : L10n.text("Source: %@", source.commit)
    }

    static let guideURL = URL(string: "https://github.com/JasonHMY/KeepUpdated/blob/main/docs/desktop.md")!
    static let supportURL = URL(string: "https://github.com/JasonHMY/KeepUpdated/issues")!
    static let privacyURL = URL(string: "https://github.com/JasonHMY/KeepUpdated#results-and-privacy")!
    static let securityURL = URL(string: "https://github.com/JasonHMY/KeepUpdated/blob/main/SECURITY.md")!
}
