package server

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/privacy"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
	"github.com/chauveaul/vibe-rpc/internal/stats"
)

// ── Agent comparison (any number of agents) ─────────────────────────────────

// sourceTotals is one agent's activity summed over the selected range.
type sourceTotals struct {
	ActiveMs int64
	Sessions int
	Prompts  int
	Tokens   int64
	Cost     float64
	Tools    map[string]int
}

func (t sourceTotals) avgSessionMs() int64 {
	if t.Sessions == 0 {
		return 0
	}
	return t.ActiveMs / int64(t.Sessions)
}

type cmpAgent struct {
	Key  string
	Name string
}

// cmpCell is one agent's value for a metric. Pct is its share of the row
// total (drawn as a segment of a stacked bar).
type cmpCell struct {
	Key   string
	Text  string
	Pct   float64
	Leads bool
}

type cmpRow struct {
	Label string
	Cells []cmpCell
}

type toolRow struct {
	Name  string
	Count string
	Pct   int // relative to that agent's top tool
}

type agentTools struct {
	Key, Name string
	Tools     []toolRow
}

type compareView struct {
	Agents    []cmpAgent
	Title     string // "Antigravity vs Claude Code vs OpenCode"
	Rows      []cmpRow
	Tools     []agentTools
	DualHours string
	DualPct   string // share of active time with 2+ agents at once
	HasData   bool
}

// rangeDayKeys returns the local day keys covered by rng, or nil for all-time.
func rangeDayKeys(rng string) map[string]bool {
	days := stats.RangeDays(rng)
	if days == 0 {
		return nil
	}
	now := time.Now()
	keys := make(map[string]bool, days)
	for i := 0; i < days; i++ {
		keys[scanner.DayKeyAt(now, i)] = true
	}
	return keys
}

func sumSource(days map[string]*scanner.SourceDay, keys map[string]bool) sourceTotals {
	t := sourceTotals{Tools: map[string]int{}}
	for k, d := range days {
		if d == nil || (keys != nil && !keys[k]) {
			continue
		}
		t.ActiveMs += d.ActiveMs
		t.Sessions += d.Sessions
		t.Prompts += d.Prompts
		t.Tokens += d.Tokens
		t.Cost += d.Cost
		for name, n := range d.Tools {
			t.Tools[name] += n
		}
	}
	return t
}

// shareRow builds one metric row across agents. Ties for the top don't lead.
func shareRow(label string, keys []string, vals []float64, texts []string) cmpRow {
	r := cmpRow{Label: label}
	var total, top float64
	for _, v := range vals {
		total += v
		top = math.Max(top, v)
	}
	leaders := 0
	for _, v := range vals {
		if v == top && top > 0 {
			leaders++
		}
	}
	for i, k := range keys {
		c := cmpCell{Key: k, Text: texts[i]}
		if total > 0 {
			c.Pct = math.Round(vals[i]/total*1000) / 10
		}
		c.Leads = leaders == 1 && vals[i] == top
		r.Cells = append(r.Cells, c)
	}
	return r
}

func topTools(m map[string]int, n int) []toolRow {
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(m))
	for k, v := range m {
		if v > 0 {
			list = append(list, kv{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	if len(list) > n {
		list = list[:n]
	}
	out := make([]toolRow, 0, len(list))
	for _, x := range list {
		pct := 100
		if list[0].v > 0 {
			pct = int(math.Round(float64(x.v) / float64(list[0].v) * 100))
		}
		out = append(out, toolRow{Name: shortTool(x.k), Count: format.FmtNum(int64(x.v)), Pct: pct})
	}
	return out
}

// buildCompare compares the given agents (tracked ones, in display order)
// over rng.
func buildCompare(agg *scanner.Aggregate, rng string, agents []string) compareView {
	keys := rangeDayKeys(rng)
	v := compareView{DualPct: "0%"}
	totals := make([]sourceTotals, len(agents))
	var names []string
	var sumActive int64
	for i, a := range agents {
		totals[i] = sumSource(agg.SourceDays[a], keys)
		v.Agents = append(v.Agents, cmpAgent{Key: a, Name: config.AgentName(a)})
		names = append(names, config.AgentName(a))
		sumActive += totals[i].ActiveMs
		if totals[i].ActiveMs > 0 || totals[i].Tokens > 0 {
			v.HasData = true
		}
	}
	v.Title = strings.Join(names, " vs ")
	if len(agents) == 1 {
		v.Title = names[0]
	}

	row := func(label string, val func(sourceTotals) float64, txt func(sourceTotals) string) cmpRow {
		vals := make([]float64, len(agents))
		texts := make([]string, len(agents))
		for i, t := range totals {
			vals[i], texts[i] = val(t), txt(t)
		}
		return shareRow(label, agents, vals, texts)
	}
	v.Rows = []cmpRow{
		row("Active time", func(t sourceTotals) float64 { return float64(t.ActiveMs) }, func(t sourceTotals) string { return format.FmtHours(t.ActiveMs) }),
		row("Sessions", func(t sourceTotals) float64 { return float64(t.Sessions) }, func(t sourceTotals) string { return format.FmtNum(int64(t.Sessions)) }),
		row("Prompts", func(t sourceTotals) float64 { return float64(t.Prompts) }, func(t sourceTotals) string { return format.FmtNum(int64(t.Prompts)) }),
		row("Tokens", func(t sourceTotals) float64 { return float64(t.Tokens) }, func(t sourceTotals) string { return format.FmtNum(t.Tokens) }),
		row("API equivalent", func(t sourceTotals) float64 { return t.Cost }, func(t sourceTotals) string { return pricing.FmtCost(t.Cost) }),
		row("Avg session", func(t sourceTotals) float64 { return float64(t.avgSessionMs()) }, func(t sourceTotals) string { return fmtSessionLen(t.avgSessionMs()) }),
	}
	for i, a := range agents {
		v.Tools = append(v.Tools, agentTools{Key: a, Name: config.AgentName(a), Tools: topTools(totals[i].Tools, 5)})
	}

	var dual int64
	if keys == nil {
		dual = agg.DualMs
	} else {
		for k, ms := range agg.DualByDay {
			if keys[k] {
				dual += ms
			}
		}
	}
	v.DualHours = format.FmtHours(dual)
	// Overlap is counted in each agent's time, so compare it to the union.
	if union := sumActive - dual; union > 0 && dual > 0 {
		if pct := float64(dual) / float64(union) * 100; pct < 1 {
			v.DualPct = "<1%"
		} else {
			v.DualPct = fmt.Sprintf("%.0f%%", pct)
		}
	}
	return v
}

// trackedAgents lists the agents enabled in config, in display order.
func trackedAgents(cfg config.Config) []string {
	var out []string
	for _, a := range config.Agents {
		if cfg.Sources.Tracks(a) {
			out = append(out, a)
		}
	}
	return out
}

// shortTool prettifies a tool id and drops MCP server prefixes, so
// "mcp__plugin_x__browser_navigate" / "plugin_x:browser_navigate" read as
// "browser_navigate".
func shortTool(id string) string {
	// HumanTool renders MCP ids as "server:tool"; keep only the tool part.
	name := id
	if !strings.Contains(id, ":") {
		name = format.HumanTool(id)
	}
	if i := strings.LastIndex(name, ":"); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
	}
	if name == "" {
		return id
	}
	return name
}

func fmtSessionLen(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return format.FmtHours(ms)
}

// ── Running sessions ─────────────────────────────────────────────────────────

type runningView struct {
	SourceKey  string // "antigravity" | "claude"
	Source     string
	Project    string
	Path       string
	Status     string
	StatusText string
	Model      string
	Doing      string // current tool · file
	Elapsed    string // since session start
	LastSeen   string
	OnDiscord  bool
	Privacy    string // "" when public, else a short note
}

var runningStatusText = map[string]string{
	"working": "Working", "thinking": "Thinking", "compacting": "Compacting",
	"notification": "Waiting on you", "shipped": "Shipped", "idle": "Idle", "trigger": "Working",
}

// listRunning returns every open (not closed, recently active) session,
// newest activity first, with the one currently on Discord flagged.
func listRunning(cfg config.Config, discordID string) []runningView {
	sessions := state.ListSessions(state.ListOptions{
		IncludeAntigravity: cfg.Sources.WantAntigravity(),
		IncludeClaude:      cfg.Sources.WantClaude(),
		IncludeOpenCode:    cfg.Sources.WantOpenCode(),
		IncludeCodex:       cfg.Sources.WantCodex(),
		IncludeClaudeRPC:   true,
	})
	now := time.Now().UnixMilli()
	idleMs := int64(cfg.IdleThresholdSec) * 1000
	if idleMs <= 0 {
		idleMs = 60_000
	}
	staleMs := int64(cfg.StaleSessionMin) * 60_000
	if staleMs < 60_000 {
		staleMs = 5 * 60_000
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastActivity > sessions[j].LastActivity })

	out := []runningView{}
	for _, s := range sessions {
		if s.SessionID == "" || s.AgentClosed || s.ClaudeClosed || s.LastActivity == 0 || now-s.LastActivity > staleMs {
			continue
		}
		status := s.Status
		if status == "" || status == "stale" || now-s.LastActivity > idleMs {
			status = "idle"
		}
		v := runningView{
			SourceKey: "antigravity", Source: "Antigravity",
			Status: status, StatusText: runningStatusText[status],
			Model:     format.HumanModel(s.Model),
			LastSeen:  fmtAgo(now - s.LastActivity),
			OnDiscord: s.SessionID == discordID,
		}
		if v.StatusText == "" {
			v.StatusText = "Working"
		}
		switch s.Source {
		case "claude":
			v.SourceKey, v.Source = "claude", "Claude Code"
			if s.Model == "" {
				v.Model = "Claude"
			}
		case "opencode":
			v.SourceKey, v.Source = "opencode", "OpenCode"
		case "codex":
			v.SourceKey, v.Source = "codex", "Codex"
		}
		if s.Cwd != "" {
			v.Project, v.Path = filepath.Base(s.Cwd), s.Cwd
		} else {
			v.Project = "Unknown project"
		}
		if status != "idle" {
			tool, file := shortTool(s.CurrentTool), filepath.Base(s.CurrentFile)
			switch {
			case tool != "" && s.CurrentFile != "":
				v.Doing = tool + " · " + file
			case tool != "":
				v.Doing = tool
			}
		}
		if s.SessionStart > 0 && s.SessionStart <= now {
			v.Elapsed = format.FmtDuration(now - s.SessionStart)
		}
		if s.Cwd != "" {
			switch privacy.Resolve(s.Cwd, cfg).Visibility {
			case "hidden":
				v.Privacy = "Hidden on Discord"
			case "name-only":
				v.Privacy = "Name only on Discord"
			}
		}
		out = append(out, v)
	}
	return out
}

func fmtAgo(ms int64) string {
	switch {
	case ms < 5_000:
		return "just now"
	case ms < 60_000:
		return fmt.Sprintf("%ds ago", ms/1000)
	case ms < 3_600_000:
		return fmt.Sprintf("%dm ago", ms/60_000)
	default:
		return fmt.Sprintf("%dh ago", ms/3_600_000)
	}
}
