import AppKit

/// Serialize icon-service and filesystem work away from the main actor.
actor ApplicationIconLoader {
    static let shared = ApplicationIconLoader()

    private struct FileIdentity: Equatable {
        let revision: UUID?
        let resolvedPath: String
        let modified: Date?
        let inode: UInt64?
    }

    private final class Entry: NSObject {
        let identity: FileIdentity
        let image: NSImage
        init(identity: FileIdentity, image: NSImage) {
            self.identity = identity
            self.image = image
        }
    }

    private let cache = NSCache<NSString, Entry>()
    private let readIcon: @Sendable (String) -> NSImage

    init(readIcon: @escaping @Sendable (String) -> NSImage = { NSWorkspace.shared.icon(forFile: $0) }) {
        self.readIcon = readIcon
        cache.countLimit = 256
    }

    func icon(for paths: [String], revision: UUID? = nil) -> NSImage? {
        for path in paths {
            guard !Task.isCancelled else { return nil }
            guard path.hasPrefix("/"), path.lowercased().hasSuffix(".app") else { continue }
            var isDirectory: ObjCBool = false
            guard FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory), isDirectory.boolValue else { continue }
            let resolvedPath = URL(fileURLWithPath: path).resolvingSymlinksInPath().path
            guard let attributes = try? FileManager.default.attributesOfItem(atPath: resolvedPath) else { continue }
            let identity = FileIdentity(revision: revision, resolvedPath: resolvedPath,
                                        modified: attributes[.modificationDate] as? Date,
                                        inode: (attributes[.systemFileNumber] as? NSNumber)?.uint64Value)
            if let entry = cache.object(forKey: path as NSString), entry.identity == identity {
                return entry.image
            }
            let image = readIcon(path)
            cache.setObject(Entry(identity: identity, image: image), forKey: path as NSString)
            return image
        }
        return nil
    }
}
