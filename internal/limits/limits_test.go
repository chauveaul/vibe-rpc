package limits

import (
	"math"
	"testing"
	"time"
)

func TestSaveFromStatusLine(t *testing.T) {
	t.Setenv("VIBE_RPC_DATA_DIR", t.TempDir())
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

func TestSaveFromAntigravityStatusLine(t *testing.T) {
	t.Setenv("VIBE_RPC_DATA_DIR", t.TempDir())
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
