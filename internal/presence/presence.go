package presence

import (
	"math"
	"math/rand"
	"sort"
	"strings"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

type RotationCursor struct {
	Index  int
	LastAt int64
	Status string
}

func PickFrames(p config.PresenceConfig, status string) ([]config.Frame, string) {
	if sb, ok := p.ByStatus[status]; ok {
		base := config.Frame{Details: sb.Details, State: sb.State, LargeImageText: sb.LargeImageText}
		frames := make([]config.Frame, 0, 1+len(sb.Rotation))
		frames = append(frames, base)
		frames = append(frames, sb.Rotation...)
		return frames, sb.LargeImageText
	}
	if len(p.Rotation) > 0 {
		return p.Rotation, ""
	}
	return []config.Frame{{Details: p.Details, State: p.State}}, ""
}

func SelectFrame(rawFrames []config.Frame, vars map[string]any, status string, cursor *RotationCursor, intervalMs int64, passes func(config.Frame, map[string]any) bool, now int64) config.Frame {
	if status != cursor.Status {
		cursor.Index = 0
		cursor.LastAt = now
		cursor.Status = status
	}
	var passing []config.Frame
	for _, f := range rawFrames {
		if passes(f, vars) {
			passing = append(passing, f)
		}
	}
	frames := passing
	if len(frames) == 0 && len(rawFrames) > 0 {
		frames = rawFrames[:1]
	}
	if len(frames) == 0 {
		return config.Frame{}
	}
	if len(frames) > 1 && now-cursor.LastAt >= intervalMs {
		cursor.Index = (cursor.Index + 1) % len(frames)
		cursor.LastAt = now
	}
	return frames[cursor.Index%len(frames)]
}

type PickedSession struct {
	State     *state.SessionState
	SessionID string
	LiveCount int
}

func PickActiveSession(sessions []state.SessionState, displayedID string, now, idleMs int64) PickedSession {
	list := make([]state.SessionState, 0, len(sessions))
	for _, s := range sessions {
		if s.SessionID != "" {
			list = append(list, s)
		}
	}
	if len(list) == 0 {
		return PickedSession{}
	}
	isLive := func(s state.SessionState) bool {
		return !s.AgentClosed && !s.ClaudeClosed && now-s.LastActivity <= idleMs
	}
	liveCount := 0
	for _, s := range list {
		if isLive(s) {
			liveCount++
		}
	}
	for i := range list {
		if list[i].SessionID == displayedID && isLive(list[i]) {
			cp := list[i]
			return PickedSession{State: &cp, SessionID: cp.SessionID, LiveCount: liveCount}
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].LastActivity > list[j].LastActivity
	})
	for i := range list {
		if isLive(list[i]) {
			cp := list[i]
			return PickedSession{State: &cp, SessionID: cp.SessionID, LiveCount: liveCount}
		}
	}
	for i := range list {
		if !list[i].AgentClosed && !list[i].ClaudeClosed {
			cp := list[i]
			return PickedSession{State: &cp, SessionID: cp.SessionID, LiveCount: liveCount}
		}
	}
	cp := list[0]
	return PickedSession{State: &cp, SessionID: cp.SessionID, LiveCount: liveCount}
}

func ShouldShowGithubButton(cfg config.Config, st state.SessionState) bool {
	if !cfg.IsGithubButton() || st.Status == "stale" {
		return false
	}
	if st.Privacy != nil && st.Privacy.Visibility != "public" {
		return false
	}
	return true
}

type ThrottleInput struct {
	Hash         string
	LastSentHash string
	LastSentAt   int64
	Now          int64
	GapMs        int64
	FlushPending bool
	RecentSends  []int64
	WindowMs     int64
	MaxPerWindow int
	KeepaliveMs  int64
}

type ThrottleResult struct {
	Action string // "skip", "send", "defer"
	WaitMs int64
}

func ThrottleDecision(in ThrottleInput) ThrottleResult {
	if in.Hash == in.LastSentHash {
		due := in.KeepaliveMs > 0 && in.LastSentAt > 0 && in.Now-in.LastSentAt >= in.KeepaliveMs
		if !due {
			return ThrottleResult{Action: "skip"}
		}
	}
	var waitMs int64
	if rem := in.LastSentAt + in.GapMs - in.Now; rem > 0 {
		waitMs = rem
	}
	if in.MaxPerWindow > 0 && in.WindowMs > 0 {
		var inWin []int64
		for _, t := range in.RecentSends {
			if t > in.Now-in.WindowMs {
				inWin = append(inWin, t)
			}
		}
		if len(inWin) >= in.MaxPerWindow {
			oldest := inWin[len(inWin)-in.MaxPerWindow]
			if rem := oldest + in.WindowMs - in.Now; rem > waitMs {
				waitMs = rem
			}
		}
	}
	if waitMs == 0 && !in.FlushPending {
		return ThrottleResult{Action: "send"}
	}
	return ThrottleResult{Action: "defer", WaitMs: waitMs}
}

const (
	ReconnectBaseMs      int64 = 5_000
	ReconnectCapMs       int64 = 300_000
	ReconnectAbsentCapMs int64 = 15_000
)

func ReconnectPlan(delayMs int64, absent bool) (waitMs int64, nextDelayMs int64) {
	capMs := ReconnectCapMs
	if absent {
		capMs = ReconnectAbsentCapMs
	}
	step := delayMs
	if step < ReconnectBaseMs {
		step = ReconnectBaseMs
	}
	if step > capMs {
		step = capMs
	}
	wait := int64(math.Round(float64(step) * (0.7 + rand.Float64()*0.6)))
	next := step * 2
	if next > capMs {
		next = capMs
	}
	return wait, next
}

func ResolveLargeImageKey(cfg config.Config, status, model string) string {
	return ResolveLargeImageKeyForSource(cfg, "", status, model)
}

func ResolveLargeImageKeyForSource(cfg config.Config, source, status, model string) string {
	// "dual" status always uses the dedicated Dual Wielding GIF from StatusAssets["dual"].
	if status == "dual" && cfg.StatusAssets != nil {
		if v := cfg.StatusAssets["dual"]; v != "" {
			return v
		}
	}
	if source == "codex" && cfg.CodexStatusAssets != nil {
		if v := cfg.CodexStatusAssets[status]; v != "" {
			return v
		}
	}
	if source == "opencode" && cfg.OpenCodeStatusAssets != nil {
		if v := cfg.OpenCodeStatusAssets[status]; v != "" {
			return v
		}
	}
	if source == "claude" && cfg.ClaudeStatusAssets != nil {
		if v := cfg.ClaudeStatusAssets[status]; v != "" {
			return v
		}
	}
	if cfg.StatusAssets != nil {
		if v := cfg.StatusAssets[status]; v != "" {
			return v
		}
	}
	if cfg.ModelAssets != nil && model != "" && status != "stale" {
		m := strings.ToLower(model)
		var pick string
		switch {
		case strings.Contains(m, "neon"):
			pick = cfg.ModelAssets["neon"]
		case strings.Contains(m, "flash-lite") || strings.Contains(m, "flash_lite"):
			pick = cfg.ModelAssets["flashLite"]
		case strings.Contains(m, "flash"):
			pick = cfg.ModelAssets["flash"]
		case strings.Contains(m, "pro"):
			pick = cfg.ModelAssets["pro"]
		case strings.Contains(m, "fable"):
			pick = cfg.ModelAssets["fable"]
		case strings.Contains(m, "opus"):
			pick = cfg.ModelAssets["opus"]
		case strings.Contains(m, "sonnet"):
			pick = cfg.ModelAssets["sonnet"]
		case strings.Contains(m, "haiku"):
			pick = cfg.ModelAssets["haiku"]
		}
		if pick == "" {
			pick = cfg.ModelAssets["default"]
		}
		if pick != "" {
			return pick
		}
	}
	return cfg.Presence.LargeImageKey
}

// PickDualSnapshots finds the latest active Antigravity and Claude Code sessions so
// dual-wielding templates can display both files, projects, tools, and models.
func PickDualSnapshots(sessions []state.SessionState, live []state.LiveSession, cfg config.Config, nowMs int64) (agy *state.SessionSnapshot, cc *state.SessionSnapshot) {
	staleMs := int64(cfg.StaleSessionMin) * 60_000
	if staleMs < 60_000 {
		staleMs = 5 * 60_000
	}
	var bestAgy, bestCC *state.SessionState
	for i := range sessions {
		s := &sessions[i]
		if s.AgentClosed || s.ClaudeClosed || s.Status == "stale" {
			continue
		}
		if s.LastActivity > 0 && nowMs-s.LastActivity > staleMs {
			continue
		}
		if s.Source == "opencode" || s.Source == "codex" {
			continue // not part of the Antigravity + Claude Code dual model
		}
		if s.Source == "claude" {
			if bestCC == nil || s.LastActivity > bestCC.LastActivity {
				bestCC = s
			}
		} else {
			if bestAgy == nil || s.LastActivity > bestAgy.LastActivity {
				bestAgy = s
			}
		}
	}
	if bestAgy != nil {
		agy = &state.SessionSnapshot{
			Cwd: bestAgy.Cwd, Model: bestAgy.Model, Status: bestAgy.Status,
			CurrentTool: bestAgy.CurrentTool, CurrentFile: bestAgy.CurrentFile,
		}
	}
	if bestCC != nil {
		cc = &state.SessionSnapshot{
			Cwd: bestCC.Cwd, Model: bestCC.Model, Status: bestCC.Status,
			CurrentTool: bestCC.CurrentTool, CurrentFile: bestCC.CurrentFile,
		}
	}
	// Fallback to live transcripts if a running agent didn't write a hook session yet.
	for _, ls := range live {
		if ls.Source == "claude" && cc == nil {
			cc = &state.SessionSnapshot{Cwd: ls.Cwd, Model: "claude", Status: "working"}
		} else if ls.Source == "antigravity" && agy == nil {
			agy = &state.SessionSnapshot{Cwd: ls.Cwd, Model: "gemini", Status: "working"}
		}
	}
	return agy, cc
}

// IsDualWorking returns true when both Antigravity and Claude Code are actively on
// (and at least one is actively working/thinking/compacting, or both are live non-stale).
func IsDualWorking(st state.SessionState) bool {
	if !st.AntigravityActive || !st.ClaudeActive {
		return false
	}
	if st.Status == "stale" || st.Status == "shipped" || st.Status == "notification" || st.Status == "trigger" {
		return false
	}
	isBusy := func(s string) bool {
		return s == "working" || s == "thinking" || s == "compacting"
	}
	if isBusy(st.Status) {
		return true
	}
	if st.AgySession != nil && isBusy(st.AgySession.Status) {
		return true
	}
	if st.ClaudeSession != nil && isBusy(st.ClaudeSession.Status) {
		return true
	}
	return false
}

// ComputeActiveSources determines whether Antigravity and/or Claude Code are currently active
// based on live hook sessions, live transcripts, and running OS processes.
func ComputeActiveSources(sessions []state.SessionState, live []state.LiveSession, agyProc, claudeProc *bool, cfg config.Config, nowMs int64) (agyOn, claudeOn bool) {
	staleMs := int64(cfg.StaleSessionMin) * 60_000
	if staleMs < 60_000 {
		staleMs = 5 * 60_000
	}
	wantAgy := cfg.ShowOnDiscord("antigravity")
	wantClaude := cfg.ShowOnDiscord("claude")

	for _, s := range sessions {
		if s.AgentClosed || s.ClaudeClosed || s.Status == "stale" {
			continue
		}
		if s.LastActivity > 0 && nowMs-s.LastActivity > staleMs {
			continue
		}
		if s.Source == "claude" {
			if wantClaude {
				claudeOn = true
			}
		} else if s.Source != "opencode" && s.Source != "codex" && wantAgy {
			agyOn = true
		}
	}
	for _, ls := range live {
		if nowMs-ls.Mtime > staleMs {
			continue
		}
		if ls.Source == "claude" {
			if wantClaude {
				claudeOn = true
			}
		} else if wantAgy {
			agyOn = true
		}
	}
	if wantAgy && agyProc != nil {
		if *agyProc {
			agyOn = true
		} else {
			// OS confirmed no Antigravity process is running; ignore lingering session/transcript mtimes.
			agyOn = false
		}
	}
	if wantClaude && claudeProc != nil {
		if *claudeProc {
			claudeOn = true
		} else {
			// OS confirmed no Claude Code process is running; ignore lingering session/transcript mtimes.
			claudeOn = false
		}
	}
	return agyOn, claudeOn
}

// ResolveAppName returns the Discord activity name:
//   - "Antigravity & Claude" when both agents are active
//   - "Claude Code" when only Claude Code is active
//   - cfg.AppName (default "Antigravity") when only Antigravity is active (or dynamicAppName is off)
func ResolveAppName(st state.SessionState, cfg config.Config) string {
	base := cfg.AppName
	if base == "" {
		base = "Antigravity"
	}
	if !cfg.IsDynamicAppName() {
		return base
	}
	if st.Source == "opencode" || st.Source == "codex" {
		return config.AgentName(st.Source)
	}
	agyOn := st.AntigravityActive
	claudeOn := st.ClaudeActive
	if !st.SourcesComputed {
		for _, ls := range st.LiveSessions {
			if ls.Source == "claude" {
				if cfg.ShowOnDiscord("claude") {
					claudeOn = true
				}
			} else if cfg.ShowOnDiscord("antigravity") {
				agyOn = true
			}
		}
	}
	if !agyOn && !claudeOn && st.Status != "stale" {
		if st.Source == "claude" {
			claudeOn = true
		} else {
			agyOn = true
		}
	}
	switch {
	case agyOn && claudeOn:
		return "Antigravity & Claude"
	case claudeOn:
		return "Claude Code"
	default:
		return base
	}
}

// DefaultPublicAntigravityIcon is a verified public HTTPS Google Antigravity logo
// used as Discord fallback when using the shared default ClientID without GifBaseURL,
// so Discord never falls back to claude-rpc's orange Clawd icon.
const DefaultPublicAntigravityIcon = "https://cdn.jsdelivr.net/gh/selfhst/icons@main/png/google-antigravity.png"

// ResolveDiscordImageKey converts a configured asset into a Discord-compatible
// large_image value:
//  1. External https:// URLs pass through directly.
//  2. Local built-in "/assets/gifs/<name>.gif" assets expand via cfg.GifBaseURL when set.
//  3. With a custom Discord Application ID, local assets map to the Discord Portal
//     asset key "<name>" (e.g. "agy-working").
//  4. With the shared default ClientID and no GifBaseURL, falls back to the public
//     HTTPS Google Antigravity logo instead of an unknown asset key.
func ResolveDiscordImageKey(cfg config.Config, source, status, model string) string {
	raw := ResolveLargeImageKeyForSource(cfg, source, status, model)
	if strings.HasPrefix(raw, "/assets/gifs/") {
		file := strings.TrimPrefix(raw, "/assets/gifs/")
		if base := strings.TrimRight(strings.TrimSpace(cfg.GifBaseURL), "/"); base != "" {
			// Auto-upgrade raw.githubusercontent.com for our repo to jsDelivr (Discord proxy requires CORS/CORP headers).
			// Also migrates the pre-rename agy-rpc repo URLs to the vibe-rpc repo.
			switch base {
			case "https://raw.githubusercontent.com/chauveaul/vibe-rpc/main/internal/server/assets/gifs",
				"https://raw.githubusercontent.com/chauveaul/agy-rpc/main/internal/server/assets/gifs",
				"https://cdn.jsdelivr.net/gh/chauveaul/agy-rpc@main/internal/server/assets/gifs":
				base = config.DefaultGifBaseURL
			}
			// Version query busts Discord's media-proxy cache (it remembers failed
			// fetches per URL, e.g. 404s served while the repo was being renamed).
			return base + "/" + file + "?v=" + config.GifAssetVersion
		}
		if cfg.ClientID != "" && cfg.ClientID != config.DefaultClientID {
			return strings.TrimSuffix(file, ".gif")
		}
		return DefaultPublicAntigravityIcon
	}
	return raw
}

// DiscordListOptions lists only the sessions allowed to drive Rich Presence
// (tracked and not hidden from Discord in Settings).
func DiscordListOptions(cfg config.Config) state.ListOptions {
	return state.ListOptions{
		IncludeAntigravity: cfg.ShowOnDiscord("antigravity"),
		IncludeClaude:      cfg.ShowOnDiscord("claude"),
		IncludeOpenCode:    cfg.ShowOnDiscord("opencode"),
		IncludeCodex:       cfg.ShowOnDiscord("codex"),
		IncludeClaudeRPC:   true,
	}
}

// FilterLiveForDiscord drops live transcripts from agents hidden from Discord.
func FilterLiveForDiscord(live []state.LiveSession, cfg config.Config) []state.LiveSession {
	out := live[:0:0]
	for _, ls := range live {
		src := ls.Source
		if src != "claude" && src != "opencode" && src != "codex" {
			src = "antigravity"
		}
		if cfg.ShowOnDiscord(src) {
			out = append(out, ls)
		}
	}
	return out
}
