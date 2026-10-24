import Foundation

extension InventoryItem {
    var clientName: String? {
        if let client = plugin?.client ?? pluginCache?.client {
            return Self.clientTitle(client)
        }
        let location = evidence.first(where: { $0.hasPrefix("config:") }).map { String($0.dropFirst(7)) } ?? path
        if location.hasSuffix("/.claude.json") { return "Claude" }
        let roots = [
            ("/.codex/", "Codex"), ("/.claude/", "Claude"), ("/.hermes/", "Hermes"),
            ("/.cursor/", "Cursor"), ("/.openclaw/", "OpenClaw"), ("/.gemini/", "Gemini"),
            ("/.cc-switch/", "CC Switch"), ("/.config/opencode/", "OpenCode"),
            ("/.config/Claude/", "Claude"), ("/Library/Application Support/Claude/", "Claude"),
            ("/Library/Application Support/Code/User/", "VS Code"), ("/.vscode/", "VS Code")
        ]
        if let match = roots.first(where: { location.contains($0.0) }) { return match.1 }
        if location.contains("/.agents/") { return L10n.text("Shared") }
        return nil
    }

    private static func clientTitle(_ client: String) -> String {
        switch client.lowercased() {
        case "codex": return "Codex"
        case "claude": return "Claude"
        case "hermes": return "Hermes"
        case "cursor": return "Cursor"
        case "opencode": return "OpenCode"
        case "openclaw": return "OpenClaw"
        default: return client
        }
    }

    var installationSummary: String? {
        if kind == "mcp-server" {
            let state = L10n.text(evidence.contains("disabled") ? "Disabled" : "Configured")
            let connection: String
            if source == "local-command" { connection = L10n.text("Local program") }
            else if evidence.contains("endpoint:loopback") { connection = L10n.text("Local connection") }
            else if evidence.contains("endpoint:remote") { connection = L10n.text("Network connection") }
            else if source == "remote" { connection = L10n.text("Connection address unverified") }
            else { connection = L10n.text("Connection needs review") }
            return [clientName, state, connection].compactMap { $0 }.joined(separator: " · ")
        }
        if kind == "skill" {
            var parts = [clientName].compactMap { $0 }
            if let plugin { parts.append(L10n.text("Plugin: %@", plugin.id)) }
            if owner == "system" { parts.append(L10n.text("Included with client")) }
            return parts.isEmpty ? nil : parts.joined(separator: " · ")
        }
        if kind == "python-package", isDirectPythonTool {
            return L10n.text("Managed with %@", owner)
        }
        return nil
    }
}

extension ItemCategory {
    var explanation: String {
        switch self {
        case .all: return L10n.text("Apps and tools first. Dependencies, caches and configuration are in the details below.")
        case .applications: return L10n.text("Installed Mac apps. Each app keeps its own update method.")
        case .skills: return L10n.text("Skills available to your AI clients. Shared locations and plugin caches are kept in details.")
        case .mcp: return L10n.text("Connections used by AI clients. Open an item to choose its client connections; removing a connection does not remove the service.")
        case .homebrew: return L10n.text("Software managed by Homebrew.")
        case .npm: return L10n.text("Global packages managed by npm. Check for updates to verify available versions.")
        case .pnpm: return L10n.text("Global packages managed by pnpm. Check for updates to verify available versions.")
        case .bun: return L10n.text("Global packages managed by Bun. Bun itself is listed under Runtimes.")
        case .python: return L10n.text("Python tools managed by pipx or uv. Their environments and dependency libraries are in details.")
        case .runtimes: return L10n.text("Language engines such as Node.js, Python, Java and Bun that other programs run on.")
        case .commandLine: return L10n.text("Tools used from a terminal, such as Codex or gcloud. Package-manager entry points are in details.")
        case .services: return L10n.text("Background launch settings. Update the app or package that provides them.")
        case .system: return L10n.text("Your installed macOS version and separately confirmed system-update options.")
        case .other: return L10n.text("Other detected installation records.")
        }
    }
}
