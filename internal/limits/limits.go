// Package limits records Claude Code and Antigravity CLI subscription rate limits.
//
// Both CLIs hand live quota telemetry to their status line command on stdin;
// `vibe-rpc statusline` captures it here so VibeNotch and the dashboard can
// show real 5-hour session and 7-day weekly usage bars with reset times.
//
// Claude Code (https://code.claude.com/docs/en/statusline.md):
//
//	rate_limits.five_hour.{used_percentage,resets_at}
//	rate_limits.seven_day.{used_percentage,resets_at}
//
// Antigravity CLI (`product: "antigravity"`):
//
//	quota.{gemini-5h,gemini-weekly,3p-5h,3p-weekly}.{remaining_fraction,reset_time,reset_in_seconds}
package limits

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/paths"
)

type Window struct {
	UsedPct  float64 `json:"usedPct"`
	ResetsAt int64   `json:"resetsAt"` // unix seconds
}

// Expired reports whether the window has already reset.
func (w *Window) Expired(now time.Time) bool {
	return w == nil || (w.ResetsAt > 0 && now.Unix() >= w.ResetsAt)
}

type Claude struct {
	FiveHour  *Window `json:"fiveHour,omitempty"`
	SevenDay  *Window `json:"sevenDay,omitempty"`
	UpdatedAt int64   `json:"updatedAt"` // unix ms
}

type Antigravity struct {
	GeminiFiveHour     *Window `json:"geminiFiveHour,omitempty"`
	GeminiSevenDay     *Window `json:"geminiSevenDay,omitempty"`
	ThirdPartyFiveHour *Window `json:"thirdPartyFiveHour,omitempty"`
	ThirdPartySevenDay *Window `json:"thirdPartySevenDay,omitempty"`
	PlanTier           string  `json:"planTier,omitempty"`
	ActivePool         string  `json:"activePool,omitempty"` // "gemini" or "3p"
	UpdatedAt          int64   `json:"updatedAt"`            // unix ms
}

func Path() string    { return filepath.Join(paths.DataDir(), "claude-limits.json") }
func AgyPath() string { return filepath.Join(paths.DataDir(), "agy-limits.json") }

// PoolForModel maps a model name/ID to Antigravity's quota pool ("gemini" or "3p").
func PoolForModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return "gemini"
	}
	for _, tok := range []string{"claude", "opus", "sonnet", "haiku", "anthropic", "gpt", "openai", "o1", "o3", "o4", "3p"} {
		if strings.Contains(m, tok) {
			return "3p"
		}
	}
	return "gemini"
}

// PickPool selects the 5h and 7d windows matching model (or the last active
// pool recorded from statusline), falling back to the other pool if empty.
func (a *Antigravity) PickPool(model string) (string, *Window, *Window) {
	if a == nil {
		return "", nil, nil
	}
	pool := a.ActivePool
	if strings.TrimSpace(model) != "" {
		pool = PoolForModel(model)
	}
	if pool != "3p" {
		pool = "gemini"
	}
	if pool == "3p" {
		if a.ThirdPartyFiveHour != nil || a.ThirdPartySevenDay != nil {
			return "3p", a.ThirdPartyFiveHour, a.ThirdPartySevenDay
		}
		if a.GeminiFiveHour != nil || a.GeminiSevenDay != nil {
			return "gemini", a.GeminiFiveHour, a.GeminiSevenDay
		}
		return "3p", nil, nil
	}
	if a.GeminiFiveHour != nil || a.GeminiSevenDay != nil {
		return "gemini", a.GeminiFiveHour, a.GeminiSevenDay
	}
	if a.ThirdPartyFiveHour != nil || a.ThirdPartySevenDay != nil {
		return "3p", a.ThirdPartyFiveHour, a.ThirdPartySevenDay
	}
	return "gemini", nil, nil
}

// statusInput covers the overlapping slices of Claude Code's and Antigravity CLI's
// status line JSON payloads.
type statusInput struct {
	Product    string `json:"product"`
	PlanTier   string `json:"plan_tier"`
	Model      any    `json:"model"`
	RateLimits *struct {
		FiveHour *rawWindow `json:"five_hour"`
		SevenDay *rawWindow `json:"seven_day"`
	} `json:"rate_limits"`
	Quota map[string]*rawAgyWindow `json:"quota"`
}

type rawWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       int64    `json:"resets_at"`
}

func (r *rawWindow) toWindow() *Window {
	if r == nil || r.UsedPercentage == nil {
		return nil
	}
	return &Window{UsedPct: *r.UsedPercentage, ResetsAt: r.ResetsAt}
}

type rawAgyWindow struct {
	RemainingFraction *float64 `json:"remaining_fraction"`
	ResetTime         string   `json:"reset_time"`
	ResetInSeconds    int64    `json:"reset_in_seconds"`
}

func (r *rawAgyWindow) toWindow(now time.Time) *Window {
	if r == nil || r.RemainingFraction == nil {
		return nil
	}
	rem := math.Max(0, math.Min(1, *r.RemainingFraction))
	usedPct := (1 - rem) * 100
	var resetsAt int64
	if r.ResetTime != "" {
		if t, err := time.Parse(time.RFC3339Nano, r.ResetTime); err == nil {
			resetsAt = t.Unix()
		} else if t, err := time.Parse(time.RFC3339, r.ResetTime); err == nil {
			resetsAt = t.Unix()
		}
	}
	if resetsAt == 0 && r.ResetInSeconds > 0 {
		resetsAt = now.Unix() + r.ResetInSeconds
	}
	return &Window{UsedPct: usedPct, ResetsAt: resetsAt}
}

func extractModelString(raw any) string {
	switch m := raw.(type) {
	case string:
		return m
	case map[string]any:
		if id, ok := m["id"].(string); ok && id != "" {
			return id
		}
		if dn, ok := m["display_name"].(string); ok && dn != "" {
			return dn
		}
	}
	return ""
}

// SaveFromStatusLine parses a status line payload from either Antigravity CLI
// or Claude Code and stores its rate limits. Payloads without quota/rate_limits
// leave saved files untouched. Returns whether anything was written.
func SaveFromStatusLine(payload []byte, now time.Time) (bool, error) {
	var in statusInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return false, nil
	}
	if len(in.Quota) > 0 {
		a := Antigravity{
			GeminiFiveHour:     in.Quota["gemini-5h"].toWindow(now),
			GeminiSevenDay:     in.Quota["gemini-weekly"].toWindow(now),
			ThirdPartyFiveHour: in.Quota["3p-5h"].toWindow(now),
			ThirdPartySevenDay: in.Quota["3p-weekly"].toWindow(now),
			PlanTier:           strings.TrimSpace(in.PlanTier),
			ActivePool:         PoolForModel(extractModelString(in.Model)),
			UpdatedAt:          now.UnixMilli(),
		}
		if a.GeminiFiveHour == nil && a.GeminiSevenDay == nil &&
			a.ThirdPartyFiveHour == nil && a.ThirdPartySevenDay == nil {
			return false, nil
		}
		b, err := json.Marshal(a)
		if err != nil {
			return false, err
		}
		if err := os.MkdirAll(filepath.Dir(AgyPath()), 0o755); err != nil {
			return false, err
		}
		return true, paths.WriteFileAtomic(AgyPath(), b, 0o644)
	}
	if in.RateLimits == nil {
		return false, nil
	}
	c := Claude{
		FiveHour:  in.RateLimits.FiveHour.toWindow(),
		SevenDay:  in.RateLimits.SevenDay.toWindow(),
		UpdatedAt: now.UnixMilli(),
	}
	if c.FiveHour == nil && c.SevenDay == nil {
		return false, nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(Path(), b, 0o644)
}

// Load returns the last saved Claude Code limits, or nil if none were recorded.
func Load() *Claude {
	b, err := os.ReadFile(Path())
	if err != nil {
		return nil
	}
	var c Claude
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	return &c
}

// LoadAntigravity returns the last saved Antigravity CLI limits, or nil if none were recorded.
func LoadAntigravity() *Antigravity {
	b, err := os.ReadFile(AgyPath())
	if err != nil {
		return nil
	}
	var a Antigravity
	if json.Unmarshal(b, &a) != nil {
		return nil
	}
	return &a
}
