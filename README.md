# agy-rpc

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
- **Interactive Terminal Dashboard (`agy-rpc`)**
  - Built with `bubbletea` + `lipgloss`: 7 tabs (`Now`, `Today`, `Week`, `Streak`, `Lifetime`, `Cost`, `Code`), live 3-second refresh, keyboard navigation.
- **Built-in MCP Server (`agy-rpc mcp`)**
  - Exposes `get_lifetime_stats`, `get_today`, `get_top_files`, `get_model_split`, and `get_recap` over stdio JSON-RPC; register globally with `agy-rpc mcp install`.
- **Shareable SVG Stat Cards & Exports**
  - `agy-rpc badge`, `agy-rpc card`, `agy-rpc calendar`, `agy-rpc session-card`, `agy-rpc export --csv`, `agy-rpc recap`, `agy-rpc statusline`.

## Build & Install

Requires Go 1.24+.

```bash
go build -o agy-rpc ./cmd/agy-rpc
# or install into your $GOPATH/bin:
go install ./cmd/agy-rpc
```

Then run one-time setup:

```bash
./agy-rpc setup
```

This seeds your config, wires Antigravity & Claude Code hooks, runs an in-process hook self-test, installs OS login autostart, scans your transcript history, and starts the background daemon.

## Commands

| Command | Description |
| --- | --- |
| `agy-rpc` | Launch the 7-tab Bubbletea TUI dashboard |
| `agy-rpc setup` | Wire hooks, seed config, scan transcripts, start daemon |
| `agy-rpc settings` | Open the zero-JS Settings editor (`:47475/settings`) |
| `agy-rpc serve` | Open the zero-JS Web Dashboard (`:47475/`) |
| `agy-rpc wrapped` | Open your Coding Wrapped story (`:47475/wrapped`) |
| `agy-rpc status [--dump]` | Show daemon status & Discord preview |
| `agy-rpc today` / `week` / `projects` | Terminal analytics summaries |
| `agy-rpc recap [range]` | Standup-ready coding recap (`today`, `yesterday`, `week`, `7d`) |
| `agy-rpc insights` | Habit & dual-agent insights |
| `agy-rpc doctor [--fix]` | Diagnose and repair hooks, daemon, Discord IPC, and caches |
| `agy-rpc scan [--force]` | Incremental or full transcript rescan |
| `agy-rpc pause [30m\|2h\|off]` | Temporarily hide Discord Rich Presence |
| `agy-rpc private [on\|off\|name-only]` | Control repo visibility on Discord |
| `agy-rpc statcards [badge\|card\|calendar\|session-card]` | Render shareable SVGs |
| `agy-rpc mcp [install\|uninstall]` | Stdio MCP server / Antigravity registration |
| `agy-rpc start\|stop\|restart\|logs` | Daemon controls |
| `agy-rpc uninstall` | Remove hooks, MCP config, autostart, and stop daemon |

## Key Go Libraries

- [`github.com/spf13/cobra`](https://github.com/spf13/cobra) — CLI commands & shell completions
- [`github.com/charmbracelet/bubbletea`](https://github.com/charmbracelet/bubbletea) & [`lipgloss`](https://github.com/charmbracelet/lipgloss) — Interactive TUI
- [`github.com/fsnotify/fsnotify`](https://github.com/fsnotify/fsnotify) — Real-time state/config file watching
- [`github.com/shirou/gopsutil/v4`](https://github.com/shirou/gopsutil/v4) — Cross-platform process detection (Antigravity + Claude Code)
- [`github.com/google/uuid`](https://github.com/google/uuid) — Discord IPC frame nonces

## License

MIT — see [LICENSE](./LICENSE).
