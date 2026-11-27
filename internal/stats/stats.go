package stats

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

type WeekRow struct {
	Day      string
	Weekday  string
	ActiveMs int64
	Prompts  int
	Tokens   int64
	Cost     float64
	IsToday  bool
}

func WeekGrid(agg *scanner.Aggregate, days int) []WeekRow {
	if days <= 0 {
		days = 7
	}
	now := time.Now()
	rows := make([]WeekRow, 0, days)
	for i := days - 1; i >= 0; i-- {
		dk := scanner.DayKeyAt(now, i)
		t, _ := time.ParseInLocation("2006-01-02", dk, time.Local)
		row := WeekRow{
			Day:     dk,
			Weekday: t.Weekday().String()[:3],
			IsToday: i == 0,
		}
		if agg != nil && agg.ByDay != nil && agg.ByDay[dk] != nil {
			d := agg.ByDay[dk]
			row.ActiveMs = d.ActiveMs
			row.Prompts = d.UserMessages
			row.Tokens = d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
			row.Cost = d.Cost
		}
		rows = append(rows, row)
	}
	return rows
}

type RangeSummary struct {
	Range        string
	Days         int
	ActiveMs     int64
	PrevActiveMs int64
	Sessions     int
	Prompts      int
	ToolCalls    int
	Tokens       int64
	FreshTokens  int64
	CacheTokens  int64
	Cost         float64
	LinesAdded   int
	LinesRemoved int
	Ships        int
	DailySeries  []WeekRow
}

func RangeDays(rng string) int {
	switch strings.ToLower(strings.TrimSpace(rng)) {
	case "7d", "week":
		return 7
	case "30d", "month":
		return 30
	case "90d", "quarter":
		return 90
	case "1y", "year", "365d":
		return 365
	default:
		return 0 // all-time
	}
}

func SummarizeRange(agg *scanner.Aggregate, rng string) RangeSummary {
	if agg == nil {
		return RangeSummary{Range: rng}
	}
	days := RangeDays(rng)
	if days == 0 {
		totalTok := agg.InputTokens + agg.OutputTokens + agg.CacheReadTokens + agg.CacheWriteTokens
		fresh := agg.InputTokens + agg.OutputTokens
		return RangeSummary{
			Range:        "all",
			ActiveMs:     agg.ActiveMs,
			Sessions:     agg.Sessions,
			Prompts:      agg.UserMessages,
			ToolCalls:    agg.ToolCalls,
			Tokens:       totalTok,
			FreshTokens:  fresh,
			CacheTokens:  totalTok - fresh,
			Cost:         agg.EstimatedCost,
			LinesAdded:   agg.LinesAdded,
			LinesRemoved: agg.LinesRemoved,
			Ships:        agg.Ships,
			DailySeries:  WeekGrid(agg, 30),
		}
	}
	now := time.Now()
	out := RangeSummary{Range: rng, Days: days, DailySeries: make([]WeekRow, 0, days)}
	for i := days - 1; i >= 0; i-- {
		dk := scanner.DayKeyAt(now, i)
		t, _ := time.ParseInLocation("2006-01-02", dk, time.Local)
		row := WeekRow{Day: dk, Weekday: t.Weekday().String()[:3], IsToday: i == 0}
		if d := agg.ByDay[dk]; d != nil {
			tok := d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
			fresh := d.InputTokens + d.OutputTokens
			row.ActiveMs = d.ActiveMs
			row.Prompts = d.UserMessages
			row.Tokens = tok
			row.Cost = d.Cost
			out.ActiveMs += d.ActiveMs
			out.Sessions += d.Sessions
			out.Prompts += d.UserMessages
			out.ToolCalls += d.ToolCalls
			out.Tokens += tok
			out.FreshTokens += fresh
			out.CacheTokens += tok - fresh
			out.Cost += d.Cost
			out.LinesAdded += d.LinesAdded
			out.LinesRemoved += d.LinesRemoved
			out.Ships += d.Ships
		}
		out.DailySeries = append(out.DailySeries, row)
	}
	// Previous window for delta calculation
	for i := days*2 - 1; i >= days; i-- {
		dk := scanner.DayKeyAt(now, i)
		if d := agg.ByDay[dk]; d != nil {
			out.PrevActiveMs += d.ActiveMs
		}
	}
	return out
}

func GenerateInsights(agg *scanner.Aggregate) []string {
	if agg == nil || agg.Sessions == 0 {
		return []string{"No sessions recorded yet. Start one, or run vibe-rpc scan to import past sessions."}
	}
	var out []string

	// Multi-agent insight
	var perAgent []string
	for _, a := range format.ActiveAgents(agg) {
		if s := agg.BySource[a]; s.Sessions > 0 {
			perAgent = append(perAgent, fmt.Sprintf("%d %s sessions (%s)", s.Sessions, config.AgentName(a), format.FmtHours(s.ActiveMs)))
		}
	}
	if len(perAgent) >= 2 {
		label := "Dual-agent workflow"
		if len(perAgent) > 2 {
			label = "Multi-agent workflow"
		}
		out = append(out, label+": "+strings.Join(perAgent, " + ")+".")
	}

	// Peak hour
	if agg.PeakHour != nil && agg.PeakHour.ActiveMs > 0 {
		h := agg.PeakHour.Hour
		period := "AM"
		h12 := h
		if h == 0 {
			h12 = 12
		} else if h >= 12 {
			period = "PM"
			if h > 12 {
				h12 = h - 12
			}
		}
		out = append(out, fmt.Sprintf("Peak coding hour is %d%s (%s total active time in that hour).",
			h12, period, format.FmtHours(agg.PeakHour.ActiveMs)))
	}

	// Cache efficiency
	totalTok := agg.InputTokens + agg.OutputTokens + agg.CacheReadTokens + agg.CacheWriteTokens
	if totalTok > 0 && agg.CacheReadTokens > 0 {
		pct := int(math.Round(float64(agg.CacheReadTokens) / float64(totalTok) * 100))
		out = append(out, fmt.Sprintf("Prompt cache served %d%% of your %s lifetime tokens.", pct, format.FmtNum(totalTok)))
	}

	// Streak
	if agg.Streak >= 2 {
		out = append(out, fmt.Sprintf("Active %d-day streak (personal best: %d days).", agg.Streak, agg.LongestStreak))
	}

	// Top file hotspot
	if len(agg.TopEditedFiles) > 0 {
		top := agg.TopEditedFiles[0]
		out = append(out, fmt.Sprintf("Most-edited hotspot: %s (%d edits).", filepath.Base(top.Path), top.Count))
	}

	// Best day
	if agg.BestDay != nil && agg.BestDay.ActiveMs > 0 {
		out = append(out, fmt.Sprintf("Biggest day on record: %s with %s coded and %s.",
			agg.BestDay.Day, format.FmtHours(agg.BestDay.ActiveMs), pricing.FmtCost(agg.BestDay.Cost)))
	}

	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

type Recap struct {
	Label        string         `json:"label"`
	Days         []string       `json:"days"`
	ActiveMs     int64          `json:"activeMs"`
	Sessions     int            `json:"sessions"`
	Prompts      int            `json:"prompts"`
	Tokens       int64          `json:"tokens"`
	Cost         float64        `json:"cost"`
	LinesAdded   int            `json:"linesAdded"`
	LinesRemoved int            `json:"linesRemoved"`
	Ships        int            `json:"ships"`
	Projects     []RecapProject `json:"projects"`
}

type RecapProject struct {
	Name     string `json:"name"`
	ActiveMs int64  `json:"activeMs"`
	Tokens   int64  `json:"tokens"`
}

func BuildRecap(agg *scanner.Aggregate, rng string) Recap {
	now := time.Now()
	r := strings.ToLower(strings.TrimSpace(rng))
	if r == "" {
		r = "yesterday"
	}
	var dayKeys []string
	label := r
	switch r {
	case "today":
		dayKeys = []string{scanner.DayKeyAt(now, 0)}
		label = "Today (" + dayKeys[0] + ")"
	case "yesterday":
		dayKeys = []string{scanner.DayKeyAt(now, 1)}
		label = "Yesterday (" + dayKeys[0] + ")"
	case "week", "7d":
		for i := 6; i >= 0; i-- {
			dayKeys = append(dayKeys, scanner.DayKeyAt(now, i))
		}
		label = "Last 7 days"
	default:
		n := 7
		clean := strings.TrimSuffix(r, "d")
		if v, err := strconv.Atoi(clean); err == nil && v > 0 && v <= 365 {
			n = v
		}
		for i := n - 1; i >= 0; i-- {
			dayKeys = append(dayKeys, scanner.DayKeyAt(now, i))
		}
		label = fmt.Sprintf("Last %d days", n)
	}

	rec := Recap{Label: label, Days: dayKeys}
	if agg == nil {
		return rec
	}
	projMap := map[string]*RecapProject{}
	for _, dk := range dayKeys {
		d := agg.ByDay[dk]
		if d == nil {
			continue
		}
		rec.ActiveMs += d.ActiveMs
		rec.Sessions += d.Sessions
		rec.Prompts += d.UserMessages
		rec.Tokens += d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
		rec.Cost += d.Cost
		rec.LinesAdded += d.LinesAdded
		rec.LinesRemoved += d.LinesRemoved
		rec.Ships += d.Ships
		for pn, pd := range d.Projects {
			if pd == nil {
				continue
			}
			rp := projMap[pn]
			if rp == nil {
				rp = &RecapProject{Name: pn}
				projMap[pn] = rp
			}
			rp.ActiveMs += pd.ActiveMs
			rp.Tokens += pd.Tokens
		}
	}
	for _, p := range projMap {
		rec.Projects = append(rec.Projects, *p)
	}
	sort.Slice(rec.Projects, func(i, j int) bool { return rec.Projects[i].ActiveMs > rec.Projects[j].ActiveMs })
	return rec
}

func RenderRecapMarkdown(rec Recap) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Coding Recap — %s\n\n", rec.Label)
	fmt.Fprintf(&b, "- **Active time:** %s (%d sessions, %d prompts)\n", format.FmtHours(rec.ActiveMs), rec.Sessions, rec.Prompts)
	fmt.Fprintf(&b, "- **Tokens / API equiv:** %s tokens (%s)\n", format.FmtNum(rec.Tokens), pricing.FmtCost(rec.Cost))
	fmt.Fprintf(&b, "- **Code churn:** +%s / -%s lines", format.FmtNum(int64(rec.LinesAdded)), format.FmtNum(int64(rec.LinesRemoved)))
	if rec.Ships > 0 {
		fmt.Fprintf(&b, " · %d ships", rec.Ships)
	}
	b.WriteString("\n")
	if len(rec.Projects) > 0 {
		b.WriteString("\n### Projects\n")
		for i, p := range rec.Projects {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&b, "- **%s**: %s (%s tokens)\n", p.Name, format.FmtHours(p.ActiveMs), format.FmtNum(p.Tokens))
		}
	}
	return b.String()
}

func AggregateToCSV(agg *scanner.Aggregate) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"day", "active_ms", "active_hours", "sessions", "prompts", "tool_calls", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "lines_added", "lines_removed", "ships", "cost_usd"})
	if agg != nil {
		days := make([]string, 0, len(agg.ByDay))
		for k := range agg.ByDay {
			days = append(days, k)
		}
		sort.Strings(days)
		for _, dk := range days {
			d := agg.ByDay[dk]
			if d == nil {
				continue
			}
			_ = w.Write([]string{
				dk,
				strconv.FormatInt(d.ActiveMs, 10),
				fmt.Sprintf("%.2f", float64(d.ActiveMs)/3_600_000.0),
				strconv.Itoa(d.Sessions),
				strconv.Itoa(d.UserMessages),
				strconv.Itoa(d.ToolCalls),
				strconv.FormatInt(d.InputTokens, 10),
				strconv.FormatInt(d.OutputTokens, 10),
				strconv.FormatInt(d.CacheReadTokens, 10),
				strconv.FormatInt(d.CacheWriteTokens, 10),
				strconv.Itoa(d.LinesAdded),
				strconv.Itoa(d.LinesRemoved),
				strconv.Itoa(d.Ships),
				fmt.Sprintf("%.4f", d.Cost),
			})
		}
	}
	w.Flush()
	return buf.String()
}

// ── SVG Stat Cards & Badges (pure Go, zero JS) ────────────────────────────────

func BadgeSVG(agg *scanner.Aggregate, metric string) string {
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	label := "vibe-rpc"
	val := format.FmtHours(agg.ActiveMs)
	switch strings.ToLower(metric) {
	case "tokens":
		val = format.FmtNum(agg.InputTokens+agg.OutputTokens+agg.CacheReadTokens+agg.CacheWriteTokens) + " tokens"
	case "streak":
		val = fmt.Sprintf("%dd streak", agg.Streak)
	case "cost":
		val = pricing.FmtCost(agg.EstimatedCost)
	case "sessions":
		val = fmt.Sprintf("%d sessions", agg.Sessions)
	case "lines":
		val = "+" + format.FmtNum(int64(agg.LinesAdded))
	default:
		val = format.FmtHours(agg.ActiveMs) + " coded"
	}
	lw := len(label)*7 + 16
	vw := len(val)*7 + 16
	tw := lw + vw
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="24" role="img" aria-label="%s: %s">
  <defs><linearGradient id="bg" x1="0" y1="0" x2="1" y2="1"><stop offset="0%%" stop-color="#4285f4"/><stop offset="100%%" stop-color="#6366f1"/></linearGradient></defs>
  <rect width="%d" height="24" fill="#181d2b" rx="12"/>
  <rect x="%d" width="%d" height="24" fill="url(#bg)" rx="12"/>
  <rect x="%d" width="10" height="24" fill="url(#bg)"/>
  <g fill="#f3f6fc" font-family="JetBrains Mono,monospace" font-size="11" font-weight="700" text-anchor="middle">
    <text x="%d" y="16">%s</text>
    <text x="%d" y="16">%s</text>
  </g>
</svg>`, tw, html.EscapeString(label), html.EscapeString(val), tw, lw, vw, lw, lw/2, html.EscapeString(label), lw+vw/2, html.EscapeString(val))
}

// posterAgents is the poster footer's agent list, e.g. " · Claude Code + Codex".
func posterAgents(agg *scanner.Aggregate) string {
	var names []string
	for _, a := range format.ActiveAgents(agg) {
		names = append(names, config.AgentName(a))
	}
	if len(names) == 0 {
		return ""
	}
	return " · " + strings.Join(names, " + ")
}

func PosterCardSVG(agg *scanner.Aggregate) string {
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	toks := agg.InputTokens + agg.OutputTokens + agg.CacheReadTokens + agg.CacheWriteTokens
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="540" height="284" viewBox="0 0 540 284">
  <defs>
    <radialGradient id="cardBg" cx="50%%" cy="0%%" r="120%%"><stop offset="0%%" stop-color="#15171c"/><stop offset="56%%" stop-color="#0b0c0f"/><stop offset="100%%" stop-color="#060709"/></radialGradient>
    <linearGradient id="hero" x1="0" y1="0" x2="0" y2="1"><stop offset="35%%" stop-color="#ffffff"/><stop offset="100%%" stop-color="#b4b9c4"/></linearGradient>
  </defs>
  <rect width="540" height="284" rx="22" fill="url(#cardBg)" stroke="#ffffff" stroke-opacity="0.14" stroke-width="1.5"/>
  <rect x="28" y="26" width="150" height="24" rx="12" fill="#ffffff" fill-opacity="0.08" stroke="#ffffff" stroke-opacity="0.16"/>
  <text x="103" y="42" font-family="JetBrains Mono,monospace" font-size="10.5" font-weight="700" letter-spacing="1.4" fill="#c7cad1" text-anchor="middle">VIBE-RPC WRAPPED</text>
  <text x="26" y="112" font-family="Inter,system-ui,sans-serif" font-size="56" font-weight="800" letter-spacing="-2" fill="url(#hero)">%s</text>
  <text x="28" y="136" font-family="JetBrains Mono,monospace" font-size="11" letter-spacing="1.2" fill="#8c9099">ACTIVE CODING · %d SESSIONS · %d-DAY STREAK</text>
  <line x1="28" y1="158" x2="512" y2="158" stroke="#ffffff" stroke-opacity="0.08" stroke-width="1"/>
  <g font-family="JetBrains Mono,monospace">
    <text x="28" y="186" font-size="10.5" letter-spacing="1.2" fill="#5e626b">TOKENS</text>
    <text x="28" y="212" font-family="Inter,system-ui,sans-serif" font-size="22" font-weight="700" fill="#f5f5f7">%s</text>
    <text x="190" y="186" font-size="10.5" letter-spacing="1.2" fill="#5e626b">API EQUIV</text>
    <text x="190" y="212" font-family="Inter,system-ui,sans-serif" font-size="22" font-weight="700" fill="#f5f5f7">%s</text>
    <text x="355" y="186" font-size="10.5" letter-spacing="1.2" fill="#5e626b">LINES ADDED</text>
    <text x="355" y="212" font-family="Inter,system-ui,sans-serif" font-size="22" font-weight="700" fill="#f5f5f7">+%s</text>
  </g>
  <text x="28" y="256" font-family="JetBrains Mono,monospace" font-size="10.5" fill="#5e626b">github.com/chauveaul/vibe-rpc%s</text>
</svg>`,
		html.EscapeString(format.FmtHours(agg.ActiveMs)),
		agg.Sessions,
		agg.Streak,
		html.EscapeString(format.FmtNum(toks)),
		html.EscapeString(pricing.FmtCost(agg.EstimatedCost)),
		html.EscapeString(format.FmtNum(int64(agg.LinesAdded))),
		html.EscapeString(posterAgents(agg)),
	)
}

func HeatmapSVG(agg *scanner.Aggregate, days int) string {
	if days <= 0 {
		days = 91
	}
	rows := WeekGrid(agg, days)
	var maxMs int64
	for _, r := range rows {
		if r.ActiveMs > maxMs {
			maxMs = r.ActiveMs
		}
	}
	cols := (len(rows) + 6) / 7
	w := cols*16 + 32
	h := 7*16 + 50
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, w, h, w, h)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" rx="18" fill="#151925" stroke="#2c354c" stroke-width="1.5"/>`, w, h)
	fmt.Fprintf(&b, `<text x="18" y="26" font-family="JetBrains Mono,monospace" font-size="11" font-weight="700" fill="#8ab4f8">CODING HEATMAP · LAST %d DAYS</text>`, days)
	colors := []string{"#22283a", "#264679", "#3464b2", "#4285f4", "#8ab4f8"}
	for idx, r := range rows {
		col := idx / 7
		row := idx % 7
		lvl := 0
		if r.ActiveMs > 0 && maxMs > 0 {
			ratio := float64(r.ActiveMs) / float64(maxMs)
			switch {
			case ratio >= 0.75:
				lvl = 4
			case ratio >= 0.5:
				lvl = 3
			case ratio >= 0.25:
				lvl = 2
			default:
				lvl = 1
			}
		}
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="12" height="12" rx="4" fill="%s"><title>%s: %s</title></rect>`,
			18+col*16, 38+row*16, colors[lvl], r.Day, html.EscapeString(format.FmtHours(r.ActiveMs)))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func SessionCardSVG(st state.SessionState) string {
	proj := format.HumanProject(st.Cwd)
	if proj == "" {
		proj = "Antigravity"
	}
	tok := st.Tokens.Input + st.Tokens.Output + st.Tokens.CacheRead + st.Tokens.CacheWrite
	src := "Antigravity"
	if st.Source == "claude" {
		src = "Claude Code"
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="460" height="174" viewBox="0 0 460 174">
  <defs><linearGradient id="scBg" x1="0" y1="0" x2="1" y2="1"><stop offset="0%%" stop-color="#1b2236"/><stop offset="100%%" stop-color="#10131e"/></linearGradient></defs>
  <rect width="460" height="174" rx="20" fill="url(#scBg)" stroke="#38496d" stroke-width="1.5"/>
  <text x="24" y="40" font-family="JetBrains Mono,monospace" font-size="11" font-weight="700" fill="#8ab4f8">%s · %s</text>
  <text x="24" y="78" font-family="Space Grotesk,sans-serif" font-size="28" font-weight="700" fill="#ffffff">%s</text>
  <text x="24" y="106" font-family="JetBrains Mono,monospace" font-size="13" fill="#c7d2e5">%s · %d prompts · %d tools · %s tokens</text>
  <text x="24" y="144" font-family="JetBrains Mono,monospace" font-size="11" fill="#64748b">vibe-rpc live session snapshot</text>
</svg>`,
		html.EscapeString(strings.ToUpper(src)),
		html.EscapeString(strings.ToUpper(st.Status)),
		html.EscapeString(proj),
		html.EscapeString(format.HumanModel(st.Model)),
		st.Messages,
		st.Tools,
		html.EscapeString(format.FmtNum(tok)),
	)
}
