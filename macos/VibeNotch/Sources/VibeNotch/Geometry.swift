import AppKit

/// Where the notch is on screen, in AppKit screen coordinates (origin at the
/// bottom-left). On Macs without a notch we pretend there's one the height of
/// the menu bar at the top center, so VibeNotch still works on external
/// displays.
struct NotchGeometry: Equatable {
    var screenFrame: CGRect
    var notch: CGRect
    var hasPhysicalNotch: Bool

    var notchWidth: CGFloat { notch.width }
    var notchHeight: CGFloat { notch.height }

    static func current() -> NotchGeometry {
        let screens = NSScreen.screens
        let screen = screens.first(where: { $0.safeAreaInsets.top > 0 }) ?? NSScreen.main ?? screens.first!
        let frame = screen.frame
        let insetTop = screen.safeAreaInsets.top
        if insetTop > 0,
           let left = screen.auxiliaryTopLeftArea,
           let right = screen.auxiliaryTopRightArea {
            let width = frame.width - left.width - right.width
            let rect = CGRect(x: frame.minX + left.width, y: frame.maxY - insetTop, width: width, height: insetTop)
            return NotchGeometry(screenFrame: frame, notch: rect, hasPhysicalNotch: true)
        }
        let menuBar = max(24, frame.maxY - screen.visibleFrame.maxY)
        let width: CGFloat = 190
        let rect = CGRect(x: frame.midX - width / 2, y: frame.maxY - menuBar, width: width, height: menuBar)
        return NotchGeometry(screenFrame: frame, notch: rect, hasPhysicalNotch: false)
    }
}

/// The shapes VibeNotch can take.
enum NotchMode: Equatable {
    case hidden          // exactly the hardware notch
    case live            // notch + "ears" showing a live activity
    case expanded        // the full panel
    case alert(Int)      // N agents waiting on a permission prompt or question
    case limitAlert(Int) // N transient usage-limit warnings (90% / 95% / 100%)

    static let expandedSize = CGSize(width: 660, height: 210)
    static let earWidth: CGFloat = 74
    static let alertRowHeight: CGFloat = 42

    func size(for g: NotchGeometry) -> CGSize {
        switch self {
        case .hidden: return CGSize(width: g.notchWidth, height: g.notchHeight)
        case .live: return CGSize(width: g.notchWidth + 2 * Self.earWidth, height: g.notchHeight)
        case .expanded: return Self.expandedSize
        case .alert(let n):
            let width = min(Self.expandedSize.width, max(g.notchWidth + 2 * Self.earWidth, 460))
            return CGSize(width: width, height: g.notchHeight + 10 + CGFloat(max(1, n)) * Self.alertRowHeight)
        case .limitAlert(let n):
            let width = min(Self.expandedSize.width, max(g.notchWidth + 2 * Self.earWidth, 440))
            return CGSize(width: width, height: g.notchHeight + 10 + CGFloat(max(1, n)) * Self.alertRowHeight)
        }
    }
}
