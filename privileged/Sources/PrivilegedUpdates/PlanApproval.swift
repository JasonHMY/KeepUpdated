import Foundation

public let planApprovalSchema = "keepupdated/approval/v1"
public let planApprovalInputLimit = 4 * 1024 * 1024

// This is an ordinary local confirmation, separate from administrator rights.
public struct PlanApprovalRequest: Codable {
    public let schema: String
    public let challenge: String
    public let planDigest: String
    public let action: String
    public let expiresAt: String
    public let summary: String
    public let details: String

    enum CodingKeys: String, CodingKey {
        case schema, challenge, action, summary, details
        case planDigest = "plan_digest", expiresAt = "expires_at"
    }

    public static func read(_ data: Data, now: Date = Date()) throws -> PlanApprovalRequest {
        let keys: Set<String> = ["schema", "challenge", "plan_digest", "action", "expires_at", "summary", "details"]
        guard !data.isEmpty, data.count <= planApprovalInputLimit,
              let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              Set(object.keys) == keys, object.values.allSatisfy({ $0 is String }) else {
            throw PrivilegeError.invalidRequest
        }
        let request = try JSONDecoder().decode(Self.self, from: data)
        guard request.schema == planApprovalSchema,
              matches(request.challenge, "^[0-9a-f]{64}$"), matches(request.planDigest, "^[0-9a-f]{64}$"),
              ["update", "uninstall"].contains(request.action),
              !request.summary.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              !request.details.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              !request.summary.contains("\0"), !request.details.contains("\0"),
              let expiry = request.expiry, expiry > now else { throw PrivilegeError.invalidRequest }
        return request
    }

    public var expiry: Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: expiresAt) { return date }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: expiresAt)
    }

    public func timeout(now: Date = Date()) -> TimeInterval {
        min(120, max(0, (expiry ?? .distantPast).timeIntervalSince(now)))
    }

    public func response(approved: Bool, now: Date = Date()) -> PlanApprovalResponse {
        PlanApprovalResponse(schema: schema, challenge: challenge, planDigest: planDigest,
                             approved: approved && (expiry ?? .distantPast) > now)
    }
}

public struct PlanApprovalResponse: Codable {
    public let schema: String
    public let challenge: String
    public let planDigest: String
    public let approved: Bool

    enum CodingKeys: String, CodingKey {
        case schema, challenge, approved
        case planDigest = "plan_digest"
    }
}

public func planApprovalLifetimeText(chinese: Bool) -> String {
    chinese
        ? "批准后，即使关闭聊天，操作仍会继续。每个管理器最多运行 15 分钟，整项操作最多 2 小时。这里不提供强制停止安装；每项结果会单独保存。"
        : "After approval, this operation continues even if you close the chat. Each manager is limited to 15 minutes; the whole operation is limited to 2 hours. Installation cannot be force-stopped here. Results are saved for each item."
}
