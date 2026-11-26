package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/discord"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/gitinfo"
	"github.com/chauveaul/vibe-rpc/internal/install"
	"github.com/chauveaul/vibe-rpc/internal/notch"
	"github.com/chauveaul/vibe-rpc/internal/notify"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/pause"
	"github.com/chauveaul/vibe-rpc/internal/presence"
	"github.com/chauveaul/vibe-rpc/internal/privacy"
	"github.com/chauveaul/vibe-rpc/internal/proc"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

const (
	desktopKeepaliveSec = 60
	bridgeKeepaliveSec  = 20
	logRotateBytes      = 5 * 1024 * 1024
)

type logger struct {
	mu   sync.Mutex
	path string
}

func (l *logger) Log(formatStr string, args ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(formatStr, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(l.path), 0o755)
	if info, err := os.Stat(l.path); err == nil && info.Size() > logRotateBytes {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.WriteString(line)
		_ = f.Close()
	}
}

type Runner struct {
	log *logger

	mu              sync.Mutex
	cfg             config.Config
	agg             *scanner.Aggregate
	liveSessions    []state.LiveSession
	agents          proc.ActiveAgents
	displayedID     string
	cursor          presence.RotationCursor
	prevStatus      string
	client          *discord.Client
	connected       bool
	usingBridge     bool
	reconnectDelay  int64
	lastSentHash    string
	lastSentAt      int64
	recentSends     []int64
	flushTimer      *time.Timer
	flushPending    bool
	pendingActivity *discord.Activity
	pendingClear    bool
	pendingHash     string
	stopped         bool
	stopCh          chan struct{}
	wakePushCh      chan struct{}
	reconnectCh     chan bool // true if absent
}

func Run() error {
	log := &logger{path: paths.LogPath()}
	if owner := claimSingleInstance(); owner != 0 {
		log.Log("another daemon is already running (pid %d) — exiting", owner)
		return nil
	}
	ClearStopped()

	startedAt := time.Now().UnixMilli()
	WriteInfo(Info{
		PID:       os.Getpid(),
		Version:   paths.Version,
		StartedAt: startedAt,
		BeatAt:    startedAt,
	})

	r := &Runner{
		log:            log,
		cfg:            config.LoadFrom(paths.ConfigPath(), func(m string) { log.Log("config: %s", m) }),
		agg:            scanner.ReadAggregate(),
		reconnectDelay: presence.ReconnectBaseMs,
		stopCh:         make(chan struct{}),
		wakePushCh:     make(chan struct{}, 1),
		reconnectCh:    make(chan bool, 1),
	}

	defer func() {
		r.cleanup()
	}()

	log.Log("daemon starting (pid=%d, version=%s, sources=antigravity:%v,claude:%v)",
		os.Getpid(), paths.Version, r.cfg.Sources.WantAntigravity(), r.cfg.Sources.WantClaude())

	// Initial probes
	r.refreshLiveAndAgents()
	go func() {
		res := scanner.Scan(scanner.ScanOptions{})
		if res.Aggregate != nil {
			r.mu.Lock()
			r.agg = res.Aggregate
			r.mu.Unlock()
			r.triggerPush()
		}
	}()

	// Filesystem watcher
	watcher := r.startWatcher()
	if watcher != nil {
		defer watcher.Close()
	}

	// Signal handling
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start Discord IPC connection loop
	go r.discordLoop()

	// Periodic timers
	updateMs := r.cfg.UpdateIntervalMs
	if updateMs < 1000 {
		updateMs = 4000
	}
	pushTicker := time.NewTicker(time.Duration(updateMs) * time.Millisecond)
	defer pushTicker.Stop()

	liveTicker := time.NewTicker(5 * time.Second)
	defer liveTicker.Stop()

	rescanSec := r.cfg.RescanIntervalSec
	if rescanSec < 30 {
		rescanSec = 300
	}
	scanTicker := time.NewTicker(time.Duration(rescanSec) * time.Second)
	defer scanTicker.Stop()

	beatTicker := time.NewTicker(BeatEvery)
	defer beatTicker.Stop()

	sweepTicker := time.NewTicker(30 * time.Minute)
	defer sweepTicker.Stop()

	pollDur := 30 * time.Second
	if runtime.GOOS == "windows" {
		pollDur = 3 * time.Second
	}
	pollTicker := time.NewTicker(pollDur)
	defer pollTicker.Stop()
	r.ensureNotch()

	for {
		select {
		case sig := <-sigCh:
			log.Log("received signal %v — shutting down", sig)
			return nil
		case <-r.stopCh:
			return nil
		case <-r.wakePushCh:
			r.pushNow()
		case <-pushTicker.C:
			r.pushNow()
		case <-liveTicker.C:
			r.refreshLiveAndAgents()
			r.pushNow()
		case <-pollTicker.C:
			r.reloadConfigAndAgg()
			r.pushNow()
			r.ensureNotch()
		case <-scanTicker.C:
			go func() {
				res := scanner.Scan(scanner.ScanOptions{})
				if res.Aggregate != nil {
					r.mu.Lock()
					r.agg = res.Aggregate
					r.mu.Unlock()
					r.triggerPush()
				}
			}()
		case <-beatTicker.C:
			WriteInfo(Info{
				PID:       os.Getpid(),
				Version:   paths.Version,
				StartedAt: startedAt,
				BeatAt:    time.Now().UnixMilli(),
			})
		case <-sweepTicker.C:
			if n := state.SweepStale(6 * time.Hour); n > 0 {
				log.Log("swept %d stale state files", n)
			}
		}
	}
}

func (r *Runner) triggerPush() {
	select {
	case r.wakePushCh <- struct{}{}:
	default:
	}
}

func (r *Runner) startWatcher() *fsnotify.Watcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		r.log.Log("fsnotify init failed: %v (using poll fallback)", err)
		return nil
	}
	dirs := []string{
		paths.StateDir(),
		filepath.Dir(paths.ConfigPath()),
		filepath.Dir(paths.AggregatePath()),
	}
	if claudeDir := paths.ClaudeRPCStateDir(); claudeDir != "" && claudeDir != paths.StateDir() {
		if st, err := os.Stat(claudeDir); err == nil && st.IsDir() {
			dirs = append(dirs, claudeDir)
		}
	}
	for _, d := range dirs {
		_ = os.MkdirAll(d, 0o755)
		_ = w.Add(d)
	}
	go func() {
		for {
			select {
			case <-r.stopCh:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				base := filepath.Base(ev.Name)
				if strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".lock") ||
					base == "daemon.log" || base == "daemon.json" || base == "daemon.pid" {
					continue
				}
				if ev.Name == paths.ConfigPath() || ev.Name == paths.AggregatePath() {
					r.reloadConfigAndAgg()
				}
				r.triggerPush()
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return w
}

func (r *Runner) reloadConfigAndAgg() {
	newCfg := config.LoadFrom(paths.ConfigPath(), func(m string) { r.log.Log("config: %s", m) })
	newAgg := scanner.ReadAggregate()
	r.mu.Lock()
	clientIDChanged := newCfg.ClientID != r.cfg.ClientID
	r.cfg = newCfg
	if newAgg != nil {
		r.agg = newAgg
	}
	c := r.client
	r.mu.Unlock()
	if clientIDChanged && c != nil {
		r.log.Log("clientId changed — reconnecting Discord IPC")
		c.Close()
		r.scheduleReconnect(false)
	}
}

func (r *Runner) refreshLiveAndAgents() {
	r.mu.Lock()
	cfg := r.cfg
	r.mu.Unlock()

	threshMs := int64(cfg.LiveSessionThresholdSec) * 1000
	if threshMs <= 0 {
		threshMs = 90_000
	}
	live := scanner.FindLiveSessions(scanner.LiveOptions{ThresholdMs: threshMs})
	var agents proc.ActiveAgents
	if cfg.IsProcessDetection() {
		agents = proc.Detect(cfg.Sources.WantAntigravity(), cfg.Sources.WantClaude())
	}

	r.mu.Lock()
	r.liveSessions = live
	r.agents = agents
	r.mu.Unlock()
}

func (r *Runner) scheduleReconnect(absent bool) {
	select {
	case r.reconnectCh <- absent:
	default:
	}
}

func (r *Runner) discordLoop() {
	for {
		select {
		case <-r.stopCh:
			return
		default:
		}

		r.mu.Lock()
		clientID := r.cfg.ClientID
		r.mu.Unlock()

		if config.ClassifyClientID(clientID) != "ok" {
			r.log.Log("clientId invalid (%q) — waiting for valid config", clientID)
			select {
			case <-r.stopCh:
				return
			case <-time.After(15 * time.Second):
				continue
			}
		}

		client := discord.NewClient(clientID, func() {
			r.mu.Lock()
			r.connected = false
			r.usingBridge = false
			r.lastSentHash = ""
			r.mu.Unlock()
			r.log.Log("IPC disconnected — scheduling reconnect")
			r.scheduleReconnect(false)
		})

		err := client.Login()
		if err != nil {
			absent := errors.Is(err, discord.ErrAbsent)
			r.mu.Lock()
			waitMs, nextDelay := presence.ReconnectPlan(r.reconnectDelay, absent)
			r.reconnectDelay = nextDelay
			r.mu.Unlock()
			if absent {
				r.log.Log("Discord not running — retrying in %ds", waitMs/1000)
			} else {
				r.log.Log("Discord IPC connect failed: %v — retrying in %ds", err, waitMs/1000)
			}
			select {
			case <-r.stopCh:
				return
			case <-time.After(time.Duration(waitMs) * time.Millisecond):
				continue
			}
		}

		user := client.User()
		bridge := discord.IsBridgeUser(user)
		r.mu.Lock()
		r.client = client
		r.connected = true
		r.usingBridge = bridge
		r.reconnectDelay = presence.ReconnectBaseMs
		r.lastSentHash = ""
		r.lastSentAt = 0
		r.mu.Unlock()

		if user != nil {
			if bridge {
				r.log.Log("connected to Discord IPC via arRPC bridge (%s)", user.Username)
			} else {
				r.log.Log("connected to Discord IPC as %s", user.Username)
			}
		} else {
			r.log.Log("connected to Discord IPC")
		}

		r.triggerPush()

		// Wait until disconnect or shutdown
		select {
		case <-r.stopCh:
			return
		case absent := <-r.reconnectCh:
			r.mu.Lock()
			waitMs, nextDelay := presence.ReconnectPlan(r.reconnectDelay, absent)
			r.reconnectDelay = nextDelay
			r.mu.Unlock()
			select {
			case <-r.stopCh:
				return
			case <-time.After(time.Duration(waitMs) * time.Millisecond):
			}
		}
	}
}

func (r *Runner) resolvePresence() (state.SessionState, config.Config, *scanner.Aggregate) {
	r.mu.Lock()
	cfg := r.cfg
	agg := r.agg
	live := r.liveSessions
	agents := r.agents
	displayedID := r.displayedID
	r.mu.Unlock()

	now := time.Now().UnixMilli()
	idleMs := int64(cfg.IdleThresholdSec) * 1000
	if idleMs <= 0 {
		idleMs = 60_000
	}

	// Only agents allowed on Discord (Settings → Agents) drive the card.
	sessions := state.ListSessions(presence.DiscordListOptions(cfg))
	live = presence.FilterLiveForDiscord(live, cfg)
	picked := presence.PickActiveSession(sessions, displayedID, now, idleMs)

	var st state.SessionState
	if picked.State != nil {
		st = *picked.State
		r.mu.Lock()
		r.displayedID = picked.SessionID
		r.mu.Unlock()
	} else {
		st = state.Read("")
	}

	// Filter out transcripts belonging to an agent whose OS process is confirmed closed.
	var filteredLive []state.LiveSession
	for _, ls := range live {
		if ls.Source == "claude" && agents.Claude != nil && !*agents.Claude {
			continue
		}
		if ls.Source != "claude" && agents.Antigravity != nil && !*agents.Antigravity {
			continue
		}
		filteredLive = append(filteredLive, ls)
	}
	st.LiveSessions = filteredLive
	lc := picked.LiveCount
	st.LiveCount = &lc
	if agents.Any != nil {
		st.AgentProcessAlive = agents.Any
	}
	st.AntigravityActive, st.ClaudeActive = presence.ComputeActiveSources(sessions, filteredLive, agents.Antigravity, agents.Claude, cfg, now)
	st.SourcesComputed = true
	st.AgySession, st.ClaudeSession = presence.PickDualSnapshots(sessions, filteredLive, cfg, now)

	// Enrich live session tokens & model from transcript
	if st.TranscriptPath != "" {
		if toks, mdl, ok := scanner.ReadSessionTokensAndModel(st.TranscriptPath); ok {
			if toks.Input+toks.Output+toks.CacheRead+toks.CacheWrite > 0 {
				st.Tokens = toks
			}
			if mdl != "" {
				st.Model = mdl
				if st.Source == "claude" && st.ClaudeSession != nil {
					st.ClaudeSession.Model = mdl
				} else if st.Source != "claude" && st.AgySession != nil {
					st.AgySession.Model = mdl
				}
			}
		}
	}

	st = format.ApplyIdle(st, cfg)
	st = format.ApplyShipped(st, cfg)
	st = format.ApplyTrigger(st, cfg)
	if presence.IsDualWorking(st) {
		st.Status = "dual"
	}
	st = privacy.Apply(st, cfg)

	return st, cfg, agg
}

func BuildActivity(st state.SessionState, cfg config.Config, agg *scanner.Aggregate, cursor *presence.RotationCursor) (*discord.Activity, bool) {
	if !pause.Until("").IsZero() {
		return nil, true
	}
	if st.Status == "stale" && cfg.IsHideWhenStale() {
		return nil, true
	}

	vars := format.BuildVars(st, cfg, agg)
	now := time.Now().UnixMilli()
	rotMs := int64(cfg.RotationIntervalMs)
	if rotMs <= 0 {
		rotMs = 12_000
	}

	var frame config.Frame
	var statusLargeText string
	if st.Status == "trigger" && st.TriggerFrame != nil {
		frame = config.Frame{
			Details:        st.TriggerFrame.Details,
			State:          st.TriggerFrame.State,
			LargeImageText: st.TriggerFrame.LargeImageText,
		}
	} else {
		rawFrames, lt := presence.PickFrames(cfg.Presence, st.Status)
		statusLargeText = lt
		frame = presence.SelectFrame(rawFrames, vars, st.Status, cursor, rotMs, format.FramePasses, now)
	}

	appName := presence.ResolveAppName(st, cfg)

	details := clampDiscord(format.FillTemplate(frame.Details, vars))
	stateLine := clampDiscord(format.FillTemplate(frame.State, vars))
	largeKey := presence.ResolveDiscordImageKey(cfg, st.Source, st.Status, st.Model)

	largeTextTpl := frame.LargeImageText
	if largeTextTpl == "" {
		largeTextTpl = statusLargeText
	}
	if largeTextTpl == "" {
		largeTextTpl = cfg.Presence.LargeImageText
	}
	largeText := clampDiscord(format.FillTemplate(largeTextTpl, vars))
	smallKey := format.FillTemplate(cfg.Presence.SmallImageKey, vars)
	smallText := clampDiscord(format.FillTemplate(cfg.Presence.SmallImageText, vars))

	act := &discord.Activity{
		Name:           appName,
		Type:           cfg.ActivityType,
		Details:        details,
		State:          stateLine,
		LargeImageKey:  largeKey,
		LargeImageText: largeText,
		SmallImageKey:  smallKey,
		SmallImageText: smallText,
	}

	if cfg.IsShowElapsed() && st.SessionStart > 0 && st.Status != "stale" {
		act.StartTimestamp = st.SessionStart / 1000
	}

	if cfg.IsShowPartySize() {
		concurrent := len(st.LiveSessions)
		if st.LiveCount != nil && *st.LiveCount > concurrent {
			concurrent = *st.LiveCount
		}
		if concurrent > 1 {
			act.PartyID = "vibe-rpc-sessions"
			act.PartySize = concurrent
			act.PartyMax = concurrent
		}
	}

	// Buttons (max 2)
	var btns []discord.Button
	hasCustomGithub := false
	for _, b := range cfg.Presence.Buttons {
		if strings.Contains(strings.ToLower(b.URL), "github.com") {
			hasCustomGithub = true
			break
		}
	}
	if !hasCustomGithub && presence.ShouldShowGithubButton(cfg, st) {
		if gh := gitinfo.GithubURL(st.Cwd); gh != "" {
			btns = append(btns, discord.Button{Label: "View on GitHub →", URL: gh})
		}
	}
	for _, b := range cfg.Presence.Buttons {
		if len(btns) >= 2 {
			break
		}
		lbl := format.FillTemplate(b.Label, vars)
		u := format.FillTemplate(b.URL, vars)
		if lbl != "" && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			btns = append(btns, discord.Button{Label: clampLabel(lbl), URL: u})
		}
	}
	if len(btns) > 0 {
		act.Buttons = btns
	}
	return act, false
}

func clampDiscord(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 1 {
		return s + " "
	}
	if len(s) > 128 {
		return s[:125] + "..."
	}
	return s
}

func clampLabel(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 32 {
		return s[:29] + "..."
	}
	return s
}

func activityHash(act *discord.Activity, clear bool) string {
	if clear || act == nil {
		return "CLEAR"
	}
	b, _ := json.Marshal(act)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (r *Runner) pushNow() {
	st, cfg, agg := r.resolvePresence()

	// Side-effects: desktop notify & webhooks on status transitions
	r.mu.Lock()
	prev := r.prevStatus
	r.prevStatus = st.Status
	cursorCopy := r.cursor
	r.mu.Unlock()

	if prev != "" && prev != st.Status {
		proj := notify.SanitizeLabel(format.HumanProject(st.Cwd))
		if proj == "" {
			proj = cfg.AppName
		}
		// With VibeNotch up, prompts show in the notch instead of a banner.
		if notify.ShouldNotify(cfg.Notify, prev, st.Status) && !(cfg.Notch.Enabled && notch.Running()) {
			notify.Desktop(cfg.AppName+" · Waiting on you", "Input needed in "+proj)
		}
		if notify.ShouldWebhook(cfg.Webhook, prev, st.Status) {
			notify.PostWebhook(cfg.Webhook.URL, map[string]any{
				"event":   st.Status,
				"source":  st.Source,
				"project": proj,
				"model":   st.Model,
				"ts":      time.Now().UnixMilli(),
			})
		}
	}

	act, clear := BuildActivity(st, cfg, agg, &cursorCopy)

	r.mu.Lock()
	r.cursor = cursorCopy
	if !r.connected || r.client == nil {
		r.mu.Unlock()
		return
	}

	now := time.Now().UnixMilli()
	h := activityHash(act, clear)

	gapMs := int64(cfg.MinActivityGapMs)
	if gapMs <= 0 {
		gapMs = 4_000
	}
	winMs := int64(cfg.ActivityWindowMs)
	if winMs <= 0 {
		winMs = 20_000
	}
	maxW := cfg.MaxActivityWrites
	if maxW <= 0 {
		maxW = 4
	}
	keepSec := desktopKeepaliveSec
	if r.usingBridge {
		keepSec = bridgeKeepaliveSec
	}
	if cfg.PresenceKeepaliveSec != nil && *cfg.PresenceKeepaliveSec > 0 {
		keepSec = *cfg.PresenceKeepaliveSec
	}

	dec := presence.ThrottleDecision(presence.ThrottleInput{
		Hash:         h,
		LastSentHash: r.lastSentHash,
		LastSentAt:   r.lastSentAt,
		Now:          now,
		GapMs:        gapMs,
		FlushPending: r.flushPending,
		RecentSends:  r.recentSends,
		WindowMs:     winMs,
		MaxPerWindow: maxW,
		KeepaliveMs:  int64(keepSec) * 1000,
	})

	switch dec.Action {
	case "skip":
		r.mu.Unlock()
		return
	case "defer":
		r.pendingActivity = act
		r.pendingClear = clear
		r.pendingHash = h
		if !r.flushPending {
			r.flushPending = true
			wait := time.Duration(dec.WaitMs) * time.Millisecond
			if wait < 100*time.Millisecond {
				wait = 100 * time.Millisecond
			}
			r.flushTimer = time.AfterFunc(wait, r.flushDeferred)
		}
		r.mu.Unlock()
		return
	case "send":
		r.mu.Unlock()
		r.sendActivity(act, clear, h)
	}
}

func (r *Runner) flushDeferred() {
	r.mu.Lock()
	r.flushPending = false
	act := r.pendingActivity
	clear := r.pendingClear
	h := r.pendingHash
	r.pendingActivity = nil
	r.pendingClear = false
	r.pendingHash = ""
	r.mu.Unlock()
	if h != "" {
		r.sendActivity(act, clear, h)
	}
}

func (r *Runner) sendActivity(act *discord.Activity, clear bool, hash string) {
	r.mu.Lock()
	client := r.client
	connected := r.connected
	r.mu.Unlock()
	if !connected || client == nil {
		return
	}

	var err error
	if clear || act == nil {
		err = client.ClearActivity()
	} else {
		err = client.SetActivity(*act)
	}
	if err != nil {
		r.log.Log("SET_ACTIVITY error: %v", err)
		return
	}

	now := time.Now().UnixMilli()
	r.mu.Lock()
	r.lastSentHash = hash
	r.lastSentAt = now
	cutoff := now - 60_000
	kept := r.recentSends[:0]
	for _, ts := range r.recentSends {
		if ts > cutoff {
			kept = append(kept, ts)
		}
	}
	r.recentSends = append(kept, now)
	r.mu.Unlock()
}

func (r *Runner) cleanup() {
	r.mu.Lock()
	if r.flushTimer != nil {
		r.flushTimer.Stop()
	}
	client := r.client
	r.connected = false
	r.client = nil
	r.mu.Unlock()

	if client != nil {
		_ = client.ClearActivity()
		client.Close()
	}
	if ReadPID("") == os.Getpid() {
		_ = os.Remove(paths.PidPath())
	}
	_ = os.Remove(paths.DaemonInfoPath())
	r.log.Log("daemon stopped cleanly")
}

// ensureNotch keeps VibeNotch running while it's enabled in config. Since the
// daemon itself autostarts at login, this also brings the notch back at login.
func (r *Runner) ensureNotch() {
	r.mu.Lock()
	enabled := r.cfg.Notch.Enabled
	r.mu.Unlock()
	if !enabled || !notch.Supported() || notch.Running() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if app, err := notch.Launch(install.StableBinaryPath(exe)); err == nil {
		r.log.Log("launched VibeNotch (%s)", app)
	}
}
