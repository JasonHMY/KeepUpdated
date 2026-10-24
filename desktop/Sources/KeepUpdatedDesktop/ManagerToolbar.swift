import SwiftUI

struct ManagerToolbar: View {
    let category: String
    let displayCount: Int
    let language: AppLanguage
    @Binding var query: String
    @Binding var searchExpanded: Bool
    var searchFocused: FocusState<Bool>.Binding
    @Binding var onlyUpdates: Bool
    @Binding var statusFilter: InventoryStatusFilter
    let canSelectAll: Bool
    let home: () -> Void
    let back: () -> Void
    let selectAll: () -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.colorScheme) private var colorScheme

    private var showsSearch: Bool { searchExpanded || !query.isEmpty }

    var body: some View {
        HStack(spacing: 12) {
            Button(action: home) { Image(systemName: "house").frame(width: 28, height: 28) }
                .buttonStyle(.plain).accessibilityLabel(L10n.text("Home")).help(L10n.text("Home"))
            Button(action: back) { Label(L10n.text("Back"), systemImage: "chevron.left") }
                .buttonStyle(.plain).fixedSize()
            Text(Self.countTitle(category: category, count: displayCount))
                .monospacedDigit().fixedSize()
            Spacer(minLength: 0)
            searchBar
            Toggle(L10n.text("Updates only"), isOn: $onlyUpdates).toggleStyle(.checkbox).fixedSize()
            Picker(L10n.text("Update status"), selection: $statusFilter) {
                ForEach(InventoryStatusFilter.allCases) { Text($0.title).tag($0) }
            }.labelsHidden().fixedSize().id(language)
            Button(L10n.text("Select All"), action: selectAll)
                .buttonStyle(.link).fixedSize().disabled(!canSelectAll)
                .help(L10n.text("Select all matching previewable items"))
        }
        .font(.system(size: 13, weight: colorScheme == .dark ? .light : .regular))
        .padding(14)
    }

    static func countTitle(category: String, count: Int) -> String {
        L10n.text(count == 1 ? "%@ %@ item" : "%@ %@ items", category, String(count))
    }

    private var searchBar: some View {
        HStack(spacing: 6) {
            Button {
                if showsSearch && query.isEmpty {
                    searchExpanded = false
                    searchFocused.wrappedValue = false
                } else {
                    searchExpanded = true
                    searchFocused.wrappedValue = true
                }
            } label: { Image(systemName: "magnifyingglass").frame(width: 28, height: 28) }
                .buttonStyle(.plain)
                .accessibilityLabel(L10n.text(showsSearch && query.isEmpty ? "Hide search" : "Search"))
                .help(L10n.text(showsSearch && query.isEmpty ? "Hide search" : "Search"))
            if showsSearch {
                TextField(L10n.text("Search names, sources or paths"), text: $query)
                    .textFieldStyle(.plain).focused(searchFocused)
                    .accessibilityLabel(L10n.text("Search installations"))
                    .frame(minWidth: 100, idealWidth: 160, maxWidth: 200)
                if !query.isEmpty {
                    Button { query = ""; searchFocused.wrappedValue = true } label: { Image(systemName: "xmark.circle.fill") }
                        .buttonStyle(.plain).accessibilityLabel(L10n.text("Clear search"))
                }
            }
        }
        .padding(.horizontal, showsSearch ? 6 : 0)
        .background(.primary.opacity(showsSearch ? 0.03 : 0), in: RoundedRectangle(cornerRadius: 10))
        .animation(reduceMotion ? nil : NativeMotion.disclosure, value: showsSearch)
    }
}
