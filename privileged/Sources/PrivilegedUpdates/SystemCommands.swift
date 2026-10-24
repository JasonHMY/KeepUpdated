import Foundation
import Darwin
import CryptoKit
import Security

// This interface is injected only in tests, never from request JSON or environment.
protocol SystemCommands {
    func run(_ executable: String, _ arguments: [String]) throws -> CommandOutput
    func store(_ executable: String, _ arguments: [String], uid: uid_t) throws -> CommandOutput
    func verifyCode(_ path: String, requirement: String) throws
}
struct CommandOutput {
    var status: Int32
    var data: Data
    var text: String { String(data: data, encoding: .utf8) ?? "" }
}
struct NativeCommands: SystemCommands {
    func run(_ executable: String, _ arguments: [String]) throws -> CommandOutput {
        guard ["/usr/sbin/pkgutil", "/usr/sbin/spctl", "/usr/bin/xar", "/usr/sbin/installer",
               "/usr/sbin/softwareupdate", "/usr/bin/sw_vers", "/usr/sbin/sysctl", "/usr/bin/otool", "/bin/ps", "/usr/bin/ditto", "/usr/bin/mdls", "/usr/bin/mdfind"].contains(executable) else {
            throw PrivilegeError.invalidRequest
        }
        return try launch(executable, arguments, environment: [:])
    }
    func store(_ executable: String, _ arguments: [String], uid: uid_t) throws -> CommandOutput {
        guard let account = getpwuid(uid), uid != 0 else { throw PrivilegeError.authorizationDenied }
        return try launch(executable, arguments, environment: ["HOME": String(cString: account.pointee.pw_dir),
                          "SUDO_UID": String(uid), "SUDO_GID": String(account.pointee.pw_gid), "MAS_NO_AUTO_INDEX": "1"])
    }
    func verifyCode(_ path: String, requirement: String) throws {
        var code: SecStaticCode?
        var rule: SecRequirement?
        guard SecStaticCodeCreateWithPath(URL(fileURLWithPath: path) as CFURL, [], &code) == errSecSuccess,
              SecRequirementCreateWithString(requirement as CFString, [], &rule) == errSecSuccess,
              let code, let rule,
              SecStaticCodeCheckValidity(code, SecCSFlags(rawValue: kSecCSStrictValidate | kSecCSCheckAllArchitectures), rule) == errSecSuccess else {
            throw PrivilegeError.appStoreIdentityChanged
        }
    }
    private func launch(_ executable: String, _ arguments: [String], environment: [String: String]) throws -> CommandOutput {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        process.environment = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL": "C", "HOME": "/var/empty"]
        process.environment?.merge(environment) { _, new in new }
        process.currentDirectoryURL = URL(fileURLWithPath: "/")
        process.standardInput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        let pipe = Pipe()
        process.standardOutput = pipe
        try process.run()
        // Queries are bounded; an installer already changing the machine is never
        // force-killed as a cancellation mechanism.
        let mutating = executable == "/usr/sbin/installer" && arguments.first == "-pkg" ||
            executable == "/usr/sbin/softwareupdate" && arguments.first == "--install" || arguments.first == "update"
        let timeout = DispatchWorkItem { if process.isRunning { process.terminate() } }
        if !mutating { DispatchQueue.global().asyncAfter(deadline: .now() + 120, execute: timeout) }
        defer { timeout.cancel() }
        var data = Data()
        var overflow = false
        while let part = try pipe.fileHandleForReading.read(upToCount: 65536), !part.isEmpty {
            if data.count + part.count <= 1 << 20 { data.append(part) } else { overflow = true }
        }
        process.waitUntilExit()
        guard !overflow else { throw PrivilegeError.verificationUnknown }
        return CommandOutput(status: process.terminationStatus, data: data)
    }
}

func privateDirectory(_ path: String, owner: uid_t) throws {
    var info = stat()
    guard lstat(path, &info) == 0, info.st_mode & S_IFMT == S_IFDIR,
          info.st_uid == owner, info.st_mode & 0o077 == 0 else { throw PrivilegeError.insecureState }
}

func readRegular(_ path: String, limit: Int = 65536) throws -> Data {
    let fd = open(path, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
    guard fd >= 0 else { throw PrivilegeError.sourceChanged }
    let file = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    var info = stat()
    guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
          info.st_size <= limit else { throw PrivilegeError.sourceChanged }
    var data = Data()
    while data.count <= limit {
        guard let part = try file.read(upToCount: min(65536, limit + 1 - data.count)), !part.isEmpty else { return data }
        data.append(part)
    }
    throw PrivilegeError.sourceChanged
}

// Open once, copy from that descriptor, and hash the bytes actually copied. The helper
// installs only this private copy, never a pathname controlled by the requesting user.
func stagePackage(_ target: PackageTarget, directory: String, owner: uid_t) throws -> String {
    try stageFile(target.path, sha256: target.sha256, directory: directory, filename: "approved.pkg", owner: owner)
}
func stageFile(_ sourcePath: String, sha256: String, directory: String, filename: String, owner: uid_t) throws -> String {
    try privateDirectory(directory, owner: owner)
    let source = open(sourcePath, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
    guard source >= 0 else { throw PrivilegeError.sourceChanged }
    let input = FileHandle(fileDescriptor: source, closeOnDealloc: true)
    var info = stat()
    guard fstat(source, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
          info.st_size > 0, info.st_size <= 8 * 1024 * 1024 * 1024,
          canonicalPath(sourcePath) == sourcePath
    else { throw PrivilegeError.sourceChanged }
    let path = directory + "/" + filename
    let destination = open(path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
    guard destination >= 0 else { throw PrivilegeError.insecureState }
    let output = FileHandle(fileDescriptor: destination, closeOnDealloc: true)
    var digest = SHA256()
    var count: Int64 = 0
    while let part = try input.read(upToCount: 1024 * 1024), !part.isEmpty {
        count += Int64(part.count)
        guard count <= info.st_size else { throw PrivilegeError.sourceChanged }
        digest.update(data: part)
        try output.write(contentsOf: part)
    }
    try output.synchronize()
    guard count == info.st_size,
          digest.finalize().map({ String(format: "%02x", $0) }).joined() == sha256 else {
        throw PrivilegeError.sourceChanged
    }
    return path
}

func canonicalPath(_ path: String) -> String? {
    guard let pointer = realpath(path, nil) else { return nil }
    defer { free(pointer) }
    return String(cString: pointer)
}

func packageSHA256(_ path: String) throws -> String {
    let fd = open(path, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
    guard fd >= 0 else { throw PrivilegeError.sourceChanged }
    let file = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    var info = stat()
    guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
          info.st_size > 0, info.st_size <= 8 * 1024 * 1024 * 1024,
          canonicalPath(path) == path else { throw PrivilegeError.sourceChanged }
    var digest = SHA256(), count: Int64 = 0
    while let part = try file.read(upToCount: 1024 * 1024), !part.isEmpty {
        count += Int64(part.count)
        guard count <= info.st_size else { throw PrivilegeError.sourceChanged }
        digest.update(data: part)
    }
    guard count == info.st_size else { throw PrivilegeError.sourceChanged }
    return digest.finalize().map { String(format: "%02x", $0) }.joined()
}
