package presence

import (
	"testing"

	"github.com/meze/agy-rpc/internal/config"
	"github.com/meze/agy-rpc/internal/state"
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
	agy, cc = ComputeActiveSources(nil, nil, &tTrue, &tTrue, cfg, now)
	if !agy || !cc {
		t.Fatalf("expected both active from OS processes, got agy=%v cc=%v", agy, cc)
	}
}

func TestResolveDiscordImageKey(t *testing.T) {
	cfg := config.Default()
	gotAgy := ResolveDiscordImageKey(cfg, "antigravity", "working", "gemini-4-neon-high")
	wantAgy := config.DefaultGifBaseURL + "/agy-working.gif"
	if gotAgy != wantAgy {
		t.Fatalf("expected %q, got %q", wantAgy, gotAgy)
	}

	gotCC := ResolveDiscordImageKey(cfg, "claude", "working", "claude-sonnet-4-6")
	if gotCC != wantAgy {
		t.Fatalf("expected Antigravity GIF fallback for Claude too (%q), got %q", wantAgy, gotCC)
	}
}
