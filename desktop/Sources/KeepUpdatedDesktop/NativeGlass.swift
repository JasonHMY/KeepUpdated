import SwiftUI

struct NativeGlassControlGroup<Content: View>: View {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    let spacing: CGFloat
    let content: Content

    init(spacing: CGFloat = 8, @ViewBuilder content: () -> Content) {
        self.spacing = spacing
        self.content = content()
    }

    var body: some View {
        if #available(macOS 26, *), !reduceTransparency {
            GlassEffectContainer(spacing: spacing) { content }
        } else {
            content
        }
    }
}

private struct NativeGlassButtonModifier: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    let prominent: Bool

    @ViewBuilder func body(content: Content) -> some View {
        if #available(macOS 26, *), !reduceTransparency {
            if prominent { content.buttonStyle(.glassProminent) }
            else { content.buttonStyle(.glass) }
        } else {
            if prominent { content.buttonStyle(.borderedProminent) }
            else { content.buttonStyle(.bordered) }
        }
    }
}

private struct NativeCategoryGlassModifier: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let selected: Bool

    @ViewBuilder func body(content: Content) -> some View {
        if reduceTransparency {
            content
                .foregroundStyle(Color(nsColor: selected ? .selectedControlTextColor : .controlTextColor))
                .background(Color(nsColor: selected ? .selectedControlColor : .controlBackgroundColor), in: Capsule())
        } else if #available(macOS 26, *) {
            content.glassEffect(
                selected ? .regular.tint(.accentColor.opacity(0.25)).interactive(!reduceMotion) : .regular.interactive(!reduceMotion),
                in: Capsule()
            )
        } else {
            content
                .background(.thinMaterial, in: Capsule())
                .overlay(Capsule().strokeBorder(selected ? Color.accentColor : Color.clear, lineWidth: 1.5))
        }
    }
}

extension View {
    func nativeGlassButton(prominent: Bool = false) -> some View {
        modifier(NativeGlassButtonModifier(prominent: prominent))
    }

    func nativeCategoryGlass(selected: Bool) -> some View {
        modifier(NativeCategoryGlassModifier(selected: selected))
    }
}

private struct NativeCheckGlassModifier: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    @ViewBuilder func body(content: Content) -> some View {
        if reduceTransparency {
            content.background(Color(red: 0.15, green: 0.32, blue: 0.60), in: Circle())
        } else if #available(macOS 26, *) {
            content.glassEffect(.clear.interactive(!reduceMotion), in: Circle())
        } else {
            content.background(.ultraThinMaterial, in: Circle())
        }
    }
}

extension View {
    func nativeCheckGlass() -> some View { modifier(NativeCheckGlassModifier()) }
}

private struct NativeInventoryCardGlassModifier: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    @ViewBuilder func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: 14)
        if reduceTransparency {
            content.background(Color(nsColor: .controlBackgroundColor), in: shape)
        } else if #available(macOS 26, *) {
            content.glassEffect(.regular.interactive(!reduceMotion), in: shape)
        } else {
            content.background(.thinMaterial, in: shape)
        }
    }
}

extension View {
    func nativeInventoryCardGlass() -> some View { modifier(NativeInventoryCardGlassModifier()) }
}
