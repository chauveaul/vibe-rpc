package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/daemon"
	"github.com/chauveaul/vibe-rpc/internal/notch"
	"github.com/chauveaul/vibe-rpc/internal/paths"
)

// helpGroups orders the root help. Commands not listed here (hidden ones,
// `help`) don't appear in it.
var helpGroups = []struct {
	title    string
	commands []string
}{
	{"start", []string{"setup", "settings", "upgrade", "uninstall", "completion"}},
	{"discord", []string{"status", "preview", "private", "pause", "vars"}},
	{"stats", []string{"today", "week", "projects", "recap", "insights", "export"}},
	{"share", []string{"serve", "wrapped", "statcards", "statusline", "assets"}},
	{"extras", []string{"notch", "mcp"}},
	{"daemon", []string{"start", "stop", "restart", "logs"}},
	{"fix", []string{"doctor", "scan", "backfill"}},
}

// helpArgs are the argument hints shown next to a command in the root help;
// shorter than each command's full Use line.
var helpArgs = map[string]string{
	"projects":  "[name]",
	"recap":     "[range]",
	"private":   "[on|off]",
	"pause":     "[30m|off]",
	"statcards": "[kind]",
	"assets":    "export [dir]",
	"notch":     "[on|off|install]",
	"mcp":       "[install]",
	"backfill":  "<dir>",
}

// installHelp gives the root command its own help screen; subcommands keep
// Cobra's default.
func installHelp(root *cobra.Command) {
	// Cobra adds `completion` lazily; create it now to give it a short line.
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			c.Short = "Shell completion scripts"
		}
	}
	def := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd != root {
			def(cmd, args)
			return
		}
		renderRootHelp(cmd.OutOrStdout(), root)
	})
}

func renderRootHelp(out io.Writer, root *cobra.Command) {
	r := lipgloss.NewRenderer(out)
	// No adaptive colors here, so skip the terminal background query (an OSC
	// 11 round trip whose late reply can leak into the shell prompt).
	r.SetHasDarkBackground(true)
	var (
		chip  = r.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b0c10")).Background(lipgloss.Color("#f5f5f7")).Padding(0, 1)
		bold  = r.NewStyle().Bold(true)
		mute  = r.NewStyle().Foreground(lipgloss.Color("#8c9099"))
		faint = r.NewStyle().Foreground(lipgloss.Color("#4b4f57"))
		good  = r.NewStyle().Foreground(lipgloss.Color("#4ade80"))
	)

	byName := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		byName[c.Name()] = c
	}
	label := func(name string) string {
		if a := helpArgs[name]; a != "" {
			return name + " " + a
		}
		return name
	}
	width := 0
	for _, g := range helpGroups {
		for _, n := range g.commands {
			if byName[n] != nil && len(label(n)) > width {
				width = len(label(n))
			}
		}
	}
	rule := width + 50

	var b strings.Builder
	brand := chip.Render("vibe-rpc")
	if r.ColorProfile() == termenv.Ascii {
		brand = "vibe-rpc" // no colors: the chip's padding would just be stray spaces
	}
	fmt.Fprintf(&b, "\n  %s %s\n", brand, mute.Render(paths.Version))
	fmt.Fprintf(&b, "  %s\n\n", root.Short)
	for _, l := range liveLines(good, mute) {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %s %s  %s\n", mute.Render("usage"), bold.Render("vibe-rpc <command>"), mute.Render("· no command opens the terminal UI"))

	for _, g := range helpGroups {
		fmt.Fprintf(&b, "\n  %s %s\n", bold.Render(g.title), faint.Render(strings.Repeat("─", max(0, rule-len(g.title)-1))))
		for _, n := range g.commands {
			c := byName[n]
			if c == nil {
				continue
			}
			l := label(n)
			name := bold.Render(n)
			if a := helpArgs[n]; a != "" {
				name += " " + mute.Render(a)
			}
			fmt.Fprintf(&b, "    %s%s  %s\n", name, strings.Repeat(" ", width-len(l)), c.Short)
		}
	}

	fmt.Fprintf(&b, "\n  %s %s\n", mute.Render("flags for one command"), "vibe-rpc <command> --help")
	fmt.Fprintf(&b, "  %s %s\n", mute.Render("something looks off  "), "vibe-rpc doctor --fix")
	fmt.Fprintf(&b, "  %s %s\n\n", mute.Render("source and docs      "), "github.com/chauveaul/vibe-rpc")
	_, _ = io.WriteString(out, b.String())
}

// liveLines summarize the local setup: whether the daemon runs, VibeNotch on
// macOS, and which agents are tracked. They only read local files.
func liveLines(good, mute lipgloss.Style) []string {
	cfg := config.Load()
	var agents []string
	for _, a := range config.Agents {
		if cfg.Sources.Tracks(a) {
			agents = append(agents, config.AgentName(a))
		}
	}
	first := mute.Render("○") + " daemon stopped " + mute.Render("(vibe-rpc start)")
	if daemon.AlivePID() > 0 {
		first = good.Render("●") + " daemon running"
	}
	if notch.Supported() && cfg.Notch.Enabled {
		first += mute.Render("  ·  ") + "VibeNotch on"
	}
	second := "  no agents tracked " + mute.Render("(vibe-rpc setup)")
	if len(agents) > 0 {
		second = "  tracking " + strings.Join(agents, ", ")
	}
	return []string{first, second}
}
