import XCTest
@testable import KeepUpdatedDesktop

final class GroupUninstallTests: XCTestCase {
    func testGroupScopeKeepsEveryOriginalMemberThroughFilteringAndCollapse() throws {
        let first = DemoData.item("alias-a", "Writer", "skill", "local", "available")
        var alias = first; alias.id = "alias-b"
        var copy = first; copy.id = "copy"; copy.update.status = "unknown"
        let blocked = DemoData.item("system", "Writer", "skill", "system", "unsupported")
        let aliases = InventoryTreeNode(id: "aliases:writer", item: nil, title: "Writer", children: [
            .init(id: first.id, item: first, title: first.name),
            .init(id: alias.id, item: alias, title: alias.name)
        ])
        let version = InventoryTreeNode(id: "version:1", item: nil, title: "1", version: "1")
        let group = InventoryTreeNode(id: "skill-name:writer", item: nil, title: "Writer", children: [
            aliases, .init(id: copy.id, item: copy, title: copy.name),
            .init(id: blocked.id, item: blocked, title: blocked.name), version,
            .init(id: first.id, item: first, title: first.name)
        ])
        let expected = ["alias-a", "alias-b", "copy", "system"]
        XCTAssertEqual(group.uninstallIDs, expected)
        XCTAssertTrue(version.uninstallIDs.isEmpty)
        for filtering in [false, true] {
            let rows = InventoryTreeNode.rows([group], matching: [first.id], expanded: [], filtering: filtering,
                                             collapsedWhileFiltering: [group.id])
            XCTAssertEqual(rows.count, 1)
            XCTAssertEqual(try XCTUnwrap(rows.first).node.uninstallIDs, expected)
        }
        let parent = DemoData.item("parent", "Tool", "python-package", "pipx", "unknown")
        let dependency = DemoData.item("dependency", "Dependency", "python-package", "pipx", "unsupported")
        let node = InventoryTreeNode(id: parent.id, item: parent, title: parent.name, children: [
            .init(id: "technical:python", item: nil, title: "Python", children: [
                .init(id: dependency.id, item: dependency, title: dependency.name)
            ])
        ])
        XCTAssertEqual(node.uninstallIDs, ["dependency", "parent"])
    }

    @MainActor
    func testPreviewDismissalAndConfirmationKeepExactGroupPlan() async throws {
        let fixture = try Fixture()
        defer { fixture.remove() }
        var plan = makePlan()
        plan.items[1].status = "blocked"
        plan.items[1].reason = "plugin_uninstall_requires_client"
        let bytes = try fixture.write(plan)
        let store = fixture.store()
        store.plan(action: .uninstall, ids: ["a", "b"])
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(store.preview?.items.map(\.id), ["a", "b"])
        XCTAssertEqual(store.preview?.needsAttention.map(\.id), ["b"])
        XCTAssertEqual(try fixture.commands(), ["plan"])
        XCTAssertEqual(try fixture.selection(), ["a", "b"])
        XCTAssertFalse(FileManager.default.fileExists(atPath: try fixture.read("selection-path")))
        let arguments = try fixture.read("arguments").split(separator: "\n").map(String.init)
        for flag in ["--all", "--special", "--cleanup", "--consent"] { XCTAssertFalse(arguments.contains(flag)) }
        store.dismissPreview()
        XCTAssertNil(store.preview)
        XCTAssertFalse(store.hasConfirmedPlan)
        store.execute()
        XCTAssertEqual(try fixture.commands(), ["plan"])

        store.plan(action: .uninstall, ids: ["a", "b"])
        try await waitUntil { !store.busy }
        store.execute()
        store.execute()
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(try fixture.commands(), ["plan", "plan", "apply", "scan"])
        XCTAssertEqual(try Data(contentsOf: fixture.root.appendingPathComponent("applied-plan")), bytes)
        XCTAssertEqual(try fixture.read("confirmation"), plan.planId)
        XCTAssertFalse(store.hasConfirmedPlan)
    }

    @MainActor
    func testSharedActionRetainsBothSelectedReferences() async throws {
        let fixture = try Fixture()
        defer { fixture.remove() }
        var plan = makePlan()
        plan.items[1].status = "skipped"
        plan.items[1].reason = "duplicate_installation_target"
        plan.items[1].canonicalId = "a"
        _ = try fixture.write(plan)
        let store = fixture.store()
        store.plan(action: .uninstall, ids: ["b", "a"])
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(store.preview?.items.map(\.id), ["a", "b"])
        XCTAssertEqual(store.preview?.sharedReferences.first?.canonicalId, "a")
        XCTAssertEqual(store.preview?.executable.map(\.id), ["a"])
        XCTAssertEqual(try fixture.commands(), ["plan"])
    }

    @MainActor
    func testCleanupRegeneratesTheSameGroupWithoutApplying() async throws {
        let fixture = try Fixture()
        defer { fixture.remove() }
        var plan = makePlan()
        _ = try fixture.write(plan)
        let store = fixture.store()
        store.plan(action: .uninstall, ids: ["a", "b"])
        try await waitUntil { !store.busy }
        plan.selection?.cleanup = true
        for index in plan.items.indices { plan.items[index].cleanup = true }
        _ = try fixture.write(plan)
        store.changeCleanup(true)
        XCTAssertFalse(store.hasConfirmedPlan, "Changing cleanup invalidates the previous confirmation")
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(try fixture.selection(), ["a", "b"])
        XCTAssertEqual(store.preview?.selection?.ids, ["a", "b"])
        XCTAssertEqual(store.preview?.selection?.cleanup, true)
        XCTAssertTrue(try fixture.read("arguments").split(separator: "\n").contains("--cleanup"))
        store.dismissPreview()
        XCTAssertEqual(try fixture.commands(), ["plan", "plan"])
    }

    @MainActor
    func testChangedMissingOrDuplicatePreviewScopeCannotBeConfirmed() async throws {
        let fixture = try Fixture()
        defer { fixture.remove() }
        let changes: [(String, (inout OperationPlan) -> Void)] = [
            ("missing item", { $0.items.removeLast() }),
            ("extra item", { var item = $0.items[0]; item.id = "extra"; $0.items.append(item) }),
            ("duplicate item", { $0.items[1].id = "a" }),
            ("selection IDs", { $0.selection?.ids = ["a", "extra"] }),
            ("duplicate selection", { $0.selection?.ids = ["a", "b", "b"] }),
            ("all selection", { $0.selection?.all = true }),
            ("cleanup selection", { $0.selection?.cleanup = true })
        ]
        for (label, change) in changes {
            var plan = makePlan()
            change(&plan)
            _ = try fixture.write(plan)
            let store = fixture.store()
            store.plan(action: .uninstall, ids: ["a", "b"])
            try await waitUntil { !store.busy }
            XCTAssertNotNil(store.error, label)
            XCTAssertNil(store.preview, label)
            XCTAssertFalse(store.hasConfirmedPlan, label)
            store.execute()
        }
        XCTAssertEqual(try fixture.commands(), Array(repeating: "plan", count: changes.count))
    }

    @MainActor
    func testBlockedAndExpiredGroupPlansCannotApply() async throws {
        let fixture = try Fixture()
        defer { fixture.remove() }
        for blocked in [true, false] {
            var plan = makePlan()
            if blocked {
                for index in plan.items.indices { plan.items[index].status = "blocked" }
            } else { plan.expiresAt = "2000-01-01T00:00:00Z" }
            _ = try fixture.write(plan)
            let store = fixture.store()
            store.plan(action: .uninstall, ids: ["a", "b"])
            try await waitUntil { !store.busy }
            XCTAssertNotNil(store.preview)
            store.execute()
            XCTAssertFalse(store.busy)
        }
        XCTAssertEqual(try fixture.commands(), ["plan", "plan"])
    }

    @MainActor
    func testGroupCancellationAndFailureRemoveTemporarySelectionWithoutApplying() async throws {
        let fixture = try Fixture()
        defer { fixture.remove() }
        _ = try fixture.write(makePlan())
        try Data().write(to: fixture.root.appendingPathComponent("wait"))
        let store = fixture.store()
        store.plan(action: .uninstall, ids: ["a", "b"])
        try await waitUntil { store.progress != nil }
        let path = try fixture.read("selection-path")
        XCTAssertTrue(FileManager.default.fileExists(atPath: path))
        store.cancel()
        try await waitUntil { !store.busy }
        XCTAssertFalse(FileManager.default.fileExists(atPath: path))
        XCTAssertNil(store.preview)
        XCTAssertFalse(store.hasConfirmedPlan)
        try FileManager.default.removeItem(at: fixture.root.appendingPathComponent("wait"))
        try Data().write(to: fixture.root.appendingPathComponent("fail"))
        store.plan(action: .uninstall, ids: ["a", "b"])
        try await waitUntil { !store.busy }
        XCTAssertNotNil(store.error)
        XCTAssertNil(store.preview)
        XCTAssertFalse(store.hasConfirmedPlan)
        XCTAssertFalse(FileManager.default.fileExists(atPath: try fixture.read("selection-path")))
        XCTAssertEqual(try fixture.commands(), ["plan", "plan"])
    }

    private func makePlan() -> OperationPlan {
        let items = ["a", "b"].map { DemoData.item($0, "Writer", "skill", "local", "unknown") }
        return DemoData.plan(action: .uninstall, items: items, cleanup: false)
    }

    @MainActor
    private func waitUntil(_ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(10)
        while !condition() && Date() < deadline { try await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertTrue(condition(), "Condition did not become true")
    }

    private struct Fixture {
        let files: PrivatePlanFile
        let script: PrivatePlanFile
        var root: URL { files.directory }

        init() throws {
            files = try PrivatePlanFile(data: Data())
            let path = files.directory.path
            script = try PrivatePlanFile(data: Data("""
            #!/bin/sh
            root='\(path)'
            command="$1"; shift
            printf '%s\\n' "$command" >> "$root/commands"
            printf '%s\\n' "$@" > "$root/arguments"
            case "$command" in
            plan)
                while [ "$#" -gt 0 ]; do
                    case "$1" in --ids-file) shift; printf '%s' "$1" > "$root/selection-path"; cp "$1" "$root/selection" ;; esac
                    shift
                done
                if [ -f "$root/fail" ]; then printf 'controlled preview failure' >&2; exit 2; fi
                if [ -f "$root/wait" ]; then
                    printf '%s\\n' '{"schema":"keepupdated/progress/v1","phase":"plan","completed":0,"total":null,"current":"Fixture","state":"running","unresolved":0}' >&2
                    while :; do sleep 0.01; done
                fi
                cat "$root/response" ;;
            apply)
                while [ "$#" -gt 0 ]; do
                    case "$1" in
                    --plan) shift; cp "$1" "$root/applied-plan" ;;
                    --confirm) shift; printf '%s' "$1" > "$root/confirmation" ;;
                    esac
                    shift
                done
                cat "$root/apply-response" ;;
            scan)
                printf '%s' '{"schema":"keepupdated/v1","command":"scan","generated_at":"","items":[],"diagnostics":[],"coverage":[]}' ;;
            esac
            """.utf8))
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.url.path)
        }

        func write(_ plan: OperationPlan) throws -> Data {
            let bytes = Data(" \n".utf8) + (try ReportJSON.encode(plan)) + Data("\n ".utf8)
            try bytes.write(to: root.appendingPathComponent("response"))
            let result = ApplyReport(schema: "keepupdated/apply/v1", planId: plan.planId, startedAt: "", finishedAt: "", results: [])
            try ReportJSON.encode(result).write(to: root.appendingPathComponent("apply-response"))
            return bytes
        }

        @MainActor func store() -> InventoryStore {
            InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        }
        func read(_ name: String) throws -> String { try String(contentsOf: root.appendingPathComponent(name)) }
        func commands() throws -> [String] { try read("commands").split(separator: "\n").map(String.init) }
        func selection() throws -> [String] { try JSONDecoder().decode([String].self, from: Data(contentsOf: root.appendingPathComponent("selection"))) }
        func remove() { script.remove(); files.remove() }
    }
}
