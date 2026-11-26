package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/daemon"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/pause"
	"github.com/chauveaul/vibe-rpc/internal/presence"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/privacy"
	"github.com/chauveaul/vibe-rpc/internal/proc"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
	"github.com/chauveaul/vibe-rpc/internal/stats"
)

var tabs = []string{"Now", "Today", "Week", "Streak", "Lifetime", "Cost", "Code"}

var (
	styleBrand = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b0c10")).Background(lipgloss.Color("#f5f5f7")).Padding(0, 1)
	styleTab   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8c9099")).Padding(0, 1)
	styleTabOn = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b0c10")).Background(lipgloss.Color("#e5e7eb")).Padding(0, 1)
	styleCard  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#3f424a")).Padding(1, 2)
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#d1d5db"))
	styleVal   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff"))
	styleMute  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8c9099"))
	styleGood  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#4ade80"))
	styleWarn  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#facc15"))
	styleCrit  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f87171"))
)

type tickMsg time.Time

type model struct {
	tab        int
	width      int
	height     int
	cfg        config.Config
	agg        *scanner.Aggregate
	st         state.SessionState
	pid        int
	pausedTill time.Time
	ccLimits   *limits.Claude
	agyLimits  *limits.Antigravity
	flash      string
}

func loadSnapshot() (config.Config, *scanner.Aggregate, state.SessionState, int, time.Time, *limits.Claude, *limits.Antigravity) {
	cfg := config.Load()
	agg := scanner.ReadAggregate()
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	sessions := state.ListSessions(state.ListOptions{
		IncludeAntigravity: cfg.Sources.WantAntigravity(),
		IncludeClaude:      cfg.Sources.WantClaude(),
		IncludeClaudeRPC:   true,
	})
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
	if presence.IsDualWorking(st) {
		st.Status = "dual"
	}
	return cfg, agg, st, daemon.AlivePID(), pause.Until(""), limits.Load(), limits.LoadAntigravity()
}

func (m *model) reload() {
	m.cfg, m.agg, m.st, m.pid, m.pausedTill, m.ccLimits, m.agyLimits = loadSnapshot()
}

func newModel() model {
	m := model{width: 92, height: 32}
	m.reload()
	return m
}

func tickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tickCmd()
}

func (m model) activeProjectCwd() string {
	if m.st.Cwd != "" {
		return m.st.Cwd
	}
	cwd, _ := os.Getwd()
	return cwd
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		m.reload()
		m.flash = ""
		return m, tickCmd()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "right", "l", "tab":
			m.tab = (m.tab + 1) % len(tabs)
		case "left", "h", "shift+tab":
			m.tab = (m.tab - 1 + len(tabs)) % len(tabs)
		case "1", "2", "3", "4", "5", "6", "7":
			idx := int(msg.String()[0] - '1')
			if idx >= 0 && idx < len(tabs) {
				m.tab = idx
			}
		case "p":
			if !pause.Until("").IsZero() {
				pause.Clear("")
				m.pausedTill = time.Time{}
				m.flash = "Discord presence resumed"
			} else if until, err := pause.Set(30*time.Minute, ""); err == nil {
				m.pausedTill = until
				m.flash = "Paused Discord presence until " + until.Format(time.Kitchen)
			}
		case "v":
			if cwd := m.activeProjectCwd(); cwd != "" {
				cur := privacy.Resolve(cwd, m.cfg).Visibility
				next := "name-only"
				switch cur {
				case "name-only":
					next = "hidden"
				case "hidden":
					next = "public"
				}
				if _, err := privacy.SetCwdVisibility(cwd, next); err == nil {
					m.flash = fmt.Sprintf("Privacy for %s → %s", filepath.Base(cwd), next)
				}
			}
		case "r":
			m.reload()
			m.flash = "Refreshed"
		}
	}
	return m, nil
}

func bar(val, maxVal float64, width int) string {
	if width <= 0 {
		width = 24
	}
	if maxVal <= 0 || val <= 0 {
		return strings.Repeat("░", width)
	}
	n := int(math.Round(math.Min(1, val/maxVal) * float64(width)))
	if n == 0 && val > 0 {
		n = 1
	}
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

func styledPctBar(pct float64, width int) string {
	b := bar(pct, 100, width)
	switch {
	case pct >= 95:
		return styleCrit.Render(b)
	case pct >= 80:
		return styleWarn.Render(b)
	default:
		return styleVal.Render(b)
	}
}

func formatResetShort(resetsAt int64, isWeek bool, now time.Time) string {
	if resetsAt <= 0 {
		return ""
	}
	t := time.Unix(resetsAt, 0)
	if !t.After(now) {
		return "resetting"
	}
	if isWeek {
		if t.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02") {
			return "resets " + t.In(now.Location()).Format("15:04")
		}
		return "resets " + t.In(now.Location()).Format("Mon 15:04")
	}
	d := t.Sub(now)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("resets in %dh %dm", h, m)
	}
	return fmt.Sprintf("resets in %dm", m)
}

func renderQuotaLine(label string, w *limits.Window, isWeek bool, now time.Time) string {
	if w == nil {
		return fmt.Sprintf("  %-12s %s  %4s", label, styleMute.Render(strings.Repeat("░", 20)), "—")
	}
	if w.Expired(now) {
		return fmt.Sprintf("  %-12s %s  %4s  %s", label, styleMute.Render(strings.Repeat("░", 20)), "0%", styleMute.Render("fresh"))
	}
	pct := math.Max(0, math.Min(100, w.UsedPct))
	reset := formatResetShort(w.ResetsAt, isWeek, now)
	if reset != "" {
		reset = "  " + styleMute.Render(reset)
	}
	return fmt.Sprintf("  %-12s %s  %3.0f%%%s", label, styledPctBar(pct, 20), pct, reset)
}

func (m model) View() string {
	var b strings.Builder

	// Header
	dBadge := styleWarn.Render("● daemon stopped")
	if m.pid > 0 {
		dBadge = styleGood.Render(fmt.Sprintf("● daemon pid %d", m.pid))
	}
	header := styleBrand.Render(" vibe-rpc ") + "  " + dBadge
	if !m.pausedTill.IsZero() {
		header += "  " + styleWarn.Render("⏸ paused until "+m.pausedTill.Format(time.Kitchen))
	}
	b.WriteString(header + "\n\n")

	// Tab strip
	var tabParts []string
	for i, name := range tabs {
		label := fmt.Sprintf("%d:%s", i+1, name)
		if i == m.tab {
			tabParts = append(tabParts, styleTabOn.Render(label))
		} else {
			tabParts = append(tabParts, styleTab.Render(label))
		}
	}
	b.WriteString(strings.Join(tabParts, " ") + "\n\n")

	// Body
	w := m.width - 6
	if w < 56 {
		w = 56
	}
	if w > 96 {
		w = 96
	}
	card := styleCard.Width(w)

	switch m.tab {
	case 0:
		b.WriteString(card.Render(m.viewNow()))
	case 1:
		b.WriteString(card.Render(m.viewToday()))
	case 2:
		b.WriteString(card.Render(m.viewWeek()))
	case 3:
		b.WriteString(card.Render(m.viewStreak()))
	case 4:
		b.WriteString(card.Render(m.viewLifetime()))
	case 5:
		b.WriteString(card.Render(m.viewCost()))
	case 6:
		b.WriteString(card.Render(m.viewCode()))
	}

	foot := "  ←/→ or 1-7 tabs · p pause 30m · v privacy · r refresh · q quit"
	if m.flash != "" {
		b.WriteString("\n\n  " + styleGood.Render("✓ "+m.flash))
	}
	b.WriteString("\n\n" + styleMute.Render(foot))
	return b.String()
}

func (m model) viewNow() string {
	vars := format.BuildVars(m.st, m.cfg, m.agg)
	cursor := &presence.RotationCursor{}
	act, cleared := daemon.BuildActivity(m.st, m.cfg, m.agg, cursor)
	vis := privacy.Resolve(m.activeProjectCwd(), m.cfg).Visibility

	var lines []string
	lines = append(lines, styleTitle.Render("LIVE PRESENCE"))
	lines = append(lines, fmt.Sprintf("  Agent   : %s  (%s)", styleVal.Render(fmt.Sprint(vars["sourcePretty"])), m.st.Status))
	lines = append(lines, fmt.Sprintf("  Project : %s  %s", styleVal.Render(fmt.Sprint(vars["project"])), styleMute.Render("["+vis+"]")))
	lines = append(lines, fmt.Sprintf("  Model   : %s", styleVal.Render(fmt.Sprint(vars["modelPretty"]))))
	if m.st.CurrentTool != "" {
		lines = append(lines, fmt.Sprintf("  Tool    : %s  %s", styleVal.Render(format.HumanTool(m.st.CurrentTool)), styleMute.Render(m.st.CurrentFile)))
	}
	lines = append(lines, fmt.Sprintf("  Session : %s · %s · %s",
		fmt.Sprint(vars["duration"]), fmt.Sprint(vars["messagesLabel"]), fmt.Sprint(vars["toolsLabel"])))

	lines = append(lines, "", styleTitle.Render("DISCORD CARD"))
	if cleared || act == nil {
		lines = append(lines, styleMute.Render("  (Hidden / paused — no activity sent to Discord)"))
	} else {
		lines = append(lines, fmt.Sprintf("  App     : %s", styleVal.Render(act.Name)))
		lines = append(lines, fmt.Sprintf("  Line 1  : %s", act.Details))
		lines = append(lines, fmt.Sprintf("  Line 2  : %s", act.State))
	}

	now := time.Now()
	lines = append(lines, "", styleTitle.Render("USAGE QUOTAS"))
	hasAnyQuota := false
	if m.ccLimits != nil && (m.ccLimits.FiveHour != nil || m.ccLimits.SevenDay != nil) {
		hasAnyQuota = true
		lines = append(lines, renderQuotaLine("Claude 5h", m.ccLimits.FiveHour, false, now))
		lines = append(lines, renderQuotaLine("Claude 7d", m.ccLimits.SevenDay, true, now))
	}
	if m.agyLimits != nil {
		agyModel := ""
		if m.st.AgySession != nil && m.st.AgySession.Model != "" {
			agyModel = m.st.AgySession.Model
		} else if m.st.Source != "claude" {
			agyModel = m.st.Model
		}
		if pool, s, w := m.agyLimits.PickPool(agyModel); s != nil || w != nil {
			hasAnyQuota = true
			prefix := "Agy"
			if pool == "3p" {
				prefix = "Agy 3P"
			}
			lines = append(lines, renderQuotaLine(prefix+" 5h", s, false, now))
			lines = append(lines, renderQuotaLine(prefix+" 7d", w, true, now))
		}
	}
	if !hasAnyQuota {
		lines = append(lines, styleMute.Render("  Enable usage limits in `vibe-rpc settings` for live 5h & 7d quota bars."))
	}
	return strings.Join(lines, "\n")
}

func (m model) viewToday() string {
	dk := scanner.DayKey(time.Now().UnixMilli())
	d := &scanner.DayBucket{}
	if m.agg.ByDay != nil && m.agg.ByDay[dk] != nil {
		d = m.agg.ByDay[dk]
	}
	tok := d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
	lines := []string{
		styleTitle.Render("TODAY · " + dk),
		fmt.Sprintf("  Active time : %s", styleVal.Render(format.FmtHours(d.ActiveMs))),
		fmt.Sprintf("  Sessions    : %d", d.Sessions),
		fmt.Sprintf("  Prompts     : %d", d.UserMessages),
		fmt.Sprintf("  Tool calls  : %d", d.ToolCalls),
		fmt.Sprintf("  Tokens      : %s", styleVal.Render(format.FmtNum(tok))),
		fmt.Sprintf("  API equiv   : %s", styleGood.Render(pricing.FmtCost(d.Cost))),
		fmt.Sprintf("  Code churn  : +%d / -%d lines   ·  %d ships", d.LinesAdded, d.LinesRemoved, d.Ships),
	}

	var goalLines []string
	if m.cfg.Goals.DailyHours > 0 {
		cur := float64(d.ActiveMs) / 3_600_000
		pct := cur / m.cfg.Goals.DailyHours * 100
		b := bar(cur, m.cfg.Goals.DailyHours, 20)
		if pct >= 100 {
			b = styleGood.Render(b)
		}
		goalLines = append(goalLines, fmt.Sprintf("  Daily hours   %s  %s / %gh (%.0f%%)", b, format.FmtHours(d.ActiveMs), m.cfg.Goals.DailyHours, pct))
	}
	if m.cfg.Goals.DailyPrompts > 0 {
		pct := float64(d.UserMessages) / float64(m.cfg.Goals.DailyPrompts) * 100
		b := bar(float64(d.UserMessages), float64(m.cfg.Goals.DailyPrompts), 20)
		if pct >= 100 {
			b = styleGood.Render(b)
		}
		goalLines = append(goalLines, fmt.Sprintf("  Daily prompts %s  %d / %d (%.0f%%)", b, d.UserMessages, m.cfg.Goals.DailyPrompts, pct))
	}
	if m.cfg.Goals.WeeklyHours > 0 {
		week := stats.SummarizeRange(m.agg, "7d")
		cur := float64(week.ActiveMs) / 3_600_000
		pct := cur / m.cfg.Goals.WeeklyHours * 100
		b := bar(cur, m.cfg.Goals.WeeklyHours, 20)
		if pct >= 100 {
			b = styleGood.Render(b)
		}
		goalLines = append(goalLines, fmt.Sprintf("  Weekly hours  %s  %s / %gh (%.0f%%)", b, format.FmtHours(week.ActiveMs), m.cfg.Goals.WeeklyHours, pct))
	}
	if len(goalLines) > 0 {
		lines = append(lines, "", styleTitle.Render("GOALS"))
		lines = append(lines, goalLines...)
	}
	return strings.Join(lines, "\n")
}

func (m model) viewWeek() string {
	rows := stats.WeekGrid(m.agg, 7)
	var maxMs int64
	var sumMs int64
	for _, r := range rows {
		sumMs += r.ActiveMs
		if r.ActiveMs > maxMs {
			maxMs = r.ActiveMs
		}
	}
	lines := []string{styleTitle.Render(fmt.Sprintf("LAST 7 DAYS · %s total", format.FmtHours(sumMs)))}
	for _, r := range rows {
		tag := " "
		if r.IsToday {
			tag = "→"
		}
		lines = append(lines, fmt.Sprintf(" %s %s %s  %s  %-6s  %3d prompts",
			tag, r.Weekday, r.Day[5:], bar(float64(r.ActiveMs), float64(maxMs), 22),
			format.FmtHours(r.ActiveMs), r.Prompts))
	}
	return strings.Join(lines, "\n")
}

func (m model) viewStreak() string {
	lines := []string{
		styleTitle.Render("STREAK & CONSISTENCY"),
		fmt.Sprintf("  Current streak : %s", styleGood.Render(fmt.Sprintf("%d days", m.agg.Streak))),
		fmt.Sprintf("  Longest streak : %s", styleVal.Render(fmt.Sprintf("%d days", m.agg.LongestStreak))),
		fmt.Sprintf("  Days tracked   : %d", m.agg.DaysSinceFirst),
	}
	if m.agg.BestDay != nil {
		lines = append(lines, fmt.Sprintf("  Best day       : %s (%s, %s)",
			m.agg.BestDay.Day, format.FmtHours(m.agg.BestDay.ActiveMs), pricing.FmtCost(m.agg.BestDay.Cost)))
	}
	if m.agg.PeakHour != nil {
		lines = append(lines, fmt.Sprintf("  Peak hour      : %02d:00 (%s total)",
			m.agg.PeakHour.Hour, format.FmtHours(m.agg.PeakHour.ActiveMs)))
	}
	return strings.Join(lines, "\n")
}

func (m model) viewLifetime() string {
	tok := m.agg.InputTokens + m.agg.OutputTokens + m.agg.CacheReadTokens + m.agg.CacheWriteTokens
	agy := m.agg.BySource["antigravity"]
	cc := m.agg.BySource["claude"]
	if agy == nil {
		agy = &scanner.SourceStats{}
	}
	if cc == nil {
		cc = &scanner.SourceStats{}
	}
	var dualMs int64
	for _, ms := range m.agg.DualByDay {
		dualMs += ms
	}
	lines := []string{
		styleTitle.Render("LIFETIME COMBINED"),
		fmt.Sprintf("  Active time : %s  (%d sessions)", styleVal.Render(format.FmtHours(m.agg.ActiveMs)), m.agg.Sessions),
		fmt.Sprintf("  Prompts     : %s   ·  Tool calls: %s", format.FmtNum(int64(m.agg.UserMessages)), format.FmtNum(int64(m.agg.ToolCalls))),
		fmt.Sprintf("  Tokens      : %s   ·  Unique files: %s", format.FmtNum(tok), format.FmtNum(int64(m.agg.UniqueFiles))),
		fmt.Sprintf("  API equiv   : %s   ·  Ships: %d", styleGood.Render(pricing.FmtCost(m.agg.EstimatedCost)), m.agg.Ships),
		"",
		styleTitle.Render("BY AGENT SOURCE"),
		fmt.Sprintf("  Antigravity : %d sessions · %s · %s tokens · %s",
			agy.Sessions, format.FmtHours(agy.ActiveMs), format.FmtNum(agy.Tokens), pricing.FmtCost(agy.Cost)),
		fmt.Sprintf("  Claude Code : %d sessions · %s · %s tokens · %s",
			cc.Sessions, format.FmtHours(cc.ActiveMs), format.FmtNum(cc.Tokens), pricing.FmtCost(cc.Cost)),
		fmt.Sprintf("  Dual wield  : %s running both agents simultaneously", styleVal.Render(format.FmtHours(dualMs))),
	}
	return strings.Join(lines, "\n")
}

func monthSpend(agg *scanner.Aggregate) float64 {
	if agg == nil {
		return 0
	}
	prefix := time.Now().Format("2006-01")
	var total float64
	for day, b := range agg.ByDay {
		if strings.HasPrefix(day, prefix) && b != nil {
			total += b.Cost
		}
	}
	return total
}

func (m model) viewCost() string {
	lines := []string{
		styleTitle.Render(fmt.Sprintf("ESTIMATED API-EQUIVALENT COST · %s", pricing.FmtCost(m.agg.EstimatedCost))),
	}
	if len(m.agg.ModelSplit) == 0 {
		lines = append(lines, styleMute.Render("  No model usage recorded yet. Run `vibe-rpc scan`."))
	} else {
		maxCost := m.agg.ModelSplit[0].Cost
		for i, ms := range m.agg.ModelSplit {
			if i >= 8 {
				break
			}
			lines = append(lines, fmt.Sprintf("  %-18s %s  %8s (%2.0f%%)",
				format.HumanModel(ms.Model), bar(ms.Cost, maxCost, 20), pricing.FmtCost(ms.Cost), ms.CostPct*100))
		}
	}
	mtd := monthSpend(m.agg)
	lines = append(lines, "", styleTitle.Render("MONTHLY BUDGET"))
	if m.cfg.Budget.Monthly > 0 {
		pct := mtd / m.cfg.Budget.Monthly * 100
		lines = append(lines, fmt.Sprintf("  This month : %s  %s / $%.0f (%.0f%%)",
			styledPctBar(pct, 20), pricing.FmtCost(mtd), m.cfg.Budget.Monthly, pct))
	} else {
		lines = append(lines, fmt.Sprintf("  This month : %s  %s", styleVal.Render(pricing.FmtCost(mtd)), styleMute.Render("(set a monthly budget in `vibe-rpc settings`)")))
	}
	return strings.Join(lines, "\n")
}

func (m model) viewCode() string {
	lines := []string{
		styleTitle.Render("CODE CHURN & HOTSPOTS"),
		fmt.Sprintf("  Lines added   : %s", styleGood.Render("+"+format.FmtNum(int64(m.agg.LinesAdded)))),
		fmt.Sprintf("  Lines removed : %s", styleWarn.Render("-"+format.FmtNum(int64(m.agg.LinesRemoved)))),
		fmt.Sprintf("  Net lines     : %s", styleVal.Render(fmt.Sprintf("%+d", m.agg.LinesNet))),
		"",
		styleTitle.Render("TOP LANGUAGES"),
	}
	type kv struct {
		k string
		v int
	}
	var langs []kv
	for k, v := range m.agg.Languages {
		if v != nil {
			langs = append(langs, kv{k, v.Edits})
		}
	}
	sort.Slice(langs, func(i, j int) bool { return langs[i].v > langs[j].v })
	if len(langs) == 0 {
		lines = append(lines, styleMute.Render("  No language edits recorded yet."))
	} else {
		maxE := float64(langs[0].v)
		for i, l := range langs {
			if i >= 5 {
				break
			}
			lines = append(lines, fmt.Sprintf("  %-14s %s  %d edits", l.k, bar(float64(l.v), maxE, 18), l.v))
		}
	}
	if len(m.agg.TopEditedFiles) > 0 {
		lines = append(lines, "", styleTitle.Render("TOP EDITED FILES"))
		for i, f := range m.agg.TopEditedFiles {
			if i >= 4 {
				break
			}
			lines = append(lines, fmt.Sprintf("  %-28s %d edits", filepath.Base(f.Path), f.Count))
		}
	}
	return strings.Join(lines, "\n")
}

func Run() error {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
