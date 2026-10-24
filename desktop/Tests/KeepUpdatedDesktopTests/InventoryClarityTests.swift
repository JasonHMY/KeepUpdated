import XCTest
@testable import KeepUpdatedDesktop

final class InventoryClarityTests: XCTestCase {
    @MainActor func testPythonToolOwnsItsDetailsAndDependenciesCannotBeSelected() {
        let tool = DemoData.pythonTool("tool", "example-tool", "uv", "current")
        let environmentPath = String(tool.evidence.last!.dropFirst("environment:".count))
        var environment = DemoData.item("environment", "example-tool", "python-environment", "uv", "unsupported")
        environment.path = environmentPath
        var dependency = tool
        dependency.id = "dependency"; dependency.name = "example-library"
        dependency.path = environmentPath + "/lib/python3/site-packages/example_library.dist-info"
        dependency.update.status = "available"
        var global = dependency
        global.id = "global"; global.owner = "global-python"; global.evidence = []
        var report = DemoData.report
        report.items = [dependency, environment, tool, global]
        let store = InventoryStore(demo: true); store.replaceReport(report)
        XCTAssertEqual(store.count(.python), 1)
        XCTAssertEqual(store.visibleDisplayCount, 1)
        XCTAssertEqual(store.updateCount, 0, "Dependency versions must not inflate the main update count")
        XCTAssertEqual(Set(store.tree.flatMap(\.items).map(\.id)), Set(report.items.map(\.id)))
        let row = store.tree.first { !$0.isTechnicalDetail }
        XCTAssertEqual(row?.item?.id, tool.id)
        XCTAssertEqual(row?.children.first?.item?.id, environment.id)
        XCTAssertEqual(row?.children.first?.children.first?.item?.id, dependency.id)
        XCTAssertTrue(store.canPreviewUpdate(tool))
        XCTAssertFalse(store.canPreviewUpdate(dependency), "A newer dependency version is not an independently managed tool")
        store.selectVisible()
        XCTAssertEqual(store.selectedIDs, [tool.id])
        store.setSelected(dependency.id, selected: true)
        XCTAssertEqual(store.selectedIDs, [tool.id])
    }

    @MainActor func testMCPGroupingPreservesConnectionsAndShowsClientAndLocation() {
        var local = DemoData.item("local", "Example Service", "mcp-server", "configuration", "unsupported")
        local.source = "remote"
        local.evidence = ["config:/Users/example/.codex/config.toml", "endpoint:loopback", "disabled"]
        local.health = .init(status: "unknown", code: "disabled_not_started")
        local.update.code = "remote_not_locally_managed"
        var remote = local; remote.id = "remote"
        remote.evidence = ["config:/Users/example/.hermes/config.yaml", "endpoint:remote"]
        remote.health.code = "remote_not_contacted"
        let config = DemoData.item("configuration", "MCP configuration", "mcp-config", "configuration", "unsupported")
        var report = DemoData.report; report.items = [local, remote, config]
        let store = InventoryStore(demo: true); store.replaceReport(report)
        XCTAssertEqual(store.count(.mcp), 1)
        XCTAssertEqual(store.count(.all), 1)
        XCTAssertEqual(store.visibleDisplayCount, 1)
        let group = store.tree.first { $0.id.hasPrefix("mcp-name:") }!
        XCTAssertNil(group.item, "A display group must never become an operation ID")
        XCTAssertEqual(Set(group.children.compactMap { $0.item?.id }), ["local", "remote"])
        XCTAssertEqual(local.statusLabel, L10n.text("Disabled"))
        XCTAssertTrue(local.installationSummary?.contains("Codex") == true)
        XCTAssertTrue(local.installationSummary?.contains(L10n.text("Local connection")) == true)
        XCTAssertEqual(remote.statusLabel, L10n.text("Network connection"))
        XCTAssertTrue(remote.installationSummary?.contains("Hermes") == true)
        let rows = InventoryTreeNode.rows(store.tree, matching: Set(report.items.map(\.id)), expanded: [group.id], filtering: false)
        XCTAssertEqual(Set(rows.compactMap { $0.node.item?.id }), ["local", "remote"])
        XCTAssertEqual(Set(store.tree.flatMap(\.items).map(\.id)), ["local", "remote", "configuration"])

        var command = local
        command.id = "command"; command.source = "local-command"; command.evidence = []
        command.update.code = "use_installation_owner"
        var package = DemoData.item("package", "example-server", "node-package", "npm", "unknown")
        package.update.code = "online_query_required"
        command.related = [package.id]
        report.items = [command, package]; store.replaceReport(report)
        XCTAssertTrue(store.rowSummary(command, group: nil)?.contains(L10n.text("Update via %@: %@", "npm", "example-server")) == true)
        XCTAssertTrue(store.canPreviewUpdate(command))
        store.setSelected(command.id, selected: true)
        XCTAssertEqual(store.selectedIDs, [command.id])
        for (path, client) in [
            ("/Users/example/.claude.json", "Claude"),
            ("/Users/example/Library/Application Support/Code/User/mcp.json", "VS Code"),
            ("/Projects/example/.vscode/mcp.json", "VS Code")
        ] {
            command.evidence = ["config:" + path]
            XCTAssertEqual(command.clientName, client)
        }
    }

    @MainActor func testSkillAliasesAndCachesKeepEvidenceWithoutInflatingMainCount() {
        var skill = DemoData.item("one", "writing-helper", "skill", "git", "current")
        skill.path = "/Users/example/.codex/skills/writing-helper/SKILL.md"
        skill.realPath = "/Shared/writing-helper/SKILL.md"
        var alias = skill; alias.id = "alias"; alias.path = "/Users/example/.claude/skills/writing-helper/SKILL.md"
        var cache = skill; cache.id = "cache"; cache.path = "/Cache/plugin/skills/writing-helper/SKILL.md"
        cache.realPath = cache.path; cache.owner = "plugin-cache"; cache.source = "plugin-cache"
        cache.pluginCache = .init(client: "codex", root: "/Cache/plugin", id: "example", version: "1", registration: "unverified")
        var report = DemoData.report; report.items = [skill, alias, cache]
        let store = InventoryStore(demo: true); store.replaceReport(report)
        XCTAssertEqual(store.count(.skills), 1)
        XCTAssertEqual(store.count(.all), 1)
        XCTAssertEqual(store.visibleDisplayCount, 1)
        let group = store.tree.first { !$0.isTechnicalDetail }!
        XCTAssertEqual(group.displayTitle, skill.name)
        XCTAssertEqual(group.summary, "Claude · Codex")
        XCTAssertEqual(Set(group.items.map(\.id)), ["one", "alias"])
        XCTAssertEqual(Set(store.tree.flatMap(\.items).map(\.id)), ["one", "alias", "cache"])
        let rows = InventoryTreeNode.rows(store.tree, matching: Set(report.items.map(\.id)), expanded: [], filtering: false)
        XCTAssertFalse(rows.contains { $0.node.item?.id == "cache" })
        let details = store.tree.first { $0.isTechnicalDetail }!
        let opened = InventoryTreeNode.rows(store.tree, matching: Set(report.items.map(\.id)), expanded: [details.id, details.children[0].id], filtering: false)
        XCTAssertTrue(opened.contains { $0.node.item?.id == "cache" })
        XCTAssertFalse(store.canPreviewUpdate(cache))
    }

    @MainActor func testCountsAgreeWithHomebrewFamiliesAndEmptyCategoriesStayOutOfTheWay() {
        func formula(_ version: String) -> InventoryItem {
            var item = DemoData.item(version, "python@" + version, "formula", "homebrew", "current")
            item.path = "/Brew/Cellar/" + item.name; item.source = "homebrew/core"
            return item
        }
        var report = DemoData.report; report.items = [formula("3.11"), formula("3.12")]
        let store = InventoryStore(demo: true); store.replaceReport(report)
        XCTAssertEqual(store.count(.homebrew), 1)
        XCTAssertEqual(store.count(.all), 1)
        XCTAssertEqual(store.visibleDisplayCount, 1)
        XCTAssertFalse(store.visibleCategories.contains(.bun))
        XCTAssertFalse(store.visibleCategories.contains(.services))
        store.category = .homebrew
        XCTAssertEqual(store.visibleDisplayCount, 1)
        store.query = "3.12"
        XCTAssertEqual(store.visibleDisplayCount, 1)
        XCTAssertEqual(Set(store.presentation().products.first!.installations.map(\.id)), ["3.11", "3.12"])
    }
}
