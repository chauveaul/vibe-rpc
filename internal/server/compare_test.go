package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

func TestBuildCompareRespectsRange(t *testing.T) {
	now := time.Now()
	today := scanner.DayKeyAt(now, 0)
	old := scanner.DayKeyAt(now, 40)
	h := int64(3_600_000)
	agg := &scanner.Aggregate{
		SourceDays: map[string]map[string]*scanner.SourceDay{
			"antigravity": {
				today: {ActiveMs: 2 * h, Sessions: 2, Prompts: 10, Tokens: 1000, Cost: 1, Tools: map[string]int{"edit_file": 9, "view_file": 3}},
				old:   {ActiveMs: 10 * h, Sessions: 5},
			},
			"claude": {
				today: {ActiveMs: 6 * h, Sessions: 3, Prompts: 30, Tokens: 3000, Cost: 3, Tools: map[string]int{"Bash": 12}},
			},
			"opencode": {
				today: {ActiveMs: 2 * h, Sessions: 1, Prompts: 5, Tokens: 500},
			},
		},
		DualMs:    3 * h,
		DualByDay: map[string]int64{today: 1 * h, old: 2 * h},
	}

	v := buildCompare(agg, "7d", []string{"antigravity", "claude", "opencode"})
	if v.Title != "Antigravity vs Claude Code vs OpenCode" || len(v.Agents) != 3 || len(v.Tools) != 3 {
		t.Fatalf("agents/title = %q %d %d", v.Title, len(v.Agents), len(v.Tools))
	}
	hours := v.Rows[0]
	if hours.Cells[0].Text != "2.0h" || hours.Cells[1].Text != "6.0h" || hours.Cells[2].Text != "2.0h" {
		t.Fatalf("7d hours = %+v", hours.Cells)
	}
	if hours.Cells[1].Pct != 60 || !hours.Cells[1].Leads || hours.Cells[0].Leads {
		t.Fatalf("shares/leader = %+v", hours.Cells)
	}
	if avg := v.Rows[5]; avg.Cells[0].Text != "1.0h" || avg.Cells[1].Text != "2.0h" || avg.Cells[2].Text != "2.0h" {
		t.Fatalf("avg session row = %+v", avg.Cells)
	}
	// Ties for the top lead nobody.
	if avg := v.Rows[5]; avg.Cells[1].Leads || avg.Cells[2].Leads {
		t.Fatalf("tied leaders should not be highlighted: %+v", avg.Cells)
	}
	if v.DualHours != "1.0h" || v.DualPct != "11%" { // 1h of the 9h union (2+6+2-1)
		t.Fatalf("dual = %s / %s", v.DualHours, v.DualPct)
	}
	if v.Tools[0].Tools[0].Pct != 100 || v.Tools[0].Tools[1].Pct != 33 {
		t.Fatalf("agy tools = %+v", v.Tools[0])
	}

	two := buildCompare(agg, "all", []string{"antigravity", "claude"})
	if two.Title != "Antigravity vs Claude Code" || two.Rows[0].Cells[0].Text != "12h" || two.DualHours != "3.0h" {
		t.Fatalf("all-time two-agent = %+v / %s", two.Rows[0], two.DualHours)
	}
	if one := buildCompare(agg, "all", []string{"opencode"}); one.Title != "OpenCode" {
		t.Fatalf("single agent title = %q", one.Title)
	}
	if empty := buildCompare(&scanner.Aggregate{}, "7d", []string{"antigravity", "claude"}); empty.HasData {
		t.Fatal("empty aggregate should report no data")
	}
}

func TestListRunningFiltersAndFlags(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIBE_RPC_STATE_DIR", dir)
	t.Setenv("CLAUDE_RPC_STATE_DIR", filepath.Join(tmp, "none"))
	now := time.Now().UnixMilli()
	write := func(id string, st state.SessionState) {
		b, _ := json.Marshal(st)
		if err := os.WriteFile(filepath.Join(dir, "state-"+id+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("agy1", state.SessionState{Source: "antigravity", Status: "working", Cwd: "/tmp/alpha", CurrentTool: "edit_file", CurrentFile: "/tmp/alpha/main.go", LastActivity: now - 2_000, SessionStart: now - 600_000})
	write("cc1", state.SessionState{Source: "claude", Status: "working", Cwd: "/tmp/beta", LastActivity: now - 120_000, SessionStart: now - 900_000})
	write("old", state.SessionState{Source: "claude", Status: "working", Cwd: "/tmp/gamma", LastActivity: now - 3_600_000})
	write("closed", state.SessionState{Source: "antigravity", Status: "working", Cwd: "/tmp/delta", LastActivity: now, AgentClosed: true})

	cfg := config.Default()
	got := listRunning(cfg, "cc1")
	if len(got) != 2 {
		t.Fatalf("expected 2 running sessions, got %d: %+v", len(got), got)
	}
	if got[0].Project != "alpha" || got[0].Status != "working" || got[0].Doing == "" || got[0].OnDiscord {
		t.Fatalf("first row = %+v", got[0])
	}
	if got[1].Project != "beta" || got[1].Status != "idle" || got[1].Source != "Claude Code" || !got[1].OnDiscord {
		t.Fatalf("second row = %+v", got[1])
	}
}

func TestShortToolAndTinyDualShare(t *testing.T) {
	for in, want := range map[string]string{
		"plugin_playwright_playwright:browser_navigate": "browser_navigate",
		"mcp__plugin_x__browser_click":                  "browser_click",
	} {
		if got := shortTool(in); got != want {
			t.Errorf("shortTool(%q) = %q, want %q", in, got, want)
		}
	}
	h := int64(3_600_000)
	agg := &scanner.Aggregate{
		SourceDays: map[string]map[string]*scanner.SourceDay{
			"antigravity": {scanner.DayKeyAt(time.Now(), 0): {ActiveMs: 50 * h}},
			"claude":      {scanner.DayKeyAt(time.Now(), 0): {ActiveMs: 50 * h}},
		},
		DualMs: 60_000,
	}
	if got := buildCompare(agg, "all", []string{"antigravity", "claude"}).DualPct; got != "<1%" {
		t.Fatalf("tiny dual share = %q, want <1%%", got)
	}
	if got := buildCompare(&scanner.Aggregate{}, "all", []string{"antigravity", "claude"}).DualPct; got != "0%" {
		t.Fatalf("no dual = %q", got)
	}
}
