package server

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/daemon"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/install"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/pause"
	"github.com/chauveaul/vibe-rpc/internal/presence"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

// NotchFeed is the snapshot streamed to the macOS notch app (VibeNotch) by
// `vibe-rpc notch-feed`, one JSON object per line. Keep field names stable:
// the Swift side decodes them.
type NotchFeed struct {
	Version     string `json:"version"`
	Enabled     bool   `json:"enabled"` // notch.enabled; the app quits itself when false
	DaemonUp    bool   `json:"daemonUp"`
	Paused      bool   `json:"paused"`
	PausedUntil int64  `json:"pausedUntil,omitempty"` // unix ms

	// The live Discord card.
	Status       string `json:"status"` // working|thinking|compacting|notification|shipped|idle|dual|stale|trigger
	StatusLabel  string `json:"statusLabel"`
	Active       bool   `json:"active"` // an agent is doing something worth a live activity
	AppName      string `json:"appName"`
	Details      string `json:"details"`
	State        string `json:"state"`
	Source       string `json:"source"` // antigravity|claude
	Model        string `json:"model"`
	Project      string `json:"project"`
	SessionStart int64  `json:"sessionStart,omitempty"` // unix ms
	GifPath      string `json:"gifPath,omitempty"`      // local file for the status art
	GifURL       string `json:"gifUrl,omitempty"`       // when the art is a remote URL
	Antigravity  bool   `json:"antigravity"`
	Claude       bool   `json:"claude"`

	Today   notchToday     `json:"today"`
	Running []notchSession `json:"running"`
	Usage   notchUsage     `json:"usage"`
	// Sessions waiting on a permission prompt or a question, newest first.
	Attention []notchAttention `json:"attention"`
	Finished  []notchFinished  `json:"finished"`
}

type notchAttention struct {
	ID         string `json:"id"` // changes for every new prompt
	Source     string `json:"source"`
	Project    string `json:"project"`
	Kind       string `json:"kind"` // permission | question
	Text       string `json:"text"`
	Since      int64  `json:"since"` // unix ms
	HostApp    string `json:"hostApp,omitempty"`
	HostAppPID int32  `json:"hostAppPid,omitempty"`
}

// notchFinished is a turn an agent just finished, for VibeNotch's short
// "finished" banner. The ID is unique per turn, so the app shows it once.
type notchFinished struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Project    string `json:"project"`
	At         int64  `json:"at"`     // unix ms
	TurnMs     int64  `json:"turnMs"` // 0 when the prompt time is unknown
	HostApp    string `json:"hostApp,omitempty"`
	HostAppPID int32  `json:"hostAppPid,omitempty"`
}

// notchUsage drives the usage bars. Both Claude Code and Antigravity CLI
// report real 5-hour and weekly plan limits via `vibe-rpc statusline`; if
// Antigravity CLI limits aren't set up yet, its bar falls back to active time
// in the same rolling 5-hour window.
type notchUsage struct {
	Claude      claudeUsage `json:"claude"`
	Antigravity agyUsage    `json:"antigravity"`
}

type claudeUsage struct {
	// Source: "statusline" when limits are being recorded, "setup" when
	// vibe-rpc isn't Claude Code's status line, "other" when another status
	// line is installed, "waiting" when it is but nothing arrived yet.
	Source    string       `json:"source"`
	Session   *usageWindow `json:"session,omitempty"` // 5-hour window
	Week      *usageWindow `json:"week,omitempty"`
	UpdatedAt int64        `json:"updatedAt,omitempty"` // unix ms
}

type usageWindow struct {
	Pct      float64 `json:"pct"`      // 0–1
	ResetsAt int64   `json:"resetsAt"` // unix ms
}

type agyUsage struct {
	// Source: "statusline" when real Antigravity CLI quotas are recorded,
	// "setup" / "other" / "waiting" mirroring Claude Code.
	Source         string       `json:"source"`
	Pool           string       `json:"pool,omitempty"`    // "gemini" | "3p"
	Session        *usageWindow `json:"session,omitempty"` // 5-hour window
	Week           *usageWindow `json:"week,omitempty"`    // 7-day window
	UpdatedAt      int64        `json:"updatedAt,omitempty"`
	WindowActiveMs int64        `json:"windowActiveMs"` // fallback: active in the last 5h
	WindowHours    string       `json:"windowHours"`
	Pct            float64      `json:"pct"` // fallback: share of the 5h window spent active, 0–1
}

type notchToday struct {
	Hours     string  `json:"hours"`
	ActiveMs  int64   `json:"activeMs"`
	Prompts   int     `json:"prompts"`
	Tokens    string  `json:"tokens"`
	Cost      string  `json:"cost"`
	Streak    int     `json:"streak"`
	GoalLabel string  `json:"goalLabel,omitempty"`
	GoalPct   float64 `json:"goalPct"` // 0–1, capped; 0 when no goal
	DualHours string  `json:"dualHours"`
}

type notchSession struct {
	Source     string `json:"source"`
	Project    string `json:"project"`
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	Doing      string `json:"doing,omitempty"`
	Elapsed    string `json:"elapsed,omitempty"`
	OnDiscord  bool   `json:"onDiscord"`
}

var gifExportOnce sync.Once

// gifDir holds the built-in status GIFs on disk so the notch app can animate
// them natively. Exported once per process; cheap and idempotent.
func gifDir() string {
	dir := filepath.Join(paths.DataDir(), "gifs")
	gifExportOnce.Do(func() { _, _ = ExportGifs(dir) })
	return dir
}

// BuildNotchFeed assembles one snapshot from the same sources as the web
// dashboard's live rail.
func BuildNotchFeed() NotchFeed {
	cfg, agg, st := loadLive()
	act, _ := daemon.BuildActivity(st, cfg, agg, &presence.RotationCursor{})

	f := NotchFeed{
		Version:     paths.Version,
		DaemonUp:    daemon.AlivePID() > 0,
		Status:      st.Status,
		StatusLabel: format.StatusVerbose(st.Status, format.HumanTool(st.CurrentTool), 0),
		AppName:     cfg.AppName,
		Source:      st.Source,
		Model:       format.HumanModel(st.Model),
		Antigravity: st.AntigravityActive,
		Claude:      st.ClaudeActive,
		Running:     []notchSession{},
	}
	if f.Source == "" {
		f.Source = "antigravity"
	}
	if u := pause.Until(""); !u.IsZero() {
		f.Paused, f.PausedUntil = true, u.UnixMilli()
	}
	if st.Cwd != "" {
		f.Project = filepath.Base(st.Cwd)
	}
	if st.SessionStart > 0 {
		f.SessionStart = st.SessionStart
	}
	switch st.Status {
	case "working", "thinking", "compacting", "notification", "dual", "trigger", "shipped":
		f.Active = true
	}
	if act != nil {
		f.AppName, f.Details, f.State = act.Name, act.Details, act.State
	} else {
		f.Details = "Away — no active session"
		f.State = "Open " + agentsOr(cfg) + " to start tracking"
	}

	img := presence.ResolveLargeImageKeyForSource(cfg, st.Source, st.Status, st.Model)
	switch {
	case strings.HasPrefix(img, "/assets/gifs/"):
		p := filepath.Join(gifDir(), filepath.Base(img))
		if _, err := os.Stat(p); err == nil {
			f.GifPath = p
		}
	case strings.HasPrefix(img, "https://") || strings.HasPrefix(img, "http://"):
		f.GifURL = img
	}

	f.Enabled = cfg.Notch.Enabled
	f.Today = notchTodayFrom(agg, cfg.Goals.DailyHours, format.BuildVars(st, cfg, agg))
	f.Attention = listAttention(cfg, time.Now())
	f.Finished = []notchFinished{}
	if cfg.Notch.WantsFinishAlerts() {
		f.Finished = listFinished(cfg, time.Now())
	}
	agyModel := ""
	if st.AgySession != nil && st.AgySession.Model != "" {
		agyModel = st.AgySession.Model
	} else if st.Source != "claude" && st.Model != "" {
		agyModel = st.Model
	}
	f.Usage = buildUsage(
		agg,
		limits.Load(), install.ClaudeStatusLineState(""),
		limits.LoadAntigravity(), install.AgyStatusLineState(""), agyModel,
		time.Now(),
	)
	for _, r := range listRunning(cfg, st.SessionID) {
		f.Running = append(f.Running, notchSession{
			Source: r.SourceKey, Project: r.Project, Status: r.Status, StatusText: r.StatusText,
			Doing: r.Doing, Elapsed: r.Elapsed, OnDiscord: r.OnDiscord,
		})
	}
	return f
}

func notchTodayFrom(agg *scanner.Aggregate, goalHours float64, vars map[string]any) notchToday {
	t := notchToday{Hours: "0h", Tokens: "0", Cost: pricing.FmtCost(0), DualHours: "0h"}
	if agg == nil {
		return t
	}
	t.Streak = agg.Streak
	day := scanner.DayKey(time.Now().UnixMilli())
	if d := agg.ByDay[day]; d != nil {
		t.ActiveMs = d.ActiveMs
		t.Hours = format.FmtHours(d.ActiveMs)
		t.Prompts = d.UserMessages
		t.Tokens = format.FmtNum(d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens)
		t.Cost = pricing.FmtCost(d.Cost)
	}
	if ms := agg.DualByDay[day]; ms > 0 {
		t.DualHours = format.FmtHours(ms)
	}
	if goalHours > 0 {
		t.GoalPct = math.Min(1, float64(t.ActiveMs)/3_600_000/goalHours)
	}
	if s, ok := vars["goalLabel"].(string); ok {
		t.GoalLabel = s
	}
	return t
}

const usageWindowMs = 5 * 3_600_000

func statusLineSource(state string) string {
	switch state {
	case "vibe-rpc":
		return "waiting"
	case "other":
		return "other"
	default:
		return "setup"
	}
}

func convertWindow(w *limits.Window, now time.Time) *usageWindow {
	if w == nil {
		return nil
	}
	if w.Expired(now) {
		// The window rolled over since the CLI last reported: it's fresh.
		return &usageWindow{Pct: 0}
	}
	return &usageWindow{Pct: math.Max(0, math.Min(1, w.UsedPct/100)), ResetsAt: w.ResetsAt * 1000}
}

func buildUsage(agg *scanner.Aggregate, ccLim *limits.Claude, ccStatusLine string, agyLim *limits.Antigravity, agyStatusLine, agyModel string, now time.Time) notchUsage {
	var u notchUsage
	u.Claude.Source = statusLineSource(ccStatusLine)
	if ccLim != nil && (ccLim.FiveHour != nil || ccLim.SevenDay != nil) {
		u.Claude.Source = "statusline"
		u.Claude.UpdatedAt = ccLim.UpdatedAt
		u.Claude.Session = convertWindow(ccLim.FiveHour, now)
		u.Claude.Week = convertWindow(ccLim.SevenDay, now)
	}

	u.Antigravity.Source = statusLineSource(agyStatusLine)
	if agg != nil {
		ms := scanner.ActiveWithin(agg.RecentSpans["antigravity"], now.UnixMilli()-usageWindowMs, now.UnixMilli())
		u.Antigravity.WindowActiveMs = ms
		u.Antigravity.WindowHours = format.FmtHours(ms)
		u.Antigravity.Pct = math.Min(1, float64(ms)/usageWindowMs)
	} else {
		u.Antigravity.WindowHours = format.FmtHours(0)
	}
	if agyLim != nil {
		if pool, sess, week := agyLim.PickPool(agyModel); sess != nil || week != nil {
			u.Antigravity.Source = "statusline"
			u.Antigravity.Pool = pool
			u.Antigravity.UpdatedAt = agyLim.UpdatedAt
			u.Antigravity.Session = convertWindow(sess, now)
			u.Antigravity.Week = convertWindow(week, now)
		}
	}
	return u
}

// attentionMaxAge drops prompts nobody answered (e.g. the agent was closed
// without a SessionEnd) so they don't linger forever.
const attentionMaxAge = 30 * time.Minute

const (
	// finishedMaxAge is how long a finished turn stays in the feed; the app
	// only shows ones it hasn't seen, so this just covers feed restarts.
	finishedMaxAge = time.Minute
	// minFinishedTurn skips quick back-and-forth turns you were watching anyway.
	minFinishedTurn = 10 * time.Second
)

// listFinished lists turns that ended in the last minute and haven't been
// followed by a new one, newest first.
func listFinished(cfg config.Config, now time.Time) []notchFinished {
	out := []notchFinished{}
	for _, s := range state.ListSessions(state.ListOptions{
		IncludeAntigravity: cfg.Sources.WantAntigravity(),
		IncludeClaude:      cfg.Sources.WantClaude(),
		IncludeOpenCode:    cfg.Sources.WantOpenCode(),
		IncludeCodex:       cfg.Sources.WantCodex(),
		IncludeClaudeRPC:   true,
	}) {
		if s.LastStop == 0 || s.Status != "idle" || s.AgentClosed {
			continue
		}
		if now.UnixMilli()-s.LastStop > finishedMaxAge.Milliseconds() {
			continue
		}
		if s.LastTurnMs > 0 && s.LastTurnMs < minFinishedTurn.Milliseconds() {
			continue
		}
		f := notchFinished{
			ID:     fmt.Sprintf("%s:%d", s.SessionID, s.LastStop),
			Source: scanner.SourceKey(s.Source), At: s.LastStop, TurnMs: s.LastTurnMs,
			HostApp: s.HostApp, HostAppPID: s.HostAppPID,
		}
		if s.Cwd != "" {
			f.Project = filepath.Base(s.Cwd)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// listAttention reads raw session state (not the Discord-facing view, which
// turns "waiting" back into idle after a few seconds) so an alert stays up
// until the agent moves on.
func listAttention(cfg config.Config, now time.Time) []notchAttention {
	out := []notchAttention{}
	for _, s := range state.ListSessions(state.ListOptions{
		IncludeAntigravity: cfg.Sources.WantAntigravity(),
		IncludeClaude:      cfg.Sources.WantClaude(),
		IncludeOpenCode:    cfg.Sources.WantOpenCode(),
		IncludeCodex:       cfg.Sources.WantCodex(),
		IncludeClaudeRPC:   true,
	}) {
		if s.Status != "notification" || s.AgentClosed || s.ClaudeClosed {
			continue
		}
		if s.AttentionKind != "permission" && s.AttentionKind != "question" {
			continue
		}
		if s.LastNotification == 0 || now.UnixMilli()-s.LastNotification > attentionMaxAge.Milliseconds() {
			continue
		}
		a := notchAttention{
			ID:     fmt.Sprintf("%s:%d", s.SessionID, s.LastNotification),
			Source: s.Source, Kind: s.AttentionKind, Text: s.AttentionText, Since: s.LastNotification,
			HostApp: s.HostApp, HostAppPID: s.HostAppPID,
		}
		a.Source = scanner.SourceKey(a.Source)
		if s.Cwd != "" {
			a.Project = filepath.Base(s.Cwd)
		}
		if a.Text == "" {
			a.Text = "Needs your attention"
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since > out[j].Since })
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}
