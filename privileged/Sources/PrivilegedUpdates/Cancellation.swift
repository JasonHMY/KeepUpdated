import Foundation

// Cancellation races only with the transition into installation, not with the
// serialized work queue. Once installation starts, preserve its result/readback.
final class OperationCancellation {
    private let lock = NSLock()
    private var cancelled = false
    private var installing = false

    func cancel() {
        lock.lock(); defer { lock.unlock() }
        if !installing { cancelled = true }
    }
    func check() throws {
        lock.lock(); defer { lock.unlock() }
        if cancelled { throw PrivilegeError.cancelled }
    }
    func beginInstallation() throws {
        lock.lock(); defer { lock.unlock() }
        if cancelled { throw PrivilegeError.cancelled }
        if installing { throw PrivilegeError.alreadyAttempted }
        installing = true
    }
}
