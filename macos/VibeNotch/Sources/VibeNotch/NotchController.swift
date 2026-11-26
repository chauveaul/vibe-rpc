import AppKit
import Combine
import SwiftUI

/// A borderless, transparent panel pinned over the notch, above the menu bar.
/// It's sized for the expanded state but ignores the mouse unless open, so it
/// never blocks clicks on whatever is underneath.
final class NotchPanel: NSPanel {
    init() {
        super.init(contentRect: .zero, styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        isOpaque = false
        backgroundColor = .clear
        hasShadow = false
        level = NSWindow.Level(rawValue: NSWindow.Level.mainMenu.rawValue + 3)
        collectionBehavior = [.canJoinAllSpaces, .stationary, .fullScreenAuxiliary, .ignoresCycle]
        isMovable = false
        isReleasedWhenClosed = false
        ignoresMouseEvents = true
        hidesOnDeactivate = false
    }

    // Key (but never main, and never activating the app thanks to
    // .nonactivatingPanel) so SwiftUI controls get clicks reliably.
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
}

/// Delivers the very first click to SwiftUI instead of spending it on
/// focusing the window, and lets clicks outside the currently visible notch /
/// alert silhouette pass straight through the transparent panel to apps below.
final class FirstClickHostingView<Content: View>: NSHostingView<Content> {
    var visibleSize: () -> CGSize = { .zero }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func hitTest(_ point: NSPoint) -> NSView? {
        let size = visibleSize()
        guard size.width > 0, size.height > 0 else { return nil }
        let local = convert(point, from: superview)
        let x = (bounds.width - size.width) / 2
        let y = isFlipped ? 0 : (bounds.height - size.height)
        let visible = CGRect(x: x, y: y, width: size.width, height: size.height)
        guard visible.contains(local) else { return nil }
        return super.hitTest(point)
    }
}

/// A transient usage-limit warning shown when a 5h or weekly window crosses
/// 90%, 95%, or 100%. Auto-dismisses after 10 seconds or when hovered.
struct LimitAlert: Equatable, Identifiable {
    let id: String
    let source: String   // "claude" | "antigravity"
    let title: String
    let subtitle: String
    let threshold: Int   // 90 | 95 | 100
    let pct: Int
    let isPreview: Bool
}

/// Owns the panel and turns mouse position into hidden / live / expanded.
@MainActor
final class NotchController: ObservableObject {
    @Published private(set) var mode: NotchMode = .hidden
    @Published private(set) var geometry = NotchGeometry.current()
    /// Alerts worth showing right now: not dismissed, and their app isn't frontmost.
    @Published private(set) var alerts: [NotchFeed.Attention] = []
    /// Active transient usage-limit notifications (auto-dismiss after 10s or on hover).
    @Published private(set) var limitAlerts: [LimitAlert] = []
    /// Alert ids dismissed with ×.
    private var dismissed: Set<String> = []
    /// Highest threshold (90/95/100) already notified per window+reset cycle.
    private var notifiedTiers: [String: Int] = {
        UserDefaults.standard.dictionary(forKey: "vibeNotchNotifiedLimitTiers") as? [String: Int] ?? [:]
    }()
    private var limitTimers: [String: DispatchWorkItem] = [:]

    let feed: FeedClient
    private let panel = NotchPanel()
    private var monitors: [Any] = []
    private var collapseWork: DispatchWorkItem?
    private var hovering = false
    private var feedObserver: AnyCancellable?
    /// `--preview live|expanded|alert|alert2|limit90|limit95|limit100` pins a shape.
    private let pinned: NotchMode? = {
        let a = CommandLine.arguments
        guard let i = a.firstIndex(of: "--preview"), i + 1 < a.count else { return nil }
        switch a[i + 1] {
        case "live": return .live
        case "expanded": return .expanded
        case "alert": return .alert(1)
        case "alert2": return .alert(2)
        case "limit", "limit90", "limit95", "limit100": return .limitAlert(1)
        default: return nil
        }
    }()
    private let previewLimitThreshold: Int? = {
        let a = CommandLine.arguments
        guard let i = a.firstIndex(of: "--preview"), i + 1 < a.count else { return nil }
        switch a[i + 1] {
        case "limit90": return 90
        case "limit", "limit95": return 95
        case "limit100": return 100
        default: return nil
        }
    }()

    init(feed: FeedClient) {
        self.feed = feed
    }

    func start() {
        if let t = previewLimitThreshold {
            limitAlerts = [LimitAlert(
                id: "preview-\(t)",
                source: "antigravity",
                title: t >= 100 ? "Antigravity 5h limit reached" : "Antigravity 5h limit at \(t)%",
                subtitle: "Gemini pool · resets in 2h 14m",
                threshold: t,
                pct: t,
                isPreview: true
            )]
        }
        let root = NotchRootView(controller: self, feed: feed)
        let hosting = FirstClickHostingView(rootView: root)
        hosting.visibleSize = { [weak self] in
            guard let self else { return .zero }
            return self.mode.size(for: self.geometry)
        }
        panel.contentView = hosting
        layoutPanel()
        panel.orderFrontRegardless()

        let mask: NSEvent.EventTypeMask = [.mouseMoved, .leftMouseDragged]
        if let g = NSEvent.addGlobalMonitorForEvents(matching: mask, handler: { [weak self] _ in
            Task { @MainActor in self?.mouseMoved() }
        }) { monitors.append(g) }
        if let l = NSEvent.addLocalMonitorForEvents(matching: mask, handler: { [weak self] e in
            Task { @MainActor in self?.mouseMoved() }
            return e
        }) { monitors.append(l) }

        NotificationCenter.default.addObserver(forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.layoutPanel() }
        }
        // Switching to the agent's terminal/editor counts as having seen the alert.
        NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didActivateApplicationNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.refreshResting() }
        }
        feedObserver = feed.$feed.sink { [weak self] f in
            Task { @MainActor in
                guard let self else { return }
                // Turned off in Settings (or `vibe-rpc notch off`): bow out.
                if let f, !f.enabled, self.pinned == nil {
                    NSApp.terminate(nil)
                    return
                }
                self.refreshResting()
            }
        }
        feed.start()
    }

    /// Pins the panel to the top center of the notch's screen.
    private func layoutPanel() {
        geometry = NotchGeometry.current()
        let size = NotchMode.expandedSize
        let frame = CGRect(x: geometry.notch.midX - size.width / 2,
                           y: geometry.screenFrame.maxY - size.height,
                           width: size.width, height: size.height)
        panel.setFrame(frame, display: true)
    }

    /// The resting (non-hover) shape: interactive alerts first, then transient
    /// usage-limit notifications, then live ears.
    private var restingMode: NotchMode {
        if !alerts.isEmpty { return .alert(alerts.count) }
        if !limitAlerts.isEmpty { return .limitAlert(limitAlerts.count) }
        guard let f = feed.feed, f.active, !f.paused else { return .hidden }
        return .live
    }

    private func refreshResting() {
        updateAlerts()
        checkUsageLimits()
        if let pinned { setMode(pinned); return }
        if mode != .expanded { setMode(restingMode) }
    }

    /// Checks Claude Code and Antigravity 5h/7d windows for 90%, 95%, and 100%
    /// threshold crossings, raising a 10-second / hover-to-dismiss notification
    /// at most once per threshold per reset cycle.
    private func checkUsageLimits() {
        guard pinned == nil, let u = feed.feed?.usage else { return }
        let nowMs = Int64(Date().timeIntervalSince1970 * 1000)
        var changedStore = false

        // Prune expired cycle keys so UserDefaults stays tiny.
        for key in Array(notifiedTiers.keys) {
            if let last = key.split(separator: ":").last, let resetMs = Int64(last), resetMs > 0, resetMs <= nowMs {
                notifiedTiers.removeValue(forKey: key)
                changedStore = true
            }
        }

        struct Candidate {
            let key: String
            let source: String
            let pool: String?
            let isWeek: Bool
            let window: NotchFeed.Usage.Window
        }
        var cands: [Candidate] = []
        if u.claude.source == "statusline" {
            if let s = u.claude.session { cands.append(Candidate(key: "claude-5h", source: "claude", pool: nil, isWeek: false, window: s)) }
            if let w = u.claude.week { cands.append(Candidate(key: "claude-7d", source: "claude", pool: nil, isWeek: true, window: w)) }
        }
        if u.antigravity.source == "statusline" {
            let pool = u.antigravity.pool ?? "gemini"
            if let s = u.antigravity.session { cands.append(Candidate(key: "agy-\(pool)-5h", source: "antigravity", pool: pool, isWeek: false, window: s)) }
            if let w = u.antigravity.week { cands.append(Candidate(key: "agy-\(pool)-7d", source: "antigravity", pool: pool, isWeek: true, window: w)) }
        }

        var newlyAdded: [LimitAlert] = []
        for c in cands {
            let pctInt = Int((c.window.pct * 100).rounded())
            let cycleKey = "\(c.key):\(c.window.resetsAt)"
            if pctInt < 85 && c.window.resetsAt == 0 && notifiedTiers[cycleKey] != nil {
                notifiedTiers.removeValue(forKey: cycleKey)
                changedStore = true
            }
            let threshold: Int
            if pctInt >= 100 { threshold = 100 }
            else if pctInt >= 95 { threshold = 95 }
            else if pctInt >= 90 { threshold = 90 }
            else { continue }

            let prev = notifiedTiers[cycleKey] ?? 0
            guard threshold > prev else { continue }
            notifiedTiers[cycleKey] = threshold
            changedStore = true

            // If the user is already looking at the expanded Usage card, mark
            // the threshold seen without interrupting the open panel.
            if mode == .expanded { continue }

            let agent = c.source == "claude" ? "Claude" : "Antigravity"
            let winLabel = c.isWeek ? "weekly" : "5h"
            let title = threshold >= 100
                ? "\(agent) \(winLabel) limit reached"
                : "\(agent) \(winLabel) limit at \(pctInt)%"
            let subtitle = Self.limitSubtitle(pool: c.pool, isWeek: c.isWeek, resetsAt: c.window.resetsAt)
            newlyAdded.append(LimitAlert(
                id: "\(cycleKey):\(threshold)",
                source: c.source,
                title: title,
                subtitle: subtitle,
                threshold: threshold,
                pct: pctInt,
                isPreview: false
            ))
        }

        if changedStore {
            UserDefaults.standard.set(notifiedTiers, forKey: "vibeNotchNotifiedLimitTiers")
        }
        guard !newlyAdded.isEmpty else { return }

        withAnimation(.spring(response: 0.38, dampingFraction: 0.82)) {
            for item in newlyAdded {
                // Replace any lower-tier active toast for the same window.
                let prefix = item.id.split(separator: ":").dropLast().joined(separator: ":") + ":"
                for old in limitAlerts where old.id.hasPrefix(prefix) {
                    limitTimers.removeValue(forKey: old.id)?.cancel()
                }
                limitAlerts.removeAll { $0.id.hasPrefix(prefix) }
                limitAlerts.insert(item, at: 0)
            }
            if limitAlerts.count > 2 {
                for dropped in limitAlerts.dropFirst(2) {
                    limitTimers.removeValue(forKey: dropped.id)?.cancel()
                }
                limitAlerts = Array(limitAlerts.prefix(2))
            }
        }
        for item in newlyAdded {
            scheduleLimitDismiss(id: item.id)
        }
    }

    private func scheduleLimitDismiss(id: String) {
        limitTimers[id]?.cancel()
        let work = DispatchWorkItem { [weak self] in
            Task { @MainActor in self?.dismissLimitAlert(id: id) }
        }
        limitTimers[id] = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 10.0, execute: work)
    }

    func dismissLimitAlert(id: String) {
        limitTimers.removeValue(forKey: id)?.cancel()
        guard limitAlerts.contains(where: { $0.id == id }) else { return }
        withAnimation(.spring(response: 0.38, dampingFraction: 0.82)) {
            limitAlerts.removeAll { $0.id == id }
        }
        if mode != .expanded && pinned == nil {
            setMode(restingMode)
        }
    }

    func dismissAllLimitAlerts() {
        for (_, work) in limitTimers { work.cancel() }
        limitTimers.removeAll()
        guard !limitAlerts.isEmpty else { return }
        withAnimation(.spring(response: 0.38, dampingFraction: 0.82)) {
            limitAlerts.removeAll()
        }
        if mode != .expanded && pinned == nil {
            setMode(restingMode)
        }
    }

    private static func limitSubtitle(pool: String?, isWeek: Bool, resetsAt: Int64) -> String {
        let prefix: String = {
            if let pool {
                let name = pool == "3p" ? "3P" : "Gemini"
                return isWeek ? "\(name) weekly" : "\(name) pool"
            }
            return isWeek ? "Weekly" : "5-hour window"
        }()
        guard resetsAt > 0 else { return prefix }
        if isWeek {
            let d = Date(timeIntervalSince1970: Double(resetsAt) / 1000)
            let f = DateFormatter()
            f.dateFormat = Calendar.current.isDateInToday(d) ? "HH:mm" : "EEE"
            return "\(prefix) · resets \(f.string(from: d))"
        }
        let s = max(0, Int(Double(resetsAt) / 1000 - Date().timeIntervalSince1970))
        let h = s / 3600, m = (s % 3600) / 60
        let rel = h > 0 ? "\(h)h \(m)m" : "\(m)m"
        return "\(prefix) · resets in \(rel)"
    }

    /// Hides alerts while their app is frontmost (they come back if you leave
    /// without answering) and ones dismissed with ×.
    private func updateAlerts() {
        let incoming = feed.feed?.attention ?? []
        let front = NSWorkspace.shared.frontmostApplication
        dismissed.formIntersection(incoming.map(\.id)) // forget answered prompts
        let visible = incoming.filter { !dismissed.contains($0.id) && !isFront($0, front) }
        if visible != alerts {
            withAnimation(.spring(response: 0.38, dampingFraction: 0.82)) { alerts = visible }
        }
    }

    private func isFront(_ a: NotchFeed.Attention, _ front: NSRunningApplication?) -> Bool {
        guard let front else { return false }
        if let pid = a.hostAppPid, pid > 0, front.processIdentifier == pid { return true }
        if let host = a.hostApp, let url = front.bundleURL, url.path == host { return true }
        return false
    }

    /// Brings the agent's terminal/editor forward; the alert hides while it's
    /// frontmost and disappears for good once the prompt is answered.
    func focus(_ a: NotchFeed.Attention) {
        if let pid = a.hostAppPid, pid > 0, let app = NSRunningApplication(processIdentifier: pid) {
            app.activate()
        } else if let host = a.hostApp {
            NSWorkspace.shared.openApplication(at: URL(fileURLWithPath: host), configuration: .init())
        } else {
            dismiss(a) // nowhere to go: treat the click as "got it"
        }
    }

    func dismiss(_ a: NotchFeed.Attention) {
        dismissed.insert(a.id)
        refreshResting()
    }

    private func notchOnlyRect() -> CGRect {
        let w = geometry.notchWidth
        return CGRect(x: geometry.notch.midX - w / 2, y: geometry.screenFrame.maxY - geometry.notchHeight,
                      width: w, height: geometry.notchHeight)
    }

    /// The visible usage-limit banner hanging directly below the notch.
    private func limitBannerRect() -> CGRect {
        let s = mode.size(for: geometry)
        let h = max(0, s.height - geometry.notchHeight)
        return CGRect(x: geometry.notch.midX - s.width / 2,
                      y: geometry.screenFrame.maxY - s.height,
                      width: s.width, height: h)
    }

    /// Hit area for opening: the notch (or notch + ears) plus a few points
    /// below it, so "hover under the notch" opens it.
    private func triggerRect() -> CGRect {
        // With alerts showing, only the notch itself opens the panel, so
        // the alert rows below stay clickable.
        if case .alert = mode {
            return notchOnlyRect()
        }
        let s = (mode == .live ? NotchMode.live : NotchMode.hidden).size(for: geometry)
        return CGRect(x: geometry.notch.midX - s.width / 2 - 6,
                      y: geometry.screenFrame.maxY - s.height - 8,
                      width: s.width + 12, height: s.height + 8)
    }

    private func expandedRect() -> CGRect {
        let s = NotchMode.expandedSize
        return CGRect(x: geometry.notch.midX - s.width / 2 - 10,
                      y: geometry.screenFrame.maxY - s.height - 14,
                      width: s.width + 20, height: s.height + 14)
    }

    private func mouseMoved() {
        if pinned != nil { return }
        let p = NSEvent.mouseLocation
        if mode == .expanded {
            if expandedRect().contains(p) {
                collapseWork?.cancel()
                collapseWork = nil
            } else if collapseWork == nil {
                let work = DispatchWorkItem { [weak self] in
                    Task { @MainActor in
                        guard let self else { return }
                        self.collapseWork = nil
                        if !self.expandedRect().contains(NSEvent.mouseLocation) { self.setMode(self.restingMode) }
                    }
                }
                collapseWork = work
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.22, execute: work)
            }
        } else if case .limitAlert = mode {
            // Hovering the notification dismisses it immediately; hovering the
            // notch itself dismisses it and opens the expanded panel.
            if limitBannerRect().contains(p) {
                dismissAllLimitAlerts()
            } else if notchOnlyRect().contains(p) {
                dismissAllLimitAlerts()
                setMode(.expanded)
            }
        } else if triggerRect().contains(p) {
            setMode(.expanded)
        }
    }

    func setMode(_ new: NotchMode) {
        guard new != mode else { return }
        withAnimation(.spring(response: 0.38, dampingFraction: 0.82)) { mode = new }
        if case .alert = new {
            panel.ignoresMouseEvents = false
        } else {
            panel.ignoresMouseEvents = (new != .expanded)
        }
    }

    // MARK: Actions

    func pauseToggle() {
        if feed.feed?.paused == true { feed.run(["pause", "off"]) } else { feed.run(["pause", "30m"]) }
    }
    func open(_ page: String) { feed.run([page]) } // serve | settings | wrapped

    /// Persists notch.enabled = false (so the daemon won't relaunch it), then quits.
    func turnOff() {
        feed.run(["notch", "off"])
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { NSApp.terminate(nil) }
    }
}
