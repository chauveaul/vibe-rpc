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
	"sync"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/paths"
)

type Window struct {
	UsedPct      float64 `json:"usedPct"`
	ResetsAt     int64   `json:"resetsAt"`               // unix seconds
	PrevResetsAt int64   `json:"prevResetsAt,omitempty"` // unix seconds of previous non-zero window on rollover
	PrevUsedPct  float64 `json:"prevUsedPct,omitempty"`  // usedPct before that rollover
}

// Expired reports whether the window has already reset.
func (w *Window) Expired(now time.Time) bool {
	return w == nil || (w.ResetsAt > 0 && now.Unix() >= w.ResetsAt)
}

// ResetEvent reports whether this window reset within the last maxAge after
// non-zero usage — either because ResetsAt has passed or because a rollover
// was recorded in PrevResetsAt. Returns the reset unix timestamp (seconds)
// and the usage percentage prior to the reset.
func (w *Window) ResetEvent(now time.Time, maxAge time.Duration) (int64, float64, bool) {
	if w == nil {
		return 0, 0, false
	}
	maxSec := int64(maxAge.Seconds())
	if maxSec <= 0 {
		maxSec = 120
	}
	nowSec := now.Unix()
	if w.UsedPct > 0 && w.ResetsAt > 0 && nowSec >= w.ResetsAt && nowSec-w.ResetsAt <= maxSec {
		return w.ResetsAt, w.UsedPct, true
	}
	if w.PrevUsedPct > 0 && w.PrevResetsAt > 0 && nowSec >= w.PrevResetsAt-sameWindowSlackSec && nowSec-w.PrevResetsAt <= maxSec {
		return w.PrevResetsAt, w.PrevUsedPct, true
	}
	return 0, 0, false
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

func parseResetTime(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return 0
}

func (r *rawAgyWindow) toWindow(now time.Time) *Window {
	if r == nil || r.RemainingFraction == nil {
		return nil
	}
	rem := math.Max(0, math.Min(1, *r.RemainingFraction))
	usedPct := (1 - rem) * 100
	resetsAt := parseResetTime(r.ResetTime)
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
		incoming := &Antigravity{
			GeminiFiveHour:     in.Quota["gemini-5h"].toWindow(now),
			GeminiSevenDay:     in.Quota["gemini-weekly"].toWindow(now),
			ThirdPartyFiveHour: in.Quota["3p-5h"].toWindow(now),
			ThirdPartySevenDay: in.Quota["3p-weekly"].toWindow(now),
			PlanTier:           strings.TrimSpace(in.PlanTier),
			ActivePool:         PoolForModel(extractModelString(in.Model)),
			UpdatedAt:          now.UnixMilli(),
		}
		if incoming.GeminiFiveHour == nil && incoming.GeminiSevenDay == nil &&
			incoming.ThirdPartyFiveHour == nil && incoming.ThirdPartySevenDay == nil {
			return false, nil
		}
		merged := mergeAntigravity(LoadAntigravity(), incoming, now)
		b, err := json.Marshal(merged)
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
	incoming := &Claude{
		FiveHour:  in.RateLimits.FiveHour.toWindow(),
		SevenDay:  in.RateLimits.SevenDay.toWindow(),
		UpdatedAt: now.UnixMilli(),
	}
	if incoming.FiveHour == nil && incoming.SevenDay == nil {
		return false, nil
	}
	merged, updated := mergeClaude(loadSaved(), incoming, now)
	if !updated || merged == nil {
		return false, nil
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(Path(), b, 0o644)
}

// sameWindowSlackSec tolerates minor server-side timestamp rounding/sub-second
// drift in resets_at when deciding whether two readings belong to the same
// 5-hour or 7-day window.
const sameWindowSlackSec = 120

func cloneWindow(w *Window) *Window {
	if w == nil {
		return nil
	}
	cp := *w
	return &cp
}

func cloneClaude(c *Claude) *Claude {
	if c == nil {
		return nil
	}
	return &Claude{
		FiveHour:  cloneWindow(c.FiveHour),
		SevenDay:  cloneWindow(c.SevenDay),
		UpdatedAt: c.UpdatedAt,
	}
}

func recordRollover(prev, next *Window) *Window {
	out := cloneWindow(next)
	if out == nil {
		return nil
	}
	if prev != nil && prev.UsedPct > 0 && prev.ResetsAt > 0 {
		out.PrevResetsAt = prev.ResetsAt
		out.PrevUsedPct = prev.UsedPct
	} else if prev != nil && prev.PrevResetsAt > out.PrevResetsAt {
		out.PrevResetsAt = prev.PrevResetsAt
		out.PrevUsedPct = prev.PrevUsedPct
	}
	return out
}

func preservePrevReset(picked, other *Window) *Window {
	out := cloneWindow(picked)
	if out == nil || other == nil {
		return out
	}
	if other.PrevResetsAt > out.PrevResetsAt {
		out.PrevResetsAt = other.PrevResetsAt
		out.PrevUsedPct = other.PrevUsedPct
	}
	return out
}

func mergeAgyWindow(prev, next *Window, now time.Time) *Window {
	if next == nil {
		return cloneWindow(prev)
	}
	if prev == nil {
		return cloneWindow(next)
	}
	prevExp := prev.Expired(now)
	nextExp := next.Expired(now)
	if (prevExp && !nextExp) ||
		(prev.ResetsAt > 0 && next.ResetsAt > prev.ResetsAt+sameWindowSlackSec) ||
		(prev.ResetsAt > 0 && next.ResetsAt == 0 && next.UsedPct == 0 && now.Unix() >= prev.ResetsAt-sameWindowSlackSec) {
		return recordRollover(prev, next)
	}
	if nextExp && !prevExp {
		return cloneWindow(prev)
	}
	if prev.ResetsAt > 0 && next.ResetsAt > 0 && prev.ResetsAt > next.ResetsAt+sameWindowSlackSec {
		return cloneWindow(prev)
	}
	return preservePrevReset(next, prev)
}

func mergeAntigravity(prev, incoming *Antigravity, now time.Time) *Antigravity {
	if incoming == nil {
		return prev
	}
	if prev == nil {
		return incoming
	}
	planTier := incoming.PlanTier
	if planTier == "" {
		planTier = prev.PlanTier
	}
	activePool := incoming.ActivePool
	if activePool == "" {
		activePool = prev.ActivePool
	}
	return &Antigravity{
		GeminiFiveHour:     mergeAgyWindow(prev.GeminiFiveHour, incoming.GeminiFiveHour, now),
		GeminiSevenDay:     mergeAgyWindow(prev.GeminiSevenDay, incoming.GeminiSevenDay, now),
		ThirdPartyFiveHour: mergeAgyWindow(prev.ThirdPartyFiveHour, incoming.ThirdPartyFiveHour, now),
		ThirdPartySevenDay: mergeAgyWindow(prev.ThirdPartySevenDay, incoming.ThirdPartySevenDay, now),
		PlanTier:           planTier,
		ActivePool:         activePool,
		UpdatedAt:          incoming.UpdatedAt,
	}
}

// pickWindow chooses the fresher observation for a rate-limit window, returning
// whether b was chosen over a. Within the same unexpired reset window, usage
// cannot decrease, so a stale refresh tick from an idle session never regresses
// a higher reading. When b rolls over to a new window, the previous window's
// reset timestamp and usage are recorded in PrevResetsAt/PrevUsedPct.
func pickWindow(a, b *Window, aAt, bAt int64, now time.Time) (*Window, bool) {
	if b == nil {
		return cloneWindow(a), false
	}
	if a == nil {
		return cloneWindow(b), true
	}
	aExp := a.Expired(now)
	bExp := b.Expired(now)
	if aExp && !bExp {
		return recordRollover(a, b), true
	}
	if bExp && !aExp {
		return cloneWindow(a), false
	}
	if a.ResetsAt > 0 && b.ResetsAt > 0 {
		if b.ResetsAt > a.ResetsAt+sameWindowSlackSec {
			return recordRollover(a, b), true
		}
		if a.ResetsAt > b.ResetsAt+sameWindowSlackSec {
			return cloneWindow(a), false
		}
		if b.UsedPct > a.UsedPct {
			return preservePrevReset(b, a), true
		}
		if a.UsedPct > b.UsedPct {
			return preservePrevReset(a, b), false
		}
		if bAt >= aAt {
			return preservePrevReset(b, a), true
		}
		return preservePrevReset(a, b), false
	}
	if a.ResetsAt > 0 && b.ResetsAt == 0 && b.UsedPct == 0 && a.UsedPct > 0 && now.Unix() >= a.ResetsAt-sameWindowSlackSec {
		return recordRollover(a, b), true
	}
	if bAt >= aAt {
		return preservePrevReset(b, a), true
	}
	return preservePrevReset(a, b), false
}

// mergeClaude merges two Claude rate-limit snapshots, preferring newer reset
// windows and higher monotonic usage within the same window.
func mergeClaude(a, b *Claude, now time.Time) (*Claude, bool) {
	if b == nil || (b.FiveHour == nil && b.SevenDay == nil) {
		return cloneClaude(a), false
	}
	if a == nil || (a.FiveHour == nil && a.SevenDay == nil) {
		return cloneClaude(b), true
	}
	fh, fhFromB := pickWindow(a.FiveHour, b.FiveHour, a.UpdatedAt, b.UpdatedAt, now)
	sd, sdFromB := pickWindow(a.SevenDay, b.SevenDay, a.UpdatedAt, b.UpdatedAt, now)
	updatedAt := a.UpdatedAt
	if (fhFromB || sdFromB) && b.UpdatedAt > updatedAt {
		updatedAt = b.UpdatedAt
	}
	return &Claude{
		FiveHour:  fh,
		SevenDay:  sd,
		UpdatedAt: updatedAt,
	}, fhFromB || sdFromB
}

func loadSaved() *Claude {
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

type claudeConfigUsage struct {
	CachedUsageUtilization *struct {
		FetchedAtMs int64 `json:"fetchedAtMs"`
		Utilization *struct {
			FiveHour *rawCachedWindow `json:"five_hour"`
			SevenDay *rawCachedWindow `json:"seven_day"`
			Limits   []struct {
				Kind     string   `json:"kind"`
				Percent  *float64 `json:"percent"`
				ResetsAt string   `json:"resets_at"`
			} `json:"limits"`
		} `json:"utilization"`
	} `json:"cachedUsageUtilization"`
}

type rawCachedWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

func (r *rawCachedWindow) toWindow() *Window {
	if r == nil || r.Utilization == nil {
		return nil
	}
	return &Window{
		UsedPct:  *r.Utilization,
		ResetsAt: parseResetTime(r.ResetsAt),
	}
}

var claudeConfigCache struct {
	mu      sync.Mutex
	path    string
	modNano int64
	size    int64
	limits  *Claude
}

// loadClaudeConfigCache reads Claude Code's cachedUsageUtilization from
// ~/.claude.json (updated by /usage and Claude Code's own usage fetches),
// caching the parsed snapshot by file mtime+size so 1s notch-feed polls only
// pay an os.Stat when the file hasn't changed.
func loadClaudeConfigCache() *Claude {
	p := paths.ClaudeConfigPath()
	fi, err := os.Stat(p)
	if err != nil {
		return nil
	}
	modNano := fi.ModTime().UnixNano()
	size := fi.Size()

	claudeConfigCache.mu.Lock()
	var prevCached *Claude
	if claudeConfigCache.path == p {
		if claudeConfigCache.modNano == modNano && claudeConfigCache.size == size {
			cached := cloneClaude(claudeConfigCache.limits)
			claudeConfigCache.mu.Unlock()
			return cached
		}
		prevCached = cloneClaude(claudeConfigCache.limits)
	}
	claudeConfigCache.mu.Unlock()

	parsed := parseClaudeConfigFile(p, fi.ModTime().UnixMilli())
	if prevCached != nil && parsed != nil {
		parsed, _ = mergeClaude(prevCached, parsed, time.Now())
	}

	claudeConfigCache.mu.Lock()
	claudeConfigCache.path = p
	claudeConfigCache.modNano = modNano
	claudeConfigCache.size = size
	claudeConfigCache.limits = cloneClaude(parsed)
	claudeConfigCache.mu.Unlock()

	return parsed
}

func parseClaudeConfigFile(path string, fallbackMs int64) *Claude {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg claudeConfigUsage
	if json.Unmarshal(b, &cfg) != nil || cfg.CachedUsageUtilization == nil || cfg.CachedUsageUtilization.Utilization == nil {
		return nil
	}
	u := cfg.CachedUsageUtilization.Utilization
	fh := u.FiveHour.toWindow()
	sd := u.SevenDay.toWindow()
	for _, lim := range u.Limits {
		if lim.Percent == nil {
			continue
		}
		switch lim.Kind {
		case "session":
			if fh == nil {
				fh = &Window{UsedPct: *lim.Percent, ResetsAt: parseResetTime(lim.ResetsAt)}
			}
		case "weekly_all":
			if sd == nil {
				sd = &Window{UsedPct: *lim.Percent, ResetsAt: parseResetTime(lim.ResetsAt)}
			}
		}
	}
	if fh == nil && sd == nil {
		return nil
	}
	updatedAt := cfg.CachedUsageUtilization.FetchedAtMs
	if updatedAt <= 0 {
		updatedAt = fallbackMs
	}
	return &Claude{
		FiveHour:  fh,
		SevenDay:  sd,
		UpdatedAt: updatedAt,
	}
}

// Load returns the freshest Claude Code limits from either `vibe-rpc statusline`
// (claude-limits.json) or Claude Code's own cachedUsageUtilization in
// ~/.claude.json, or nil if neither has recorded limits.
func Load() *Claude {
	saved := loadSaved()
	cached := loadClaudeConfigCache()
	merged, _ := mergeClaude(saved, cached, time.Now())
	return merged
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
