import SwiftUI

struct RuntimeReviewView: View {
    let review: RuntimeReview
    @ObservedObject private var language = LanguageSettings.shared
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(L10n.text("Runtime Dependency Impact")).font(.headline)
            Text(review.currentVersion + " → " + review.targetVersion + " · " + review.owner).font(.callout)
            Text(review.compatibilityLabel).font(.callout).foregroundStyle(.orange)
            Text(L10n.text("Consumers listed here are for impact review; they are not selected for update or rebuild."))
                .font(.caption).foregroundStyle(.secondary)
            if review.consumers.isEmpty {
                Text(L10n.text("No consumers were identified. Undiscovered dependencies may still exist.")).font(.caption)
            }
            ForEach(Array(review.consumers.enumerated()), id: \.offset) { _, consumer in
                VStack(alignment: .leading, spacing: 3) {
                    Text(consumer.name + (consumer.version.map { " · " + $0 } ?? "")).font(.callout)
                    if let path = consumer.path { Text(path).font(.caption).textSelection(.enabled) }
                }
            }
            if !review.unknownImpact.isEmpty {
                Text(L10n.text("Unknown impact")).font(.subheadline.weight(.semibold))
                ForEach(Array(review.unknownImpact.enumerated()), id: \.offset) { _, note in
                    if L10n.catalog(.english)[note] != nil {
                        Text(L10n.text(note)).font(.caption).foregroundStyle(.orange)
                    } else {
                        DisclosureGroup(L10n.text("Original impact note")) { Text(note).font(.caption).textSelection(.enabled) }
                    }
                }
            }
        }.padding(.vertical, 6)
    }
}

struct MarketplaceRefreshView: View {
    let refresh: MarketplaceRefresh
    @ObservedObject private var language = LanguageSettings.shared
    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(L10n.text("Marketplace Refresh Result")).font(.subheadline.weight(.semibold))
            Text(refresh.stateLabel).font(.callout)
            Text(L10n.text("Marketplace metadata refresh and plugin installation are separate results."))
                .font(.caption).foregroundStyle(.secondary)
            if refresh.attempted {
                Text(refresh.commandSucceeded ? L10n.text("Refresh command completed") : L10n.text("Refresh command failed"))
                    .font(.caption)
            }
            Text(L10n.text("Commit before refresh: %@", refresh.beforeCommit)).font(.caption.monospaced()).textSelection(.enabled)
            if let after = refresh.afterCommit {
                Text(L10n.text("Reported commit after refresh: %@", after)).font(.caption.monospaced()).textSelection(.enabled)
            }
            if !refresh.reason.isEmpty { ReasonMessage(code: refresh.reason) }
        }.padding(.vertical, 6)
    }
}
