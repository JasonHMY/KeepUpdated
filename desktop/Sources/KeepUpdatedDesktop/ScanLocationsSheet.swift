import SwiftUI
import UniformTypeIdentifiers

struct ScanLocationsSheet: View {
    @ObservedObject private var language = LanguageSettings.shared
    @ObservedObject var store: InventoryStore
    @Environment(\.dismiss) private var dismiss
    var onClose: (() -> Void)? = nil
    @State private var selectedKind: ScanLocationKind = .skillsRoot
    @State private var choosingLocation = false
    @State private var selectionError: String?
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(L10n.text("Scan Locations")).font(.title2.weight(.semibold))
            Text(L10n.text("Common installation locations are scanned automatically. Add other agents' Skills, MCP configurations or projects here. Only paths are saved, never configuration contents."))
                .font(.callout).foregroundStyle(.secondary)
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    ForEach(ScanLocationKind.allCases) { kind in
                        VStack(alignment: .leading, spacing: 8) {
                            HStack {
                                Text(kind.label).font(.headline)
                                Spacer()
                                Button(L10n.text("Add…")) { selectedKind = kind; choosingLocation = true }
                                    .accessibilityLabel(L10n.text("Add %@", String(describing: kind.label)))
                            }
                            if store.locations[kind].isEmpty {
                                Text(L10n.text("No extra locations")).font(.caption).foregroundStyle(.secondary)
                            }
                            ForEach(store.locations[kind], id: \.self) { path in
                                HStack {
                                    Text(path).font(.callout).lineLimit(2).truncationMode(.middle).textSelection(.enabled)
                                    Spacer()
                                    Button { store.removeLocation(path, kind: kind) } label: { Image(systemName: "minus.circle") }
                                        .buttonStyle(.plain).accessibilityLabel(L10n.text("Remove scan location %@", String(describing: path)))
                                }
                            }
                        }
                    }
                }.frame(maxWidth: .infinity, alignment: .leading)
            }
            if let selectionError { Text(selectionError).font(.caption).foregroundStyle(.orange) }
            Text(store.demo ? L10n.text("Demo mode does not save locations or read file contents.") : L10n.text("Removing a scan location does not delete its folder or file. Scan again after making changes."))
                .font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button(L10n.text("Done")) { if let onClose { onClose() } else { dismiss() } }.nativeGlassButton(prominent: true).keyboardShortcut(.defaultAction)
            }
        }.padding(24).frame(minWidth: 480, maxWidth: .infinity, minHeight: 380, maxHeight: .infinity, alignment: .topLeading)
            .fileImporter(isPresented: $choosingLocation, allowedContentTypes: selectedKind.selectsDirectory ? [.folder] : [.item], allowsMultipleSelection: true) { result in
                switch result {
                case .success(let urls):
                    // This application is unsandboxed; the picker provides explicit user-selected paths.
                    store.addLocations(urls.map(\.path), kind: selectedKind)
                    selectionError = nil
                case .failure: selectionError = L10n.text("The selected location could not be read. Try again.")
                }
            }
    }
}
