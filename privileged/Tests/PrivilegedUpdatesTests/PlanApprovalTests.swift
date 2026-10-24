import XCTest
@testable import PrivilegedUpdates

final class PlanApprovalTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1800000000)

    private func input(_ changes: [String: Any] = [:]) throws -> Data {
        var value: [String: Any] = [
            "schema": planApprovalSchema,
            "challenge": String(repeating: "a", count: 64),
            "plan_digest": String(repeating: "b", count: 64),
            "action": "update",
            "expires_at": "2027-01-15T08:10:00.123456789Z",
            "summary": "Two selected installations",
            "details": "Install A 1 → 2\nInstall B 3 → 4\nSkill diff:\n-old\n+new\nCleanup: data\nQuit and reopen: A\nBlocked: B"
        ]
        for (key, valueChange) in changes { value[key] = valueChange }
        return try JSONSerialization.data(withJSONObject: value)
    }

    func testExactRequestPreservesFullDetailsAndBindsBothDecisionFields() throws {
        let request = try PlanApprovalRequest.read(input(), now: now)
        XCTAssertTrue(request.details.contains("Blocked: B"))
        XCTAssertTrue(request.details.contains("-old\n+new"))
        XCTAssertEqual(request.timeout(now: now), 120)
        for approved in [false, true] {
            let response = request.response(approved: approved, now: now)
            let object = try XCTUnwrap(JSONSerialization.jsonObject(with: encode(response)) as? [String: Any])
            XCTAssertEqual(object["schema"] as? String, planApprovalSchema)
            XCTAssertEqual(object["challenge"] as? String, request.challenge)
            XCTAssertEqual(object["plan_digest"] as? String, request.planDigest)
            XCTAssertEqual(object["approved"] as? Bool, approved)
            XCTAssertEqual(object.count, 4)
        }
    }

    func testUnknownFieldsAndInvalidIdentitiesCannotReachConfirmation() throws {
        for changes: [String: Any] in [
            ["approved": true], ["path": "/tmp/plan.json"], ["schema": "other"],
            ["challenge": String(repeating: "A", count: 64)], ["plan_digest": "digest"],
            ["action": "shell"], ["summary": "   "], ["details": "\0hidden"],
            ["expires_at": "invalid"], ["expires_at": "2027-01-15T07:59:59Z"],
            ["challenge": 1]
        ] {
            XCTAssertThrowsError(try PlanApprovalRequest.read(input(changes), now: now), "\(changes)")
        }
        XCTAssertThrowsError(try PlanApprovalRequest.read(Data(repeating: 32, count: planApprovalInputLimit + 1), now: now))
    }

    func testExpiryDuringPromptDeniesAndDeadlineCannotBeExtended() throws {
        let request = try PlanApprovalRequest.read(input(["expires_at": "2027-01-15T08:00:05Z"]), now: now)
        XCTAssertEqual(request.timeout(now: now), 5)
        XCTAssertFalse(request.response(approved: true, now: now.addingTimeInterval(5)).approved)
        XCTAssertEqual(request.timeout(now: now.addingTimeInterval(10)), 0)
    }

    func testLifetimeCopyIsAvailableInBothLanguages() {
        XCTAssertTrue(planApprovalLifetimeText(chinese: false).contains("close the chat"))
        XCTAssertTrue(planApprovalLifetimeText(chinese: false).contains("15 minutes"))
        XCTAssertTrue(planApprovalLifetimeText(chinese: false).contains("2 hours"))
        XCTAssertTrue(planApprovalLifetimeText(chinese: true).contains("关闭聊天"))
        XCTAssertTrue(planApprovalLifetimeText(chinese: true).contains("15 分钟"))
        XCTAssertTrue(planApprovalLifetimeText(chinese: true).contains("2 小时"))
    }
}
