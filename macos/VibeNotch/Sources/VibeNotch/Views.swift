import AppKit
import SwiftUI

// MARK: - Shape

/// A notch silhouette: flat top flush with the screen edge, small concave
/// flares at the top corners (so it melts into the menu bar like the hardware
/// notch) and rounded bottom corners. Both radii animate.
struct NotchShape: Shape {
    var topRadius: CGFloat
    var bottomRadius: CGFloat

    var animatableData: AnimatablePair<CGFloat, CGFloat> {
        get { AnimatablePair(topRadius, bottomRadius) }
        set { topRadius = newValue.first; bottomRadius = newValue.second }
    }

    func path(in r: CGRect) -> Path {
        let t = min(topRadius, r.width / 4)
        let b = min(bottomRadius, (r.width - 2 * t) / 2, r.height - t)
        var p = Path()
        p.move(to: CGPoint(x: r.minX, y: r.minY))
        p.addQuadCurve(to: CGPoint(x: r.minX + t, y: r.minY + t), control: CGPoint(x: r.minX + t, y: r.minY))
        p.addLine(to: CGPoint(x: r.minX + t, y: r.maxY - b))
        p.addQuadCurve(to: CGPoint(x: r.minX + t + b, y: r.maxY), control: CGPoint(x: r.minX + t, y: r.maxY))
        p.addLine(to: CGPoint(x: r.maxX - t - b, y: r.maxY))
        p.addQuadCurve(to: CGPoint(x: r.maxX - t, y: r.maxY - b), control: CGPoint(x: r.maxX - t, y: r.maxY))
        p.addLine(to: CGPoint(x: r.maxX - t, y: r.minY + t))
        p.addQuadCurve(to: CGPoint(x: r.maxX, y: r.minY), control: CGPoint(x: r.maxX - t, y: r.minY))
        p.closeSubpath()
        return p
    }
}

// MARK: - Palette (matches the vibe-rpc dashboard's monochrome glass)

enum Palette {
    static let text = Color(white: 0.96)
    static let soft = Color(white: 0.78)
    static let mute = Color(white: 0.55)
    static let faint = Color(white: 0.37)
    static let card = Color.white.opacity(0.06)
    static let border = Color.white.opacity(0.09)
    static let green = Color(red: 0.29, green: 0.87, blue: 0.50)
    static let amber = Color(red: 0.98, green: 0.80, blue: 0.08)
    static let orange = Color(red: 0.98, green: 0.57, blue: 0.24)
    static let red = Color(red: 0.97, green: 0.44, blue: 0.44)

    static func limitColor(_ threshold: Int) -> Color {
        if threshold >= 100 { return red }
        if threshold >= 95 { return orange }
        return amber
    }

    static func statusColor(_ s: String) -> Color {
        switch s {
        case "working", "thinking", "compacting", "dual", "trigger": return green
        case "notification": return amber
        case "shipped": return .white
        default: return faint
        }
    }
}

// MARK: - Root

struct NotchRootView: View {
    @ObservedObject var controller: NotchController
    @ObservedObject var feed: FeedClient

    var body: some View {
        let g = controller.geometry
        let mode = controller.mode
        let size = mode.size(for: g)
        let expanded = mode == .expanded
        let isAlert: Bool = {
            switch mode {
            case .alert, .limitAlert: return true
            default: return false
            }
        }()

        VStack(spacing: 0) {
            ZStack(alignment: .top) {
                NotchShape(topRadius: expanded ? 14 : (isAlert ? 10 : 6), bottomRadius: expanded ? 30 : (isAlert ? 22 : 11))
                    .fill(Color.black)
                    .shadow(color: .black.opacity(expanded ? 0.55 : (isAlert ? 0.4 : 0)), radius: 18, y: 8)

                Group {
                    switch mode {
                    case .hidden: EmptyView()
                    case .live: LiveEars(feed: feed.feed, notchWidth: g.notchWidth, height: g.notchHeight)
                    case .expanded: ExpandedPanel(controller: controller, feed: feed, notchHeight: g.notchHeight, notchWidth: g.notchWidth)
                    case .alert: AlertStack(controller: controller, notchHeight: g.notchHeight)
                    case .limitAlert: LimitAlertStack(controller: controller, notchHeight: g.notchHeight)
                    }
                }
                .transition(.opacity.combined(with: .scale(scale: 0.96, anchor: .top)))
            }
            .frame(width: size.width, height: size.height)
            .clipShape(NotchShape(topRadius: expanded ? 14 : (isAlert ? 10 : 6), bottomRadius: expanded ? 30 : (isAlert ? 22 : 11)))
            .contextMenu {
                Button("Settings…") { controller.open("settings") }
                Divider()
                Button("Turn Off VibeNotch") { controller.turnOff() }
            }
            Spacer(minLength: 0)
        }
        .frame(width: NotchMode.expandedSize.width, height: NotchMode.expandedSize.height, alignment: .top)
    }
}

// MARK: - Live activity ears

struct LiveEars: View {
    let feed: NotchFeed?
    let notchWidth: CGFloat
    let height: CGFloat

    var body: some View {
        HStack(spacing: 0) {
            HStack {
                StatusArt(feed: feed, size: height - 10, corner: 6)
                Spacer(minLength: 0)
            }
            .padding(.leading, 12)
            .frame(width: NotchMode.earWidth + 6)

            Spacer().frame(width: notchWidth - 12)

            HStack(spacing: 6) {
                Spacer(minLength: 0)
                PulseDot(color: Palette.statusColor(feed?.status ?? ""))
                ElapsedText(start: feed?.sessionStart, fallback: feed?.today.hours ?? "")
                    .font(.system(size: 11, weight: .semibold, design: .monospaced))
                    .foregroundStyle(Palette.text)
            }
            .padding(.trailing, 14)
            .frame(width: NotchMode.earWidth + 6)
        }
        .frame(height: height)
    }
}

struct PulseDot: View {
    let color: Color
    @State private var on = false
    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 6, height: 6)
            .shadow(color: color.opacity(0.8), radius: on ? 4 : 1)
            .opacity(on ? 1 : 0.55)
            .onAppear { withAnimation(.easeInOut(duration: 0.9).repeatForever()) { on = true } }
    }
}

/// Ticks every second from the session start; shows the fallback otherwise.
struct ElapsedText: View {
    let start: Int64?
    let fallback: String
    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            Text(label(now: ctx.date))
                .monospacedDigit()
        }
    }
    private func label(now: Date) -> String {
        guard let start, start > 0 else { return fallback }
        let s = max(0, Int(now.timeIntervalSince1970) - Int(start / 1000))
        let h = s / 3600, m = (s % 3600) / 60, sec = s % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec) : String(format: "%d:%02d", m, sec)
    }
}

// MARK: - Status art (animated GIF)

struct StatusArt: View {
    let feed: NotchFeed?
    let size: CGFloat
    let corner: CGFloat
    var body: some View {
        Group {
            if let path = feed?.gifPath {
                AnimatedImage(url: URL(fileURLWithPath: path))
            } else if let s = feed?.gifUrl, let u = URL(string: s) {
                AnimatedImage(url: u)
            } else {
                Image(systemName: "sparkles").foregroundStyle(Palette.mute)
            }
        }
        .frame(width: size, height: size)
        .background(Color.white.opacity(0.05))
        .clipShape(RoundedRectangle(cornerRadius: corner, style: .continuous))
    }
}

/// NSImageView animates GIFs natively.
struct AnimatedImage: NSViewRepresentable {
    let url: URL

    func makeNSView(context: Context) -> NSImageView {
        let v = NSImageView()
        v.animates = true
        v.imageScaling = .scaleProportionallyUpOrDown
        v.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        v.setContentCompressionResistancePriority(.defaultLow, for: .vertical)
        load(into: v)
        return v
    }

    func updateNSView(_ v: NSImageView, context: Context) {
        if (v.image?.name() ?? "") != url.absoluteString { load(into: v) }
    }

    private func load(into v: NSImageView) {
        if url.isFileURL {
            let img = NSImage(contentsOf: url)
            img?.setName(url.absoluteString)
            v.image = img
        } else {
            let u = url
            Task.detached {
                guard let data = try? Data(contentsOf: u), let img = NSImage(data: data) else { return }
                await MainActor.run {
                    img.setName(u.absoluteString)
                    v.image = img
                }
            }
        }
    }
}

// MARK: - Expanded panel

struct ExpandedPanel: View {
    @ObservedObject var controller: NotchController
    @ObservedObject var feed: FeedClient
    let notchHeight: CGFloat
    let notchWidth: CGFloat

    var body: some View {
        let f = feed.feed
        VStack(spacing: 10) {
            // Header row sits beside the notch.
            HStack {
                HStack(spacing: 7) {
                    BrandMark()
                    Text("vibe-rpc").font(.system(size: 12, weight: .bold)).foregroundStyle(Palette.text)
                    if let v = f?.version, !v.isEmpty {
                        Text("v\(v)").font(.system(size: 10, design: .monospaced)).foregroundStyle(Palette.faint)
                    }
                }
                Spacer()
                StatusPill(feed: f)
            }
            .frame(height: max(notchHeight - 6, 20))
            .padding(.horizontal, 30)

            if let problem = feed.problem, f == nil {
                Text(problem)
                    .font(.system(size: 12))
                    .foregroundStyle(Palette.soft)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                HStack(alignment: .top, spacing: 10) {
                    DiscordCard(feed: f).frame(width: 232)
                    TodayCard(feed: f).frame(width: 146)
                    UsageCard(feed: f) { controller.open("settings") }
                }
                .frame(height: 118)
                .padding(.horizontal, 26)

                ActionBar(controller: controller, paused: f?.paused ?? false, pausedUntil: f?.pausedUntil)
                    .padding(.horizontal, 26)
            }
            Spacer(minLength: 0)
        }
        .padding(.top, 4)
    }
}

struct BrandMark: View {
    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 5, style: .continuous)
                .fill(LinearGradient(colors: [.white, Color(white: 0.8)], startPoint: .topLeading, endPoint: .bottomTrailing))
            Image(systemName: "chevron.down")
                .font(.system(size: 9, weight: .black))
                .foregroundStyle(.black)
        }
        .frame(width: 16, height: 16)
    }
}

struct StatusPill: View {
    let feed: NotchFeed?
    var body: some View {
        let status = feed?.status ?? "stale"
        let label: String = {
            guard let f = feed else { return "Starting…" }
            if f.paused { return "Paused" }
            if !f.daemonUp { return "Daemon stopped" }
            if status == "dual" { return "Dual wielding" }
            return f.statusLabel.isEmpty ? status.capitalized : f.statusLabel
        }()
        HStack(spacing: 6) {
            Circle().fill(feed?.paused == true ? Palette.faint : Palette.statusColor(status)).frame(width: 6, height: 6)
            Text(label).font(.system(size: 11, weight: .semibold)).foregroundStyle(Palette.soft).lineLimit(1)
        }
        .padding(.horizontal, 9).padding(.vertical, 3)
        .background(Capsule().fill(Color.white.opacity(0.07)))
    }
}

struct Card<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title.uppercased())
                .font(.system(size: 9, weight: .bold, design: .monospaced))
                .tracking(1.2)
                .foregroundStyle(Palette.faint)
            content
            Spacer(minLength: 0)
        }
        .padding(11)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(RoundedRectangle(cornerRadius: 14, style: .continuous).fill(Palette.card))
        .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Palette.border))
    }
}

struct DiscordCard: View {
    let feed: NotchFeed?
    var body: some View {
        Card(title: "On Discord") {
            HStack(alignment: .top, spacing: 10) {
                StatusArt(feed: feed, size: 58, corner: 12)
                VStack(alignment: .leading, spacing: 2) {
                    Text(feed?.appName ?? "vibe-rpc")
                        .font(.system(size: 13, weight: .bold)).foregroundStyle(Palette.text).lineLimit(1)
                    Text(feed?.details ?? "")
                        .font(.system(size: 11.5)).foregroundStyle(Palette.soft).lineLimit(2)
                    Text(feed?.state ?? "")
                        .font(.system(size: 10.5, design: .monospaced)).foregroundStyle(Palette.mute).lineLimit(1)
                    if let f = feed, f.active, let start = f.sessionStart {
                        HStack(spacing: 4) {
                            Text(f.model).lineLimit(1)
                            Text("·")
                            ElapsedText(start: start, fallback: "")
                        }
                        .font(.system(size: 10, weight: .medium, design: .monospaced))
                        .foregroundStyle(Palette.faint)
                        .padding(.top, 1)
                    }
                }
            }
        }
    }
}

struct TodayCard: View {
    let feed: NotchFeed?
    var body: some View {
        let t = feed?.today ?? NotchFeed.Today()
        Card(title: "Today") {
            Text(t.tokens)
                .font(.system(size: 26, weight: .heavy, design: .rounded))
                .lineLimit(1)
                .minimumScaleFactor(0.6)
                .foregroundStyle(LinearGradient(colors: [.white, Color(white: 0.72)], startPoint: .top, endPoint: .bottom))
                .monospacedDigit()
            Text("tokens · \(t.hours) coding")
                .font(.system(size: 10.5, design: .monospaced)).foregroundStyle(Palette.mute).lineLimit(1)
            if t.goalPct > 0 || t.goalLabel != nil {
                GeometryReader { g in
                    ZStack(alignment: .leading) {
                        Capsule().fill(Color.white.opacity(0.08))
                        Capsule().fill(Color.white.opacity(0.85)).frame(width: max(4, g.size.width * t.goalPct))
                    }
                }
                .frame(height: 4)
                .padding(.top, 2)
            } else {
                Text("\(t.cost) · \(t.streak)-day streak")
                    .font(.system(size: 10, design: .monospaced)).foregroundStyle(Palette.faint).lineLimit(1)
            }
        }
    }
}

struct ActionBar: View {
    @ObservedObject var controller: NotchController
    let paused: Bool
    let pausedUntil: Int64?

    var body: some View {
        HStack(spacing: 8) {
            ActionButton(icon: paused ? "play.fill" : "pause.fill",
                         label: paused ? "Resume" : "Pause 30m",
                         prominent: paused) { controller.pauseToggle() }
            if paused, let until = pausedUntil {
                Text("until \(Date(timeIntervalSince1970: Double(until) / 1000).formatted(date: .omitted, time: .shortened))")
                    .font(.system(size: 10, design: .monospaced)).foregroundStyle(Palette.faint)
            }
            Spacer()
            ActionButton(icon: "square.grid.2x2", label: "Dashboard") { controller.open("serve") }
            ActionButton(icon: "slider.horizontal.3", label: "Settings") { controller.open("settings") }
            ActionButton(icon: "sparkles", label: "Wrapped") { controller.open("wrapped") }
        }
    }
}

struct ActionButton: View {
    let icon: String
    let label: String
    var prominent = false
    let action: () -> Void
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            HStack(spacing: 5) {
                Image(systemName: icon).font(.system(size: 10, weight: .bold))
                Text(label).font(.system(size: 11, weight: .semibold))
            }
            .foregroundStyle(prominent ? Color.black : Palette.text)
            .padding(.horizontal, 11).padding(.vertical, 6)
            .background(Capsule().fill(prominent ? Color.white : Color.white.opacity(hover ? 0.14 : 0.07)))
            .overlay(Capsule().strokeBorder(Color.white.opacity(prominent ? 0 : 0.1)))
            // The whole capsule is clickable, not just the glyphs.
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
        .help(label)
    }
}

// MARK: - Usage

/// Real 5-hour session and 7-day weekly plan limits for both Claude Code and
/// Antigravity CLI (auto-switching between Gemini & 3P pools), falling back to
/// rolling 5-hour active time for Antigravity until its status line is enabled.
struct UsageCard: View {
    let feed: NotchFeed?
    let openSettings: () -> Void

    var body: some View {
        let u = feed?.usage ?? NotchFeed.Usage()
        let agyPool = u.antigravity.pool == "3p" ? "3P" : "Gemini"
        Card(title: "Usage") {
            VStack(alignment: .leading, spacing: 6) {
                switch u.claude.source {
                case "statusline":
                    UsageRow(label: "Claude 5h", pct: u.claude.session?.pct, value: pctText(u.claude.session))
                        .help(resetHelp(u.claude.session, isWeek: false, prefix: "Claude 5h"))
                    UsageRow(label: "Claude 7d", pct: u.claude.week?.pct, value: pctText(u.claude.week))
                        .help(resetHelp(u.claude.week, isWeek: true, prefix: "Claude 7d"))
                default:
                    ClaudeHint(source: u.claude.source, openSettings: openSettings)
                }
                switch u.antigravity.source {
                case "statusline":
                    UsageRow(label: "Agy 5h", pct: u.antigravity.session?.pct, value: pctText(u.antigravity.session), tint: Color(white: 0.74))
                        .help(resetHelp(u.antigravity.session, isWeek: false, prefix: "Antigravity \(agyPool) 5h"))
                    UsageRow(label: "Agy 7d", pct: u.antigravity.week?.pct, value: pctText(u.antigravity.week), tint: Color(white: 0.74))
                        .help(resetHelp(u.antigravity.week, isWeek: true, prefix: "Antigravity \(agyPool) 7d"))
                default:
                    UsageRow(label: "Agy 5h", pct: u.antigravity.pct, value: u.antigravity.windowHours, tint: Color(white: 0.62))
                        .help("Active time in the last 5h")
                    AgyCompactHint(source: u.antigravity.source, openSettings: openSettings)
                }
            }
        }
    }

    private func pctText(_ w: NotchFeed.Usage.Window?) -> String {
        guard let w else { return "—" }
        return "\(Int((w.pct * 100).rounded()))%"
    }

    private func resetHelp(_ w: NotchFeed.Usage.Window?, isWeek: Bool, prefix: String) -> String {
        guard let w, w.resetsAt > 0 else { return prefix }
        return isWeek ? "\(prefix) · resets \(weekday(w.resetsAt))" : "\(prefix) · resets in \(relative(w.resetsAt))"
    }

    private func relative(_ ms: Int64) -> String {
        let s = max(0, Int(Double(ms) / 1000 - Date().timeIntervalSince1970))
        let h = s / 3600, m = (s % 3600) / 60
        return h > 0 ? "\(h)h \(m)m" : "\(m)m"
    }

    private func weekday(_ ms: Int64) -> String {
        let d = Date(timeIntervalSince1970: Double(ms) / 1000)
        let f = DateFormatter()
        f.dateFormat = Calendar.current.isDateInToday(d) ? "HH:mm" : "EEE"
        return f.string(from: d)
    }
}

struct UsageRow: View {
    let label: String
    let pct: Double?
    let value: String
    var tint: Color = .white

    var body: some View {
        HStack(spacing: 7) {
            Text(label)
                .font(.system(size: 9.5, weight: .semibold, design: .monospaced))
                .foregroundStyle(Palette.mute)
                .frame(width: 56, alignment: .leading)
            GeometryReader { g in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.white.opacity(0.08))
                    if let pct {
                        Capsule()
                            .fill(color(pct))
                            .frame(width: max(pct > 0 ? 4 : 0, g.size.width * min(1, pct)))
                            .animation(.easeOut(duration: 0.4), value: pct)
                    }
                }
            }
            .frame(height: 6)
            Text(value)
                .font(.system(size: 10, weight: .semibold, design: .monospaced))
                .foregroundStyle(Palette.text)
                .monospacedDigit()
                .frame(width: 34, alignment: .trailing)
        }
        .frame(height: 13)
    }

    private func color(_ p: Double) -> Color {
        if p >= 0.95 { return Color(red: 0.97, green: 0.44, blue: 0.44) }
        if p >= 0.8 { return Palette.amber }
        return tint.opacity(tint == .white ? 0.9 : 1)
    }
}

/// Shown in place of the Claude bars until limits can be read.
struct ClaudeHint: View {
    let source: String
    let openSettings: () -> Void

    var body: some View {
        let text: String = {
            switch source {
            case "waiting": return "Claude limits appear after its next reply"
            case "other": return "Custom Claude status line in use"
            default: return "Turn on Claude limits in Settings"
            }
        }()
        Button(action: openSettings) {
            HStack(spacing: 5) {
                Image(systemName: source == "waiting" ? "hourglass" : "gauge.with.dots.needle.33percent")
                    .font(.system(size: 9.5))
                Text(text)
                    .font(.system(size: 10))
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
            }
            .foregroundStyle(Palette.soft)
            .frame(maxWidth: .infinity, minHeight: 31, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(source == "waiting")
    }
}

/// Compact one-line prompt shown under the Antigravity active-time fallback bar.
struct AgyCompactHint: View {
    let source: String
    let openSettings: () -> Void

    var body: some View {
        let text: String = {
            switch source {
            case "waiting": return "Agy limits after next CLI reply"
            case "other": return "Custom Agy status line in use"
            default: return "Turn on Agy limits in Settings"
            }
        }()
        Button(action: openSettings) {
            HStack(spacing: 5) {
                Image(systemName: source == "waiting" ? "hourglass" : "gauge.with.dots.needle.33percent")
                    .font(.system(size: 9))
                Text(text)
                    .font(.system(size: 9.5))
                    .lineLimit(1)
            }
            .foregroundStyle(Palette.mute)
            .frame(maxWidth: .infinity, minHeight: 13, maxHeight: 13, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(source == "waiting")
    }
}

// MARK: - Usage limit notifications (90% / 95% / 100%)

/// Transient banners that drop below the notch when a 5-hour or weekly quota
/// crosses 90%, 95%, or 100%. They retract automatically after 10 seconds or
/// immediately when hovered.
struct LimitAlertStack: View {
    @ObservedObject var controller: NotchController
    let notchHeight: CGFloat

    var body: some View {
        VStack(spacing: 0) {
            Spacer().frame(height: notchHeight + 4)
            ForEach(controller.limitAlerts) { item in
                LimitAlertRow(alert: item)
                    .frame(height: NotchMode.alertRowHeight)
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 16)
    }
}

struct LimitAlertRow: View {
    let alert: LimitAlert
    @State private var remaining: CGFloat = 1

    var body: some View {
        let accent = Palette.limitColor(alert.threshold)
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                AgentLogo(source: alert.source)
                    .frame(width: 26, height: 26)
                    .overlay(alignment: .topTrailing) {
                        Circle()
                            .fill(accent)
                            .frame(width: 8, height: 8)
                            .overlay(Circle().stroke(Color.black, lineWidth: 1.5))
                            .offset(x: 2, y: -2)
                    }
                VStack(alignment: .leading, spacing: 1) {
                    Text(alert.title)
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(Palette.text)
                        .lineLimit(1)
                        .truncationMode(.tail)
                    Text(alert.subtitle)
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundStyle(Palette.mute)
                        .lineLimit(1)
                }
                Spacer(minLength: 0)
                Text("\(alert.pct)%")
                    .font(.system(size: 11, weight: .bold, design: .monospaced))
                    .monospacedDigit()
                    .foregroundStyle(accent)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(Capsule().fill(accent.opacity(0.14)))
                    .overlay(Capsule().strokeBorder(accent.opacity(0.35)))
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 4)

            GeometryReader { g in
                ZStack(alignment: .leading) {
                    Color.white.opacity(0.05)
                    accent.opacity(0.65)
                        .frame(width: max(0, g.size.width * remaining))
                }
            }
            .frame(height: 2)
        }
        .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Color.white.opacity(0.04)))
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        .padding(.vertical, 2)
        .onAppear {
            guard !alert.isPreview else { return }
            withAnimation(.linear(duration: 10)) { remaining = 0 }
        }
    }
}

// MARK: - Attention alerts

/// Rows that drop below the notch when an agent needs a permission or an
/// answer. Clicking a row jumps to the agent's app; × dismisses it.
struct AlertStack: View {
    @ObservedObject var controller: NotchController
    let notchHeight: CGFloat

    var body: some View {
        VStack(spacing: 0) {
            Spacer().frame(height: notchHeight + 4)
            ForEach(controller.alerts) { a in
                AlertRow(alert: a, focus: { controller.focus(a) }, dismiss: { controller.dismiss(a) })
                    .frame(height: NotchMode.alertRowHeight)
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 16)
    }
}

struct AlertRow: View {
    let alert: NotchFeed.Attention
    let focus: () -> Void
    let dismiss: () -> Void
    @State private var hover = false

    var body: some View {
        HStack(spacing: 10) {
            Button(action: focus) {
                HStack(spacing: 10) {
                    AgentLogo(source: alert.source)
                        .frame(width: 28, height: 28)
                        .overlay(alignment: .topTrailing) {
                            Circle()
                                .fill(Palette.amber)
                                .frame(width: 8, height: 8)
                                .overlay(Circle().stroke(Color.black, lineWidth: 1.5))
                                .offset(x: 2, y: -2)
                        }
                    VStack(alignment: .leading, spacing: 1) {
                        Text(alert.text)
                            .font(.system(size: 12, weight: .semibold))
                            .foregroundStyle(Palette.text)
                            .lineLimit(1)
                            .truncationMode(.tail)
                        HStack(spacing: 4) {
                            Text(alert.source == "claude" ? "Claude Code" : "Antigravity")
                            if !alert.project.isEmpty { Text("·"); Text(alert.project) }
                            Text("·")
                            SinceText(ms: alert.since)
                        }
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundStyle(Palette.mute)
                        .lineLimit(1)
                    }
                    Spacer(minLength: 0)
                    if alert.hostApp != nil || (alert.hostAppPid ?? 0) > 0 {
                        Image(systemName: "arrow.up.forward.app")
                            .font(.system(size: 11, weight: .semibold))
                            .foregroundStyle(hover ? Palette.text : Palette.faint)
                    }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .onHover { hover = $0 }
            .help(alert.hostApp.map { "Switch to " + URL(fileURLWithPath: $0).deletingPathExtension().lastPathComponent } ?? alert.text)

            Button(action: dismiss) {
                Image(systemName: "xmark")
                    .font(.system(size: 9, weight: .bold))
                    .foregroundStyle(Palette.mute)
                    .frame(width: 20, height: 20)
                    .background(Circle().fill(Color.white.opacity(0.07)))
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .help("Dismiss")
        }
        .padding(.horizontal, 8)
        .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Color.white.opacity(hover ? 0.07 : 0.03)))
        .padding(.vertical, 2)
    }
}

/// "12s ago", ticking.
struct SinceText: View {
    let ms: Int64
    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            let s = max(0, Int(ctx.date.timeIntervalSince1970) - Int(ms / 1000))
            Text(s < 60 ? "\(s)s ago" : s < 3600 ? "\(s / 60)m ago" : "\(s / 3600)h ago")
                .monospacedDigit()
        }
    }
}

// MARK: - Agent logos

/// The agent's real logo: the icon of the installed Claude / Antigravity app,
/// falling back to vibe-rpc's own art for that agent.
struct AgentLogo: View {
    let source: String

    var body: some View {
        Image(nsImage: AgentLogo.image(for: source))
            .resizable()
            .interpolation(.high)
            .aspectRatio(contentMode: .fit)
    }

    private static var cache: [String: NSImage] = [:]

    static func image(for source: String) -> NSImage {
        if let img = cache[source] { return img }
        let bundleID = source == "claude" ? "com.anthropic.claudefordesktop" : "com.google.antigravity"
        var img: NSImage?
        if let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) {
            img = NSWorkspace.shared.icon(forFile: url.path)
        }
        if img == nil {
            let gif = FileManager.default.homeDirectoryForCurrentUser
                .appendingPathComponent(".vibe-rpc/gifs/\(source == "claude" ? "claude" : "agy")-idle.gif")
            img = NSImage(contentsOf: gif)
        }
        let result = img ?? NSImage(systemSymbolName: "sparkles", accessibilityDescription: nil) ?? NSImage()
        cache[source] = result
        return result
    }
}
