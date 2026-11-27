package limits

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveFromStatusLine(t *testing.T) {
	t.Setenv("VIBE_RPC_DATA_DIR", t.TempDir())
	t.Setenv("VIBE_RPC_CLAUDE_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	now := time.Unix(1_800_000_000, 0)
	payload := []byte(`{"model":{"id":"x"},"rate_limits":{"five_hour":{"used_percentage":62.5,"resets_at":1800007200},"seven_day":{"used_percentage":31,"resets_at":1800400000}}}`)
	if ok, err := SaveFromStatusLine(payload, now); !ok || err != nil {
		t.Fatalf("save = %v, %v", ok, err)
	}
	c := Load()
	if c == nil || c.FiveHour.UsedPct != 62.5 || c.FiveHour.ResetsAt != 1800007200 || c.SevenDay.UsedPct != 31 || c.UpdatedAt != now.UnixMilli() {
		t.Fatalf("loaded = %+v", c)
	}
	// Payloads without rate_limits (API-key users) leave the saved data alone.
	if ok, _ := SaveFromStatusLine([]byte(`{"model":{"id":"x"}}`), now); ok {
		t.Fatal("payload without rate_limits should not overwrite")
	}
	if ok, _ := SaveFromStatusLine([]byte(`not json`), now); ok {
		t.Fatal("garbage should be ignored")
	}
	if Load().FiveHour.UsedPct != 62.5 {
		t.Fatal("saved limits were clobbered")
	}
	if !c.FiveHour.Expired(time.Unix(1800007200, 0)) || c.FiveHour.Expired(now) {
		t.Fatal("Expired is wrong")
	}
}

func TestSaveFromStatusLineMonotonicAndRollover(t *testing.T) {
	t.Setenv("VIBE_RPC_DATA_DIR", t.TempDir())
	t.Setenv("VIBE_RPC_CLAUDE_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	now := time.Now().UTC().Truncate(time.Second)
	reset5h := now.Add(2 * time.Hour).Unix()
	reset7d := now.Add(48 * time.Hour).Unix()

	// Active session reaches 91% / 43%.
	active := []byte(`{"rate_limits":{"five_hour":{"used_percentage":91,"resets_at":` + itoa(reset5h) + `},"seven_day":{"used_percentage":43,"resets_at":` + itoa(reset7d) + `}}}`)
	if ok, err := SaveFromStatusLine(active, now); !ok || err != nil {
		t.Fatalf("save active = %v, %v", ok, err)
	}

	// An idle session ticking refreshInterval with stale 68% / 39% for the same window must not regress it.
	stale := []byte(`{"rate_limits":{"five_hour":{"used_percentage":68,"resets_at":` + itoa(reset5h+10) + `},"seven_day":{"used_percentage":39,"resets_at":` + itoa(reset7d) + `}}}`)
	if ok, err := SaveFromStatusLine(stale, now.Add(30*time.Second)); ok || err != nil {
		t.Fatalf("stale tick should be ignored, got ok=%v err=%v", ok, err)
	}
	got := Load()
	if got.FiveHour.UsedPct != 91 || got.SevenDay.UsedPct != 43 {
		t.Fatalf("stale tick regressed limits: %+v", got)
	}

	// Partial payload with higher 5h updates 5h and preserves 7d.
	partial := []byte(`{"rate_limits":{"five_hour":{"used_percentage":95,"resets_at":` + itoa(reset5h) + `}}}`)
	if ok, err := SaveFromStatusLine(partial, now.Add(time.Minute)); !ok || err != nil {
		t.Fatalf("partial save = %v, %v", ok, err)
	}
	got = Load()
	if got.FiveHour.UsedPct != 95 || got.SevenDay == nil || got.SevenDay.UsedPct != 43 {
		t.Fatalf("partial merge wrong: %+v", got)
	}

	// New reset window (rollover) drops 5h cleanly back to 2%.
	nextReset5h := now.Add(7 * time.Hour).Unix()
	rolled := []byte(`{"rate_limits":{"five_hour":{"used_percentage":2,"resets_at":` + itoa(nextReset5h) + `},"seven_day":{"used_percentage":44,"resets_at":` + itoa(reset7d) + `}}}`)
	if ok, err := SaveFromStatusLine(rolled, now.Add(2*time.Hour+time.Minute)); !ok || err != nil {
		t.Fatalf("rollover save = %v, %v", ok, err)
	}
	got = Load()
	if got.FiveHour.UsedPct != 2 || got.FiveHour.ResetsAt != nextReset5h || got.SevenDay.UsedPct != 44 {
		t.Fatalf("rollover merge wrong: %+v", got)
	}
}

func TestLoadMergesClaudeConfigCachedUsage(t *testing.T) {
	t.Setenv("VIBE_RPC_DATA_DIR", t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), ".claude.json")
	t.Setenv("VIBE_RPC_CLAUDE_CONFIG", cfgPath)

	now := time.Now().UTC().Truncate(time.Second)
	reset5h := now.Add(2 * time.Hour)
	reset7d := now.Add(72 * time.Hour)

	// Statusline froze at 68% / 39% while a background subagent ran.
	frozen := []byte(`{"rate_limits":{"five_hour":{"used_percentage":68,"resets_at":` + itoa(reset5h.Unix()) + `},"seven_day":{"used_percentage":39,"resets_at":` + itoa(reset7d.Unix()) + `}}}`)
	if ok, err := SaveFromStatusLine(frozen, now.Add(-20*time.Minute)); !ok || err != nil {
		t.Fatalf("save frozen = %v, %v", ok, err)
	}

	// Claude Code wrote fresher 91% / 43% into ~/.claude.json cachedUsageUtilization.
	claudeJSON := `{
		"cachedUsageUtilization": {
			"fetchedAtMs": ` + itoa(now.UnixMilli()) + `,
			"utilization": {
				"five_hour": {"utilization": 91, "resets_at": "` + reset5h.Format(time.RFC3339Nano) + `"},
				"seven_day": {"utilization": 43, "resets_at": "` + reset7d.Format(time.RFC3339Nano) + `"}
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(claudeJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Load()
	if got == nil || got.FiveHour == nil || got.SevenDay == nil {
		t.Fatalf("loaded = %+v", got)
	}
	if got.FiveHour.UsedPct != 91 || got.SevenDay.UsedPct != 43 {
		t.Fatalf("expected merged 91%%/43%%, got 5h=%.1f%% 7d=%.1f%%", got.FiveHour.UsedPct, got.SevenDay.UsedPct)
	}

	// Updating ~/.claude.json on disk invalidates the stat cache and reflects the new values.
	claudeJSON2 := `{
		"cachedUsageUtilization": {
			"fetchedAtMs": ` + itoa(now.Add(time.Minute).UnixMilli()) + `,
			"utilization": {
				"limits": [
					{"kind": "session", "percent": 96, "resets_at": "` + reset5h.Format(time.RFC3339Nano) + `"},
					{"kind": "weekly_all", "percent": 45, "resets_at": "` + reset7d.Format(time.RFC3339Nano) + `"}
				]
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(claudeJSON2), 0o644); err != nil {
		t.Fatal(err)
	}
	got = Load()
	if got.FiveHour.UsedPct != 96 || got.SevenDay.UsedPct != 45 {
		t.Fatalf("expected refreshed 96%%/45%% via limits fallback, got 5h=%.1f%% 7d=%.1f%%", got.FiveHour.UsedPct, got.SevenDay.UsedPct)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestSaveFromAntigravityStatusLine(t *testing.T) {
	t.Setenv("VIBE_RPC_DATA_DIR", t.TempDir())
	t.Setenv("VIBE_RPC_CLAUDE_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	now := time.Date(2026, 11, 26, 22, 6, 0, 0, time.UTC)
	payload := []byte(`{
		"product": "antigravity",
		"plan_tier": "Google AI Pro",
		"model": {"id": "Gemini 4 Neon (High)", "display_name": "Gemini 4 Neon (High)"},
		"quota": {
			"3p-5h": {"remaining_fraction": 0, "reset_time": "2026-11-27T00:39:18Z", "reset_in_seconds": 9177},
			"3p-weekly": {"remaining_fraction": 0.5, "reset_time": "2026-12-03T19:39:18Z", "reset_in_seconds": 595977},
			"gemini-5h": {"remaining_fraction": 0.9485, "reset_time": "2026-11-27T02:53:47Z", "reset_in_seconds": 17246},
			"gemini-weekly": {"remaining_fraction": 0.75, "reset_time": "2026-12-03T16:53:47Z", "reset_in_seconds": 586046}
		}
	}`)
	if ok, err := SaveFromStatusLine(payload, now); !ok || err != nil {
		t.Fatalf("save agy = %v, %v", ok, err)
	}
	a := LoadAntigravity()
	if a == nil || a.PlanTier != "Google AI Pro" || a.ActivePool != "gemini" {
		t.Fatalf("loaded = %+v", a)
	}
	if math.Abs(a.GeminiFiveHour.UsedPct-5.15) > 0.01 || a.GeminiFiveHour.ResetsAt != time.Date(2026, 11, 27, 2, 53, 47, 0, time.UTC).Unix() {
		t.Fatalf("gemini 5h = %+v", a.GeminiFiveHour)
	}
	if a.GeminiSevenDay.UsedPct != 25 || a.ThirdPartyFiveHour.UsedPct != 100 || a.ThirdPartySevenDay.UsedPct != 50 {
		t.Fatalf("windows = %+v", a)
	}
	// Pool selection switches automatically with the active model.
	if pool, s, w := a.PickPool("claude-opus-4-6"); pool != "3p" || s.UsedPct != 100 || w.UsedPct != 50 {
		t.Fatalf("3p pool = %s %+v %+v", pool, s, w)
	}
	if pool, s, w := a.PickPool("gemini-4-neon-high"); pool != "gemini" || math.Abs(s.UsedPct-5.15) > 0.01 || w.UsedPct != 25 {
		t.Fatalf("gemini pool = %s %+v %+v", pool, s, w)
	}
	// Claude limits file must stay untouched by Antigravity payloads.
	if Load() != nil {
		t.Fatal("antigravity payload must not write claude-limits.json")
	}
}
