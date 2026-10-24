import XCTest
@testable import KeepUpdatedDesktop

final class InventoryProductTests: XCTestCase {
    private func item(_ id: String, _ name: String, _ kind: String, _ path: String, owner: String = "standalone") -> InventoryItem {
        var item = DemoData.item(id, name, kind, owner, "unknown")
        item.path = path
        item.realPath = path
        return item
    }

    @MainActor
    func testSharedSkillIsOneProductAndSearchDoesNotReduceItsLocations() throws {
        let source = item("shared", "agent-memory-systems", "skill", "/Example/.agents/skills/agent-memory-systems")
        let clients = ["codex", "claude", "hermes"].map { client -> InventoryItem in
            var value = item(client, source.name, "skill", "/Example/.\(client)/skills/agent-memory-systems")
            value.realPath = source.path
            return value
        }
        let store = InventoryStore(demo: true)
        var report = DemoData.report; report.items = [source] + clients
        store.replaceReport(report)
        store.category = .skills
        let product = try XCTUnwrap(store.presentation().products.first)
        XCTAssertEqual(store.presentation().products.count, 1)
        XCTAssertEqual(store.count(.skills), 1)
        XCTAssertEqual(Set(product.installations.map(\.id)), ["shared", "codex", "claude", "hermes"])
        XCTAssertEqual(product.productCategory, .skills)
        XCTAssertEqual(product.productCategory.symbol, "wand.and.stars")
        store.query = "/.codex/"
        let filtered = store.presentation()
        XCTAssertEqual(filtered.items.map(\.id), ["codex"])
        XCTAssertEqual(filtered.products.map(\.id), [product.id])
        XCTAssertEqual(Set(filtered.products[0].installations.map(\.id)), Set(report.items.map(\.id)))
        store.query = "no matching locations"
        XCTAssertTrue(store.presentation().products.isEmpty)
        XCTAssertEqual(store.product(id: product.id)?.installations.count, 4)
    }

    @MainActor
    func testSkillProductCombinesPluginLocationsAndKeepsUnverifiedCachesReadOnly() throws {
        let shared = item("shared", "writing", "skill", "/Example/.agents/skills/writing")
        var plugin = item("plugin", "writing", "skill", "/Example/plugin/skills/writing", owner: "claude-plugin")
        plugin.plugin = .init(client: "claude", id: "example@market", scope: "user", root: "/Example/plugin", version: "1", commit: "abc")
        var cache = item("cache", "writing", "skill", "/Example/cache/skills/writing", owner: "codex-plugin")
        cache.source = "plugin-cache"
        cache.pluginCache = .init(client: "codex", root: "/Example/cache", id: "example", version: "1", registration: "unverified")
        let store = InventoryStore(demo: true)
        var report = DemoData.report; report.items = [shared, plugin, cache]
        store.replaceReport(report)
        let product = try XCTUnwrap(store.presentation().products.first)
        XCTAssertEqual(store.presentation().products.count, 1)
        XCTAssertEqual(store.count(.skills), 1)
        XCTAssertEqual(store.count(.all), 1)
        XCTAssertEqual(Set(product.installations.map(\.id)), [shared.id, plugin.id])
        XCTAssertEqual(product.supportingItems.map(\.id), [cache.id])
        store.query = "/Example/cache"
        XCTAssertEqual(store.presentation().products.map(\.id), [product.id])
        XCTAssertEqual(Set(store.presentation().products[0].installations.map(\.id)), [shared.id, plugin.id])
    }

    func testMCPKeepsEveryConnectionButDoesNotExpandToItsPackageOwner() throws {
        var first = item("one", "server", "mcp-server", "/Example/.codex/config.toml")
        first.related = ["package"]
        var second = first; second.id = "two"; second.path = "/Example/.hermes/config.yaml"
        let package = item("package", "server-package", "node-package", "/Packages/server", owner: "npm")
        let product = try XCTUnwrap(InventoryTree.products(InventoryTree.build([first, second, package])).first { $0.productCategory == .mcp })
        XCTAssertEqual(Set(product.installations.map(\.id)), ["one", "two"])
        XCTAssertFalse(product.installations.contains { $0.id == "package" })
    }

    func testAppChildrenAndTechnicalCollectionsAreReadOnlySupportingDetail() throws {
        let app = item("app", "Editor", "application", "/Applications/Editor.app")
        var helper = item("helper", "Helper", "application", app.path + "/Contents/Helper.app", owner: "app-component")
        helper.related = [app.id]
        let config = item("config", "Configuration", "mcp-config", "/Example/config.json")
        let products = InventoryTree.products(InventoryTree.build([app, helper, config]))
        let product = try XCTUnwrap(products.first { $0.item?.id == app.id })
        XCTAssertEqual(product.installations.map(\.id), [app.id])
        XCTAssertEqual(product.supportingItems.map(\.id), [helper.id])
        let technical = try XCTUnwrap(products.first { $0.isTechnicalDetail })
        XCTAssertTrue(technical.installations.isEmpty)
        XCTAssertEqual(technical.supportingItems.map(\.id), [config.id])
    }

    func testJavaVersionsAndSourcesShareOneProductWithoutDuplicatingAPathObservation() throws {
        let first = item("java17", "java", "runtime", "/Library/Java/JavaVirtualMachines/jdk-17/Contents/Home/bin/java", owner: "version-manager")
        let second = item("java21", "java", "runtime", "/Example/Library/Java/JavaVirtualMachines/jdk-21/Contents/Home/bin/java", owner: "version-manager")
        var observed = item("active", "java", "executable", "/Example/bin/java", owner: "PATH")
        observed.realPath = second.path
        let products = InventoryTree.products(InventoryTree.build([first, second, observed]))
        let product = try XCTUnwrap(products.first)
        XCTAssertEqual(products.count, 1)
        XCTAssertEqual(product.productTitle, "Java")
        XCTAssertEqual(Set(product.installations.map(\.id)), [first.id, second.id])
        XCTAssertEqual(product.supportingItems.map(\.id), [observed.id])
        XCTAssertEqual(Set(product.items.map(\.id)), [first.id, second.id, observed.id])
    }

    @MainActor
    func testSamePackageAcrossManagersIsCountedOnceInAllAndRetainsBothLocations() throws {
        var npm = item("npm", "example-tool", "node-package", "/Example/npm/example-tool", owner: "npm")
        npm.identity = "example-tool"; npm.source = "npm-public"
        var pnpm = npm; pnpm.id = "pnpm"; pnpm.owner = "pnpm"; pnpm.path = "/Example/pnpm/example-tool"; pnpm.realPath = pnpm.path
        let store = InventoryStore(demo: true)
        var report = DemoData.report; report.items = [npm, pnpm]
        store.replaceReport(report)
        XCTAssertEqual(store.count(.all), 1)
        XCTAssertEqual(store.count(.npm), 1)
        XCTAssertEqual(store.count(.pnpm), 1)
        store.category = .pnpm
        XCTAssertEqual(store.presentation().products.count, 1)
        XCTAssertEqual(Set(store.presentation().products[0].installations.map(\.id)), [npm.id, pnpm.id])
    }

    func testDifferentKnownIdentitiesAreNotMergedBecauseTheirNamesMatch() {
        var first = item("one", "Editor", "application", "/Apps/One.app")
        var second = item("two", "Editor", "application", "/Apps/Two.app")
        first.identity = "org.example.one"; second.identity = "org.example.two"
        XCTAssertEqual(InventoryTree.products(InventoryTree.build([first, second])).count, 2)
    }
}
