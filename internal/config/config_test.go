package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveDiffOnlyWritesOverrides(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.json")

	base := Default()
	edited := Default()
	edited.AppName = "My Agy"
	edited.IdleThresholdSec = 90

	loaded, err := SaveDiff(cfgPath, base, edited)
	if err != nil {
		t.Fatalf("SaveDiff failed: %v", err)
	}
	if loaded.AppName != "My Agy" || loaded.IdleThresholdSec != 90 {
		t.Fatalf("unexpected reloaded config: appName=%q idle=%d", loaded.AppName, loaded.IdleThresholdSec)
	}
	raw, _ := os.ReadFile(cfgPath)
	if len(raw) > 200 {
		t.Fatalf("expected sparse diff file, got %d bytes: %s", len(raw), string(raw))
	}

	// Reject malformed client ID
	bad := edited
	bad.ClientID = "not-a-snowflake"
	if _, err := SaveDiff(cfgPath, loaded, bad); err == nil {
		t.Fatalf("expected validation error for bad clientId")
	}
}

func TestShowOnDiscordNeedsTrackingAndPermission(t *testing.T) {
	c := Default()
	for _, a := range Agents {
		if !c.ShowOnDiscord(a) || !c.Sources.Tracks(a) {
			t.Fatalf("%s should be tracked and shown by default", a)
		}
	}
	c.Sources.OpenCode = BoolPtr(false)
	c.DiscordSources.Claude = BoolPtr(false)
	if c.ShowOnDiscord("opencode") || c.Sources.Tracks("opencode") {
		t.Fatal("untracked agents never show on Discord")
	}
	if c.ShowOnDiscord("claude") || !c.Sources.Tracks("claude") {
		t.Fatal("hidden-from-Discord agents stay tracked")
	}
	if !c.ShowOnDiscord("antigravity") {
		t.Fatal("antigravity unaffected")
	}
}

func TestJoinAgentNames(t *testing.T) {
	cases := []struct {
		keys []string
		conj string
		want string
	}{
		{nil, "or", ""},
		{[]string{"codex"}, "or", "Codex"},
		{[]string{"antigravity", "claude"}, "or", "Antigravity or Claude Code"},
		{[]string{"antigravity", "claude", "opencode", "codex"}, "and", "Antigravity, Claude Code, OpenCode and Codex"},
	}
	for _, c := range cases {
		if got := JoinAgentNames(c.keys, c.conj); got != c.want {
			t.Errorf("JoinAgentNames(%v, %q) = %q, want %q", c.keys, c.conj, got, c.want)
		}
	}
}
