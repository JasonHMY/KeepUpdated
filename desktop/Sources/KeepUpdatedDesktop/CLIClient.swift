import Foundation
import Darwin

struct CommandOutput {
    var data: Data
    var status: Int32
}

final class CLIClient: @unchecked Sendable {
    private let lock = NSLock()
    private var active: CommandRun?
    #if DEBUG
    private let testExecutable: URL?
    init(testExecutable: URL? = nil) { self.testExecutable = testExecutable }
    #else
    init() {}
    #endif

    func cancel() {
        lock.lock()
        let active = active
        lock.unlock()
        active?.cancel()
    }

    func run(_ arguments: [String], onProgress: (@Sendable (InventoryProgress) -> Void)? = nil) async throws -> CommandOutput {
        let execution = try reserveRun()
        defer { releaseRun(execution) }
        return try await withTaskCancellationHandler(operation: {
            try Task.checkCancellation()
            return try await withCheckedThrowingContinuation { continuation in
                DispatchQueue.global(qos: .userInitiated).async {
                    do { continuation.resume(returning: try self.runBlocking(arguments, execution: execution, onProgress: onProgress)) }
                    catch { continuation.resume(throwing: error) }
                }
            }
        }, onCancel: { execution.cancel() })
    }

    private func reserveRun() throws -> CommandRun {
        lock.lock(); defer { lock.unlock() }
        guard active == nil else { throw ClientError.message("Another command is still running.") }
        let execution = CommandRun()
        active = execution
        return execution
    }
    private func releaseRun(_ execution: CommandRun) {
        lock.lock(); defer { lock.unlock() }
        if active === execution { active = nil }
    }

    private func executable() throws -> URL {
        #if DEBUG
        if let testExecutable { return testExecutable }
        if let path = ProcessInfo.processInfo.environment["KEEPUPDATED_CLI_PATH"], path.hasPrefix("/"), FileManager.default.isExecutableFile(atPath: path) {
            return URL(fileURLWithPath: path)
        }
        #endif
        guard let url = Bundle.main.url(forResource: "keepupdated", withExtension: nil), FileManager.default.isExecutableFile(atPath: url.path) else {
            throw ClientError.message("The bundled CLI is missing. Download the complete KeepUpdated app again.")
        }
        return url
    }

    private func runBlocking(_ arguments: [String], execution: CommandRun,
                             onProgress: (@Sendable (InventoryProgress) -> Void)?) throws -> CommandOutput {
        // Foundation raises an Objective-C exception above this count, bypassing Swift catch.
        guard arguments.count <= 4096 else {
            throw ClientError.message("The command has too many arguments. Reduce the selection or scan locations and try again.")
        }
        let process = Process()
        process.executableURL = try executable()
        process.arguments = arguments
        process.standardInput = FileHandle.nullDevice
        let stdout = Pipe(), stderr = Pipe()
        process.standardOutput = stdout
        process.standardError = stderr
        let output = CapturedData(), errors = CapturedData()
        let group = DispatchGroup()
        try execution.start(process)
        group.enter()
        DispatchQueue.global().async {
            defer { group.leave() }
            do {
                while let chunk = try readPipeChunk(stdout.fileHandleForReading), !chunk.isEmpty {
                    if output.data.count + chunk.count <= 64 * 1024 * 1024 { output.data.append(chunk) }
                    else { output.overflow = true }
                }
            } catch { output.failed = true }
        }
        group.enter()
        DispatchQueue.global().async {
            defer { group.leave() }
            var stream = ProgressStream()
            let receive: ((InventoryProgress) -> Void)? = onProgress.map { callback in
                { event in if !execution.isCancelled { callback(event) } }
            }
            do {
                while let chunk = try readPipeChunk(stderr.fileHandleForReading), !chunk.isEmpty {
                    stream.append(chunk, onProgress: receive)
                }
                stream.finish(onProgress: receive)
            } catch { errors.failed = true }
            errors.data = stream.diagnostics
        }
        process.waitUntilExit()
        group.wait()
        if execution.isCancelled { throw CancellationError() }
        guard process.terminationReason == .exit, process.terminationStatus == 0 || process.terminationStatus == 1 else {
            let detail = String(decoding: errors.data.prefix(2000), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
            throw ClientError.commandFailed(process.terminationStatus, detail)
        }
        if output.overflow { throw ClientError.message("The command output exceeded the size limit.") }
        if output.failed || errors.failed { throw ClientError.message("The command output could not be read completely.") }
        return CommandOutput(data: output.data, status: process.terminationStatus)
    }
}

// FileHandle.read(upToCount:) can wait to fill the requested size on a pipe.
// POSIX read returns the bytes available now, including a single progress line.
private func readPipeChunk(_ handle: FileHandle) throws -> Data? {
    var data = Data(count: 16 * 1024)
    while true {
        let count = data.withUnsafeMutableBytes { Darwin.read(handle.fileDescriptor, $0.baseAddress, $0.count) }
        if count == 0 { return nil }
        if count > 0 { data.count = count; return data }
        if errno != EINTR { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
    }
}

private final class CapturedData: @unchecked Sendable {
    var data = Data()
    var overflow = false
    var failed = false
}

private final class CommandRun: @unchecked Sendable {
    private let lock = NSLock()
    private var process: Process?
    private var cancelled = false
    var isCancelled: Bool { lock.lock(); defer { lock.unlock() }; return cancelled }
    func start(_ process: Process) throws {
        lock.lock(); defer { lock.unlock() }
        if cancelled { throw CancellationError() }
        try process.run()
        self.process = process
    }
    func cancel() {
        lock.lock()
        cancelled = true
        let process = process
        lock.unlock()
        if let process, process.isRunning { process.terminate() }
    }
}

struct PrivatePlanFile {
    let directory: URL
    let url: URL
    init(data: Data) throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("keepupdated-plan-" + UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        let url = directory.appendingPathComponent("plan.json")
        guard FileManager.default.createFile(atPath: url.path, contents: data, attributes: [.posixPermissions: 0o600]) else {
            try? FileManager.default.removeItem(at: directory)
            throw ClientError.message("The operation preview could not be saved.")
        }
        self.directory = directory
        self.url = url
    }
    func remove() { try? FileManager.default.removeItem(at: directory) }
}
