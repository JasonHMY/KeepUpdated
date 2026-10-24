import AppKit
import SwiftUI

/// AppKit owns virtualized scrolling; SwiftUI still owns each inventory card.
struct NativeInventoryCollection<Card: View>: NSViewRepresentable {
    let products: [InventoryTreeNode]
    let active: Bool
    let entrances: InventoryCardEntrances
    @ViewBuilder let card: (InventoryTreeNode) -> Card
    @Environment(\.self) private var environment

    func makeCoordinator() -> Coordinator {
        Coordinator(products: products, active: active, entrances: entrances,
                    environment: environment, card: card)
    }

    func makeNSView(context: Context) -> NativeInventoryScrollView { context.coordinator.makeScrollView() }

    func updateNSView(_ scroll: NativeInventoryScrollView, context: Context) {
        context.coordinator.update(products: products, active: active, environment: environment, card: card)
    }

    static func dismantleNSView(_ scroll: NativeInventoryScrollView, coordinator: Coordinator) {
        coordinator.stop()
    }

    final class Coordinator: NSObject, NSCollectionViewDataSource, NSCollectionViewDelegateFlowLayout {
        private var products: [InventoryTreeNode]
        private var active: Bool
        private let entrances: InventoryCardEntrances
        private var environment: EnvironmentValues
        private var card: (InventoryTreeNode) -> Card
        private var window = InventoryRenderWindow()
        private var heights: [String: (width: CGFloat, height: CGFloat)] = [:]
        private let measurementHost = NSHostingView(rootView: AnyView(EmptyView()))
        private var observer: NSObjectProtocol?
        private var pendingWork: DispatchWorkItem?
        private var generation = 0
        private(set) var mutationInProgress = false
        private var pendingUpdate: PendingUpdate?

        private struct PendingUpdate {
            let products: [InventoryTreeNode]
            let active: Bool
            let environment: EnvironmentValues
            let card: (InventoryTreeNode) -> Card
        }
        private var pendingBottom: Bool?
        private var lastClipY: CGFloat = 0
        private(set) var stopped = false
        private(set) var createdHostingViews = 0
        private(set) var collection: InventoryCollectionView?
        private(set) var scroll: NativeInventoryScrollView?
        var renderedCount: Int { min(products.count, window.limit) }
        var hasPendingScroll: Bool { pendingBottom != nil }

        init(products: [InventoryTreeNode], active: Bool, entrances: InventoryCardEntrances,
             environment: EnvironmentValues, card: @escaping (InventoryTreeNode) -> Card) {
            self.products = products
            self.active = active
            self.entrances = entrances
            self.environment = environment
            self.card = card
        }

        deinit { if let observer { NotificationCenter.default.removeObserver(observer) } }

        func makeScrollView() -> NativeInventoryScrollView {
            let scroll = NativeInventoryScrollView(frame: NSRect(x: 0, y: 0, width: 1120, height: 800))
            scroll.hasVerticalScroller = true
            scroll.hasHorizontalScroller = false
            scroll.autohidesScrollers = true
            scroll.automaticallyAdjustsContentInsets = false
            scroll.contentInsets = NSEdgeInsets(top: 0, left: 0, bottom: 0, right: 0)
            scroll.drawsBackground = false
            let collection = InventoryCollectionView(frame: scroll.contentView.bounds)
            collection.backgroundColors = [.clear]
            collection.isSelectable = false
            collection.setAccessibilityRole(.list)
            collection.setAccessibilitySubrole(.collectionListSubrole)
            let layout = InventoryCollectionLayout()
            layout.minimumInteritemSpacing = 14
            layout.minimumLineSpacing = 14
            layout.sectionInset = NSEdgeInsets(top: 0, left: 18, bottom: 18, right: 18)
            collection.collectionViewLayout = layout
            collection.register(InventoryCollectionItem.self, forItemWithIdentifier: .inventoryCard)
            collection.dataSource = self
            collection.delegate = self
            collection.scrollToEdge = { [weak self] bottom in self?.requestEdge(bottom: bottom) }
            scroll.documentView = collection
            self.scroll = scroll
            self.collection = collection
            scroll.contentView.postsBoundsChangedNotifications = true
            observer = NotificationCenter.default.addObserver(forName: NSView.boundsDidChangeNotification,
                object: scroll.contentView, queue: .main) { [weak self] _ in self?.clipChanged() }
            collection.reloadData()
            return scroll
        }

        func update(products: [InventoryTreeNode], active: Bool, environment: EnvironmentValues,
                    card: @escaping (InventoryTreeNode) -> Card) {
            guard !stopped else { return }
            if mutationInProgress {
                pendingUpdate = PendingUpdate(products: products, active: active, environment: environment, card: card)
                if self.products.map(\.id) != products.map(\.id) || !active {
                    cancelPending()
                    self.active = active
                }
                return
            }
            let changedProducts = self.products.map(\.id) != products.map(\.id)
            let changedText = self.environment.dynamicTypeSize != environment.dynamicTypeSize ||
                self.environment.font != environment.font || self.environment.locale != environment.locale ||
                self.environment.layoutDirection != environment.layoutDirection ||
                self.environment.colorScheme != environment.colorScheme
            self.products = products
            self.card = card
            self.environment = environment
            if self.active != active || changedProducts {
                cancelPending()
                self.active = active
            }
            if changedProducts {
                window = InventoryRenderWindow()
                heights.removeAll()
                collection?.reloadData()
                scrollToBoundary(bottom: false)
            } else {
                if changedText {
                    heights.removeAll()
                    (collection?.collectionViewLayout as? InventoryCollectionLayout)?.invalidateSizing()
                }
                updateVisibleCards()
            }
        }

        func stop() {
            guard !stopped else { return }
            stopped = true
            cancelPending()
            if let observer { NotificationCenter.default.removeObserver(observer) }
            observer = nil
            collection?.scrollToEdge = nil
            collection?.dataSource = nil
            collection?.delegate = nil
            measurementHost.rootView = AnyView(EmptyView())
            heights.removeAll()
            pendingUpdate = nil
        }

        func collectionView(_ collectionView: NSCollectionView, numberOfItemsInSection section: Int) -> Int { renderedCount }

        func collectionView(_ collectionView: NSCollectionView, itemForRepresentedObjectAt indexPath: IndexPath) -> NSCollectionViewItem {
            let item = collectionView.makeItem(withIdentifier: .inventoryCard, for: indexPath) as! InventoryCollectionItem
            guard products.indices.contains(indexPath.item) else { return item }
            configure(item, at: indexPath.item)
            return item
        }

        func collectionView(_ collectionView: NSCollectionView, layout collectionViewLayout: NSCollectionViewLayout,
                            sizeForItemAt indexPath: IndexPath) -> NSSize {
            let width = max(1, (collectionView.bounds.width - 36 - 42) / 4)
            return NSSize(width: width, height: height(at: indexPath.item, width: width))
        }

        func height(at index: Int, width: CGFloat) -> CGFloat {
            guard products.indices.contains(index) else { return 1 }
            let product = products[index]
            if let cached = heights[product.id], cached.width == width { return cached.height }
            measurementHost.rootView = AnyView(card(product).environment(\.self, environment)
                .frame(width: width).fixedSize(horizontal: false, vertical: true)
                .transaction { $0.disablesAnimations = true }.id(product.id))
            let height = max(1, ceil(measurementHost.fittingSize.height))
            heights[product.id] = (width, height)
            return height
        }

        private func configure(_ item: InventoryCollectionItem, at index: Int) {
            let product = products[index]
            let content = card(product)
            let root = AnyView(InventoryEnteringCard(id: product.id, index: index, active: active,
                entrances: entrances) { content }
                .environment(\.self, environment).id(product.id))
            if item.setRoot(root, id: product.id) { createdHostingViews += 1 }
        }

        func collectionView(_ collectionView: NSCollectionView, willDisplay item: NSCollectionViewItem,
                            forRepresentedObjectAt indexPath: IndexPath) {
            guard !stopped, indexPath.item < renderedCount, let item = item as? InventoryCollectionItem else { return }
            configure(item, at: indexPath.item)
        }

        private func updateVisibleCards() {
            guard let collection else { return }
            for item in collection.visibleItems() {
                guard let item = item as? InventoryCollectionItem,
                      let path = collection.indexPath(for: item), path.item < renderedCount else { continue }
                configure(item, at: path.item)
            }
        }

        private func clipChanged() {
            guard let scroll, let collection else { return }
            let clip = scroll.contentView.bounds
            let advanced = clip.minY > lastClipY + 0.5
            lastClipY = clip.minY
            guard active, !stopped, advanced, pendingBottom == nil, !mutationInProgress,
                  renderedCount < products.count else { return }
            let height = max(collection.bounds.height, collection.collectionViewLayout?.collectionViewContentSize.height ?? 0)
            guard height - clip.maxY < max(clip.height, 172 * 3) else { return }
            enqueue { coordinator in coordinator.appendBatch(completion: {}) }
        }

        func requestEdge(bottom: Bool) {
            guard active, !stopped, renderedCount > 0 else { return }
            cancelPending()
            pendingBottom = bottom
            enqueue { $0.driveEdgeRequest() }
        }

        private func driveEdgeRequest() {
            guard active, !stopped, let bottom = pendingBottom, !mutationInProgress else { return }
            if bottom, renderedCount < products.count {
                let token = generation
                appendBatch { [weak self] in
                    guard let self, !self.stopped else { return }
                    if token == self.generation, self.pendingBottom == true { self.scrollToBoundary(bottom: true) }
                    self.enqueue { $0.driveEdgeRequest() }
                }
            } else {
                scrollToBoundary(bottom: bottom)
                pendingBottom = nil
            }
        }

        private func appendBatch(completion: @escaping () -> Void) {
            guard active, !stopped, !mutationInProgress, let collection else { completion(); return }
            let old = renderedCount
            var next = window
            guard next.extend(approaching: old - 1, total: products.count, active: active) else { completion(); return }
            mutationInProgress = true
            NSAnimationContext.runAnimationGroup { context in
                context.duration = 0
                context.allowsImplicitAnimation = false
                collection.performBatchUpdates({
                    self.window = next
                    collection.insertItems(at: Set((old..<self.renderedCount).map { IndexPath(item: $0, section: 0) }))
                }) { [weak self] _ in
                    guard let self else { return }
                    self.mutationInProgress = false
                    guard !self.stopped else { return }
                    if let update = self.pendingUpdate {
                        self.pendingUpdate = nil
                        self.update(products: update.products, active: update.active,
                                    environment: update.environment, card: update.card)
                    }
                    collection.layoutSubtreeIfNeeded()
                    completion()
                    if self.pendingBottom != nil { self.enqueue { $0.driveEdgeRequest() } }
                }
            }
        }

        private func scrollToBoundary(bottom: Bool) {
            guard let scroll, let collection else { return }
            collection.layoutSubtreeIfNeeded()
            let clip = scroll.contentView
            let y = bottom ? max(0, collection.bounds.maxY - clip.bounds.height) : 0
            clip.scroll(to: NSPoint(x: 0, y: y))
            scroll.reflectScrolledClipView(clip)
            collection.layoutSubtreeIfNeeded()
            NSAccessibility.post(element: collection, notification: .layoutChanged)
            lastClipY = clip.bounds.minY
        }

        private func enqueue(_ action: @escaping (Coordinator) -> Void) {
            pendingWork?.cancel()
            let token = generation
            let work = DispatchWorkItem { [weak self] in
                guard let self, !self.stopped, self.active, token == self.generation else { return }
                self.pendingWork = nil
                action(self)
            }
            pendingWork = work
            DispatchQueue.main.async(execute: work)
        }

        private func cancelPending() {
            generation += 1
            pendingWork?.cancel()
            pendingWork = nil
            pendingBottom = nil
        }
    }
}

private extension NSUserInterfaceItemIdentifier {
    static let inventoryCard = Self("inventory-card")
}

final class InventoryCollectionItem: NSCollectionViewItem {
    private(set) var host: NSHostingView<AnyView>?
    private var productID: String?

    override func loadView() { view = NSView() }

    @discardableResult
    func setRoot(_ root: AnyView, id: String) -> Bool {
        let created = host == nil
        if let host { host.rootView = root }
        else {
            let host = NSHostingView(rootView: root)
            host.translatesAutoresizingMaskIntoConstraints = false
            view.addSubview(host)
            NSLayoutConstraint.activate([
                host.leadingAnchor.constraint(equalTo: view.leadingAnchor),
                host.trailingAnchor.constraint(equalTo: view.trailingAnchor),
                host.topAnchor.constraint(equalTo: view.topAnchor),
                host.bottomAnchor.constraint(equalTo: view.bottomAnchor)
            ])
            self.host = host
        }
        if productID != id {
            productID = id
            view.setAccessibilityIdentifier("inventory-cell-" + id)
            NSAccessibility.post(element: view, notification: .layoutChanged)
        }
        return created
    }
}

final class NativeInventoryScrollView: NSScrollView {
    private(set) var layoutPassCount = 0
    private var layoutWidth: CGFloat?

    override func layout() {
        super.layout()
        layoutPassCount += 1
        guard let collection = documentView as? NSCollectionView else { return }
        let width = contentView.bounds.width
        // AppKit may already resize the document before this callback; its cached item metrics still need invalidation.
        guard layoutWidth.map({ abs($0 - width) > 0.5 }) ?? true else { return }
        layoutWidth = width
        if abs(collection.frame.width - width) > 0.5 {
            collection.setFrameSize(NSSize(width: width, height: collection.frame.height))
        }
        (collection.collectionViewLayout as? InventoryCollectionLayout)?.invalidateSizing()
    }
}

final class InventoryCollectionLayout: NSCollectionViewFlowLayout {
    func invalidateSizing() {
        let context = NSCollectionViewFlowLayoutInvalidationContext()
        context.invalidateFlowLayoutDelegateMetrics = true
        context.invalidateFlowLayoutAttributes = true
        invalidateLayout(with: context)
    }

    override func layoutAttributesForElements(in rect: NSRect) -> [NSCollectionViewLayoutAttributes] {
        guard let collectionView else { return super.layoutAttributesForElements(in: rect) }
        let fullWidth = NSRect(x: min(0, rect.minX), y: rect.minY,
                               width: max(collectionView.bounds.maxX, rect.maxX) - min(0, rect.minX), height: rect.height)
        let rows = Set(super.layoutAttributesForElements(in: fullWidth).compactMap { attributes -> IndexPath? in
            guard attributes.representedElementCategory == .item, let path = attributes.indexPath else { return nil }
            return IndexPath(item: (path.item / 4) * 4, section: path.section)
        })
        var result = super.layoutAttributesForElements(in: rect).filter { $0.representedElementCategory != .item }
        var included: Set<IndexPath> = []
        for row in rows.sorted() {
            let end = min(row.item + 4, collectionView.numberOfItems(inSection: row.section))
            guard row.item < end else { continue }
            for index in row.item..<end {
                let path = IndexPath(item: index, section: row.section)
                guard included.insert(path).inserted,
                      let attributes = super.layoutAttributesForItem(at: path) else { continue }
                let aligned = topAligned(attributes)
                if aligned.frame.intersects(rect) { result.append(aligned) }
            }
        }
        return result
    }

    override func layoutAttributesForItem(at indexPath: IndexPath) -> NSCollectionViewLayoutAttributes? {
        super.layoutAttributesForItem(at: indexPath).map(topAligned)
    }

    private func topAligned(_ attributes: NSCollectionViewLayoutAttributes) -> NSCollectionViewLayoutAttributes {
        guard attributes.representedElementCategory == .item, let collectionView,
              let path = attributes.indexPath else { return attributes }
        let start = (path.item / 4) * 4
        let end = min(start + 4, collectionView.numberOfItems(inSection: path.section))
        guard start < end else { return attributes }
        let top = (start..<end).compactMap { index in
            super.layoutAttributesForItem(at: IndexPath(item: index, section: path.section))?.frame.minY
        }.min() ?? attributes.frame.minY
        let aligned = attributes.copy() as! NSCollectionViewLayoutAttributes
        aligned.frame.origin.y = top
        return aligned
    }
}

final class InventoryCollectionView: NSCollectionView {
    var scrollToEdge: ((Bool) -> Void)?
    static let scrollTop = NSAccessibility.Action(rawValue: "AXScrollToTop")
    static let scrollBottom = NSAccessibility.Action(rawValue: "AXScrollToBottom")

    override func setFrameSize(_ newSize: NSSize) {
        let widthChanged = abs(newSize.width - frame.width) > 0.5
        super.setFrameSize(newSize)
        // AppKit can resize the document before the scroll view's layout callback.
        if widthChanged { (collectionViewLayout as? InventoryCollectionLayout)?.invalidateSizing() }
    }

    override func isAccessibilityEnabled() -> Bool { true }

    override func accessibilityChildren() -> [Any]? {
        guard let clip = enclosingScrollView?.contentView, let layout = collectionViewLayout else { return [] }
        let visible = visibleItems().compactMap { item -> (IndexPath, NSView)? in
            guard let path = indexPath(for: item), item.view.window != nil,
                  let frame = layout.layoutAttributesForItem(at: path)?.frame,
                  frame.intersects(clip.bounds) else { return nil }
            return (path, item.view)
        }.sorted { $0.0 < $1.0 }
        return NSAccessibility.unignoredChildren(from: visible.map { $0.1 })
    }

    override func accessibilityVisibleChildren() -> [Any]? { accessibilityChildren() }

    // These public legacy overrides preserve the existing collection's standard endpoint actions.
    override func accessibilityActionNames() -> [NSAccessibility.Action] {
        var actions = super.accessibilityActionNames()
        for action in [Self.scrollTop, Self.scrollBottom] where !actions.contains(action) { actions.append(action) }
        return actions
    }

    override func accessibilityPerformAction(_ action: NSAccessibility.Action) {
        if action == Self.scrollTop { scrollToEdge?(false) }
        else if action == Self.scrollBottom { scrollToEdge?(true) }
        else { super.accessibilityPerformAction(action) }
    }
}
