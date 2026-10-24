import XCTest
@testable import KeepUpdatedDesktop

final class InventoryTreeTests: XCTestCase {
    private func item(_ id: String, _ path: String, related: [String] = []) -> InventoryItem {
        item(id, "application", path, related: related)
    }
    private func item(_ id: String, _ kind: String, _ path: String, related: [String] = []) -> InventoryItem {
        var value = DemoData.item(id, id, kind, "standalone", "current")
        value.path = path
        value.related = related
        return value
    }
    func testStrictAppOwnershipAndOriginalIDs() {
        let app = item("host", "/Apps/Host.app")
        let child = item("child", "/Apps/Host.app/Contents/Helpers/Crash.app", related: ["host", "cask"])
        let cask = item("cask", "cask", "/Casks/host")
        let unrelated = item("unrelated", "/Apps/Other.app", related: ["host"])
        let missing = item("missing", "/Apps/Missing.app/Contents/Child.app", related: ["absent"])
        let tree = InventoryTree.build([child, app, cask, unrelated, missing])
        XCTAssertEqual(tree.count, 4)
        XCTAssertEqual(tree.first { $0.item?.id == "host" }?.children.compactMap { $0.item?.id }, ["child"])
        XCTAssertEqual(Set(tree.flatMap(\.items).map(\.id)), Set(["host", "child", "cask", "unrelated", "missing"]))
    }
    func testCyclesConflictsAndPathPrefixTrapsStayFlat() {
        let host = item("host", "/Apps/Host.app", related: ["child"])
        let child = item("child", "/Apps/Host.app/Contents/Child.app", related: ["host"])
        XCTAssertTrue(InventoryTree.build([host, child]).allSatisfy { $0.children.isEmpty })
        let plainHost = item("host", "/Apps/Host.app")
        let other = item("other", "/Apps/Other.app")
        let conflict = item("conflict", "/Apps/Host.app/Contents/Conflict.app", related: ["host", "other"])
        let prefix = item("prefix", "/Apps/Host.app.extra/Child.app", related: ["host"])
        let traversal = item("dot", "/Apps/Host.app/../Child.app", related: ["host"])
        XCTAssertTrue(InventoryTree.build([plainHost, other, conflict, prefix, traversal]).allSatisfy { $0.children.isEmpty })
    }
    func testResolvedPathsCannotEscapeTheirDeclaredParent() {
        var host = item("host", "/Apps/Host.app"); host.realPath = host.path
        var child = item("child", "/Apps/Host.app/Contents/Child.app", related: ["host"])
        child.realPath = "/Shared/Child.app"
        XCTAssertTrue(InventoryTree.build([host, child]).allSatisfy { $0.children.isEmpty })
    }
    func testFolderIDsAreScopedToTheirParent() {
        var values: [InventoryItem] = []
        for hostID in ["one", "two"] {
            let host = item(hostID, "/Apps/" + hostID + ".app")
            values.append(host)
            for childID in ["a", "b"] {
                var child = item(hostID + childID, host.path + "/Contents/" + childID + ".app", related: [hostID])
                child.identity = "org.example.helper"
                values.append(child)
            }
        }
        let tree = InventoryTree.build(values)
        let rows = InventoryTreeNode.rows(tree, matching: Set(values.map(\.id)), expanded: [], filtering: true)
        XCTAssertEqual(rows.count, Set(rows.map(\.id)).count)
        XCTAssertEqual(rows.compactMap { $0.node.item }.count, values.count)
    }
    func testAliasAndMCPDisplayGroupsKeepIndependentInstances() {
        var first = item("a", "skill", "/Clients/one/helper")
        first.realPath = "/Shared/helper"
        var alias = item("b", "skill", "/Clients/two/helper")
        alias.realPath = first.realPath
        var copy = item("c", "skill", "/Copies/helper")
        copy.name = first.name
        copy.realPath = copy.path
        var mcp = item("mcp", "mcp-server", "/Clients/mcp")
        mcp.realPath = first.realPath
        var mcp2 = mcp; mcp2.id = "mcp2"
        let tree = InventoryTree.build([first, alias, copy, mcp, mcp2])
        XCTAssertEqual(tree.count, 2)
        let folder = tree.first { $0.id.hasPrefix("skill-name:") }!
        XCTAssertEqual(Set(folder.items.map(\.id)), ["a", "b", "c"])
        XCTAssertTrue(folder.children.contains { $0.id.hasPrefix("aliases:") && Set($0.items.map(\.id)) == ["a", "b"] })
        let connections = tree.first { $0.id.hasPrefix("mcp-name:") }!
        XCTAssertNil(connections.item)
        XCTAssertEqual(Set(connections.children.compactMap { $0.item?.id }), ["mcp", "mcp2"])
        XCTAssertTrue(connections.children.allSatisfy { $0.children.isEmpty })
    }
    func testPluginFoldersRequireCompleteConsistentOwnershipAndContainment() {
        var a = item("a", "skill", "/Plugins/tool/skills/a")
        a.plugin = .init(client: "codex", id: "tool@market", scope: "user", root: "/Plugins/tool", version: "1")
        var b = a; b.id = "b"; b.path = "/Plugins/tool/skills/b"
        XCTAssertEqual(InventoryTree.build([a, b]).count, 1)
        var outside = b; outside.path = "/Plugins/tool-other/skills/b"
        XCTAssertEqual(InventoryTree.build([a, outside]).count, 2)
        var disagree = b; disagree.plugin?.version = "2"
        XCTAssertEqual(InventoryTree.build([a, disagree]).count, 2)
        var differentCopy = b; differentCopy.plugin?.root = "/Plugins/copy"; differentCopy.path = "/Plugins/copy/skills/b"
        XCTAssertEqual(InventoryTree.build([a, differentCopy]).count, 2)
        var incomplete = b; incomplete.plugin?.scope = ""
        XCTAssertEqual(InventoryTree.build([a, incomplete]).count, 2)
    }
    func testPythonRequiresEnvironmentEvidenceAndDoesNotNestSharedRuntime() {
        let env = item("env", "python-environment", "/Envs/tool", related: ["runtime"])
        var package = item("package", "python-package", "/Envs/tool/lib/python3/site-packages/pkg.dist-info")
        package.evidence = ["environment:/Envs/tool"]
        var outside = package; outside.id = "outside"; outside.path = "/Envs/tool-other/pkg.dist-info"
        var missing = package; missing.id = "missing"; missing.evidence = []
        let runtime = item("runtime", "runtime", "/Runtime/python", related: ["env"])
        let tree = InventoryTree.build([env, package, outside, missing, runtime])
        XCTAssertEqual(tree.count, 2)
        let details = tree.first { $0.isTechnicalDetail }!
        XCTAssertEqual(details.children.count, 3)
        XCTAssertEqual(details.children.first { $0.item?.id == "env" }?.children.compactMap { $0.item?.id }, ["package"])
        XCTAssertEqual(Set(details.children.compactMap { $0.item?.id }), ["env", "outside", "missing"])
        XCTAssertEqual(tree.first { !$0.isTechnicalDetail }?.item?.id, "runtime")
    }
    func testCollapsedChildrenAreExposedBySearchAndUpdateFilter() {
        let host = item("host", "/Apps/Host.app")
        var child = item("child", "/Apps/Host.app/Contents/Child.app", related: ["host"])
        child.update.status = "available"
        child.health.status = "error"
        let tree = InventoryTree.build([host, child])
        let all = Set([host.id, child.id])
        XCTAssertEqual(InventoryTreeNode.rows(tree, matching: all, expanded: [], filtering: false).compactMap { $0.node.item?.id }, ["host"])
        let matches = Set([child.id])
        let visible = InventoryTreeNode.rows(tree, matching: matches, expanded: [], filtering: true)
        XCTAssertEqual(visible.compactMap { $0.node.item?.id }, ["host", "child"])
        XCTAssertEqual(tree[0].summary, L10n.text("Items: %@ · Updates: %@ · Need review: %@", "1", "1", "1"))
        XCTAssertEqual(InventoryTreeNode.rows(tree, matching: matches, expanded: [], filtering: true, collapsedWhileFiltering: [tree[0].id]).count, 1)
        XCTAssertEqual(matches, ["child"], "Ancestor context never changes the matching ID set")
    }
    func testHomebrewVersionsDecodeAndStayReadOnly() throws {
        var formula = item("formula", "formula", "/Cellar/example")
        formula.installedVersions = ["1.0", "2.0", "2.0"]
        var report = DemoData.report; report.items = [formula]
        let encoded = try ReportJSON.encode(report)
        let decoded = try XCTUnwrap(ReportJSON.decode(InventoryReport.self, from: encoded, schema: "keepupdated/v1").items.first)
        XCTAssertEqual(decoded.installedVersions, formula.installedVersions)
        let tree = InventoryTree.build([decoded])
        XCTAssertEqual(tree[0].children.map(\.version), ["1.0", "2.0"])
        XCTAssertTrue(tree[0].children.allSatisfy { $0.item == nil })
        XCTAssertEqual(tree.flatMap(\.items).map(\.id), [formula.id])
    }
    func testVersionedFormulaFamiliesCombinePrefixesButKeepTapsAndOriginalItems() {
        func formula(_ name: String, _ prefix: String = "/Brew", _ source: String = "homebrew/core") -> InventoryItem {
            var value = item(prefix + source + name, "formula", prefix + "/Cellar/" + name)
            value.name = name; value.owner = "homebrew"; value.source = source
            return value
        }
        let a = formula("python@3.11"), b = formula("python@3.12")
        let foreignTap = formula("python@3.13", "/Brew", "vendor/tap")
        let otherPrefix = formula("python@3.14", "/Other")
        let namedOnly = formula("python@preview")
        let tree = InventoryTree.build([a, b, foreignTap, otherPrefix, namedOnly])
        XCTAssertEqual(tree.count, 3)
        let folder = tree.first { $0.item == nil }!
        XCTAssertEqual(Set(folder.items.map(\.id)), [a.id, b.id, otherPrefix.id])
        XCTAssertEqual(folder.children.count, 3)
        XCTAssertTrue(folder.children.allSatisfy { $0.item != nil })
        XCTAssertEqual(InventoryTree.build([formula("openssl@3"), formula("openssl@4")]).count, 1)
    }
    func testLanguageRuntimeFamiliesKeepEveryOriginalInstallation() {
        func runtime(_ id: String, _ path: String, _ name: String = "java") -> InventoryItem {
            var value = item(id, "runtime", path); value.owner = "version-manager"; value.name = name
            return value
        }
        let a = runtime("a", "/Library/Java/JavaVirtualMachines/a/Contents/Home/bin/java")
        let b = runtime("b", "/Library/Java/JavaVirtualMachines/b/Contents/Home/bin/java")
        let unrelated = runtime("other", "/Other/Java/a/Contents/Home/bin/java")
        let java = InventoryTree.build([a, b, unrelated])
        XCTAssertEqual(java.count, 1)
        XCTAssertEqual(Set(java[0].installations.map(\.id)), ["a", "b", "other"])
        let py1 = runtime("py1", "/Example/.local/share/uv/python/cpython-one/bin/python3", "python3")
        let py2 = runtime("py2", "/Example/.local/share/uv/python/cpython-two/bin/python3", "python3")
        XCTAssertEqual(InventoryTree.build([py1, py2]).count, 1)
        var copy = a; copy.id = "copy"; copy.kind = "application"; copy.identity = "org.example.app"
        var copy2 = copy; copy2.id = "copy2"; copy2.path = "/Other/App.app"
        XCTAssertEqual(InventoryTree.build([copy, copy2]).count, 1)
    }
    @MainActor func testSelectionAndPlanRetainExactOriginalIDsAcrossCollapse() async throws {
        let store = InventoryStore(demo: true)
        var host = item("host", "/Apps/Host.app"); host.update.status = "available"
        var child = item("child", "/Apps/Host.app/Contents/Child.app", related: ["host"]); child.update.status = "available"
        var report = DemoData.report; report.items = [host, child]
        store.replaceReport(report)
        store.setSelected(host.id, selected: true)
        XCTAssertEqual(store.selectedIDs, [host.id])
        store.clearSelection()
        store.onlyUpdates = true
        store.query = "child"
        XCTAssertEqual(store.filtered.map(\.id), [child.id])
        store.selectVisible()
        XCTAssertEqual(store.selectedIDs, [child.id])
        _ = InventoryTreeNode.rows(store.tree, matching: [child.id], expanded: [], filtering: false)
        store.previewSelected()
        for _ in 0..<200 where store.busy { try await Task.sleep(nanoseconds: 5_000_000) }
        XCTAssertEqual(store.preview?.items.map(\.id), [child.id])
        store.query = ""
        store.selectVisible()
        XCTAssertEqual(store.selectedIDs, [host.id, child.id])
    }
}
