import XCTest
@testable import KeepUpdatedDesktop

final class InventoryOverviewTests: XCTestCase {
    func testOnlyAnExplicitCurrentCheckCountsAsCurrent() {
        let current = DemoData.item("current", "Checked Tool", "node-package", "npm", "current")
        XCTAssertEqual(InventoryStatusFilter.status(of: current), .current)

        for status in ["unknown", "unsupported", "error", "failed", "skipped", "verification_unknown", "future-status"] {
            var item = current
            item.id = status
            item.update = .init(status: status, code: "invalid_response")
            item.latestVersion = item.installedVersion
            XCTAssertEqual(InventoryStatusFilter.status(of: item), .unknown, status)
            XCTAssertFalse(InventoryStatusFilter.current.matches(item), status)
            XCTAssertTrue(InventoryStatusFilter.all.matches(item), status)
        }
    }

    func testManualSourcesKeepTheirOwnReviewBucket() {
        var manualApp = DemoData.manualAppItem
        manualApp.path = "/Applications/Example.app"
        XCTAssertTrue(manualApp.canOpenForManualUpdate)
        XCTAssertEqual(InventoryStatusFilter.status(of: manualApp), .manual)
        XCTAssertEqual(InventoryStatusFilter.status(of: DemoData.appStoreItem), .manual)
        XCTAssertNotNil(DemoData.appStoreItem.appStoreURL)

        let volta = DemoData.item("volta", "node", "runtime", "volta")
        let system = DemoData.item("system", "macOS", "os", "system", "unknown")
        let skillshare = DemoData.skillshareExamples[0]
        for item in [volta, system, skillshare] {
            XCTAssertEqual(InventoryStatusFilter.status(of: item), .manual, item.id)
            XCTAssertFalse(InventoryStatusFilter.updates.matches(item), item.id)
        }

        manualApp.update = .init(status: "error", code: "timeout")
        XCTAssertEqual(InventoryStatusFilter.status(of: manualApp), .unknown,
                       "A failed check stays unconfirmed even when the app has a manual update route")
        manualApp.update = .init(status: "current", code: "exact_source_version_match")
        XCTAssertEqual(InventoryStatusFilter.status(of: manualApp), .current,
                       "An explicitly checked current app remains current despite its update method")
    }

    func testOverviewExcludesSupportingRecordsAndPartitionsInstallations() {
        let available = DemoData.item("available", "Updated Tool", "node-package", "npm")
        let current = DemoData.item("current", "Current Tool", "node-package", "npm", "current")
        let unknown = DemoData.item("unknown", "Unchecked Tool", "node-package", "npm", "unknown")
        let failed = DemoData.item("failed", "Failed Tool", "node-package", "npm", "error")
        var manual = DemoData.manualAppItem
        manual.path = "/Applications/Example.app"
        let dependency = DemoData.item("dependency", "Library", "python-package", "global-python")
        let configuration = DemoData.item("configuration", "MCP config", "mcp-config", "configuration", "current")
        let overview = InventoryOverview(items: [available, current, unknown, failed, manual, dependency, configuration])

        XCTAssertEqual(Set(overview.items.map(\.id)), [available.id, current.id, unknown.id, failed.id, manual.id])
        XCTAssertEqual(overview.count(.all), 5)
        XCTAssertEqual(overview.count(.updates), 1)
        XCTAssertEqual(overview.count(.current), 1)
        XCTAssertEqual(overview.count(.unknown), 2)
        XCTAssertEqual(overview.count(.manual), 1)
        XCTAssertEqual(InventoryStatusFilter.allCases.filter { $0 != .all }.reduce(0) { $0 + overview.count($1) }, overview.count(.all))
    }

    @MainActor func testSameBundleInDifferentLocationsKeepsExactTargets() {
        var first = DemoData.item("app-main", "Example Editor", "application", "homebrew")
        first.path = "/Applications/Example Editor.app"
        first.identity = "com.example.editor"
        var second = first
        second.id = "app-project"
        second.path = "/Projects/Example/Example Editor.app"
        second.update.status = "current"
        var report = DemoData.report
        report.items = [first, second]
        let store = InventoryStore(demo: true)
        store.replaceReport(report)

        let family = store.tree.first { $0.id.hasPrefix("family:") }
        XCTAssertEqual(family?.children.count, 2, "A visual family must keep both exact installations")
        XCTAssertEqual(Set(family?.items.map(\.path) ?? []), [first.path, second.path])
        XCTAssertEqual(InventoryOverview(items: report.items).count(.all), 2)
        XCTAssertEqual(Set(store.tree.flatMap(\.items).map(\.id)), [first.id, second.id])
        XCTAssertEqual(Set(store.tree.flatMap(\.uninstallIDs)), [first.id, second.id])
        store.statusFilter = .updates
        XCTAssertEqual(store.filtered.map(\.id), [first.id])
        store.selectVisible()
        XCTAssertEqual(store.selectedIDs, [first.id])
        store.statusFilter = .current
        XCTAssertEqual(store.filtered.map(\.id), [second.id])
        XCTAssertEqual(store.selectedIDs, [first.id], "Filtering must not silently remove a selected exact installation")
    }

    @MainActor func testStatusCategoryAndSearchComposeWithoutChangingSelection() {
        let npm = DemoData.item("npm", "Example Agent", "node-package", "npm")
        let skill = DemoData.item("skill", "Example Skill", "skill", "git")
        let checked = DemoData.item("checked", "Checked Agent", "node-package", "npm", "current")
        var report = DemoData.report
        report.items = [npm, skill, checked]
        let store = InventoryStore(demo: true)
        store.replaceReport(report)
        store.setSelected(skill.id, selected: true)
        store.category = .npm
        store.statusFilter = .updates
        store.query = "Example Agent"
        XCTAssertEqual(store.filtered.map(\.id), [npm.id])
        XCTAssertEqual(store.presentation().previewCount, 1)
        store.selectVisible()
        XCTAssertEqual(store.selectedIDs, [npm.id, skill.id])
        store.statusFilter = .current
        XCTAssertTrue(store.filtered.isEmpty)
        XCTAssertEqual(store.selectedIDs, [npm.id, skill.id])
        store.query = ""
        XCTAssertEqual(store.filtered.map(\.id), [checked.id])
        store.statusFilter = .all
        store.category = .all
        XCTAssertEqual(Set(store.filtered.map(\.id)), [npm.id, skill.id, checked.id])
        XCTAssertEqual(store.selectedIDs, [npm.id, skill.id])
    }

    @MainActor func testStatusFilteringExpandsMatchingGroupAndRespectsExplicitCollapse() throws {
        var available = DemoData.item("connection-one", "Shared MCP", "mcp-server", "configuration")
        available.evidence = ["config:/Example/.codex/config.toml"]
        var checked = available
        checked.id = "connection-two"
        checked.update.status = "current"
        checked.evidence = ["config:/Example/.claude.json"]
        var report = DemoData.report
        report.items = [available, checked]
        let store = InventoryStore(demo: true)
        store.replaceReport(report)
        let group = try XCTUnwrap(store.tree.first { $0.id.hasPrefix("mcp-name:") })
        store.setSelected(available.id, selected: true)
        XCTAssertEqual(store.presentation().rows.map(\.id), [group.id])

        store.statusFilter = .updates
        let filtered = store.presentation()
        XCTAssertEqual(filtered.items.map(\.id), [available.id])
        XCTAssertEqual(filtered.rows.first?.id, group.id)
        XCTAssertEqual(filtered.rows.first?.expanded, true)
        XCTAssertEqual(filtered.rows.compactMap { $0.node.item?.id }, [available.id])
        XCTAssertEqual(filtered.rows.last?.parentID, group.id)
        XCTAssertEqual(store.presentation(collapsedWhileFiltering: [group.id]).rows.map(\.id), [group.id])
        XCTAssertEqual(store.selectedIDs, [available.id])

        store.statusFilter = .all
        XCTAssertEqual(store.presentation().rows.map(\.id), [group.id], "Temporary filter expansion must not overwrite stored expansion")
        let expanded = store.presentation(expanded: [group.id])
        XCTAssertEqual(Set(expanded.rows.compactMap { $0.node.item?.id }), [available.id, checked.id])
        XCTAssertEqual(Set(group.uninstallIDs), [available.id, checked.id])
        XCTAssertEqual(store.selectedIDs, [available.id])
    }

    func testUnconfirmedVersionsIncludeUnknownManualSources() {
        var storeApp = DemoData.appStoreItem
        storeApp.update = .init(status: "unknown", code: "mac_store_not_queried")
        var manualApp = DemoData.manualAppItem
        manualApp.update = .init(status: "unknown", code: "online_query_required")
        let system = DemoData.item("system", "macOS", "os", "system", "unknown")
        let volta = DemoData.item("volta", "node", "runtime", "volta", "unknown")
        let skillshare = DemoData.skillshareExamples[0]
        let unknownManual = [storeApp, manualApp, system, volta, skillshare]
        var verifiedManual = DemoData.manualAppItem
        verifiedManual.id = "verified-manual-app"
        verifiedManual.path = "/Applications/Verified Example.app"
        let checked = DemoData.item("checked", "Checked Tool", "node-package", "npm", "current")
        let available = DemoData.item("available", "Updated Tool", "node-package", "npm")
        let supporting = DemoData.item("config", "MCP config", "mcp-config", "configuration", "error")
        let overview = InventoryOverview(items: unknownManual + [verifiedManual, checked, available, supporting])

        XCTAssertTrue(unknownManual.allSatisfy { InventoryStatusFilter.status(of: $0) == .manual })
        XCTAssertEqual(overview.count(.manual), 6)
        XCTAssertEqual(overview.count(.unknown), 0, "The unknown action bucket is separate from unknown versions")
        XCTAssertEqual(overview.unconfirmedVersionCount, 5,
                       "Unknown manual sources still lack confirmed versions; supporting records do not inflate the count")
    }

    @MainActor func testCompletedCheckEventChangesForSuccessAndPartialButNotLocalScan() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let mode = files.directory.appendingPathComponent("mode")
        let calls = files.directory.appendingPathComponent("calls")
        try Data("success".utf8).write(to: mode)
        let script = try executable("""
        #!/bin/sh
        printf '%s\\n' "$*" >> '\(calls.path)'
        if [ "$1" = "scan" ]; then
            printf '{"schema":"keepupdated/v1","command":"scan","generated_at":"local","items":[],"diagnostics":[],"coverage":[]}'
            exit 0
        fi
        if [ "$(cat '\(mode.path)')" = "partial" ]; then
            printf '{"schema":"keepupdated/v1","command":"check","generated_at":"partial","items":[],"diagnostics":[{"adapter":"controlled","code":"query_failed"}],"coverage":[]}'
            exit 1
        fi
        printf '{"schema":"keepupdated/v1","command":"check","generated_at":"success","items":[],"diagnostics":[],"coverage":[]}'
        """)
        defer { script.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        XCTAssertNil(store.completedCheckID)
        store.refresh(online: true)
        try await waitUntil { !store.busy }
        let initialEvent = try XCTUnwrap(store.completedCheckID)
        XCTAssertEqual(store.report?.generatedAt, "success")
        XCTAssertNil(store.error)

        try Data("partial".utf8).write(to: mode)
        store.refresh(online: true)
        try await waitUntil { !store.busy }
        let partialEvent = try XCTUnwrap(store.completedCheckID)
        XCTAssertNotEqual(partialEvent, initialEvent)
        XCTAssertEqual(store.report?.generatedAt, "partial")
        XCTAssertEqual(store.report?.diagnostics.first?.code, "query_failed")
        XCTAssertNil(store.error, "Exit 1 with valid inventory is a completed check with missing data")

        store.refresh(online: false)
        try await waitUntil { !store.busy }
        XCTAssertEqual(store.completedCheckID, partialEvent)
        XCTAssertEqual(store.report?.command, "scan")
        let arguments = try String(contentsOf: calls).split(separator: "\n")
        XCTAssertEqual(arguments.count, 3)
        XCTAssertTrue(arguments.prefix(2).allSatisfy { $0.hasPrefix("check --online") && $0.contains("--progress") })
        XCTAssertTrue(arguments.last?.hasPrefix("scan") == true)
    }

    @MainActor func testFailedAndCancelledOnlineCheckKeepReportWithoutNewEvent() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let mode = files.directory.appendingPathComponent("mode")
        try Data("success".utf8).write(to: mode)
        let script = try executable("""
        #!/bin/sh
        case "$(cat '\(mode.path)')" in
        error) printf 'controlled failure' >&2; exit 2 ;;
        invalid) printf '{}'; exit 0 ;;
        cancel)
            printf '%s\\n' '{"schema":"keepupdated/progress/v1","phase":"updates","completed":0,"total":1,"current":"controlled","state":"running","unresolved":0}' >&2
            while :; do sleep 0.01; done ;;
        esac
        printf '{"schema":"keepupdated/v1","command":"check","generated_at":"preserved","items":[],"diagnostics":[],"coverage":[]}'
        """)
        defer { script.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        store.refresh(online: true)
        try await waitUntil { !store.busy }
        let completed = try XCTUnwrap(store.completedCheckID)
        let preserved = try ReportJSON.encode(XCTUnwrap(store.report))
        for failure in ["error", "invalid"] {
            try Data(failure.utf8).write(to: mode)
            store.refresh(online: true)
            try await waitUntil { !store.busy }
            XCTAssertEqual(store.completedCheckID, completed, failure)
            XCTAssertEqual(try ReportJSON.encode(XCTUnwrap(store.report)), preserved, failure)
            XCTAssertNotNil(store.error, failure)
        }

        try Data("cancel".utf8).write(to: mode)
        store.refresh(online: true)
        try await waitUntil { store.progress?.phase == "updates" }
        XCTAssertTrue(store.canCancelCurrentOperation)
        store.cancel()
        try await waitUntil { !store.busy }
        XCTAssertEqual(store.completedCheckID, completed)
        XCTAssertEqual(try ReportJSON.encode(XCTUnwrap(store.report)), preserved)
        XCTAssertNil(store.progress)
    }

    private func executable(_ text: String) throws -> PrivatePlanFile {
        let file = try PrivatePlanFile(data: Data(text.utf8))
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: file.url.path)
        return file
    }

    @MainActor private func waitUntil(_ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(3)
        while !condition() && Date() < deadline { try await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertTrue(condition(), "condition did not become true")
    }

}
