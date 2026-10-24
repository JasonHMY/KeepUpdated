import Foundation

enum ScanLocationKind: String, CaseIterable, Identifiable, Codable {
    case mcpConfig, skillsRoot, project, appRoot
    var id: String { rawValue }
    var label: String {
        switch self {
        case .mcpConfig: return L10n.text("MCP configuration files")
        case .skillsRoot: return L10n.text("Skills folders")
        case .project: return L10n.text("Project folders")
        case .appRoot: return L10n.text("Application folders")
        }
    }
    var flag: String {
        switch self {
        case .mcpConfig: return "--mcp-config"
        case .skillsRoot: return "--skills-root"
        case .project: return "--project"
        case .appRoot: return "--app-root"
        }
    }
    var selectsDirectory: Bool { self != .mcpConfig }
}

struct ScanLocations: Codable, Equatable {
    var mcpConfig: [String] = []
    var skillsRoot: [String] = []
    var project: [String] = []
    var appRoot: [String] = []
    subscript(kind: ScanLocationKind) -> [String] {
        get {
            switch kind {
            case .mcpConfig: return mcpConfig
            case .skillsRoot: return skillsRoot
            case .project: return project
            case .appRoot: return appRoot
            }
        }
        set {
            switch kind {
            case .mcpConfig: mcpConfig = newValue
            case .skillsRoot: skillsRoot = newValue
            case .project: project = newValue
            case .appRoot: appRoot = newValue
            }
        }
    }
    var arguments: [String] {
        ScanLocationKind.allCases.flatMap { kind in self[kind].flatMap { [kind.flag, $0] } }
    }
    static func load(defaults: UserDefaults = .standard) -> ScanLocations {
        guard let data = defaults.data(forKey: "scanLocations"), let value = try? JSONDecoder().decode(ScanLocations.self, from: data) else { return ScanLocations() }
        return value
    }
    func save(defaults: UserDefaults = .standard) {
        if let data = try? JSONEncoder().encode(self) { defaults.set(data, forKey: "scanLocations") }
    }
}
