import AppKit

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var controller: NotchController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let feed = FeedClient(binary: BinaryLocator.locate())
        let controller = NotchController(feed: feed)
        controller.start()
        self.controller = controller
    }

    func applicationWillTerminate(_ notification: Notification) {
        controller?.feed.stop()
    }
}

// Top-level code in main.swift runs on the main thread.
MainActor.assumeIsolated {
    let app = NSApplication.shared
    // No Dock icon or menu bar of its own: VibeNotch lives only in the notch.
    app.setActivationPolicy(.accessory)
    let delegate = AppDelegate()
    app.delegate = delegate
    withExtendedLifetime(delegate) { app.run() }
}
