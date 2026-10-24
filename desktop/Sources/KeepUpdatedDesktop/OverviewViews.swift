import SwiftUI
import AppKit

struct UpdateBackdrop: View {
    var categoryPosition: Double = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        ZStack {
            LinearGradient(colors: [Color(red: 0.015, green: 0.10, blue: 0.28), Color(red: 0.04, green: 0.23, blue: 0.55)], startPoint: .topLeading, endPoint: .bottomTrailing)
            RadialGradient(colors: [Color(red: 0.36, green: 0.10, blue: 0.66).opacity(0.8), .clear], center: UnitPoint(x: 1 - categoryPosition * 0.25, y: 0), startRadius: 0, endRadius: 950)
            RadialGradient(colors: [Color(red: 0, green: 0.48, blue: 0.9).opacity(0.65), .clear], center: UnitPoint(x: 0.32 + categoryPosition * 0.30, y: 0.76), startRadius: 0, endRadius: 650)
        }.animation(reduceMotion ? nil : NativeMotion.categoryNavigation, value: categoryPosition)
            .ignoresSafeArea().accessibilityHidden(true)
    }
}

struct SculptedIcon: View {
    let symbol: String
    var tint: Color = .blue
    var size: CGFloat = 32
    var body: some View {
        Image(systemName: symbol).font(.system(size: size * 0.56, weight: .semibold))
            .foregroundStyle(.white).shadow(color: .black.opacity(0.25), radius: 1, y: 2)
            .frame(width: size, height: size)
            .background(LinearGradient(colors: [.white.opacity(0.85), tint, tint.opacity(0.7)], startPoint: .topLeading, endPoint: .bottomTrailing), in: RoundedRectangle(cornerRadius: size * 0.24))
            .overlay(RoundedRectangle(cornerRadius: size * 0.24).strokeBorder(.white.opacity(0.5), lineWidth: 1))
            .shadow(color: Color(red: 0.02, green: 0.1, blue: 0.3).opacity(0.5), radius: 1, y: 3)
            .shadow(color: .black.opacity(0.2), radius: 4, y: 5)
            .accessibilityHidden(true)
    }
}

extension ItemCategory {
    var iconTint: Color {
        switch self {
        case .skills, .runtimes: return .purple
        case .mcp, .python: return .cyan
        case .homebrew, .pnpm: return .orange
        case .npm, .bun: return .teal
        case .system, .commandLine, .services, .other: return .gray
        default: return .blue
        }
    }
}

struct BrandArtwork: View {
    var size: CGFloat = 300
    var interactive = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var pointer: CGSize = .zero
    @GestureState private var pressed = false

    var body: some View {
        let movement = interactive && !reduceMotion ? pointer : .zero
        Image(nsImage: NSApplication.shared.applicationIconImage)
            .resizable().scaledToFit().frame(width: size, height: size)
            .rotation3DEffect(.degrees(-Double(movement.height) * 5), axis: (x: 1, y: 0, z: 0))
            .rotation3DEffect(.degrees(-12 + Double(movement.width) * 7), axis: (x: 0, y: 1, z: 0))
            .rotationEffect(.degrees(-8))
            .scaleEffect(pressed && interactive && !reduceMotion ? 0.96 : 1)
            .offset(x: movement.width * 8, y: movement.height * 8)
            .shadow(color: .black.opacity(0.3), radius: 12, x: 10, y: 22)
            .animation(reduceMotion ? nil : .spring(response: 0.3, dampingFraction: 0.7), value: movement)
            .animation(reduceMotion ? nil : .spring(response: 0.3, dampingFraction: 0.6), value: pressed)
            .contentShape(Rectangle())
            .onContinuousHover { phase in
                guard interactive, !reduceMotion else { return }
                switch phase {
                case .active(let location):
                    pointer = CGSize(width: min(1, max(-1, location.x / size * 2 - 1)),
                                     height: min(1, max(-1, location.y / size * 2 - 1)))
                case .ended: pointer = .zero
                }
            }
            .gesture(DragGesture(minimumDistance: 0).updating($pressed) { _, value, _ in value = true }, including: interactive ? .all : .none)
            .accessibilityHidden(true)
    }
}

struct OverviewGlass: ViewModifier {
    var darkened = false
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    func body(content: Content) -> some View {
        content.background {
            if reduceTransparency {
                RoundedRectangle(cornerRadius: 18).fill(Color(red: 0.18, green: 0.30, blue: 0.59))
            } else {
                RoundedRectangle(cornerRadius: 18).fill(.ultraThinMaterial).environment(\.colorScheme, .dark)
                    .overlay(RoundedRectangle(cornerRadius: 18).fill(darkened ? Color(red: 0.01, green: 0.1, blue: 0.28).opacity(0.3) : .white.opacity(0.07)))
            }
        }.overlay(RoundedRectangle(cornerRadius: 18).strokeBorder(.white.opacity(0.25), lineWidth: 1))
    }
}

struct WelcomeView: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let busy: Bool
    let check: () -> Void
    var body: some View {
        GeometryReader { geometry in
            ScrollView {
                VStack(spacing: max(36, geometry.size.height * 0.12)) {
                    HStack(spacing: 42) {
                        BrandArtwork(size: min(300, geometry.size.width * 0.28, max(140, geometry.size.height * 0.42)), interactive: true)
                        Text(verbatim: "Keep Updated")
                            .font(.system(size: 48, weight: .light))
                            .fixedSize(horizontal: false, vertical: true)
                    }.frame(maxWidth: .infinity).padding(.horizontal, 48)
                    Button(action: check) {
                        ZStack {
                            Text(L10n.text("Check")).opacity(busy ? 0 : 1)
                            if busy { ProgressView().controlSize(.small).tint(.white) }
                        }.font(.system(size: 21, weight: .regular))
                            .animation(reduceMotion ? nil : NativeMotion.selection, value: busy)
                            .frame(width: 94, height: 94)
                            .nativeCheckGlass()
                    }.buttonStyle(MotionButtonStyle(prominent: true)).disabled(busy)
                        .accessibilityLabel(L10n.text("Check")).accessibilityIdentifier("overview-check")
                }.padding(.vertical, 32).frame(minHeight: geometry.size.height)
            }
        }.foregroundStyle(.white)
    }
}

struct OverviewSummaryView: View {
    let overview: InventoryOverview
    let manage: (InventoryStatusFilter) -> Void
    var body: some View {
        GeometryReader { geometry in
            ScrollView {
                VStack(spacing: 24) {
                    VStack(spacing: 16) {
                        Text(L10n.text("Found %@ installation records", String(overview.items.count))).font(.system(size: 26, weight: .medium))
                        Button(L10n.text("Manage installations")) { manage(.all) }.buttonStyle(OverviewButtonStyle())
                        Text(L10n.text("%@ versions not confirmed", String(overview.unconfirmedVersionCount))).font(.caption).foregroundStyle(.white.opacity(0.8))
                        Text(L10n.text("Each path is counted separately. Categories group related records.")).font(.caption).foregroundStyle(.white.opacity(0.8))
                    }.padding(.vertical, 20)
                    HStack(alignment: .top, spacing: 16) {
                        panel(.updates, symbol: "arrow.up", tint: .blue, explanation: "Preview updates before confirming. Special actions still need separate confirmation.", hero: true)
                            .frame(width: max(240, (geometry.size.width - 92) / 3))
                        VStack(spacing: 16) {
                            panel(.manual, symbol: "arrow.up.right", tint: .orange, explanation: "Continue in the original app or review the available options.")
                            HStack(alignment: .top, spacing: 16) {
                                panel(.unknown, symbol: "questionmark", tint: .cyan, explanation: "Some sources have not confirmed a version.")
                                panel(.current, symbol: "checkmark", tint: .teal, explanation: "These records match their checked source. See details for limits.")
                            }.fixedSize(horizontal: false, vertical: true)
                        }
                    }.fixedSize(horizontal: false, vertical: true)
                }.padding(.horizontal, 30).padding(.bottom, 24)
            }
        }.foregroundStyle(.white)
    }
    private func panel(_ status: InventoryStatusFilter, symbol: String, tint: Color, explanation: String, hero: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top) {
                Text(status.countTitle(overview.count(status))).font(.system(size: 20, weight: .semibold))
                Spacer(minLength: 0)
                if !hero { SculptedIcon(symbol: symbol, tint: tint, size: 40) }
            }
            Text(L10n.text(explanation)).font(.system(size: 16, weight: .light)).foregroundStyle(.white.opacity(0.85)).fixedSize(horizontal: false, vertical: true)
            if hero { Spacer(minLength: 10); BrandArtwork(size: 120).frame(maxWidth: .infinity) }
            Spacer(minLength: 12)
            Button(L10n.text("View items")) { manage(status) }.buttonStyle(OverviewButtonStyle()).frame(maxWidth: .infinity, alignment: .trailing)
        }.padding(20).frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).modifier(OverviewGlass(darkened: hero))
    }
}

struct OverviewButtonStyle: ButtonStyle {
    @State private var hovered = false
    @Environment(\.isEnabled) private var isEnabled
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.font(.system(size: 14, weight: .semibold))
            .foregroundStyle(.white).padding(.horizontal, 16).padding(.vertical, 10)
            .background(reduceTransparency ? Color(red: 0.25, green: 0.37, blue: 0.64) : .white.opacity(configuration.isPressed ? 0.3 : (hovered ? 0.22 : 0.15)), in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(.white.opacity(0.3), lineWidth: 1))
            .opacity(configuration.isPressed ? 0.85 : 1)
            .scaleEffect(configuration.isPressed && !reduceMotion ? 0.985 : 1)
            .animation(reduceMotion ? nil : NativeMotion.feedback, value: configuration.isPressed)
            .animation(reduceMotion ? nil : NativeMotion.feedback, value: hovered)
            .onHover { hovered = $0 && isEnabled }
    }
}
