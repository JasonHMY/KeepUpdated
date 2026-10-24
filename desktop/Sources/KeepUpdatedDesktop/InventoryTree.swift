import Foundation

/// Presentation only: folders and version rows never become inventory or operation IDs.
struct InventoryTreeNode: Identifiable {
    let id: String
    let item: InventoryItem?
    let title: String
    var children: [InventoryTreeNode] = []
    var version: String? = nil
    var isTechnicalDetail: Bool { id.hasPrefix("technical:") }
    var displayTitle: String {
        if let item { return item.displayName }
        if isTechnicalDetail, let category = ItemCategory(rawValue: title) {
            return L10n.text("%@ details", category.title)
        }
        if id.hasPrefix("cache:"), let cache = items.first?.pluginCache {
            return "\(cache.id) · \(cache.version)"
        }
        if id.hasPrefix("skill-name:") { return title }
        if id.hasPrefix("family:") { return L10n.text("%@ · multiple installations", title) }
        if id.hasPrefix("aliases:") { return children.first?.displayTitle ?? title }
        return title
    }

    var items: [InventoryItem] { (item.map { [$0] } ?? []) + children.flatMap(\.items) }
    var uninstallIDs: [String] { Array(Set(items.map(\.id))).sorted() }
    // A parent owns its supporting records; only independent installations are selectable.
    var installations: [InventoryItem] {
        guard !isTechnicalDetail else { return [] }
        if let item {
            return InventoryTree.isSupporting(item) || item.owner == "app-component" ? [] : [item]
        }
        var seen: Set<String> = []
        let members = children.flatMap(\.installations).filter { seen.insert($0.id).inserted }
        let retainedPaths = Set(members.filter { $0.kind == "runtime" }.flatMap { [$0.path, $0.realPath ?? $0.path] })
        return members.filter { $0.kind != "executable" || !retainedPaths.contains($0.realPath ?? $0.path) }
    }
    var supportingItems: [InventoryItem] {
        let selected = Set(installations.map(\.id))
        var seen: Set<String> = []
        return items.filter { !selected.contains($0.id) && seen.insert($0.id).inserted }
    }
    var productTitle: String {
        if id.hasPrefix("family:") { return title }
        if id.hasPrefix("aliases:") { return children.first?.productTitle ?? title }
        return displayTitle
    }
    var productCategory: ItemCategory { (installations.first ?? items.first)?.category ?? .other }
    var applicationIconPaths: [String] { installations.filter { $0.kind == "application" }.map(\.path) }
    var summary: String? {
        guard !children.isEmpty else { return nil }
        let descendants = children.flatMap(\.items)
        if isTechnicalDetail {
            return L10n.text("%@ supporting records", String(descendants.count))
        }
        if id.hasPrefix("mcp-name:") {
            let clients = Array(Set(descendants.compactMap(\.clientName))).sorted()
            return L10n.text("%@ connections", String(descendants.count)) +
                (clients.isEmpty ? "" : " · " + clients.joined(separator: " · "))
        }
        if id.hasPrefix("aliases:"), descendants.allSatisfy({ $0.kind == "skill" }) {
            let clients = Array(Set(descendants.compactMap(\.clientName))).sorted()
            return clients.isEmpty ? L10n.text("%@ locations", String(descendants.count)) : clients.joined(separator: " · ")
        }
        if id.hasPrefix("skill-name:") {
            return L10n.text("%@ separate installations", String(children.count))
        }
        if item?.isDirectPythonTool == true {
            return L10n.text("Dependencies: %@", String(descendants.filter { $0.kind == "python-package" }.count))
        }
        if descendants.isEmpty {
            return L10n.text("%@ installed versions · read only", String(children.count))
        }
        let updates = descendants.filter(\.canUpdate).count
        let issues = descendants.filter { $0.health.status != "ok" || ["error", "unknown"].contains($0.update.status) }.count
        return L10n.text("Items: %@ · Updates: %@ · Need review: %@", String(descendants.count), String(updates), String(issues))
    }

    var displayCount: Int {
        if isTechnicalDetail { return 0 }
        if id.hasPrefix("plugin:") { return children.reduce(0) { $0 + $1.displayCount } }
        return 1
    }

    struct Row: Identifiable {
        let node: InventoryTreeNode
        let depth: Int
        let expanded: Bool
        var parentID: String? = nil
        var id: String { node.id }
    }

    static func rows(_ nodes: [Self], matching: Set<String>, expanded: Set<String>, filtering: Bool, collapsedWhileFiltering: Set<String> = []) -> [Row] {
        func visit(_ node: Self, depth: Int, parentID: String? = nil) -> [Row] {
            let matches = node.items.contains { matching.contains($0.id) }
            guard matches || node.version != nil else { return [] }
            if node.id.hasPrefix("plugin:") {
                return node.children.flatMap { visit($0, depth: depth, parentID: node.id) }
            }
            let open = !node.children.isEmpty && (filtering && !node.isTechnicalDetail ? !collapsedWhileFiltering.contains(node.id) : expanded.contains(node.id))
            var rows = [Row(node: node, depth: depth, expanded: open, parentID: parentID)]
            if open {
                rows += node.children.flatMap { visit($0, depth: depth + 1, parentID: node.id) }
            }
            return rows
        }
        return nodes.flatMap { visit($0, depth: 0) }
    }
}

enum InventoryTree {
    static func build(_ items: [InventoryItem]) -> [InventoryTreeNode] {
        let byID = Dictionary(items.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        var parents: [String: String] = [:]
        var cyclicApps: Set<String> = []
        let appLinks = Dictionary(uniqueKeysWithValues: byID.values.filter { $0.kind == "application" }.map {
            ($0.id, Set($0.related ?? []).filter { byID[$0]?.kind == "application" })
        })
        var inspected: Set<String> = []
        func inspect(_ id: String, path: [String]) {
            if let start = path.firstIndex(of: id) { cyclicApps.formUnion(path[start...]); return }
            guard !inspected.contains(id) else { return }
            for next in appLinks[id] ?? [] { inspect(next, path: path + [id]) }
            inspected.insert(id)
        }
        for id in appLinks.keys { inspect(id, path: []) }
        for item in items {
            let candidates: [InventoryItem]
            if item.kind == "application", !cyclicApps.contains(item.id) {
                let relatedApps = Set(item.related ?? []).compactMap { byID[$0] }.filter { $0.kind == "application" }
                // Conflicting application relationships remain flat, even if one path looks plausible.
                candidates = relatedApps.count == 1 ? relatedApps.filter {
                    $0.id != item.id && $0.path.lowercased().hasSuffix(".app") &&
                    item.path.lowercased().hasSuffix(".app") && contained(item, in: $0)
                } : []
            } else if item.kind == "python-package" {
                let environments = Set(item.evidence.filter { $0.hasPrefix("environment:") }.map { String($0.dropFirst("environment:".count)) })
                candidates = environments.count == 1 ? items.filter {
                    $0.kind == "python-environment" && environments.contains($0.path) && contained(item, in: $0)
                } : []
            } else { candidates = [] }
            if candidates.count == 1 { parents[item.id] = candidates[0].id }
        }
        // Defensive cycle guard: all affected entries stay independently visible.
        for id in Array(parents.keys) {
            var seen: Set<String> = [id]
            var next = parents[id]
            while let current = next {
                if !seen.insert(current).inserted {
                    for member in seen { parents.removeValue(forKey: member) }
                    break
                }
                next = parents[current]
            }
        }
        let children = Dictionary(grouping: items.filter { parents[$0.id] != nil }, by: { parents[$0.id]! })
        func node(_ item: InventoryItem) -> InventoryTreeNode {
            var descendants = (children[item.id] ?? []).map(node)
            if item.kind == "python-environment" {
                let tools = descendants.filter { $0.item?.isDirectPythonTool == true }
                if tools.count == 1, let tool = tools.first?.item {
                    let environment = InventoryTreeNode(id: key("item", [item.id]), item: item, title: item.displayName,
                        children: peers(descendants.filter { $0.item?.id != tool.id }, scope: key("item", [item.id])))
                    return InventoryTreeNode(id: key("item", [tool.id]), item: tool, title: tool.displayName, children: [environment])
                }
            }
            if item.kind == "formula", let versions = item.installedVersions, Set(versions).count > 1 {
                descendants += Array(Set(versions)).sorted().map { version in
                    InventoryTreeNode(id: key("version", [item.id, version]), item: nil, title: version, version: version)
                }
            }
            return InventoryTreeNode(id: key("item", [item.id]), item: item, title: item.displayName, children: peers(descendants, scope: key("item", [item.id])))
        }
        let roots = peers(items.filter { parents[$0.id] == nil }.map(node), scope: "root")
        var primary: [InventoryTreeNode] = []
        var supporting: [ItemCategory: [InventoryTreeNode]] = [:]
        for root in roots {
            if root.items.allSatisfy(isSupporting), let category = root.items.first?.category {
                supporting[category, default: []].append(root)
            } else {
                primary.append(root)
            }
        }
        return primary + ItemCategory.allCases.compactMap { category in
            guard let children = supporting[category], !children.isEmpty else { return nil }
            return InventoryTreeNode(id: key("technical", [category.rawValue]), item: nil, title: category.rawValue, children: children)
        }
    }

    static func isSupporting(_ item: InventoryItem) -> Bool {
        switch item.kind {
        case "python-package": return !item.isDirectPythonTool
        case "python-environment", "mcp-config", "service": return true
        case "skill": return item.isUnregisteredSkillCache || item.isUnverifiedSkillCache
        case "executable": return ["npm", "pnpm", "npx", "corepack", "pip", "pip3"].contains(item.name)
        default: return false
        }
    }

    static func displayCount(_ items: [InventoryItem], category: ItemCategory) -> Int {
        products(build(items.filter { $0.category == category })).filter { !$0.installations.isEmpty }.count
    }

    /// Flat navigation preserves full groups even when only one location matches a filter.
    static func products(_ nodes: [InventoryTreeNode], matching: Set<String>? = nil) -> [InventoryTreeNode] {
        var skills: [String: [InventoryTreeNode]] = [:]
        var products: [InventoryTreeNode] = []
        func collect(_ node: InventoryTreeNode) {
            if let item = node.item, item.kind == "skill" {
                skills[item.name, default: []].append(node)
            } else if node.item == nil, !node.items.isEmpty, node.items.allSatisfy({ $0.kind == "skill" }) {
                node.children.forEach(collect)
            } else {
                products.append(node)
            }
        }
        nodes.forEach(collect)
        for (name, locations) in skills {
            products.append(locations.count == 1 ? locations[0] : InventoryTreeNode(
                id: key("skill-name", ["root", name]), item: nil, title: name, children: sorted(locations)))
        }
        return sorted(products).filter { node in
            matching.map { ids in node.items.contains { ids.contains($0.id) } } ?? true
        }
    }

    private static func peers(_ nodes: [InventoryTreeNode], scope: String) -> [InventoryTreeNode] {
        var pluginGroups: [String: [InventoryTreeNode]] = [:]
        var plain: [InventoryTreeNode] = []
        for node in nodes {
            if let item = node.item, let plugin = validPlugin(item) {
                pluginGroups[key("plugin", [scope, plugin.client, plugin.id, plugin.scope, plugin.root]), default: []].append(node)
            } else if let item = node.item, item.kind == "skill", let cache = item.pluginCache,
                      canonical(cache.root) != nil, inside(item.path, cache.root) || inside(item.realPath ?? "", cache.root) {
                pluginGroups[key("cache", [scope, cache.client, cache.id, cache.root, cache.registration]), default: []].append(node)
            } else { plain.append(node) }
        }
        for (id, group) in pluginGroups {
            if id.hasPrefix("cache:"), let cache = group.first?.item?.pluginCache {
                plain.append(InventoryTreeNode(id: id, item: nil,
                    title: "\(cache.id) · \(cache.version)", children: aliases(group, scope: id)))
                continue
            }
            // A root that disagrees about its registered version/commit is not one reliable installation.
            let owners = Set(group.compactMap { $0.item?.plugin })
            if group.count > 1, owners.count == 1, let plugin = owners.first {
                plain.append(InventoryTreeNode(id: id, item: nil,
                    title: "\(plugin.id) · \(plugin.client) · \(plugin.scope)", children: aliases(group, scope: id)))
            } else { plain += group }
        }
        let normalSkills = plain.filter { $0.item?.kind == "skill" && $0.item?.plugin == nil && $0.item?.source != "plugin-cache" && $0.item?.owner != "system" }
        let normalIDs = Set(normalSkills.map(\.id))
        let other = plain.filter { !normalIDs.contains($0.id) }
        return sorted(mcpNames(families(other + skillNames(aliases(normalSkills, scope: scope), scope: scope), scope: scope), scope: scope))
    }

    private static func mcpNames(_ nodes: [InventoryTreeNode], scope: String) -> [InventoryTreeNode] {
        var groups: [String: [InventoryTreeNode]] = [:]
        var plain: [InventoryTreeNode] = []
        for node in nodes {
            guard let item = node.item, item.kind == "mcp-server" else { plain.append(node); continue }
            groups[item.name, default: []].append(node)
        }
        for (name, group) in groups {
            if group.count > 1 {
                plain.append(InventoryTreeNode(id: key("mcp-name", [scope, name]), item: nil, title: name, children: sorted(group)))
            } else { plain += group }
        }
        return plain
    }

    // Same-name folders are presentation groups, never a claim of content equivalence.
    private static func skillNames(_ nodes: [InventoryTreeNode], scope: String) -> [InventoryTreeNode] {
        var groups: [String: [InventoryTreeNode]] = [:]
        var plain: [InventoryTreeNode] = []
        for node in nodes {
            let entries = node.items
            guard let item = entries.first, entries.allSatisfy({ $0.kind == "skill" && $0.owner != "system" && $0.source != "plugin-cache" && $0.plugin == nil }) else {
                plain.append(node); continue
            }
            groups[item.name, default: []].append(node)
        }
        for (name, group) in groups {
            if group.count > 1 {
                let id = key("skill-name", [scope, name])
                plain.append(InventoryTreeNode(id: id, item: nil, title: name, children: aliases(group, scope: id)))
            } else { plain += group }
        }
        return plain
    }

    static func skillGroupCount(_ items: [InventoryItem]) -> Int {
        displayCount(items, category: .skills)
    }

    private static func families(_ nodes: [InventoryTreeNode], scope: String) -> [InventoryTreeNode] {
        var groups: [String: [InventoryTreeNode]] = [:]
        var titles: [String: String] = [:]
        var plain: [InventoryTreeNode] = []
        for node in nodes {
            guard let item = node.item, let family = family(item) else { plain.append(node); continue }
            let id = key("family", [scope, family.id])
            groups[id, default: []].append(node)
            titles[id] = family.title
        }
        for (id, group) in groups {
            if group.count > 1 {
                plain.append(InventoryTreeNode(id: id, item: nil, title: titles[id]!, children: aliases(group, scope: id)))
            } else { plain += group }
        }
        return aliases(plain, scope: scope)
    }

    private static func family(_ item: InventoryItem) -> (id: String, title: String)? {
        guard let path = canonical(item.path) else { return nil }
        if ["runtime", "executable"].contains(item.kind), item.category == .runtimes {
            let names = ["java": "Java", "node": "Node.js", "python": "Python", "python3": "Python", "bun": "Bun", "go": "Go"]
            if let title = names[item.name.lowercased()] {
                // Language families are presentation only; every source keeps its original operation guards.
                return (key("runtime-family", [title]), title)
            }
        }
        if item.kind == "formula", item.owner == "homebrew", !item.source.isEmpty, item.source != "unknown",
           item.name.range(of: #"^[a-z0-9][a-z0-9+_.-]*(?:@[0-9]+(?:\.[0-9]+)*)?$"#, options: .regularExpression) != nil {
            let url = URL(fileURLWithPath: path)
            let cellar = url.deletingLastPathComponent()
            guard url.lastPathComponent == item.name, cellar.lastPathComponent == "Cellar" else { return nil }
            let base = String(item.name.split(separator: "@")[0])
            return (key("family", [item.kind, item.source, base]), base)
        }
        if ["application", "node-package", "python-package"].contains(item.kind),
           let identity = item.identity, !identity.isEmpty {
            let source = item.kind == "application" ? "" : item.source
            return (key("family", [item.kind, source, identity]), item.name)
        }
        return nil
    }

    private static func aliases(_ nodes: [InventoryTreeNode], scope: String) -> [InventoryTreeNode] {
        var groups: [String: [InventoryTreeNode]] = [:]
        var plain: [InventoryTreeNode] = []
        for node in nodes {
            guard let item = node.item, item.category != .mcp,
                  let real = item.realPath, canonical(real) != nil else { plain.append(node); continue }
            groups[key("aliases", [scope, item.kind, item.category.rawValue, real]), default: []].append(node)
        }
        for (id, group) in groups {
            if group.count > 1, Set(group.map { $0.item?.plugin }).count == 1 {
                plain.append(InventoryTreeNode(id: id, item: nil,
                    title: group[0].title, children: sorted(group)))
            } else { plain += group }
        }
        return sorted(plain)
    }

    private static func validPlugin(_ item: InventoryItem) -> PluginOwner? {
        guard item.kind == "skill", let plugin = item.plugin,
              !plugin.client.isEmpty, !plugin.id.isEmpty, !plugin.scope.isEmpty,
              canonical(plugin.root) != nil,
              inside(item.path, plugin.root) || inside(item.realPath ?? "", plugin.root) else { return nil }
        return plugin
    }
    private static func contained(_ child: InventoryItem, in parent: InventoryItem) -> Bool {
        guard inside(child.path, parent.path) else { return false }
        if let childReal = child.realPath, let parentReal = parent.realPath {
            return inside(childReal, parentReal)
        }
        return true
    }
    private static func canonical(_ path: String) -> String? {
        guard path.hasPrefix("/"), path != "/", !path.contains("//"),
              !path.split(separator: "/").contains(where: { $0 == "." || $0 == ".." }) else { return nil }
        return path.hasSuffix("/") ? String(path.dropLast()) : path
    }
    private static func inside(_ child: String, _ parent: String) -> Bool {
        guard let child = canonical(child), let parent = canonical(parent) else { return false }
        return child.hasPrefix(parent + "/")
    }
    private static func key(_ kind: String, _ values: [String]) -> String {
        kind + ":" + (try! JSONEncoder().encode(values)).base64EncodedString()
    }
    private static func sorted(_ nodes: [InventoryTreeNode]) -> [InventoryTreeNode] {
        nodes.sorted {
            let order = $0.title.localizedStandardCompare($1.title)
            return order == .orderedSame ? $0.id < $1.id : order == .orderedAscending
        }
    }
}
