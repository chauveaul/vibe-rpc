// swift-tools-version:5.10
// VibeNotch: a live vibe-rpc status panel that lives in the MacBook notch.
import PackageDescription

let package = Package(
    name: "VibeNotch",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "VibeNotch",
            path: "Sources/VibeNotch"
        )
    ]
)
