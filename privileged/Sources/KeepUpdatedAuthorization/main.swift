import Foundation
import AppKit
import CryptoKit
import CoreServices
import PrivilegedUpdates

func output<T: Encodable>(_ value: T) throws {
    FileHandle.standardOutput.write(try encode(value) + Data("\n".utf8))
}
func localized(_ english: String, _ chinese: String) -> String {
    let preference = UserDefaults(suiteName: "com.keepupdated.desktop")?.string(forKey: "interfaceLanguage")
    return authorizationLanguage(preference: preference, preferredLanguages: Locale.preferredLanguages) == "zh-Hans" ? chinese : english
}
func confirm(_ title: String, _ detail: String) -> Bool {
    NSApplication.shared.setActivationPolicy(.accessory)
    NSApplication.shared.activate(ignoringOtherApps: true)
    let alert = NSAlert()
    alert.messageText = title
    alert.informativeText = detail
    alert.alertStyle = .warning
    alert.addButton(withTitle: localized("Cancel", "取消"))
    alert.addButton(withTitle: localized("Continue", "继续"))
    return alert.runModal() == .alertSecondButtonReturn
}
func describe(_ request: Request) -> String {
    if let p = request.package {
        let target = "\(p.bundleID): \(p.fromVersion) → \(p.version)\n\(p.installationPath)\n\(p.path)\nSHA-256: \(p.sha256)\nTeam: \(p.teamID)\nReceipt: \(p.receiptID)"
        return target + "\n\n" + localized(
            "Install this selected local signed package with administrator access. Only script-free component packages are accepted. Installed files and services may change. The existing app is backed up, but system changes have no automatic rollback. Cancel before installation starts. Any required restart needs separate approval.",
            "以管理员权限安装这个已选定并签名的本地安装包。仅支持不含脚本的单组件包。安装可能修改文件和服务。现有 App 会备份，但系统变更不能自动回退。可在安装开始前取消；需要重启时会另行确认。")
    }
    if let a = request.appStore {
        return "\(a.bundleID) / \(a.adamID): \(a.fromVersion) → \(a.version)\n\(a.path)\nStore: \(a.region)\n\n" + localized(
            "Grant administrator access to the bundled mas 7.0.0 manager for this App Store item only. App Store sign-in may be required. Store packages may run installation scripts and change services. The app is backed up; restoring an older store version is not automatic. Cancel before the update starts.",
            "授权内置 mas 7.0.0 管理器以管理员权限更新这一项 App Store 应用。可能需要登录 App Store。商店安装包可能运行安装脚本并修改服务。现有 App 会备份，但不能自动恢复旧商店版本。可在更新开始前取消。")
    }
    if let s = request.system {
        return "\(s.label)\nmacOS \(s.fromVersion) (\(s.fromBuild)) → \(s.version) (\(s.build))\n\n" + localized(
            "Install this exact Apple Software Update target with administrator access. System scripts and services may change. There is no automatic system backup or rollback. Cancel before installation starts. Restart requires separate approval.",
            "以管理员权限安装这个准确的 Apple 系统更新。系统脚本和服务可能改变。不会自动备份或回退整个系统。可在安装开始前取消；重启需要另行确认。")
    }
    return "\(request.restartOf ?? "")\n\n" + localized(
        "Show the macOS restart confirmation for this operation. Save your work first. You can cancel the native dialog. Installation success will be checked again after the Mac restarts.",
        "为这次安装打开 macOS 重启确认。请先保存工作，也可以在系统弹窗中取消。Mac 重启后会重新核验安装结果。")
}
func showRestartDialog() throws {
    // Native system dialog permits cancellation and normal unsaved-document handling.
    let target = NSAppleEventDescriptor(bundleIdentifier: "com.apple.loginwindow")
    let event = NSAppleEventDescriptor(eventClass: AEEventClass(kCoreEventClass), eventID: AEEventID(kAEShowRestartDialog),
                                      targetDescriptor: target, returnID: AEReturnID(kAutoGenerateReturnID), transactionID: AETransactionID(kAnyTransactionID))
    _ = try event.sendEvent(options: [.waitForReply], timeout: 30)
}

var operationID = ""
var executionRequested = false
do {
    let args = Array(CommandLine.arguments.dropFirst())
    guard let verb = args.first else { throw PrivilegeError.invalidRequest }
    if verb == "confirm-plan", args.count == 1 {
        let request = try PlanApprovalRequest.read(readPlanApprovalInput())
        try output(confirmPlan(request))
        exit(0)
    }
    if verb == "status", args.count == 1 {
        try output(["status": serviceStatus()]); exit(0)
    }
    if verb == "register-helper", args.count == 1 {
        _ = try signingTeam()
        guard confirm(localized("Enable privileged updates?", "启用需要管理员权限的更新？"), localized("Register KeepUpdated’s signed helper with macOS. An administrator must approve it in System Settings. Each installation still needs its own exact confirmation and administrator authorization.", "向 macOS 注册 KeepUpdated 的已签名 helper。管理员需要在系统设置中批准。之后每次安装仍需确认具体目标，并单独授权管理员权限。")) else { throw PrivilegeError.cancelled }
        try registerHelper(); try output(["status": serviceStatus()]); exit(0)
    }
    if verb == "operation-status", args.count == 2 {
        let client = try HelperClient()
        try output(client.status(args[1])); exit(0)
    }
    if verb == "prepare", args.count == 1 {
        let data = try FileHandle.standardInput.read(upToCount: 65537) ?? Data()
        try output(prepareCandidates(try decode(PreparationInput.self, data)))
        exit(0)
    }
    guard verb == "check" || verb == "execute" else { throw PrivilegeError.invalidRequest }
    let data = try FileHandle.standardInput.read(upToCount: 65537) ?? Data()
    let request = try decode(Request.self, data)
    try request.validate()
    operationID = request.operationID ?? ""
    if verb == "check" {
        guard args.count == 1 else { throw PrivilegeError.invalidRequest }
        _ = try preview(request)
        let state = serviceStatus()
        if state == "enabled", let prior = request.restartOf {
            guard try HelperClient().status(prior).status == "awaiting_restart" else { throw PrivilegeError.noPendingRestart }
        }
        try output(OperationResult(planID: "", status: state == "enabled" ? "ready" : "blocked", reason: state))
        exit(0)
    }
    guard request.operationID != nil, args.count == 3, args[1] == "--confirm",
          args[2] == SHA256.hash(data: data).map({ String(format: "%02x", $0) }).joined() else { throw PrivilegeError.confirmationMismatch }
    let client = try HelperClient()
    let plan = try preview(request)
    operationID = plan.operationID
    guard confirm(localized("Approve this operation?", "确认执行这项操作？"), describe(request)) else { throw PrivilegeError.cancelled }
    try client.prepare(plan)
    do {
        let authorization = try OperationAuthorization(planID: plan.planID)
        executionRequested = true
        var result = try client.execute(plan, authorization: authorization)
        if result.status == "restart_authorized" {
            do { try showRestartDialog(); result.status = "awaiting_restart"; result.reason = "native_restart_dialog_shown" }
            catch { result.status = "awaiting_restart"; result.reason = "native_restart_dialog_unavailable" }
        }
        try output(result)
    } catch { client.cancel(); throw error }
} catch {
    let code = (error as? PrivilegeError)?.rawValue ?? "verification_unknown"
    try? output(OperationResult(planID: operationID, status: code == "cancelled" ? "cancelled" : (executionRequested ? "verification_unknown" : "blocked"), reason: code, executed: executionRequested && code != "cancelled"))
    exit(1)
}
