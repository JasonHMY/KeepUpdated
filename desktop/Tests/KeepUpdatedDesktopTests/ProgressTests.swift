import XCTest
@testable import KeepUpdatedDesktop

final class ProgressTests: XCTestCase {
    private func event(_ completed: Int = 0, total: String = "2", current: String = "Example", state: String = "running", phase: String = "updates") -> String {
        "{\"schema\":\"keepupdated/progress/v1\",\"phase\":\"\(phase)\",\"completed\":\(completed),\"total\":\(total),\"current\":\"\(current)\",\"state\":\"\(state)\",\"unresolved\":0}"
    }
    private func executable(_ text: String) throws -> PrivatePlanFile {
        let file = try PrivatePlanFile(data: Data(text.utf8))
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: file.url.path)
        return file
    }

    func testFramingUTF8ValidationAndFinalDelivery() throws {
        var stream = ProgressStream()
        var received: [InventoryProgress] = []
        let data = Data(("diagnostic before\n" + event(current: "中文") + "\n" + event(1) + "\n" + event(0) + "\n" + event(9) + "\n" + event(2, state: "finished") + "\nlast diagnostic").utf8)
        for byte in data { stream.append(Data([byte]), onProgress: { received.append($0) }) }
        stream.finish(onProgress: { received.append($0) })
        XCTAssertEqual(received.first?.current, "中文")
        XCTAssertEqual(received.last?.completed, 2)
        XCTAssertEqual(received.last?.state, "finished")
        XCTAssertFalse(received.contains { $0.completed > 2 })
        XCTAssertTrue(String(decoding: stream.diagnostics, as: UTF8.self).contains("last diagnostic"))
        XCTAssertEqual(received.map(\.completed), received.map(\.completed).sorted())
    }

    func testSequencedEventsAcceptRepeatedScanAndApplyStagesAndRejectStaleCallbacks() throws {
        func sequenced(_ json: String, _ sequence: UInt64) -> Data {
            Data((json.dropLast() + ",\"sequence\":\(sequence)}\n").utf8)
        }
        var stream = ProgressStream()
        var received: [InventoryProgress] = []
        let events = [event(2, state: "finished", phase: "scan"), event(total: "null", phase: "resolve"),
                      event(1, total: "3", phase: "apply"), event(0, total: "2", phase: "scan"),
                      event(1, total: "2", phase: "applications"), event(2, state: "finished", phase: "scan"),
                      event(2, total: "3", phase: "apply"), event(0, total: "null", phase: "verify")]
        for (offset, json) in events.enumerated() {
            stream.append(sequenced(json, UInt64(offset + 1)), onProgress: { received.append($0) })
            stream.finish(onProgress: { received.append($0) })
        }
        XCTAssertEqual(received.map(\.phase), ["scan", "resolve", "apply", "scan", "applications", "scan", "apply", "verify"])
        XCTAssertEqual(received.last?.sequence, 8)
        stream.append(sequenced(event(1, phase: "scan"), 3), onProgress: { received.append($0) })
        stream.append(Data((event(2, state: "finished") + "\n").utf8), onProgress: { received.append($0) })
        stream.finish(onProgress: { received.append($0) })
        XCTAssertEqual(received.count, 8)
        XCTAssertFalse(stream.diagnostics.isEmpty)
    }

    func testUnknownTotalsCancellationAndOversizedLines() throws {
        var stream = ProgressStream()
        var received: [InventoryProgress] = []
        stream.append(Data((String(repeating: "x", count: 200_000) + "\n" + event(total: "null") + "\n" + event(1, total: "null", state: "cancelled") + "\n").utf8), onProgress: { received.append($0) })
        stream.finish(onProgress: { received.append($0) })
        XCTAssertLessThanOrEqual(stream.diagnostics.count, 65536)
        XCTAssertEqual(received.last?.state, "cancelled")
        XCTAssertNil(received.last?.fraction)
        XCTAssertEqual(received.last?.completed, 1)
        let failed = try JSONDecoder().decode(InventoryProgress.self, from: Data(event(1, total: "3", state: "failed").utf8))
        XCTAssertTrue(failed.valid)
        XCTAssertNil(failed.fraction, "Failed work cannot display a successful completion fraction")
    }

    func testRealPipeDeliversProgressBeforeChildCanExitAndPreservesJSON() async throws {
        let gate = try PrivatePlanFile(data: Data())
        defer { gate.remove() }
        let marker = gate.directory.appendingPathComponent("continue")
        let script = try executable("""
        #!/bin/sh
        printf '%s\\n' '\(event(current: "中文"))' >&2
        while [ ! -f '\(marker.path)' ]; do sleep 0.01; done
        i=0
        while [ "$i" -lt 3000 ]; do
            printf '                                      '
            printf 'ordinary diagnostic\\n' >&2
            i=$((i+1))
        done
        printf '%s\\n' '\(event(2, state: "finished"))' >&2
        printf '{"schema":"keepupdated/v1","command":"check","generated_at":"","items":[],"diagnostics":[],"coverage":[]}'
        exit 1
        """)
        defer { script.remove() }
        let first = expectation(description: "progress while child is waiting")
        first.assertForOverFulfill = false
        let task = Task { try await CLIClient(testExecutable: script.url).run([]) { progress in
            if progress.completed == 0 { first.fulfill() }
        } }
        await fulfillment(of: [first], timeout: 3)
        try Data().write(to: marker)
        let output = try await task.value
        XCTAssertEqual(output.status, 1)
        XCTAssertTrue(try ReportJSON.decode(InventoryReport.self, from: output.data, schema: "keepupdated/v1").items.isEmpty)
        XCTAssertFalse(String(decoding: output.data, as: UTF8.self).contains("progress/v1"))
    }

    func testCancellationAndNextRunDoNotShareProcessState() async throws {
        let script = try executable("""
        #!/bin/sh
        if [ "$1" = "fast" ]; then printf '{}'; exit 0; fi
        printf '%s\\n' '\(event())' >&2
        while :; do :; done
        """)
        defer { script.remove() }
        let client = CLIClient(testExecutable: script.url)
        let started = expectation(description: "started")
        let task = Task { try await client.run([]) { _ in started.fulfill() } }
        await fulfillment(of: [started], timeout: 3)
        task.cancel()
        do { _ = try await task.value; XCTFail("cancelled command returned success") } catch is CancellationError {}
        let next = try await client.run(["fast"])
        XCTAssertEqual(String(decoding: next.data, as: UTF8.self), "{}")
    }

    @MainActor
    func testStoreUsesOneCheckPipelineAndClearsProgressOnCancelAndRestart() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let log = files.directory.appendingPathComponent("calls")
        let release = files.directory.appendingPathComponent("release")
        let script = try executable("""
        #!/bin/sh
        printf '%s\\n' "$*" >> '\(log.path)'
        printf '%s\\n' '\(event(current: "apps", phase: "scan"))' >&2
        while [ ! -f '\(release.path)' ]; do sleep 0.01; done
        printf '%s\\n' '\(event(2, state: "finished", phase: "updates"))' >&2
        printf '{"schema":"keepupdated/v1","command":"check","generated_at":"","items":[],"diagnostics":[],"coverage":[]}'
        """)
        defer { script.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        store.refresh(online: true)
        try await waitUntil { store.progress?.phase == "scan" }
        XCTAssertEqual(store.activity, store.progress?.phaseLabel)
        XCTAssertTrue(store.canCancelCurrentOperation)
        XCTAssertNil(store.acquireAppUpdate(), "Self-update cannot interrupt a real refresh")
        store.cancel()
        XCTAssertNil(store.progress)
        try await waitUntil { !store.busy }
        XCTAssertNil(store.report)
        store.refresh(online: true)
        try await waitUntil { store.progress?.phase == "scan" }
        XCTAssertEqual(store.progress?.completed, 0)
        try Data().write(to: release)
        try await waitUntil { !store.busy }
        XCTAssertNil(store.progress)
        XCTAssertEqual(store.report?.command, "check")
        let lease = try XCTUnwrap(store.acquireAppUpdate("Updating KeepUpdated…"))
        XCTAssertNil(store.progress)
        XCTAssertEqual(store.activity, L10n.text("Updating KeepUpdated…"))
        XCTAssertFalse(store.canCancelCurrentOperation)
        store.cancel()
        store.refresh(online: true)
        XCTAssertTrue(store.busy, "Ordinary cancel must not release a self-update handoff")
        XCTAssertNil(store.progress)
        store.releaseAppUpdate(lease)
        XCTAssertFalse(store.busy)
        XCTAssertTrue(store.activity.isEmpty)
        let calls = try String(contentsOf: log).split(separator: "\n")
        XCTAssertEqual(calls.count, 2)
        XCTAssertTrue(calls.allSatisfy { $0.hasPrefix("check --online") && $0.contains("--progress") })
    }

    @MainActor
    func testPlanCancelRetryAndApplyStreamsKeepRealFailureResult() async throws {
        let files = try PrivatePlanFile(data: Data())
        defer { files.remove() }
        let releasePlan = files.directory.appendingPathComponent("release-plan")
        let releaseApply = files.directory.appendingPathComponent("release-apply")
        let item = DemoData.item("controlled", "Example", "formula", "homebrew")
        let plan = DemoData.plan(action: .update, items: [item], cleanup: false)
        let planJSON = String(decoding: try DemoData.planData(plan, privilegedRequest: nil), as: UTF8.self)
        let applyJSON = "{\"schema\":\"keepupdated/apply/v1\",\"plan_id\":\"\(plan.planId)\",\"started_at\":\"\",\"finished_at\":\"\",\"results\":[{\"id\":\"controlled\",\"name\":\"Example\",\"status\":\"failed\",\"reason\":\"controlled_failure\"}]}"
        func sequence(_ json: String, _ value: Int) -> String { String(json.dropLast()) + ",\"sequence\":\(value)}" }
        let script = try executable("""
        #!/bin/sh
        case "$1" in
        plan)
            printf '%s\\n' '\(sequence(event(total: "null", phase: "resolve"), 1))' >&2
            while [ ! -f '\(releasePlan.path)' ]; do sleep 0.01; done
            printf '%s\\n' '\(sequence(event(2, state: "finished", phase: "plan"), 2))' >&2
            printf '%s' '\(planJSON)' ;;
        apply)
            printf '%s\\n' '\(sequence(event(1, phase: "apply"), 1))' >&2
            printf '%s\\n' '\(sequence(event(total: "null", phase: "scan"), 2))' >&2
            printf '%s\\n' '\(sequence(event(total: "null", phase: "verify"), 3))' >&2
            while [ ! -f '\(releaseApply.path)' ]; do sleep 0.01; done
            printf '%s\\n' '\(sequence(event(2, state: "finished", phase: "apply"), 4))' >&2
            printf '%s' '\(applyJSON)'
            exit 1 ;;
        scan)
            printf '{"schema":"keepupdated/v1","command":"scan","generated_at":"","items":[],"diagnostics":[],"coverage":[]}' ;;
        esac
        """)
        defer { script.remove() }
        let store = InventoryStore(demo: false, client: CLIClient(testExecutable: script.url))
        var report = DemoData.report; report.items = [item]; store.replaceReport(report)
        store.plan(action: .update, ids: [item.id])
        try await waitUntil { store.progress?.phase == "resolve" }
        XCTAssertNil(store.progress?.fraction)
        XCTAssertTrue(store.canCancelCurrentOperation)
        store.cancel()
        try await waitUntil { !store.busy }
        XCTAssertNil(store.preview)
        XCTAssertNil(store.progress)
        store.plan(action: .update, ids: [item.id])
        try await waitUntil { store.progress?.phase == "resolve" }
        XCTAssertEqual(store.progress?.sequence, 1)
        try Data().write(to: releasePlan)
        try await waitUntil { !store.busy }
        XCTAssertEqual(store.preview?.planId, plan.planId)
        store.execute()
        try await waitUntil { store.progress?.phase == "verify" }
        XCTAssertTrue(store.applying)
        XCTAssertFalse(store.canCancelCurrentOperation)
        XCTAssertNil(store.lastResults)
        try Data().write(to: releaseApply)
        try await waitUntil { !store.busy }
        XCTAssertNil(store.error)
        XCTAssertEqual(store.lastResults?.completed.count, 0)
        XCTAssertEqual(store.lastResults?.failed.count, 1)
        XCTAssertEqual(store.lastResults?.results.first?.reason, "controlled_failure")
        XCTAssertNil(store.progress)
    }

    @MainActor
    private func waitUntil(_ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(3)
        while !condition() && Date() < deadline { try await Task.sleep(nanoseconds: 10_000_000) }
        XCTAssertTrue(condition(), "condition did not become true")
    }

    func testProgressDoesNotReplaceFailureDiagnostics() async throws {
        let script = try executable("#!/bin/sh\nprintf '%s\\n' '\(event())' >&2\nprintf 'controlled failure' >&2\nexit 2\n")
        defer { script.remove() }
        do { _ = try await CLIClient(testExecutable: script.url).run([]) { _ in }; XCTFail("expected failure") }
        catch { XCTAssertTrue((error as? ClientError)?.technicalDetail?.contains("controlled failure") == true) }
    }
}
