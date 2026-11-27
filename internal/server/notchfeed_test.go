package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

func TestBuildNotchFeedShape(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("CLAUDE_RPC_STATE_DIR", filepath.Join(tmp, "none"))

	f := BuildNotchFeed()
	if f.Running == nil {
		t.Fatal("running must encode as [] not null for the Swift decoder")
	}
	if f.AppName == "" || f.Details == "" {
		t.Fatalf("card text missing: %+v", f)
	}
	if f.GifPath != "" {
		if _, err := os.Stat(f.GifPath); err != nil {
			t.Fatalf("gifPath %q doesn't exist: %v", f.GifPath, err)
		}
	}
	b, _ := json.Marshal(f)
	for _, key := range []string{`"status"`, `"active"`, `"today"`, `"running":[]`, `"paused"`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("feed JSON missing %s: %s", key, b)
		}
	}
}

func TestNotchTodayGoal(t *testing.T) {
	day := scanner.DayKey(time.Now().UnixMilli())
	agg := &scanner.Aggregate{Streak: 3, ByDay: map[string]*scanner.DayBucket{day: {ActiveMs: 2 * 3_600_000, UserMessages: 12}}}
	td := notchTodayFrom(agg, 4, map[string]any{"goalLabel": "2.0h / 4h · 50%"})
	if td.Hours != "2.0h" || td.Prompts != 12 || td.GoalPct != 0.5 || td.Streak != 3 || td.GoalLabel == "" {
		t.Fatalf("today = %+v", td)
	}
	if over := notchTodayFrom(agg, 1, nil); over.GoalPct != 1 {
		t.Fatalf("goal pct should cap at 1, got %v", over.GoalPct)
	}
}

func TestIsRunningAt(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	ours := httptest.NewServer(NewHandler())
	defer ours.Close()
	if !IsRunningAt(strings.TrimPrefix(ours.URL, "http://")) {
		t.Fatal("should recognise a vibe-rpc dashboard")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer other.Close()
	if IsRunningAt(strings.TrimPrefix(other.URL, "http://")) {
		t.Fatal("must not mistake another server for vibe-rpc")
	}
	if IsRunningAt("127.0.0.1:1") {
		t.Fatal("nothing listening should be false")
	}
}

func TestBuildUsage(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	nowMs := now.UnixMilli()
	h := int64(3_600_000)
	agg := &scanner.Aggregate{RecentSpans: map[string][][2]int64{
		"antigravity": {{nowMs - 7*h, nowMs - 4*h}, {nowMs - 30*60_000, nowMs}}, // 1h + 0.5h inside the 5h window
	}}
	u := buildUsage(agg, nil, "none", nil, "none", "", now)
	if u.Claude.Source != "setup" || u.Antigravity.Source != "setup" || u.Antigravity.WindowActiveMs != 3*h/2 || u.Antigravity.Pct != 0.3 {
		t.Fatalf("usage = %+v", u)
	}
	if buildUsage(nil, nil, "other", nil, "other", "", now).Claude.Source != "other" ||
		buildUsage(nil, nil, "vibe-rpc", nil, "vibe-rpc", "", now).Claude.Source != "waiting" ||
		buildUsage(nil, nil, "none", nil, "vibe-rpc", "", now).Antigravity.Source != "waiting" {
		t.Fatal("statusline source states")
	}
	lim := &limits.Claude{
		FiveHour:  &limits.Window{UsedPct: 62, ResetsAt: now.Unix() + 3600},
		SevenDay:  &limits.Window{UsedPct: 31, ResetsAt: now.Unix() - 10}, // already reset
		UpdatedAt: nowMs,
	}
	agyLim := &limits.Antigravity{
		GeminiFiveHour:     &limits.Window{UsedPct: 5, ResetsAt: now.Unix() + 7200},
		GeminiSevenDay:     &limits.Window{UsedPct: 26, ResetsAt: now.Unix() + 86400},
		ThirdPartyFiveHour: &limits.Window{UsedPct: 100, ResetsAt: now.Unix() + 1800},
		ThirdPartySevenDay: &limits.Window{UsedPct: 50, ResetsAt: now.Unix() - 5}, // already reset
		ActivePool:         "gemini",
		UpdatedAt:          nowMs,
	}
	u = buildUsage(agg, lim, "vibe-rpc", agyLim, "vibe-rpc", "gemini-4-neon-high", now)
	if u.Claude.Source != "statusline" || u.Claude.Session.Pct != 0.62 || u.Claude.Session.ResetsAt != (now.Unix()+3600)*1000 {
		t.Fatalf("session = %+v", u.Claude.Session)
	}
	if u.Claude.Week.Pct != 0 {
		t.Fatalf("an expired window should read as fresh, got %+v", u.Claude.Week)
	}
	if u.Antigravity.Source != "statusline" || u.Antigravity.Pool != "gemini" || u.Antigravity.Session.Pct != 0.05 || u.Antigravity.Week.Pct != 0.26 {
		t.Fatalf("agy gemini = %+v", u.Antigravity)
	}
	// Switching to a 3P model inside Antigravity flips to the 3p pool.
	u3p := buildUsage(agg, lim, "vibe-rpc", agyLim, "vibe-rpc", "claude-opus-4-6", now)
	if u3p.Antigravity.Pool != "3p" || u3p.Antigravity.Session.Pct != 1 || u3p.Antigravity.Week.Pct != 0 {
		t.Fatalf("agy 3p = %+v", u3p.Antigravity)
	}
}

func TestSettingsNotchToggle(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	var calls []bool
	orig := applyNotchToggle
	applyNotchToggle = func(on bool) error { calls = append(calls, on); return nil }
	defer func() { applyNotchToggle = orig }()
	h := NewHandler()

	form := baseSettingsForm()
	form.Set("notch_section", "1")
	form.Set("notchEnabled", "on")
	postSettings(t, h, "/settings/save", form)
	if !config.Load().Notch.Enabled || len(calls) != 1 || !calls[0] {
		t.Fatalf("enable: cfg=%v calls=%v", config.Load().Notch.Enabled, calls)
	}
	postSettings(t, h, "/settings/save", form) // unchanged: no launch/quit
	if len(calls) != 1 {
		t.Fatalf("unchanged save should not toggle, calls=%v", calls)
	}
	form.Del("notchEnabled")
	postSettings(t, h, "/settings/save", form)
	if config.Load().Notch.Enabled || len(calls) != 2 || calls[1] {
		t.Fatalf("disable: calls=%v", calls)
	}
	// Forms without the section never touch the setting.
	postSettings(t, h, "/settings/save", baseSettingsForm())
	if len(calls) != 2 {
		t.Fatal("save without notch_section toggled the notch")
	}

	// The "finished" banner checkbox saves both ways.
	form.Set("notchFinishAlerts", "on")
	postSettings(t, h, "/settings/save", form)
	if !config.Load().Notch.WantsFinishAlerts() {
		t.Fatal("finish banner should be on after saving it checked")
	}
	form.Del("notchFinishAlerts")
	postSettings(t, h, "/settings/save", form)
	if config.Load().Notch.WantsFinishAlerts() {
		t.Fatal("finish banner should be off after saving it unchecked")
	}
}

func TestListAttention(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "state")
	_ = os.MkdirAll(dir, 0o755)
	t.Setenv("VIBE_RPC_STATE_DIR", dir)
	t.Setenv("CLAUDE_RPC_STATE_DIR", filepath.Join(tmp, "none"))
	now := time.Now()
	ms := now.UnixMilli()
	write := func(id string, st state.SessionState) {
		b, _ := json.Marshal(st)
		_ = os.WriteFile(filepath.Join(dir, "state-"+id+".json"), b, 0o644)
	}
	write("cc", state.SessionState{Source: "claude", Status: "notification", Cwd: "/w/api", AttentionKind: "permission", AttentionText: "Claude needs your permission to use Bash", LastNotification: ms - 5_000, LastActivity: ms - 5_000, HostApp: "/Applications/iTerm.app", HostAppPID: 42})
	write("agy", state.SessionState{Source: "antigravity", Status: "notification", Cwd: "/w/web", AttentionKind: "question", AttentionText: "Deploy?", LastNotification: ms - 1_000, LastActivity: ms - 1_000})
	write("idle", state.SessionState{Source: "claude", Status: "notification", AttentionKind: "idle", LastNotification: ms})
	write("old", state.SessionState{Source: "claude", Status: "notification", AttentionKind: "permission", LastNotification: ms - 2*3_600_000})
	write("busy", state.SessionState{Source: "claude", Status: "working", AttentionKind: "permission", LastNotification: ms})
	write("gone", state.SessionState{Source: "claude", Status: "notification", AttentionKind: "question", LastNotification: ms, ClaudeClosed: true})

	got := listAttention(config.Default(), now)
	if len(got) != 2 || got[0].Source != "antigravity" || got[1].Source != "claude" {
		t.Fatalf("attention = %+v", got)
	}
	if got[1].Project != "api" || got[1].HostAppPID != 42 || got[1].Kind != "permission" || got[1].ID == "" {
		t.Fatalf("claude alert = %+v", got[1])
	}
}

func TestListFinished(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "state")
	_ = os.MkdirAll(dir, 0o755)
	t.Setenv("VIBE_RPC_STATE_DIR", dir)
	t.Setenv("CLAUDE_RPC_STATE_DIR", filepath.Join(tmp, "none"))
	now := time.Now()
	ms := now.UnixMilli()
	write := func(id string, st state.SessionState) {
		st.SessionID = id
		b, _ := json.Marshal(st)
		_ = os.WriteFile(filepath.Join(dir, "state-"+id+".json"), b, 0o644)
	}
	write("cc", state.SessionState{Source: "claude", Status: "idle", Cwd: "/w/api", LastStop: ms - 3_000, LastTurnMs: 95_000, LastActivity: ms - 3_000, HostAppPID: 42})
	write("cx", state.SessionState{Source: "codex", Status: "idle", Cwd: "/w/web", LastStop: ms - 1_000, LastTurnMs: 0, LastActivity: ms - 1_000})
	write("quick", state.SessionState{Source: "claude", Status: "idle", LastStop: ms - 1_000, LastTurnMs: 4_000, LastActivity: ms - 1_000})
	write("old", state.SessionState{Source: "claude", Status: "idle", LastStop: ms - 5*60_000, LastTurnMs: 60_000, LastActivity: ms - 5*60_000})
	write("again", state.SessionState{Source: "claude", Status: "working", LastStop: ms - 2_000, LastTurnMs: 60_000, LastActivity: ms})

	got := listFinished(config.Default(), now)
	if len(got) != 2 || got[0].Source != "codex" || got[1].Source != "claude" {
		t.Fatalf("finished = %+v", got)
	}
	if got[1].Project != "api" || got[1].TurnMs != 95_000 || got[1].HostAppPID != 42 || got[1].ID == "" {
		t.Fatalf("claude finish = %+v", got[1])
	}

	off := false
	cfg := config.Default()
	cfg.Notch.FinishAlerts = &off
	if cfg.Notch.WantsFinishAlerts() {
		t.Fatal("FinishAlerts=false should turn the banner off")
	}
	if !config.Default().Notch.WantsFinishAlerts() {
		t.Fatal("finish banner should default to on")
	}
}
