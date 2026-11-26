package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chauveaul/vibe-rpc/internal/card"
	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/daemon"
	"github.com/chauveaul/vibe-rpc/internal/detect"
	"github.com/chauveaul/vibe-rpc/internal/doctor"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/hook"
	"github.com/chauveaul/vibe-rpc/internal/install"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/mcp"
	"github.com/chauveaul/vibe-rpc/internal/notch"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/pause"
	"github.com/chauveaul/vibe-rpc/internal/presence"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/privacy"
	"github.com/chauveaul/vibe-rpc/internal/proc"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/server"
	"github.com/chauveaul/vibe-rpc/internal/state"
	"github.com/chauveaul/vibe-rpc/internal/stats"
	"github.com/chauveaul/vibe-rpc/internal/tui"
	"github.com/chauveaul/vibe-rpc/internal/update"
)

// openBrowser opens u shortly after the server starts listening.
func openBrowser(u string) {
	go func() {
		time.Sleep(250 * time.Millisecond)
		openBrowserNow(u)
	}()
}

// openBrowserNow opens u and waits for the opener, for callers that exit
// right after (a background openBrowser would die with the process).
func openBrowserNow(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Run()
}

func loadAggOrScan() *scanner.Aggregate {
	if agg := scanner.ReadAggregate(); agg != nil {
		return agg
	}
	return scanner.Scan(scanner.ScanOptions{}).Aggregate
}

func resolveLiveState() (config.Config, *scanner.Aggregate, state.SessionState) {
	cfg := config.Load()
	agg := loadAggOrScan()
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	sessions := state.ListSessions(presence.DiscordListOptions(cfg))
	idleMs := int64(cfg.IdleThresholdSec) * 1000
	if idleMs <= 0 {
		idleMs = 60_000
	}
	now := time.Now().UnixMilli()
	picked := presence.PickActiveSession(sessions, "", now, idleMs)
	var st state.SessionState
	if picked.State != nil {
		st = *picked.State
	} else {
		st = state.Read("")
	}
	var agyProc, claudeProc *bool
	if cfg.IsProcessDetection() {
		ag := proc.Detect(cfg.Sources.WantAntigravity(), cfg.Sources.WantClaude())
		agyProc, claudeProc = ag.Antigravity, ag.Claude
		st.AgentProcessAlive = ag.Any
	}
	st.AntigravityActive, st.ClaudeActive = presence.ComputeActiveSources(sessions, nil, agyProc, claudeProc, cfg, now)
	st.SourcesComputed = true
	st.AgySession, st.ClaudeSession = presence.PickDualSnapshots(sessions, nil, cfg, now)
	st = format.ApplyIdle(st, cfg)
	st = format.ApplyShipped(st, cfg)
	st = format.ApplyTrigger(st, cfg)
	if presence.IsDualWorking(st) {
		st.Status = "dual"
	}
	return cfg, agg, st
}

func main() {
	hook.EnsureDaemonFn = daemon.EnsureRunning

	root := &cobra.Command{
		Use:     "vibe-rpc",
		Short:   "Discord Rich Presence & coding analytics for Antigravity + Claude Code (100% Go)",
		Version: paths.Version,
		// Print just the error, not the whole usage block, when a command fails.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run()
		},
	}

	// setup / install
	var noAutostart, noClaude, noOpenCode bool
	var agentsFlag string
	setupCmd := &cobra.Command{
		Use:     "setup",
		Aliases: []string{"install"},
		Short:   "Pick your coding agents, wire their hooks, seed config, scan history, and start the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Setting up vibe-rpc (pure Go)...")
			if created, err := install.SeedConfig(""); err != nil {
				return err
			} else if created {
				fmt.Printf("  ✓ seeded %s\n", paths.ConfigPath())
			} else {
				fmt.Printf("  · config already exists at %s\n", paths.ConfigPath())
			}

			tracked, err := chooseAgents(agentsFlag, os.Stdin, os.Stdout)
			if err != nil {
				return err
			}
			if err := saveTrackedAgents(tracked); err != nil {
				return err
			}

			if tracked["antigravity"] {
				if changed, err := install.InstallAntigravityHooks("", ""); err != nil {
					return err
				} else if changed {
					fmt.Printf("  ✓ wired Antigravity hooks in %s\n", paths.AgyHooksPath())
				} else {
					fmt.Printf("  · Antigravity hooks already up to date in %s\n", paths.AgyHooksPath())
				}
			}

			if !noClaude && tracked["claude"] {
				if changed, err := install.InstallClaudeHooks("", ""); err != nil {
					fmt.Printf("  ! skipped Claude Code hooks: %v\n", err)
				} else if changed {
					fmt.Printf("  ✓ wired Claude Code hooks in %s\n", paths.ClaudeSettingsPath())
				} else {
					fmt.Printf("  · Claude Code hooks already up to date in %s\n", paths.ClaudeSettingsPath())
				}
			}

			if !noOpenCode && tracked["opencode"] && scanner.OpenCodeBinary() != "" {
				if changed, err := install.InstallOpenCodePlugin(""); err != nil {
					fmt.Printf("  ! OpenCode plugin: %v\n", err)
				} else if changed {
					fmt.Printf("  ✓ installed OpenCode plugin at %s\n", install.OpenCodePluginPath())
				} else {
					fmt.Println("  ✓ OpenCode plugin already installed")
				}
			}
			if err := install.SelfTestHooks(); err != nil {
				return err
			}
			fmt.Println("  ✓ hook self-test passed (Antigravity fail-closed allow + Claude continue)")

			if !noAutostart {
				if p, err := install.InstallAutostart(""); err != nil {
					fmt.Printf("  ! autostart skipped: %v\n", err)
				} else if p != "" {
					fmt.Printf("  ✓ installed login autostart (%s)\n", p)
				}
			}

			res := scanner.Scan(scanner.ScanOptions{})
			fmt.Printf("  ✓ scanned %d transcript(s) (%d new, %d cached)\n", res.Total, res.Scanned, res.Skipped)

			daemon.ClearStopped()
			daemon.StopAndWait()
			pid, err := daemon.SpawnDetached()
			if err != nil {
				return err
			}
			fmt.Printf("  ✓ started daemon (pid %d)\n", pid)
			fmt.Printf("\nOpen settings: vibe-rpc settings\nOpen dashboard: vibe-rpc serve\n")
			return nil
		},
	}
	setupCmd.Flags().BoolVar(&noAutostart, "no-autostart", false, "Skip installing OS login autostart")
	setupCmd.Flags().BoolVar(&noClaude, "no-claude", false, "Skip wiring Claude Code hooks")
	setupCmd.Flags().BoolVar(&noOpenCode, "no-opencode", false, "Skip installing the OpenCode plugin")
	setupCmd.Flags().StringVar(&agentsFlag, "agents", "", "Agents to track, comma-separated: antigravity,claude,opencode (or \"all\"); asks interactively if omitted")

	// uninstall
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop daemon and remove Antigravity/Claude hooks, MCP entry, and autostart",
		RunE: func(cmd *cobra.Command, args []string) error {
			daemon.MarkStopped()
			daemon.StopAndWait()
			fmt.Println("  ✓ stopped daemon")
			if ok, _ := install.UninstallAntigravityHooks(""); ok {
				fmt.Printf("  ✓ removed Antigravity hooks from %s\n", paths.AgyHooksPath())
			}
			if ok, _ := install.UninstallClaudeStatusLine(""); ok {
				fmt.Println("✓ Removed vibe-rpc's Claude Code status line")
			}
			if ok, _ := install.UninstallAgyStatusLine(""); ok {
				fmt.Println("✓ Removed vibe-rpc's Antigravity CLI status line")
			}
			notch.Quit()
			if ok, _ := install.UninstallOpenCodePlugin(); ok {
				fmt.Println("✓ Removed the OpenCode plugin")
			}
			if ok, _ := install.UninstallClaudeHooks(""); ok {
				fmt.Printf("  ✓ removed Claude Code hooks from %s\n", paths.ClaudeSettingsPath())
			}
			if ok, _ := mcp.UninstallConfig(""); ok {
				fmt.Printf("  ✓ removed MCP entry from %s\n", paths.AgyMcpConfigPath())
			}
			_ = install.UninstallAutostart()
			fmt.Println("  ✓ removed autostart")
			return nil
		},
	}

	// Web surfaces: serve, settings, wrapped
	var noOpen bool
	var portFlag int
	servePage := func(route string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			p := server.Port()
			if portFlag > 0 {
				p = portFlag
			}
			addr := fmt.Sprintf("127.0.0.1:%d", p)
			u := fmt.Sprintf("http://%s%s", addr, route)
			// A vibe-rpc dashboard is already running there: just open it.
			if server.IsRunningAt(addr) {
				fmt.Printf("vibe-rpc is already serving at %s\n", u)
				if !noOpen {
					openBrowserNow(u)
				}
				return nil
			}
			fmt.Printf("Serving vibe-rpc at %s (Ctrl+C to stop)\n", u)
			if !noOpen {
				openBrowser(u)
			}
			return server.Serve(addr)
		}
	}
	serveCmd := &cobra.Command{Use: "serve", Short: "Open the local zero-JS web dashboard", RunE: servePage("/")}
	serveCmd.Flags().BoolVar(&noOpen, "no-open", false, "Do not automatically open the browser")
	serveCmd.Flags().IntVar(&portFlag, "port", 0, "Port to bind on 127.0.0.1 (default 47475)")

	settingsCmd := &cobra.Command{Use: "settings", Short: "Open the visual settings editor in your browser", RunE: servePage("/settings")}
	settingsCmd.Flags().BoolVar(&noOpen, "no-open", false, "Do not automatically open the browser")
	settingsCmd.Flags().IntVar(&portFlag, "port", 0, "Port to bind on 127.0.0.1 (default 47475)")

	wrappedCmd := &cobra.Command{Use: "wrapped", Short: "Open your Coding Wrapped story in your browser", RunE: servePage("/wrapped")}
	wrappedCmd.Flags().BoolVar(&noOpen, "no-open", false, "Do not automatically open the browser")
	wrappedCmd.Flags().IntVar(&portFlag, "port", 0, "Port to bind on 127.0.0.1 (default 47475)")

	// daemon & lifecycle
	daemonCmd := &cobra.Command{
		Use:    "daemon",
		Short:  "Run the Discord Rich Presence daemon in the foreground",
		Hidden: true,
		RunE:   func(cmd *cobra.Command, args []string) error { return daemon.Run() },
	}
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the background Discord Rich Presence daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			daemon.ClearStopped()
			if pid := daemon.AlivePID(); pid > 0 {
				fmt.Printf("vibe-rpc daemon already running (pid %d)\n", pid)
				return nil
			}
			pid, err := daemon.SpawnDetached()
			if err != nil {
				return err
			}
			fmt.Printf("vibe-rpc daemon started (pid %d)\n", pid)
			return nil
		},
	}
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the background daemon",
		Run: func(cmd *cobra.Command, args []string) {
			daemon.MarkStopped()
			daemon.StopAndWait()
			fmt.Println("vibe-rpc daemon stopped")
		},
	}
	restartCmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the background daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			daemon.ClearStopped()
			daemon.StopAndWait()
			pid, err := daemon.SpawnDetached()
			if err != nil {
				return err
			}
			fmt.Printf("vibe-rpc daemon restarted (pid %d)\n", pid)
			return nil
		},
	}

	// hook
	var sourceFlag string
	hookCmd := &cobra.Command{
		Use:    "hook <event>",
		Short:  "Internal hook entrypoint invoked by Antigravity or Claude Code",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ev := "unknown"
			if len(args) > 0 {
				ev = args[0]
			}
			hook.RunCLI(ev, sourceFlag, os.Stdin, os.Stdout)
		},
	}
	hookCmd.Flags().StringVar(&sourceFlag, "source", "", "Explicit source: antigravity or claude")

	// status / dump
	var dumpFlag bool
	statusCmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"dump"},
		Short:   "Show current daemon status, active session, and Discord card preview",
		Run: func(cmd *cobra.Command, args []string) {
			cfg, agg, st := resolveLiveState()
			vars := format.BuildVars(st, cfg, agg)
			act, cleared := daemon.BuildActivity(st, cfg, agg, &presence.RotationCursor{})
			if dumpFlag || cmd.CalledAs() == "dump" {
				b, _ := json.MarshalIndent(map[string]any{
					"pid":      daemon.AlivePID(),
					"state":    st,
					"activity": act,
					"cleared":  cleared,
					"vars":     vars,
				}, "", "  ")
				fmt.Println(string(b))
				return
			}
			pid := daemon.AlivePID()
			if pid > 0 {
				fmt.Printf("Daemon   : running (pid %d)\n", pid)
			} else {
				fmt.Println("Daemon   : stopped")
			}
			if u := pause.Until(""); !u.IsZero() {
				fmt.Printf("Paused   : until %s\n", u.Format(time.Kitchen))
			}
			fmt.Printf("Agent    : %v (%s)\n", vars["sourcePretty"], st.Status)
			fmt.Printf("Project  : %v\n", vars["project"])
			fmt.Printf("Model    : %v\n", vars["modelPretty"])
			if cleared || act == nil {
				fmt.Println("Discord  : (hidden / paused)")
			} else {
				fmt.Printf("Discord  : [%s] %s | %s\n", act.Name, act.Details, act.State)
			}
		},
	}
	statusCmd.Flags().BoolVar(&dumpFlag, "dump", false, "Dump raw state, variables, and activity as JSON")

	// today
	todayCmd := &cobra.Command{
		Use:   "today",
		Short: "Print today's combined coding stats",
		Run: func(cmd *cobra.Command, args []string) {
			agg := loadAggOrScan()
			dk := scanner.DayKey(time.Now().UnixMilli())
			d := &scanner.DayBucket{}
			if agg != nil && agg.ByDay != nil && agg.ByDay[dk] != nil {
				d = agg.ByDay[dk]
			}
			tok := d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
			fmt.Printf("Today (%s)\n", dk)
			fmt.Printf("  Active time : %s (%d sessions)\n", format.FmtHours(d.ActiveMs), d.Sessions)
			fmt.Printf("  Prompts     : %d  ·  Tool calls: %d\n", d.UserMessages, d.ToolCalls)
			fmt.Printf("  Tokens      : %s  ·  API equiv: %s\n", format.FmtNum(tok), pricing.FmtCost(d.Cost))
			fmt.Printf("  Code churn  : +%d / -%d lines  ·  Ships: %d\n", d.LinesAdded, d.LinesRemoved, d.Ships)
		},
	}

	// week
	var compareFlag bool
	weekCmd := &cobra.Command{
		Use:   "week",
		Short: "Print a 7-day activity breakdown",
		Run: func(cmd *cobra.Command, args []string) {
			agg := loadAggOrScan()
			rows := stats.WeekGrid(agg, 7)
			var totalMs int64
			for _, r := range rows {
				totalMs += r.ActiveMs
				mark := " "
				if r.IsToday {
					mark = "→"
				}
				fmt.Printf("%s %s %s  %-6s  %3d prompts  %8s tokens  %s\n",
					mark, r.Weekday, r.Day, format.FmtHours(r.ActiveMs), r.Prompts, format.FmtNum(r.Tokens), pricing.FmtCost(r.Cost))
			}
			fmt.Printf("\nTotal: %s\n", format.FmtHours(totalMs))
			if compareFlag {
				sum := stats.SummarizeRange(agg, "7d")
				fmt.Printf("Previous 7d: %s\n", format.FmtHours(sum.PrevActiveMs))
			}
		},
	}
	weekCmd.Flags().BoolVar(&compareFlag, "compare", false, "Compare against the previous 7 days")

	// projects
	var projLimit int
	projectsCmd := &cobra.Command{
		Use:   "projects [name]",
		Short: "List projects ranked by active time or inspect a single project",
		Run: func(cmd *cobra.Command, args []string) {
			agg := loadAggOrScan()
			if agg == nil || len(agg.Projects) == 0 {
				fmt.Println("No projects recorded yet. Run `vibe-rpc scan`.")
				return
			}
			if len(args) > 0 {
				ps := agg.Projects[args[0]]
				if ps == nil {
					fmt.Printf("Project %q not found.\n", args[0])
					return
				}
				fmt.Printf("%s\n  Active: %s · Sessions: %d · Prompts: %d · Tools: %d · Cost: %s · Lines: +%d/-%d\n",
					args[0], format.FmtHours(ps.ActiveMs), ps.Sessions, ps.UserMessages, ps.ToolCalls,
					pricing.FmtCost(ps.Cost), ps.LinesAdded, ps.LinesRemoved)
				return
			}
			type kv struct {
				name string
				ps   *scanner.ProjectStats
			}
			var list []kv
			for n, ps := range agg.Projects {
				list = append(list, kv{n, ps})
			}
			sort.Slice(list, func(i, j int) bool { return list[i].ps.ActiveMs > list[j].ps.ActiveMs })
			if projLimit > 0 && len(list) > projLimit {
				list = list[:projLimit]
			}
			for _, p := range list {
				fmt.Printf("%-24s %6s  %3d sessions  %8s\n", p.name, format.FmtHours(p.ps.ActiveMs), p.ps.Sessions, pricing.FmtCost(p.ps.Cost))
			}
		},
	}
	projectsCmd.Flags().IntVar(&projLimit, "limit", 15, "Maximum projects to show")

	// recap
	var recapMD, recapJSON bool
	recapCmd := &cobra.Command{
		Use:   "recap [today|yesterday|week|Nd]",
		Short: "Generate a standup-ready coding recap",
		Run: func(cmd *cobra.Command, args []string) {
			rng := "yesterday"
			if len(args) > 0 {
				rng = args[0]
			}
			rec := stats.BuildRecap(loadAggOrScan(), rng)
			if recapJSON {
				b, _ := json.MarshalIndent(rec, "", "  ")
				fmt.Println(string(b))
				return
			}
			fmt.Print(stats.RenderRecapMarkdown(rec))
		},
	}
	recapCmd.Flags().BoolVar(&recapMD, "md", true, "Output Markdown")
	recapCmd.Flags().BoolVar(&recapJSON, "json", false, "Output JSON")

	// insights
	insightsCmd := &cobra.Command{
		Use:   "insights",
		Short: "Print data-driven insights about your coding habits",
		Run: func(cmd *cobra.Command, args []string) {
			for _, line := range stats.GenerateInsights(loadAggOrScan()) {
				fmt.Printf("→ %s\n", line)
			}
		},
	}

	// export
	var exportCSV bool
	var exportOut string
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Export combined coding stats as JSON or CSV",
		RunE: func(cmd *cobra.Command, args []string) error {
			agg := loadAggOrScan()
			var data []byte
			if exportCSV {
				data = []byte(stats.AggregateToCSV(agg))
			} else {
				data, _ = json.MarshalIndent(agg, "", "  ")
				data = append(data, '\n')
			}
			if exportOut != "" {
				return os.WriteFile(exportOut, data, 0o644)
			}
			fmt.Print(string(data))
			return nil
		},
	}
	exportCmd.Flags().BoolVar(&exportCSV, "csv", false, "Export daily CSV instead of JSON")
	exportCmd.Flags().StringVar(&exportOut, "out", "", "Write output to file path")

	// statcards (badge, card, calendar, session-card)
	var cardMetric, cardOut string
	var cardPNG bool
	var cardScale float64
	statcardsCmd := &cobra.Command{
		Use:     "statcards [badge|card|calendar|session-card]",
		Aliases: []string{"badge", "card", "calendar", "session-card"},
		Short:   "Render shareable SVG badges, poster cards (or a PNG with --png), and heatmaps",
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := cmd.CalledAs()
			if kind == "statcards" {
				kind = "card"
				if len(args) > 0 {
					kind = args[0]
				}
			}
			agg := loadAggOrScan()
			if cardPNG {
				if kind != "card" {
					return fmt.Errorf("--png is only supported for the stats card (vibe-rpc card --png)")
				}
				if cardScale < 1 || cardScale > 4 {
					return fmt.Errorf("--scale must be between 1 and 4")
				}
				out := cardOut
				if out == "" {
					out = "vibe-rpc-card.png"
				} else if strings.EqualFold(filepath.Ext(out), ".svg") {
					return fmt.Errorf("--png writes a PNG; use an --out path ending in .png")
				}
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				if err := card.WritePNG(f, card.FromAggregate(agg), cardScale); err != nil {
					f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
				abs, _ := filepath.Abs(out)
				fmt.Printf("Wrote %s (%dx%d)\n", abs, int(card.Width*cardScale), int(card.Height*cardScale))
				return nil
			}
			var svg string
			switch kind {
			case "badge":
				svg = stats.BadgeSVG(agg, cardMetric)
			case "calendar", "heatmap":
				svg = stats.HeatmapSVG(agg, 91)
			case "session-card":
				_, _, st := resolveLiveState()
				svg = stats.SessionCardSVG(st)
			default:
				svg = stats.PosterCardSVG(agg)
			}
			if cardOut != "" {
				return os.WriteFile(cardOut, []byte(svg), 0o644)
			}
			fmt.Println(svg)
			return nil
		},
	}
	statcardsCmd.Flags().StringVar(&cardMetric, "metric", "hours", "Badge metric: hours|tokens|streak|cost|sessions|lines")
	statcardsCmd.Flags().StringVar(&cardOut, "out", "", "Write to file path (PNG defaults to ./vibe-rpc-card.png)")
	statcardsCmd.Flags().BoolVar(&cardPNG, "png", false, "Render the stats card as a 1200×630 PNG for sharing on Discord, X or LinkedIn")
	statcardsCmd.Flags().Float64Var(&cardScale, "scale", 2, "PNG pixel density: 1 = 1200×630, 2 = 2400×1260")

	// statusline
	var slTpl string
	statuslineCmd := &cobra.Command{
		Use:   "statusline",
		Short: "Print a single-line status string for tmux / shell prompts / status bars",
		Run: func(cmd *cobra.Command, args []string) {
			// Claude Code pipes its session JSON (incl. rate limits) on stdin.
			if payload := readPipedStdin(300*time.Millisecond, 1<<20); len(payload) > 0 {
				_, _ = limits.SaveFromStatusLine(payload, time.Now())
			}
			cfg, agg, st := resolveLiveState()
			vars := format.BuildVars(st, cfg, agg)
			if slTpl == "" {
				slTpl = "{sourcePretty} · {project} · {modelPretty} · {todayHours} today"
			}
			fmt.Println(format.FillTemplate(slTpl, vars))
		},
	}
	statuslineCmd.Flags().StringVar(&slTpl, "template", "", "Template string using {var} placeholders")

	// mcp
	mcpCmd := &cobra.Command{
		Use:   "mcp [install|uninstall]",
		Short: "Run the stdio MCP server or register it in ~/.gemini/config/mcp_config.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				switch args[0] {
				case "install":
					if err := mcp.InstallConfig("", ""); err != nil {
						return err
					}
					fmt.Printf("Registered vibe-rpc MCP server in %s\n", paths.AgyMcpConfigPath())
					return nil
				case "uninstall":
					ok, err := mcp.UninstallConfig("")
					if err != nil {
						return err
					}
					if ok {
						fmt.Printf("Removed vibe-rpc MCP server from %s\n", paths.AgyMcpConfigPath())
					} else {
						fmt.Println("vibe-rpc MCP server was not registered.")
					}
					return nil
				}
			}
			return mcp.ServeStdio(os.Stdin, os.Stdout)
		},
	}

	// private / public / privacy
	privateCmd := &cobra.Command{
		Use:     "private [on|off|name-only|status]",
		Aliases: []string{"public", "privacy"},
		Short:   "Manage Discord visibility for the current working directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			action := "status"
			if cmd.CalledAs() == "public" {
				action = "off"
			} else if len(args) > 0 {
				action = strings.ToLower(args[0])
			}
			switch action {
			case "on", "hidden":
				if err := privacy.AddPrivateCwd(cwd); err != nil {
					return err
				}
				fmt.Printf("%s -> hidden\n", cwd)
			case "off", "public":
				if err := privacy.RemovePrivateCwd(cwd); err != nil {
					return err
				}
				fmt.Printf("%s -> public\n", cwd)
			case "name-only":
				if _, err := privacy.SetCwdVisibility(cwd, "name-only"); err != nil {
					return err
				}
				fmt.Printf("%s -> name-only\n", cwd)
			default:
				res := privacy.Resolve(cwd, config.Load())
				fmt.Printf("%s -> %s (%s)\n", cwd, res.Visibility, res.Reason)
			}
			return nil
		},
	}

	// pause / resume
	pauseCmd := &cobra.Command{
		Use:     "pause [duration|off|status]",
		Aliases: []string{"resume", "unpause"},
		Short:   "Temporarily pause Discord Rich Presence (e.g. 30m, 2h, off)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.CalledAs() == "resume" || cmd.CalledAs() == "unpause" {
				pause.Clear("")
				fmt.Println("Discord presence resumed.")
				return nil
			}
			arg := ""
			if len(args) > 0 {
				arg = strings.ToLower(args[0])
			}
			switch arg {
			case "off", "clear", "resume":
				pause.Clear("")
				fmt.Println("Discord presence resumed.")
			case "status":
				if u := pause.Until(""); u.IsZero() {
					fmt.Println("Not paused.")
				} else {
					fmt.Printf("Paused until %s (%s remaining)\n", u.Format(time.Kitchen), time.Until(u).Round(time.Second))
				}
			default:
				dur, ok := pause.ParseDuration(arg)
				if !ok {
					return fmt.Errorf("invalid duration %q (use e.g. 30m, 2h, 1h30m)", arg)
				}
				until, err := pause.Set(dur, "")
				if err != nil {
					return err
				}
				fmt.Printf("Discord presence paused until %s\n", until.Format(time.Kitchen))
			}
			return nil
		},
	}

	// logs / tail
	var logLines int
	logsCmd := &cobra.Command{
		Use:     "logs",
		Aliases: []string{"tail", "log"},
		Short:   "Show recent daemon log entries",
		Run: func(cmd *cobra.Command, args []string) {
			b, err := os.ReadFile(paths.LogPath())
			if err != nil {
				fmt.Printf("No log yet at %s\n", paths.LogPath())
				return
			}
			lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
			if logLines > 0 && len(lines) > logLines {
				lines = lines[len(lines)-logLines:]
			}
			fmt.Println(strings.Join(lines, "\n"))
		},
	}
	logsCmd.Flags().IntVarP(&logLines, "lines", "n", 40, "Number of recent lines to display")

	// doctor
	var fixFlag bool
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose hooks, config, daemon, Discord IPC, and transcript caches",
		Run: func(cmd *cobra.Command, args []string) {
			if fixFlag {
				actions := doctor.Fix()
				if len(actions) == 0 {
					fmt.Println("Nothing to fix.")
				} else {
					for _, a := range actions {
						fmt.Printf("  ✓ %s\n", a)
					}
				}
				fmt.Println()
			}
			for _, c := range doctor.Run() {
				sym := "✓"
				switch c.Status {
				case doctor.Warn:
					sym = "!"
				case doctor.Fail:
					sym = "✗"
				case doctor.Info:
					sym = "·"
				}
				fmt.Printf("  %s %-18s %s\n", sym, c.Name, c.Detail)
				if c.Fix != "" {
					fmt.Printf("      fix: %s\n", c.Fix)
				}
			}
		},
	}
	doctorCmd.Flags().BoolVar(&fixFlag, "fix", false, "Attempt to repair hooks, config, cache, and daemon")

	// scan / rescan
	var forceScan bool
	scanCmd := &cobra.Command{
		Use:     "scan",
		Aliases: []string{"rescan"},
		Short:   "Scan Antigravity and Claude Code transcripts into the aggregate cache",
		Run: func(cmd *cobra.Command, args []string) {
			res := scanner.Scan(scanner.ScanOptions{Force: forceScan || cmd.CalledAs() == "rescan"})
			fmt.Printf("Scanned %d files (%d parsed, %d cached, %d pruned). Total sessions: %d\n",
				res.Total, res.Scanned, res.Skipped, res.Removed, res.Aggregate.Sessions)
		},
	}
	scanCmd.Flags().BoolVar(&forceScan, "force", false, "Re-parse all transcripts ignoring cache")

	// backfill
	backfillCmd := &cobra.Command{
		Use:   "backfill <dir>",
		Short: "Scan an additional archive directory of JSONL transcripts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			abs, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			res := scanner.Scan(scanner.ScanOptions{ExtraDirs: []string{abs}})
			fmt.Printf("Backfilled from %s: %d parsed, %d total sessions\n", abs, res.Scanned, res.Aggregate.Sessions)
			return nil
		},
	}

	// preview & vars
	previewCmd := &cobra.Command{
		Use:   "preview",
		Short: "Preview how every status card renders with your current config and stats",
		Run: func(cmd *cobra.Command, args []string) {
			cfg, agg, st := resolveLiveState()
			if st.Cwd == "" {
				st.Cwd, _ = os.Getwd()
			}
			if st.CurrentTool == "" {
				st.CurrentTool = "edit_file"
				st.CurrentFile = "main.go"
			}
			for _, s := range config.Statuses {
				cp := st
				cp.Status = s
				act, _ := daemon.BuildActivity(cp, cfg, agg, &presence.RotationCursor{})
				if act != nil {
					fmt.Printf("[%-12s] %s | %s | %s\n", s, act.Name, act.Details, act.State)
				}
			}
		},
	}
	varsCmd := &cobra.Command{
		Use:   "vars",
		Short: "List all template variables and their live values",
		Run: func(cmd *cobra.Command, args []string) {
			cfg, agg, st := resolveLiveState()
			vars := format.BuildVars(st, cfg, agg)
			keys := make([]string, 0, len(vars))
			for k := range vars {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("{%-24s} = %v\n", k, vars[k])
			}
		},
	}

	// assets export (Antigravity Discord GIFs)
	assetsCmd := &cobra.Command{
		Use:   "assets",
		Short: "Export built-in animated Antigravity GIFs for Discord Rich Presence",
	}
	assetsExportCmd := &cobra.Command{
		Use:   "export [dir]",
		Short: "Write all 7 animated Antigravity GIFs to a folder (default ./agy-discord-gifs)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "agy-discord-gifs"
			if len(args) > 0 {
				dir = args[0]
			}
			written, err := server.ExportGifs(dir)
			if err != nil {
				return err
			}
			for _, p := range written {
				fmt.Printf("  ✓ %s\n", p)
			}
			fmt.Printf("\nUpload these to Discord Developer Portal -> Rich Presence -> Art Assets (keep names like agy-working), or host them over HTTPS and set 'Public GIF Base URL' in `vibe-rpc settings`.\n")
			return nil
		},
	}
	assetsCmd.AddCommand(assetsExportCmd)

	// notch (macOS): the VibeNotch companion app
	notchFeedCmd := &cobra.Command{
		Use:    "notch-feed",
		Short:  "Stream live status as JSON lines for the VibeNotch app",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			enc := json.NewEncoder(os.Stdout)
			for {
				// A failed write means the notch app went away: exit quietly.
				if err := enc.Encode(server.BuildNotchFeed()); err != nil {
					return nil
				}
				time.Sleep(time.Second)
			}
		},
	}
	notchCmd := &cobra.Command{
		Use:       "notch [on|off]",
		Short:     "Turn VibeNotch, the live status panel in your MacBook's notch, on or off (macOS)",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !notch.Supported() {
				return fmt.Errorf("VibeNotch is macOS-only")
			}
			on := len(args) == 0 || args[0] == "on"
			if len(args) == 1 && args[0] != "on" && args[0] != "off" {
				return fmt.Errorf("use `vibe-rpc notch on` or `vibe-rpc notch off`")
			}
			if err := setNotchEnabled(on); err != nil {
				return err
			}
			if !on {
				notch.Quit()
				fmt.Println("VibeNotch is off. Turn it back on with `vibe-rpc notch` or in Settings.")
				return nil
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			app, err := notch.Launch(install.StableBinaryPath(exe))
			if err != nil {
				return err
			}
			fmt.Printf("VibeNotch is on (%s). Hover under the notch to open it.\n", app)
			if install.ClaudeStatusLineState("") != "vibe-rpc" || install.AgyStatusLineState("") != "vibe-rpc" {
				fmt.Println("Tip: for real 5-hour & weekly usage bars, turn on Claude Code / Antigravity usage limits in `vibe-rpc settings`.")
			}
			return nil
		},
	}

	// upgrade
	var upCheck, upForce bool
	upgradeCmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"update"},
		Short:   "Update vibe-rpc to the latest GitHub release",
		Long: `Checks GitHub Releases for a newer vibe-rpc, downloads the build for this
platform, verifies its SHA-256 checksum, checks the new binary runs, and
replaces the current executable. A running daemon is restarted on the new
version. Homebrew/Scoop installs should be upgraded with their package manager.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			cur := paths.Version
			fmt.Printf("Current version: %s\n", cur)
			rel, err := update.Client{}.Latest(ctx)
			if errors.Is(err, update.ErrNoRelease) {
				fmt.Println("No releases have been published yet — nothing to upgrade to.")
				return nil
			}
			if err != nil {
				return err
			}
			newer := update.Newer(rel.Version(), cur)
			fmt.Printf("Latest release:  %s\n", rel.Version())
			if !newer && !upForce {
				fmt.Println("You're up to date.")
				return nil
			}
			if upCheck {
				if newer {
					fmt.Printf("Update available. Run `vibe-rpc upgrade` to install it.\n%s\n", rel.HTMLURL)
				}
				return nil
			}
			exe, err := update.Executable()
			if err != nil {
				return err
			}
			if pm := update.ManagedBy(exe); pm != "" {
				return fmt.Errorf("%s is managed by %s", exe, pm)
			}
			fmt.Printf("Downloading %s for %s/%s…\n", rel.Tag, runtime.GOOS, runtime.GOARCH)
			bin, err := update.Client{}.Fetch(ctx, rel, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			fmt.Println("Checksum verified.")
			if err := update.Install(bin, exe, rel.Version()); err != nil {
				return err
			}
			fmt.Printf("Installed vibe-rpc %s at %s\n", rel.Version(), exe)
			if daemon.AlivePID() > 0 {
				daemon.ClearStopped()
				daemon.StopAndWait()
				pid, err := daemon.SpawnDetached()
				if err != nil {
					return fmt.Errorf("upgraded, but restarting the daemon failed: %w (run `vibe-rpc start`)", err)
				}
				fmt.Printf("Daemon restarted on the new version (pid %d).\n", pid)
			}
			fmt.Println("Restart `vibe-rpc serve` if it's running to load the new dashboard.")
			return nil
		},
	}
	upgradeCmd.Flags().BoolVar(&upCheck, "check", false, "Only check whether an update is available")
	upgradeCmd.Flags().BoolVar(&upForce, "force", false, "Reinstall even if already on the latest version")

	root.AddCommand(
		setupCmd, uninstallCmd, serveCmd, settingsCmd, wrappedCmd,
		daemonCmd, startCmd, stopCmd, restartCmd, upgradeCmd, notchCmd, notchFeedCmd, hookCmd,
		statusCmd, todayCmd, weekCmd, projectsCmd, recapCmd,
		insightsCmd, exportCmd, statcardsCmd, statuslineCmd, mcpCmd,
		privateCmd, pauseCmd, logsCmd, doctorCmd, scanCmd, backfillCmd,
		previewCmd, varsCmd, assetsCmd,
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// setNotchEnabled persists notch.enabled without freezing other defaults.
func setNotchEnabled(on bool) error {
	base := config.Load()
	edited := base.Clone()
	edited.Notch.Enabled = on
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	return err
}

// readPipedStdin reads stdin only when it's a pipe/file (never a terminal),
// giving up after timeout so a caller that leaves stdin open can't hang us.
func readPipedStdin(timeout time.Duration, limit int64) []byte {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	ch := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, limit))
		ch <- b
	}()
	select {
	case b := <-ch:
		return b
	case <-time.After(timeout):
		return nil
	}
}

// chooseAgents decides which agents setup tracks: from --agents, otherwise by
// asking in a terminal (defaulting to what's installed and already tracked),
// otherwise silently using installed agents that are tracked in config.
func chooseAgents(flag string, in *os.File, out io.Writer) (map[string]bool, error) {
	all := detect.All()
	cfg := config.Load()
	tracked := map[string]bool{}

	if flag = strings.TrimSpace(flag); flag != "" {
		if flag == "all" {
			for _, a := range all {
				tracked[a.Key] = true
			}
			return tracked, nil
		}
		for _, k := range strings.Split(flag, ",") {
			k = strings.ToLower(strings.TrimSpace(k))
			switch k {
			case "agy":
				k = "antigravity"
			case "claude-code", "claudecode":
				k = "claude"
			}
			if !slices.Contains(config.Agents, k) {
				return nil, fmt.Errorf("unknown agent %q (use antigravity, claude, opencode)", k)
			}
			tracked[k] = true
		}
		return tracked, nil
	}

	fmt.Fprintln(out, "  Coding agents on this machine:")
	for _, a := range all {
		where := "not found"
		if a.Installed {
			where = a.Where
		}
		fmt.Fprintf(out, "    %-12s %s\n", a.Name, where)
	}

	interactive := false
	if fi, err := in.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		interactive = true
	}
	reader := bufio.NewReader(in)
	for _, a := range all {
		def := a.Installed && cfg.Sources.Tracks(a.Key)
		if !interactive {
			tracked[a.Key] = def
			continue
		}
		hint := "[Y/n]"
		if !def {
			hint = "[y/N]"
		}
		fmt.Fprintf(out, "  Track %s? %s ", a.Name, hint)
		line, _ := reader.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			tracked[a.Key] = true
		case "n", "no":
			tracked[a.Key] = false
		default:
			tracked[a.Key] = def
		}
	}
	var names []string
	for _, a := range all {
		if tracked[a.Key] {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "  ! no agents selected; vibe-rpc won't track anything until you enable one in Settings")
	} else {
		fmt.Fprintf(out, "  ✓ tracking %s (change anytime in Settings → Agents)\n", strings.Join(names, ", "))
	}
	return tracked, nil
}

// saveTrackedAgents persists the tracking choice without freezing defaults.
func saveTrackedAgents(tracked map[string]bool) error {
	base := config.Load()
	edited := base.Clone()
	edited.Sources = config.SourcesConfig{
		Antigravity: config.BoolPtr(tracked["antigravity"]),
		Claude:      config.BoolPtr(tracked["claude"]),
		OpenCode:    config.BoolPtr(tracked["opencode"]),
	}
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	return err
}
