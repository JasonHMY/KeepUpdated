import AppKit
import Foundation
import PrivilegedUpdates

func readPlanApprovalInput() throws -> Data {
    var data = Data()
    while let chunk = try FileHandle.standardInput.read(upToCount: 65536), !chunk.isEmpty {
        guard data.count + chunk.count <= planApprovalInputLimit else { throw PrivilegeError.invalidRequest }
        data.append(chunk)
    }
    return data
}

func confirmPlan(_ request: PlanApprovalRequest) -> PlanApprovalResponse {
    guard request.timeout() > 0 else { return request.response(approved: false) }
    let deadline = Date().addingTimeInterval(request.timeout())
    NSApplication.shared.setActivationPolicy(.accessory)
    NSApplication.shared.activate(ignoringOtherApps: true)
    let alert = NSAlert()
    alert.messageText = request.action == "uninstall"
        ? localized("Approve these uninstalls?", "确认卸载这些项目？")
        : localized("Approve these updates?", "确认更新这些项目？")
    alert.informativeText = localized("Review the full list and effects below. Cancel leaves this plan unexecuted.", "请查看下方完整清单和影响。取消后，这个计划不会执行。") + "\n\n" + localized(
        planApprovalLifetimeText(chinese: false), planApprovalLifetimeText(chinese: true))
    alert.alertStyle = .warning
    alert.addButton(withTitle: localized("Cancel", "取消"))
    alert.addButton(withTitle: localized("Approve", "批准"))
    alert.buttons[0].keyEquivalent = "\r"
    alert.buttons[1].keyEquivalent = ""

    let scroll = NSScrollView(frame: NSRect(x: 0, y: 0, width: 700, height: 420))
    scroll.hasVerticalScroller = true
    scroll.borderType = .bezelBorder
    let text = NSTextView(frame: scroll.contentView.bounds)
    text.isEditable = false
    text.isSelectable = true
    text.isRichText = false
    text.minSize = NSSize(width: 0, height: scroll.contentSize.height)
    text.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
    text.isVerticallyResizable = true
    text.isHorizontallyResizable = false
    text.autoresizingMask = [.width]
    text.textContainer?.containerSize = NSSize(width: scroll.contentSize.width, height: CGFloat.greatestFiniteMagnitude)
    text.textContainer?.widthTracksTextView = true
    text.textContainerInset = NSSize(width: 10, height: 10)
    text.font = .systemFont(ofSize: 13)
    text.string = request.summary + "\n\n" + request.details
    scroll.documentView = text
    alert.accessoryView = scroll

    var timedOut = false
    let timer = Timer(timeInterval: max(0, deadline.timeIntervalSinceNow), repeats: false) { _ in
        timedOut = true
        NSApplication.shared.abortModal()
        alert.window.orderOut(nil)
    }
    RunLoop.main.add(timer, forMode: .modalPanel)
    defer { timer.invalidate() }
    let result = alert.runModal()
    return request.response(approved: !timedOut && Date() < deadline && result == .alertSecondButtonReturn)
}
