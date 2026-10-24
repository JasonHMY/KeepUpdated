import XCTest
@testable import KeepUpdatedDesktop

final class MCPCandidateTests: XCTestCase {
    private func discovery() -> MCPLauncherDiscovery {
        .init(status: "available", reason: "mcp_launcher_candidate_requires_selection", configuredReference: "example@1.0.0", configuredVersion: "1.0.0", observedAt: "2026-10-20T00:00:00Z", candidate: .init(ecosystem: "npm", package: "example", version: "2.0.0", reference: "example@2.0.0", sourceIdentity: "npm-default:fixture", constraintResult: "exact_same_package"), candidateId: String(repeating: "a", count: 64))
    }

    func testCandidateSelectionKeepsBindingAndResetsApprovalOnEdit() {
        let observed = discovery()
        var choices = ReviewChoices()
        choices.selectMCPCandidate(observed)
        XCTAssertEqual(choices.mcpReference, "example@2.0.0")
        XCTAssertEqual(choices.mcpCandidate, observed.candidateId)
        XCTAssertFalse(choices.specialForPreview)
        choices.mcpReferenceApproved = true
        XCTAssertTrue(choices.specialForPreview)
        XCTAssertEqual(choices.arguments, ["--mcp-reference", "example@2.0.0", "--mcp-candidate", observed.candidateId!])
        choices.mcpReference = "example@3.0.0"
        XCTAssertTrue(choices.mcpCandidate.isEmpty)
        XCTAssertFalse(choices.mcpReferenceApproved)
        choices.selectMCPCandidate(observed)
        XCTAssertFalse(choices.mcpReferenceApproved)
    }

    func testUnknownAndUnsupportedCandidatesCannotBeSelected() {
        for change in ["unknown", "constraints", "source", "id"] {
            var value = discovery()
            switch change {
            case "unknown": value.status = "unknown"
            case "constraints": value.candidate?.constraintResult = "unknown"
            case "source": value.candidate?.sourceIdentity = "unverified"
            default: value.candidateId = nil
            }
            var choices = ReviewChoices()
            choices.selectMCPCandidate(value)
            XCTAssertTrue(choices.mcpReference.isEmpty)
            XCTAssertTrue(choices.mcpCandidate.isEmpty)
        }
    }

    func testExecutableCandidateRequiresMatchingSelectionAndPreviewEvidence() {
        let observed = discovery()
        var item = PlanItem(id: "mcp", name: "MCP", kind: "mcp-server", status: "ready", specialApproved: true)
        item.mcp = .init(operation: "replace-package-reference", configPath: "/Example/config.toml", serverKey: "example", packageReference: .init(launcher: "npx", before: "example@1.0.0", after: "example@2.0.0"))
        item.mcpDiscovery = observed
        let selection = PlanSelection(special: true, mcpReference: "example@2.0.0", mcpCandidate: observed.candidateId)
        XCTAssertTrue(item.isExecutable(in: selection))
        var different = selection
        different.mcpCandidate = String(repeating: "b", count: 64)
        XCTAssertFalse(item.isExecutable(in: different))
        item.mcpDiscovery = nil
        XCTAssertFalse(item.isExecutable(in: selection), "Old records without evidence need a new candidate preview")
        different.mcpCandidate = nil
        XCTAssertTrue(item.isExecutable(in: different), "Existing explicitly reviewed manual migrations remain supported")
    }
}
