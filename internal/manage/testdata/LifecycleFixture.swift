import AppKit

// This isolated test app has real NSApplication termination callbacks and an edited document.
final class FixtureDocument: NSDocument {
    override func data(ofType typeName: String) throws -> Data { Data("unsaved fixture".utf8) }
}

final class FixtureDelegate: NSObject, NSApplicationDelegate {
    let control: URL
    let document = FixtureDocument()
    var timer: Timer?
    var awaitingReply = false
    init(control: URL) { self.control = control }
    func mark(_ name: String) { try? Data().write(to: control.appendingPathComponent(name)) }
    func applicationDidFinishLaunching(_ notification: Notification) {
        document.updateChangeCount(.changeDone)
        mark("ready")
        timer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { [self] _ in
            if FileManager.default.fileExists(atPath: control.appendingPathComponent("stop").path) {
                if awaitingReply { NSApplication.shared.reply(toApplicationShouldTerminate: true) }
                else { NSApplication.shared.terminate(nil) }
            }
        }
        RunLoop.main.add(timer!, forMode: .common)
        // A failed harness cannot leave a test application running indefinitely.
        let safetyTimer = Timer.scheduledTimer(withTimeInterval: 45, repeats: false) { [self] _ in
            mark("stop")
        }
        RunLoop.main.add(safetyTimer, forMode: .common)
    }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        mark("requested")
        if FileManager.default.fileExists(atPath: control.appendingPathComponent("stop").path) { return .terminateNow }
        let mode = (try? String(contentsOf: control.appendingPathComponent("mode"), encoding: .utf8)) ?? "allow"
        switch mode {
        case "cancel": return .terminateCancel
        case "unsaved":
            if document.isDocumentEdited { mark("unsaved-refused"); return .terminateCancel }
            return .terminateNow
        case "timeout": awaitingReply = true; return .terminateLater
        default: return .terminateNow
        }
    }
    func applicationWillTerminate(_ notification: Notification) { mark("stopped") }
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
guard let path = Bundle.main.object(forInfoDictionaryKey: "KeepUpdatedFixtureControl") as? String else {
    fatalError("This fixture must run inside its isolated test bundle")
}
let delegate = FixtureDelegate(control: URL(fileURLWithPath: path))
app.delegate = delegate
app.run()
