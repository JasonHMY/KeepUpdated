import SwiftUI

struct InventoryProgressView: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let progress: InventoryProgress
    @ObservedObject private var language = LanguageSettings.shared

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                MotionText(text: progress.state == "cancelled" ? L10n.text("Cancelled") : (progress.state == "failed" ? L10n.text("Stage could not be completed") : progress.phaseLabel))
                    .font(.callout.weight(.semibold))
                Spacer(minLength: 12)
                MotionText(text: processedLabel).font(.caption).monospacedDigit()
            }
            if let fraction = progress.fraction {
                ProgressView(value: fraction).progressViewStyle(.linear).id(progress.phase)
                    .accessibilityLabel(progress.phaseLabel)
                    .accessibilityValue(processedLabel)
                    .animation(reduceMotion ? nil : NativeMotion.selection, value: fraction)
            } else if progress.state == "running" {
                ProgressView().progressViewStyle(.linear)
                    .accessibilityLabel(progress.phaseLabel)
                    .accessibilityValue(processedLabel)
            }
            HStack(alignment: .top) {
                if !progress.currentLabel.isEmpty {
                    Text(progress.currentLabel).foregroundStyle(.secondary)
                }
                Spacer(minLength: 12)
                if progress.unresolved > 0 {
                    Text(L10n.text("Unresolved in this stage: %@", String(progress.unresolved)))
                        .foregroundStyle(.orange)
                }
            }.font(.caption)
        }
        .fixedSize(horizontal: false, vertical: true)
        .accessibilityElement(children: .contain)
        .help(L10n.text("Counts show work processed in this stage, including unresolved results. They do not measure successful updates or time remaining."))
    }

    private var processedLabel: String {
        if let total = progress.total {
            return L10n.text("Stage work: %@ of %@ processed", String(progress.completed), String(total))
        }
        return L10n.text("Stage work: %@ processed", String(progress.completed))
    }
}

/// A named stage with unknown work uses the same linear treatment as counted work.
struct ActivityProgressView: View {
    let label: String
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            MotionText(text: label).font(.callout.weight(.semibold))
            ProgressView().progressViewStyle(.linear).accessibilityLabel(label)
        }.fixedSize(horizontal: false, vertical: true)
    }
}
