package presence

import (
	"testing"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

func TestResolveAppName(t *testing.T) {
	cfg := config.Default()

	// Only Antigravity active
	agyOnly := state.SessionState{Source: "antigravity", Status: "working", AntigravityActive: true}
	if got := ResolveAppName(agyOnly, cfg); got != "Antigravity" {
		t.Fatalf("expected Antigravity, got %q", got)
	}

	// Only Claude Code active
	ccOnly := state.SessionState{Source: "claude", Status: "working", ClaudeActive: true}
	if got := ResolveAppName(ccOnly, cfg); got != "Claude Code" {
		t.Fatalf("expected Claude Code, got %q", got)
	}

	// Both Antigravity & Claude Code active
	both := state.SessionState{Source: "antigravity", Status: "working", AntigravityActive: true, ClaudeActive: true}
	if got := ResolveAppName(both, cfg); got != "Antigravity & Claude" {
		t.Fatalf("expected Antigravity & Claude, got %q", got)
	}

	// Dynamic app name disabled -> always uses cfg.AppName
	cfgStatic := config.Default()
	cfgStatic.AppName = "Custom Name"
	cfgStatic.DynamicAppName = config.BoolPtr(false)
	if got := ResolveAppName(both, cfgStatic); got != "Custom Name" {
		t.Fatalf("expected Custom Name when dynamicAppName=false, got %q", got)
	}
}

func TestComputeActiveSources(t *testing.T) {
	cfg := config.Default()
	now := int64(1_700_000_000_000)

	sessions := []state.SessionState{
		{Source: "antigravity", Status: "working", LastActivity: now - 5_000},
		{Source: "claude", Status: "thinking", LastActivity: now - 10_000},
	}
	agy, cc := ComputeActiveSources(sessions, nil, nil, nil, cfg, now)
	if !agy || !cc {
		t.Fatalf("expected both active, got agy=%v cc=%v", agy, cc)
	}

	// Closed Claude session should not count
	sessions[1].ClaudeClosed = true
	agy, cc = ComputeActiveSources(sessions, nil, nil, nil, cfg, now)
	if !agy || cc {
		t.Fatalf("expected only agy active, got agy=%v cc=%v", agy, cc)
	}

	// Running OS processes should also trigger both
	tTrue := true
	tFalse := false
	agy, cc = ComputeActiveSources(nil, nil, &tTrue, &tTrue, cfg, now)
	if !agy || !cc {
		t.Fatalf("expected both active from OS processes, got agy=%v cc=%v", agy, cc)
	}

	// Once Claude OS process exits (claudeProc=false), lingering recent sessions/transcripts must NOT keep Claude active.
	sessions[1].ClaudeClosed = false
	live := []state.LiveSession{
		{Source: "antigravity", Mtime: now - 2_000},
		{Source: "claude", Mtime: now - 3_000},
	}
	agy, cc = ComputeActiveSources(sessions, live, &tTrue, &tFalse, cfg, now)
	if !agy || cc {
		t.Fatalf("expected claude=false when claudeProc=false despite recent session/transcript, got agy=%v cc=%v", agy, cc)
	}
	st := state.SessionState{
		Source:            "antigravity",
		Status:            "working",
		LiveSessions:      live,
		AntigravityActive: agy,
		ClaudeActive:      cc,
		SourcesComputed:   true,
	}
	if got := ResolveAppName(st, cfg); got != "Antigravity" {
		t.Fatalf("expected Antigravity after Claude closes, got %q", got)
	}
}

func TestResolveDiscordImageKey(t *testing.T) {
	cfg := config.Default()
	gotAgy := ResolveDiscordImageKey(cfg, "antigravity", "working", "gemini-4-neon-high")
	wantAgy := config.DefaultGifBaseURL + "/agy-working.gif?v=" + config.GifAssetVersion
	if gotAgy != wantAgy {
		t.Fatalf("expected %q, got %q", wantAgy, gotAgy)
	}

	gotCC := ResolveDiscordImageKey(cfg, "claude", "working", "claude-sonnet-4-6")
	wantCC := config.DefaultGifBaseURL + "/claude-working.gif?v=" + config.GifAssetVersion
	if gotCC != wantCC {
		t.Fatalf("expected custom Claude GIF (%q), got %q", wantCC, gotCC)
	}

	gotDual := ResolveDiscordImageKey(cfg, "antigravity", "dual", "gemini-4-neon-high")
	wantDual := config.DefaultGifBaseURL + "/agy-dual.gif?v=" + config.GifAssetVersion
	if gotDual != wantDual {
		t.Fatalf("expected Dual Wielding GIF (%q), got %q", wantDual, gotDual)
	}
}

func TestDualWieldingDetection(t *testing.T) {
	cfg := config.Default()
	now := int64(1_700_000_000_000)
	sessions := []state.SessionState{
		{Source: "antigravity", Status: "working", Cwd: "/tmp/vibe-rpc", Model: "gemini-4-neon-high", CurrentFile: "server.go", LastActivity: now - 2_000},
		{Source: "claude", Status: "working", Cwd: "/tmp/web-app", Model: "claude-opus-4-6", CurrentFile: "app.go", LastActivity: now - 3_000},
	}
	agySnap, ccSnap := PickDualSnapshots(sessions, nil, cfg, now)
	if agySnap == nil || ccSnap == nil {
		t.Fatalf("expected both snapshots, got agy=%v cc=%v", agySnap, ccSnap)
	}
	if agySnap.CurrentFile != "server.go" || ccSnap.CurrentFile != "app.go" {
		t.Fatalf("unexpected files: agy=%q cc=%q", agySnap.CurrentFile, ccSnap.CurrentFile)
	}
	st := state.SessionState{
		Source:            "antigravity",
		Status:            "working",
		AntigravityActive: true,
		ClaudeActive:      true,
		AgySession:        agySnap,
		ClaudeSession:     ccSnap,
	}
	if !IsDualWorking(st) {
		t.Fatal("expected IsDualWorking=true when both agents are active & working")
	}
}

func TestDiscordFilters(t *testing.T) {
	cfg := config.Default()
	cfg.DiscordSources.Claude = config.BoolPtr(false)
	o := DiscordListOptions(cfg)
	if !o.IncludeAntigravity || o.IncludeClaude || !o.IncludeOpenCode {
		t.Fatalf("options = %+v", o)
	}
	live := []state.LiveSession{{Source: "claude"}, {Source: "antigravity"}, {Source: "opencode"}}
	got := FilterLiveForDiscord(live, cfg)
	if len(got) != 2 || got[0].Source != "antigravity" || got[1].Source != "opencode" {
		t.Fatalf("filtered = %+v", got)
	}
}
