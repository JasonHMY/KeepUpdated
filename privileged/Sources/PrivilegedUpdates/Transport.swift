import Foundation
import Darwin

@objc public protocol PrivilegedService {
    func prepare(_ plan: Data, reply: @escaping (String) -> Void)
    func execute(_ id: String, authorization: Data, reply: @escaping (Data?, String) -> Void)
    func status(_ id: String, reply: @escaping (Data?, String) -> Void)
    func cancel(reply: @escaping () -> Void)
}

private func errorCode(_ error: Error) -> String {
    (error as? PrivilegeError)?.rawValue ?? "verification_unknown"
}

final class Session: NSObject, PrivilegedService {
    let id = UUID()
    let uid: uid_t
    let engine: HelperEngine
    let queue: DispatchQueue
    var right: String?
    let cancellation = OperationCancellation()
    private var prepared = false
    init(uid: uid_t, engine: HelperEngine, queue: DispatchQueue) {
        self.uid = uid; self.engine = engine; self.queue = queue
    }
    func prepare(_ data: Data, reply: @escaping (String) -> Void) {
        queue.async {
            do {
                try self.cancellation.check()
                guard !self.prepared else { throw PrivilegeError.alreadyAttempted }
                self.prepared = true
                let plan = try decode(SpecialPlan.self, data)
                try self.engine.prepare(plan, session: self.id, uid: self.uid)
                do {
                    try self.cancellation.check()
                    try createRight(plan.planID); self.right = plan.planID
                    self.queue.asyncAfter(deadline: .now() + max(0, Double(plan.expiresAt) - Date().timeIntervalSince1970)) {
                        if self.right == plan.planID { self.engine.cancel(session: self.id); self.cleanRight() }
                    }
                }
                catch { self.engine.cancel(session: self.id); throw error }
                reply("")
            } catch { reply(errorCode(error)) }
        }
    }
    func execute(_ id: String, authorization: Data, reply: @escaping (Data?, String) -> Void) {
        queue.async {
            defer { self.cleanRight() }
            do {
                let result = try self.engine.execute(id, session: self.id, cancellation: self.cancellation) { try validateAuthorization(authorization, planID: id) }
                reply(try encode(result), "")
            } catch {
                self.engine.cancel(session: self.id)
                reply(nil, errorCode(error))
            }
        }
    }
    func status(_ id: String, reply: @escaping (Data?, String) -> Void) {
        queue.async {
            do { reply(try encode(self.engine.status(id, uid: self.uid)), "") }
            catch { reply(nil, errorCode(error)) }
        }
    }
    func cancel(reply: @escaping () -> Void) {
        // This must run on the XPC delivery thread, not behind an in-flight backup.
        cancellation.cancel()
        reply()
        queue.async { self.engine.cancel(session: self.id); self.cleanRight() }
    }
    func connectionClosed() { cancel {} }
    private func cleanRight() { if let right { removeRight(right); self.right = nil } }
}

private final class Listener: NSObject, NSXPCListenerDelegate {
    let engine: HelperEngine
    let team: String
    let queue = DispatchQueue(label: "com.keepupdated.privileged.operations")
    init(engine: HelperEngine, team: String) { self.engine = engine; self.team = team }
    func listener(_ listener: NSXPCListener, shouldAcceptNewConnection connection: NSXPCConnection) -> Bool {
        guard connection.effectiveUserIdentifier != 0 else { return false }
        connection.setCodeSigningRequirement(signingRequirement(team: team, identifier: authorizationIdentifier))
        let session = Session(uid: connection.effectiveUserIdentifier, engine: engine, queue: queue)
        connection.exportedInterface = NSXPCInterface(with: PrivilegedService.self)
        connection.exportedObject = session
        connection.invalidationHandler = { session.connectionClosed() }
        connection.interruptionHandler = { session.connectionClosed() }
        connection.resume()
        return true
    }
}

public func runHelper() throws -> Never {
    guard getuid() == 0, geteuid() == 0 else { throw PrivilegeError.authorizationDenied }
    let team = try signingTeam()
    // Do not follow a writable parent into someone else's state directory.
    for path in ["/Library", "/Library/Application Support"] {
        var info = stat()
        guard lstat(path, &info) == 0, info.st_mode & S_IFMT == S_IFDIR, info.st_uid == 0,
              info.st_mode & 0o022 == 0 else { throw PrivilegeError.insecureState }
    }
    let root = "/Library/Application Support/KeepUpdatedPrivileged"
    if mkdir(root, 0o700) != 0 && errno != EEXIST { throw PrivilegeError.insecureState }
    let engine = try HelperEngine(root: root)
    let delegate = Listener(engine: engine, team: team)
    let listener = NSXPCListener(machServiceName: helperServiceName)
    listener.delegate = delegate
    listener.resume()
    withExtendedLifetime((delegate, listener)) { RunLoop.current.run() }
    exit(0)
}

public final class HelperClient {
    private let connection: NSXPCConnection
    public init() throws {
        let team = try signingTeam()
        guard serviceStatus() == "enabled" else { throw PrivilegeError.helperUnavailable }
        connection = NSXPCConnection(machServiceName: helperServiceName, options: .privileged)
        connection.setCodeSigningRequirement(signingRequirement(team: team, identifier: helperServiceName))
        connection.remoteObjectInterface = NSXPCInterface(with: PrivilegedService.self)
        connection.resume()
    }
    deinit { connection.invalidate() }

    // Lost replies must not cause an automatic retry. Query status using the planID.
    private func call(_ invoke: (PrivilegedService, @escaping (Data?, String) -> Void) -> Void) throws -> Data {
        let semaphore = DispatchSemaphore(value: 0)
        let lock = NSLock()
        var completed = false
        var output: Data?
        var error = ""
        let finish: (Data?, String) -> Void = { data, code in
            lock.lock(); defer { lock.unlock() }
            guard !completed else { return }
            completed = true; output = data; error = code; semaphore.signal()
        }
        guard let proxy = connection.remoteObjectProxyWithErrorHandler({ _ in finish(nil, "helper_unavailable") }) as? PrivilegedService else {
            throw PrivilegeError.helperUnavailable
        }
        invoke(proxy, finish)
        guard semaphore.wait(timeout: .now() + 7200) == .success else { throw PrivilegeError.verificationUnknown }
        lock.lock(); defer { lock.unlock() }
        if !error.isEmpty { throw PrivilegeError(rawValue: error) ?? .verificationUnknown }
        return output ?? Data()
    }
    public func prepare(_ plan: SpecialPlan) throws {
        let data = try encode(plan)
        _ = try call { proxy, finish in proxy.prepare(data) { finish(nil, $0) } }
    }
    public func execute(_ plan: SpecialPlan, authorization: OperationAuthorization) throws -> OperationResult {
        let data = try authorization.externalForm()
        let response = try call { proxy, finish in proxy.execute(plan.planID, authorization: data, reply: finish) }
        return try decode(OperationResult.self, response)
    }
    public func status(_ id: String) throws -> OperationResult {
        let response = try call { proxy, finish in proxy.status(id, reply: finish) }
        return try decode(OperationResult.self, response)
    }
    public func cancel() { _ = try? call { proxy, finish in proxy.cancel { finish(nil, "") } } }
}
