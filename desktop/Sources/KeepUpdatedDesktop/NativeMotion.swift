import SwiftUI

/// Shared timing for local interactions; never applied to inventory/report mutations.
enum NativeMotion {
    static let navigation = Animation.spring(response: 0.36, dampingFraction: 1)
    static let categoryNavigation = Animation.easeOut(duration: 0.22)
    static let inventoryEntrance = Animation.spring(response: 0.38, dampingFraction: 1)
    static let selection = Animation.spring(response: 0.22, dampingFraction: 1)
    static let disclosure = Animation.spring(response: 0.26, dampingFraction: 1)
    static let feedback = Animation.easeOut(duration: 0.14)

    static func reveal(reduceMotion: Bool, offset: CGFloat = 8) -> AnyTransition {
        reduceMotion ? .identity : .modifier(
            active: NativeReveal(visibility: 0, visible: false, offset: offset),
            identity: NativeReveal(visibility: 1, visible: true, offset: offset))
    }

    // A narrow overlap keeps two sets of text from becoming readable at once.
    static func opacity(_ visibility: Double) -> Double {
        let value = min(1, max(0, (visibility - 0.45) / 0.55))
        return value * value * (3 - 2 * value)
    }
}

private struct NativeReveal: AnimatableModifier {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var visibility: Double
    let visible: Bool
    let offset: CGFloat
    var animatableData: Double {
        get { visibility }
        set { visibility = newValue }
    }
    func body(content: Content) -> some View {
        content.opacity(reduceMotion ? (visible ? 1 : 0) : NativeMotion.opacity(visibility))
            .offset(y: reduceMotion ? 0 : offset * (1 - visibility))
            .allowsHitTesting(visible).disabled(!visible).accessibilityHidden(!visible)
    }
}

/// Persistent pages can reverse from their current presentation without losing scroll/focus state.
struct NativePageMotion: ViewModifier {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let position: Int
    let selection: Int

    func body(content: Content) -> some View {
        content
            .transaction { $0.animation = nil }
            .modifier(PageVisual(visibility: position == selection ? 1 : 0,
                                 offset: position == selection ? 0 : (position < selection ? -16 : 16),
                                 active: position == selection, reduceMotion: reduceMotion))
            .animation(reduceMotion ? nil : NativeMotion.navigation, value: selection)
            .allowsHitTesting(position == selection)
            .disabled(position != selection)
            .accessibilityHidden(position != selection)
            .zIndex(position == selection ? 1 : 0)
    }

    private struct PageVisual: AnimatableModifier {
        var visibility: Double
        var offset: Double
        let active: Bool
        let reduceMotion: Bool
        var animatableData: AnimatablePair<Double, Double> {
            get { AnimatablePair(visibility, offset) }
            set { visibility = newValue.first; offset = newValue.second }
        }
        func body(content: Content) -> some View {
            content
                .opacity(reduceMotion ? (active ? 1 : 0) : NativeMotion.opacity(visibility))
                .offset(y: reduceMotion ? 0 : offset)
                .scaleEffect(reduceMotion ? 1 : 0.985 + 0.015 * visibility)
        }
    }
}

/// Only custom controls use this feedback; native glass buttons retain their system behavior.
struct MotionButtonStyle: ButtonStyle {
    @State private var hovered = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.isEnabled) private var isEnabled
    var prominent = false

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .opacity(!isEnabled ? 0.5 : (configuration.isPressed ? 0.78 : 1))
            .scaleEffect(prominent && configuration.isPressed && !reduceMotion ? 0.98 : 1)
            .brightness(hovered && isEnabled ? 0.04 : 0)
            .animation(reduceMotion || configuration.isPressed ? nil : NativeMotion.feedback, value: configuration.isPressed)
            .animation(reduceMotion ? nil : NativeMotion.feedback, value: hovered)
            .onHover { hovered = $0 }
    }
}

/// Text-only swaps cannot leave an outgoing action targeting a stale installation.
struct MotionText: View {
    let text: String
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    var body: some View {
        ZStack(alignment: .leading) {
            Text(text).id(text).transition(.opacity)
        }
        .animation(reduceMotion ? nil : NativeMotion.selection, value: text)
        .accessibilityElement(children: .ignore).accessibilityLabel(text).accessibilityAddTraits(.isStaticText)
    }
}

/// A shallow depth reveal: cards settle from the lower right without moving the grid's layout.
struct NativeInventoryEntrance: ViewModifier {
    let visible: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        let settled = visible || reduceMotion
        content
            .opacity(settled ? 1 : 0)
            .scaleEffect(settled ? 1 : 0.96)
            .blur(radius: settled ? 0 : 2)
            .offset(x: settled ? 0 : 24, y: settled ? 0 : 16)
    }
}
