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

// ── Antigravity vs Claude Code ───────────────────────────────────────────────

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

// cmpRow is one metric compared across both agents. AgyPct is Antigravity's
// share of the pair (0–100), drawn as a two-sided bar.
type cmpRow struct {
	Label     string
	Agy       string
	Claude    string
	AgyPct    int
	ClaudePct int
	AgyLeads  bool
	CCLeads   bool
}

type toolRow struct {
	Name  string
	Count string
	Pct   int // relative to that agent's top tool
}

type compareView struct {
	Rows        []cmpRow
	AgyTools    []toolRow
	ClaudeTools []toolRow
	DualHours   string
	DualPct     string // share of combined active time
	HasData     bool
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

func shareRow(label string, a, c float64, fa, fc string) cmpRow {
	r := cmpRow{Label: label, Agy: fa, Claude: fc, AgyPct: 50, ClaudePct: 50}
	if a+c > 0 {
		r.AgyPct = int(math.Round(a / (a + c) * 100))
		r.ClaudePct = 100 - r.AgyPct
	}
	r.AgyLeads, r.CCLeads = a > c, c > a
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

func buildCompare(agg *scanner.Aggregate, rng string) compareView {
	keys := rangeDayKeys(rng)
	agy := sumSource(agg.SourceDays["antigravity"], keys)
	cc := sumSource(agg.SourceDays["claude"], keys)

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

	v := compareView{
		HasData: agy.ActiveMs+cc.ActiveMs > 0 || agy.Tokens+cc.Tokens > 0,
		Rows: []cmpRow{
			shareRow("Active time", float64(agy.ActiveMs), float64(cc.ActiveMs), format.FmtHours(agy.ActiveMs), format.FmtHours(cc.ActiveMs)),
			shareRow("Sessions", float64(agy.Sessions), float64(cc.Sessions), format.FmtNum(int64(agy.Sessions)), format.FmtNum(int64(cc.Sessions))),
			shareRow("Prompts", float64(agy.Prompts), float64(cc.Prompts), format.FmtNum(int64(agy.Prompts)), format.FmtNum(int64(cc.Prompts))),
			shareRow("Tokens", float64(agy.Tokens), float64(cc.Tokens), format.FmtNum(agy.Tokens), format.FmtNum(cc.Tokens)),
			shareRow("API equivalent", agy.Cost, cc.Cost, pricing.FmtCost(agy.Cost), pricing.FmtCost(cc.Cost)),
			shareRow("Avg session", float64(agy.avgSessionMs()), float64(cc.avgSessionMs()), fmtSessionLen(agy.avgSessionMs()), fmtSessionLen(cc.avgSessionMs())),
		},
		AgyTools:    topTools(agy.Tools, 5),
		ClaudeTools: topTools(cc.Tools, 5),
		DualHours:   format.FmtHours(dual),
		DualPct:     "0%",
	}
	// Overlap counts once in each agent's time, so compare it to the union.
	if union := agy.ActiveMs + cc.ActiveMs - dual; union > 0 && dual > 0 {
		if pct := float64(dual) / float64(union) * 100; pct < 1 {
			v.DualPct = "<1%"
		} else {
			v.DualPct = fmt.Sprintf("%.0f%%", pct)
		}
	}
	return v
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
		if s.Source == "claude" {
			v.SourceKey, v.Source = "claude", "Claude Code"
			if s.Model == "" {
				v.Model = "Claude"
			}
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
