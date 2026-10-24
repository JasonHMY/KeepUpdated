import Foundation

/// Counts work processed in one phase, including unresolved results, not successful updates.
struct InventoryProgress: Decodable, Equatable, Sendable {
    let schema: String
    var sequence: UInt64? = nil
    let phase: String
    let completed: Int
    let total: Int?
    let current: String
    let state: String
    let unresolved: Int

    static let phases = ["scan", "dependencies", "skill-sources", "updates", "resolve", "plan", "apply", "verify", "applications"]
    var valid: Bool {
        schema == "keepupdated/progress/v1" && Self.phases.contains(phase) &&
        ["running", "finished", "cancelled", "failed"].contains(state) &&
        completed >= 0 && completed <= 10_000_000 && unresolved >= 0 && unresolved <= completed &&
        (total == nil || (total! >= completed && total! <= 10_000_000)) &&
        current.utf8.count <= 2048 && !current.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) &&
        (state != "finished" || total == nil || total == completed)
    }
    var fraction: Double? {
        guard !["cancelled", "failed"].contains(state), let total, total > 0 else { return nil }
        return Double(completed) / Double(total)
    }
    var phaseLabel: String {
        switch phase {
        case "scan": return L10n.text("Scanning local installations…")
        case "dependencies": return L10n.text("Checking dependencies…")
        case "skill-sources": return L10n.text("Checking Skill sources…")
        case "applications": return L10n.text("Reading application metadata…")
        case "resolve": return L10n.text("Checking installation sources…")
        case "plan": return L10n.text("Preparing operation preview…")
        case "apply": return L10n.text("Executing approved operations…")
        case "verify": return L10n.text("Verifying installations…")
        default: return L10n.text("Checking available updates…")
        }
    }
    var currentLabel: String {
        guard phase == "scan" else { return current }
        let labels = ["processes": "Running applications", "apps": "Applications", "homebrew": "Homebrew",
                      "packages": "Packages", "executables": "Executables", "runtimes": "Runtimes",
                      "configurations": "Agent configurations", "skills": "Skills", "skill-origins": "Skill origins",
                      "skill-transactions": "Skill recovery records", "system": "System", "services": "Services",
                      "relationships": "Installation relationships"]
        return labels[current].map { L10n.text($0) } ?? ""
    }
    func follows(_ previous: InventoryProgress?) -> Bool {
        guard valid else { return false }
        guard let previous else { return sequence == nil || sequence! > 0 }
        if let sequence {
            guard sequence > 0 else { return false }
            return previous.sequence.map { sequence > $0 } ?? true
        }
        guard previous.sequence == nil else { return false }
        guard let rank = Self.phases.firstIndex(of: phase), let oldRank = Self.phases.firstIndex(of: previous.phase) else { return false }
        if rank != oldRank { return rank > oldRank }
        return previous.state == "running" && total == previous.total && completed >= previous.completed && unresolved >= previous.unresolved
    }
}

/// Bytes are framed before UTF-8 decoding; stderr may also contain ordinary diagnostics.
struct ProgressStream {
    private var line = Data()
    private var discarding = false
    private var last: InventoryProgress?
    private var delivered: InventoryProgress?
    private var lastDelivery: UInt64 = 0
    private(set) var diagnostics = Data()
    private let lineLimit = 8192
    private let diagnosticLimit = 64 * 1024

    mutating func append(_ bytes: Data, onProgress: ((InventoryProgress) -> Void)?) {
        for byte in bytes {
            if byte == 10 {
                if !discarding { consume(onProgress) }
                line.removeAll(keepingCapacity: true)
                discarding = false
            } else if !discarding {
                if line.count < lineLimit { line.append(byte) }
                else { saveDiagnostic(line); line.removeAll(keepingCapacity: true); discarding = true }
            }
        }
    }
    mutating func finish(onProgress: ((InventoryProgress) -> Void)?) {
        if !discarding && !line.isEmpty { consume(onProgress) }
        if let last, last != delivered { onProgress?(last); delivered = last }
        line.removeAll()
    }
    private mutating func saveDiagnostic(_ bytes: Data) {
        diagnostics.append(bytes.prefix(max(0, diagnosticLimit - diagnostics.count)))
    }
    private mutating func consume(_ onProgress: ((InventoryProgress) -> Void)?) {
        if onProgress != nil, let event = try? JSONDecoder().decode(InventoryProgress.self, from: line), event.follows(last) {
            let now = DispatchTime.now().uptimeNanoseconds
            let immediate = event.phase != last?.phase || event.completed < (last?.completed ?? 0) || event.state != "running" || (last?.current.isEmpty == true && !event.current.isEmpty)
            last = event
            if immediate || now - lastDelivery >= 10_000_000 {
                onProgress?(event); delivered = event; lastDelivery = now
            }
        } else { saveDiagnostic(line); saveDiagnostic(Data([10])) }
    }
}
