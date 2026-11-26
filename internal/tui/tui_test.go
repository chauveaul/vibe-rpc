package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/pause"
	"github.com/chauveaul/vibe-rpc/internal/privacy"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

func TestTUIViewsAndHotkeys(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_PRIVATE_LIST", filepath.Join(tmp, "private.json"))

	now := time.Now()
	today := scanner.DayKey(now.UnixMilli())
	cfg := config.Default()
	cfg.Goals.DailyHours = 4
	cfg.Budget.Monthly = 100

	m := model{
		width:  92,
		height: 32,
		cfg:    cfg,
		agg: &scanner.Aggregate{
			ActiveMs:      5 * 3_600_000,
			EstimatedCost: 42.5,
			ByDay: map[string]*scanner.DayBucket{
				today: {ActiveMs: 2 * 3_600_000, UserMessages: 14, Cost: 12.5},
			},
			DualByDay: map[string]int64{today: 30 * 60_000},
		},
		st: state.SessionState{
			Source: "antigravity",
			Status: "working",
			Model:  "gemini-4-neon-high",
			Cwd:    filepath.Join(tmp, "proj"),
		},
		ccLimits: &limits.Claude{
			FiveHour: &limits.Window{UsedPct: 60, ResetsAt: now.Unix() + 3600},
			SevenDay: &limits.Window{UsedPct: 22, ResetsAt: now.Unix() + 86400},
		},
		agyLimits: &limits.Antigravity{
			GeminiFiveHour: &limits.Window{UsedPct: 12, ResetsAt: now.Unix() + 7200},
			GeminiSevenDay: &limits.Window{UsedPct: 30, ResetsAt: now.Unix() + 86400},
		},
	}

	// Render every tab without panicking and verify key sections appear.
	for i := range tabs {
		m.tab = i
		out := m.View()
		if !strings.Contains(out, "vibe-rpc") {
			t.Fatalf("tab %d missing header", i)
		}
		if i == 0 && (!strings.Contains(out, "USAGE QUOTAS") || !strings.Contains(out, "Claude 5h") || !strings.Contains(out, "Agy 5h")) {
			t.Fatalf("Now tab missing quotas: %s", out)
		}
		if i == 1 && !strings.Contains(out, "GOALS") {
			t.Fatalf("Today tab missing GOALS: %s", out)
		}
		if i == 5 && !strings.Contains(out, "MONTHLY BUDGET") {
			t.Fatalf("Cost tab missing MONTHLY BUDGET: %s", out)
		}
	}

	// Press 'p' to pause 30m, then 'p' again to resume.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(model)
	if pause.Until("").IsZero() || !strings.Contains(m.View(), "paused until") {
		t.Fatal("p hotkey did not pause presence")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(model)
	if !pause.Until("").IsZero() {
		t.Fatal("second p hotkey did not resume presence")
	}

	// Press 'v' to cycle privacy: public -> name-only -> hidden -> public.
	for _, want := range []string{"name-only", "hidden", "public"} {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
		m = next.(model)
		if got := privacy.Resolve(m.st.Cwd, m.cfg).Visibility; got != want {
			t.Fatalf("privacy after v = %q, want %q", got, want)
		}
	}
}
