import Foundation
import PrivilegedUpdates

do { try runHelper() }
catch {
    FileHandle.standardError.write(Data("KeepUpdated helper prerequisites unavailable.\n".utf8))
    exit(1)
}
