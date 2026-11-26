import Foundation

// Mirrors server.NotchFeed in the Go code (internal/server/notchfeed.go).
struct NotchFeed: Decodable, Equatable {
    var version: String = ""
    var enabled: Bool = true
    var daemonUp: Bool = false
    var paused: Bool = false
    var pausedUntil: Int64?

    var status: String = "stale"
    var statusLabel: String = ""
    var active: Bool = false
    var appName: String = "vibe-rpc"
    var details: String = ""
    var state: String = ""
    var source: String = "antigravity"
    var model: String = ""
    var project: String = ""
    var sessionStart: Int64?
    var gifPath: String?
    var gifUrl: String?
    var antigravity: Bool = false
    var claude: Bool = false

    var today: Today = Today()
    var running: [Session] = []
    var usage: Usage = Usage()
    var attention: [Attention] = []

    struct Attention: Decodable, Equatable, Identifiable {
        var id: String
        var source: String
        var project: String
        var kind: String  // permission | question
        var text: String
        var since: Int64  // unix ms
        var hostApp: String?
        var hostAppPid: Int32?
    }

    struct Usage: Decodable, Equatable {
        var claude: Claude = Claude()
        var antigravity: Agy = Agy()

        struct Claude: Decodable, Equatable {
            /// statusline | waiting | setup | other
            var source: String = "setup"
            var session: Window?
            var week: Window?
            var updatedAt: Int64?
        }
        struct Window: Decodable, Equatable {
            var pct: Double
            var resetsAt: Int64  // unix ms, 0 when unknown
        }
        struct Agy: Decodable, Equatable {
            /// statusline | waiting | setup | other
            var source: String = "setup"
            var pool: String?
            var session: Window?
            var week: Window?
            var updatedAt: Int64?
            var windowActiveMs: Int64 = 0
            var windowHours: String = "0h"
            var pct: Double = 0
        }
    }

    struct Today: Decodable, Equatable {
        var hours: String = "0h"
        var activeMs: Int64 = 0
        var prompts: Int = 0
        var tokens: String = "0"
        var cost: String = "$0"
        var streak: Int = 0
        var goalLabel: String?
        var goalPct: Double = 0
        var dualHours: String = "0h"
    }

    struct Session: Decodable, Equatable, Identifiable {
        var source: String
        var project: String
        var status: String
        var statusText: String
        var doing: String?
        var elapsed: String?
        var onDiscord: Bool
        var id: String { source + "|" + project + "|" + (elapsed ?? "") }
    }
}

/// Runs `vibe-rpc notch-feed` and publishes each JSON line it prints.
/// Restarts the feed if it exits (e.g. after `vibe-rpc upgrade`).
@MainActor
final class FeedClient: ObservableObject {
    @Published private(set) var feed: NotchFeed?
    @Published private(set) var problem: String?

    let binary: URL?
    private var process: Process?
    private var buffer = Data()
    private var stopped = false

    init(binary: URL?) {
        self.binary = binary
        if binary == nil {
            problem = "vibe-rpc not found. Install it, then relaunch with `vibe-rpc notch`."
        }
    }

    func start() {
        guard let binary, process == nil, !stopped else { return }
        let p = Process()
        p.executableURL = binary
        p.arguments = ["notch-feed"]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        out.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let chunk = handle.availableData
            guard !chunk.isEmpty else { return }
            Task { @MainActor in self?.ingest(chunk) }
        }
        p.terminationHandler = { [weak self] _ in
            Task { @MainActor in
                guard let self else { return }
                out.fileHandleForReading.readabilityHandler = nil
                self.process = nil
                self.buffer.removeAll()
                if !self.stopped {
                    try? await Task.sleep(nanoseconds: 2_000_000_000)
                    self.start()
                }
            }
        }
        do {
            try p.run()
            process = p
        } catch {
            problem = "Couldn't start \(binary.path): \(error.localizedDescription)"
        }
    }

    func stop() {
        stopped = true
        process?.terminate()
        process = nil
    }

    private func ingest(_ chunk: Data) {
        buffer.append(chunk)
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = buffer.subdata(in: buffer.startIndex..<nl)
            buffer.removeSubrange(buffer.startIndex...nl)
            guard !line.isEmpty else { continue }
            if let decoded = try? JSONDecoder().decode(NotchFeed.self, from: line) {
                if decoded != feed { feed = decoded }
                problem = nil
            }
        }
    }

    /// Runs a vibe-rpc subcommand in the background (pause, serve, …).
    func run(_ args: [String]) {
        // VIBENOTCH_DRYRUN=<file> appends button actions to <file> instead of running them (testing).
        if let log = ProcessInfo.processInfo.environment["VIBENOTCH_DRYRUN"] {
            let line = "\(Date().timeIntervalSince1970) \(args.joined(separator: " "))\n"
            if let h = FileHandle(forWritingAtPath: log) {
                h.seekToEndOfFile(); h.write(Data(line.utf8)); try? h.close()
            } else {
                try? line.write(toFile: log, atomically: true, encoding: .utf8)
            }
            return
        }
        guard let binary else { return }
        let p = Process()
        p.executableURL = binary
        p.arguments = args
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try? p.run()
    }
}

/// Finds the vibe-rpc binary: --vibe-rpc <path> (passed by `vibe-rpc notch`),
/// $VIBE_RPC_BIN, the last path that worked, then common install locations.
enum BinaryLocator {
    private static let defaultsKey = "vibeRpcBinary"

    static func locate() -> URL? {
        var candidates: [String] = []
        let args = CommandLine.arguments
        if let i = args.firstIndex(of: "--vibe-rpc"), i + 1 < args.count {
            candidates.append(args[i + 1])
        }
        if let env = ProcessInfo.processInfo.environment["VIBE_RPC_BIN"] {
            candidates.append(env)
        }
        if let saved = UserDefaults.standard.string(forKey: defaultsKey) {
            candidates.append(saved)
        }
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        candidates += [
            "/opt/homebrew/bin/vibe-rpc",
            "/usr/local/bin/vibe-rpc",
            home + "/.local/bin/vibe-rpc",
            home + "/go/bin/vibe-rpc",
        ]
        for c in candidates where FileManager.default.isExecutableFile(atPath: c) {
            UserDefaults.standard.set(c, forKey: defaultsKey)
            return URL(fileURLWithPath: c)
        }
        return nil
    }
}
