<p align="center"><img src="./assets/vibe-rpc-icon.png" width="112" alt="vibe-rpc icon"></p>

<h1 align="center">vibe-rpc</h1>

<p align="center">Discord Rich Presence + coding analytics for <b>Google Antigravity</b> and <b>Claude Code</b>.</p>

**100% Pure Go** Discord Rich Presence, live status tracking, and coding analytics for **Google Antigravity** and **Claude Code** — combined into one single native binary with **zero JavaScript/TypeScript** anywhere (even the local web dashboard, settings editor, and Wrapped story are pure Go SSR HTML + CSS).

Built to match the functionality, paper-and-ink aesthetic, customizability, and Discord Rich Presence card feel of `claude-rpc` while unifying **both Antigravity and Claude Code**.

## Features

- **Combined Dual-Agent Tracking**
  - Hooks into **Antigravity** (`~/.gemini/config/hooks.json`, strictly obeying the fail-closed `PreToolUse` `{"decision":"allow"}` contract) and **Claude Code** (`~/.claude/settings.json`, `{"continue":true}`).
  - Passively merges live sessions from `$TMPDIR/claude-rpc` and scans transcripts from both `~/.gemini/{antigravity,antigravity-cli,antigravity-ide}/brain/` and `~/.claude/projects/`.
  - Dynamically switches your Discord card's app title between **`Antigravity`** and **`Claude Code`** depending on which agent is active (configurable).
- **Exact Discord Rich Presence Integration**
  - Direct raw binary Discord IPC protocol (macOS/Linux Unix sockets + Windows Named Pipes) with automatic arRPC / Vesktop bridge detection, keepalive, sliding-window rate throttling (4 writes / 20s), exponential reconnect backoff, animated GIF status assets, conditional rotation frames, multi-session party counts, and automatic public GitHub repo buttons.
- **Zero-JS Local Web Suite** (`http://127.0.0.1:47475`)
  - `/` — Editorial paper-and-ink dashboard with time ranges (`7d`/`30d`/`90d`/`1y`/`all`), live Discord preview rail, pure Go SVG activity & code churn charts, clickable 90-day heatmap day modal, project breakdown drawer, model cost split, language stack, achievements, and leaderboards.
  - `/settings` — Full visual settings editor for every status (`working`, `thinking`, `compacting`, `shipped`, `notification`, `idle`), live preview, extra rotation frames, custom buttons, source toggles, daemon start/stop/restart + log viewer, and per-workspace privacy (`Public` / `Name only` / `Hidden`).
  - `/wrapped` — Full-screen pure CSS animated Coding Wrapped story slides.
- **Interactive Terminal Dashboard (`vibe-rpc`)**
  - Built with `bubbletea` + `lipgloss`: 7 tabs (`Now`, `Today`, `Week`, `Streak`, `Lifetime`, `Cost`, `Code`), live 3-second refresh, keyboard navigation.
- **Built-in MCP Server (`vibe-rpc mcp`)**
  - Exposes `get_lifetime_stats`, `get_today`, `get_top_files`, `get_model_split`, and `get_recap` over stdio JSON-RPC; register globally with `vibe-rpc mcp install`.
- **Shareable SVG Stat Cards & Exports**
  - `vibe-rpc badge`, `vibe-rpc card`, `vibe-rpc calendar`, `vibe-rpc session-card`, `vibe-rpc export --csv`, `vibe-rpc recap`, `vibe-rpc statusline`.

## Build & Install

Requires Go 1.26+.

```bash
go build -o vibe-rpc ./cmd/vibe-rpc
# or install into your $GOPATH/bin:
go install ./cmd/vibe-rpc
```

Then run one-time setup:

```bash
./vibe-rpc setup
```

This seeds your config, wires Antigravity & Claude Code hooks, runs an in-process hook self-test, installs OS login autostart, scans your transcript history, and starts the background daemon.

## Commands

| Command | Description |
| --- | --- |
| `vibe-rpc` | Launch the 7-tab Bubbletea TUI dashboard |
| `vibe-rpc setup` | Wire hooks, seed config, scan transcripts, start daemon |
| `vibe-rpc settings` | Open the zero-JS Settings editor (`:47475/settings`) |
| `vibe-rpc serve` | Open the zero-JS Web Dashboard (`:47475/`) |
| `vibe-rpc wrapped` | Open your Coding Wrapped story (`:47475/wrapped`) |
| `vibe-rpc status [--dump]` | Show daemon status & Discord preview |
| `vibe-rpc today` / `week` / `projects` | Terminal analytics summaries |
| `vibe-rpc recap [range]` | Standup-ready coding recap (`today`, `yesterday`, `week`, `7d`) |
| `vibe-rpc insights` | Habit & dual-agent insights |
| `vibe-rpc doctor [--fix]` | Diagnose and repair hooks, daemon, Discord IPC, and caches |
| `vibe-rpc scan [--force]` | Incremental or full transcript rescan |
| `vibe-rpc pause [30m\|2h\|off]` | Temporarily hide Discord Rich Presence |
| `vibe-rpc private [on\|off\|name-only]` | Control repo visibility on Discord |
| `vibe-rpc statcards [badge\|card\|calendar\|session-card]` | Render shareable SVGs |
| `vibe-rpc mcp [install\|uninstall]` | Stdio MCP server / Antigravity registration |
| `vibe-rpc start\|stop\|restart\|logs` | Daemon controls |
| `vibe-rpc uninstall` | Remove hooks, MCP config, autostart, and stop daemon |

## Key Go Libraries

- [`github.com/spf13/cobra`](https://github.com/spf13/cobra) — CLI commands & shell completions
- [`github.com/charmbracelet/bubbletea`](https://github.com/charmbracelet/bubbletea) & [`lipgloss`](https://github.com/charmbracelet/lipgloss) — Interactive TUI
- [`github.com/fsnotify/fsnotify`](https://github.com/fsnotify/fsnotify) — Real-time state/config file watching
- [`github.com/shirou/gopsutil/v4`](https://github.com/shirou/gopsutil/v4) — Cross-platform process detection (Antigravity + Claude Code)
- [`github.com/google/uuid`](https://github.com/google/uuid) — Discord IPC frame nonces

## License

MIT — see [LICENSE](./LICENSE).
