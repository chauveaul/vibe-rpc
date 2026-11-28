package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/card"
	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/daemon"
	"github.com/chauveaul/vibe-rpc/internal/detect"
	"github.com/chauveaul/vibe-rpc/internal/doctor"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/install"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/notch"
	"github.com/chauveaul/vibe-rpc/internal/notify"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/presence"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/privacy"
	"github.com/chauveaul/vibe-rpc/internal/proc"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
	"github.com/chauveaul/vibe-rpc/internal/stats"
)

const DefaultPort = 47475

//go:embed assets/* assets/gifs/*
var assetFS embed.FS

// BuiltinGifs lists all embedded animated Discord GIFs (Antigravity, Dual Wielding, and Claude Code).
var BuiltinGifs = []struct {
	Key   string
	Group string // "antigravity", "dual", "claude"
	Label string
	File  string
}{
	{"working", "antigravity", "Working", "agy-working.gif"},
	{"thinking", "antigravity", "Thinking", "agy-thinking.gif"},
	{"compacting", "antigravity", "Compacting", "agy-compacting.gif"},
	{"shipped", "antigravity", "Shipped", "agy-shipped.gif"},
	{"notification", "antigravity", "Waiting", "agy-notification.gif"},
	{"idle", "antigravity", "Idle", "agy-idle.gif"},
	{"stale", "antigravity", "Away", "agy-stale.gif"},
	{"dual", "dual", "Dual", "agy-dual.gif"},
	{"working", "claude", "Working", "claude-working.gif"},
	{"thinking", "claude", "Thinking", "claude-thinking.gif"},
	{"compacting", "claude", "Compacting", "claude-compacting.gif"},
	{"shipped", "claude", "Shipped", "claude-shipped.gif"},
	{"notification", "claude", "Waiting", "claude-notification.gif"},
	{"idle", "claude", "Idle", "claude-idle.gif"},
	{"stale", "claude", "Away", "claude-stale.gif"},
	{"working", "opencode", "Working", "opencode-working.gif"},
	{"thinking", "opencode", "Thinking", "opencode-thinking.gif"},
	{"compacting", "opencode", "Compacting", "opencode-compacting.gif"},
	{"shipped", "opencode", "Shipped", "opencode-shipped.gif"},
	{"notification", "opencode", "Waiting", "opencode-notification.gif"},
	{"idle", "opencode", "Idle", "opencode-idle.gif"},
	{"stale", "opencode", "Away", "opencode-stale.gif"},
	{"working", "codex", "Working", "codex-working.gif"},
	{"thinking", "codex", "Thinking", "codex-thinking.gif"},
	{"compacting", "codex", "Compacting", "codex-compacting.gif"},
	{"shipped", "codex", "Shipped", "codex-shipped.gif"},
	{"notification", "codex", "Waiting", "codex-notification.gif"},
	{"idle", "codex", "Idle", "codex-idle.gif"},
	{"stale", "codex", "Away", "codex-stale.gif"},
}

// ExportGifs writes all embedded Antigravity animated GIFs to dir.
func ExportGifs(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, g := range BuiltinGifs {
		b, err := assetFS.ReadFile("assets/gifs/" + g.File)
		if err != nil {
			return written, err
		}
		out := filepath.Join(dir, g.File)
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return written, err
		}
		written = append(written, out)
	}
	return written, nil
}

var (
	dashTpl     = template.Must(template.ParseFS(assetFS, "assets/dashboard.html"))
	settingsTpl = template.Must(template.ParseFS(assetFS, "assets/settings.html"))
	wrappedTpl  = template.Must(template.ParseFS(assetFS, "assets/wrapped.html"))
)

func Port() int {
	if raw := os.Getenv("VIBE_RPC_PORT"); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return DefaultPort
}

func isAllowedHost(host string) bool {
	if host == "" {
		return true
	}
	h := host
	if strings.HasPrefix(h, "[") {
		if idx := strings.Index(h, "]"); idx != -1 {
			h = h[1:idx]
		}
	} else if idx := strings.LastIndex(h, ":"); idx != -1 {
		h = h[:idx]
	}
	h = strings.ToLower(h)
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "example.com" // example.com used by httptest
}

func sameOriginOK(r *http.Request) bool {
	orig := r.Header.Get("Origin")
	if orig == "" {
		return true
	}
	u, err := url.Parse(orig)
	if err != nil {
		return false
	}
	return isAllowedHost(u.Host)
}

func guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && !sameOriginOK(r) {
			http.Error(w, "cross-origin POST blocked", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func loadLive() (config.Config, *scanner.Aggregate, state.SessionState) {
	cfg := config.Load()
	agg := scanner.ReadAggregate()
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	// Mirrors the daemon: the live card only shows agents allowed on Discord.
	sessions := state.ListSessions(presence.DiscordListOptions(cfg))
	idleMs := int64(cfg.IdleThresholdSec) * 1000
	if idleMs <= 0 {
		idleMs = 60_000
	}
	now := time.Now().UnixMilli()
	picked := presence.PickActiveSession(sessions, "", now, idleMs)
	var st state.SessionState
	if picked.State != nil {
		st = *picked.State
	} else {
		st = state.Read("")
	}
	var agyProc, claudeProc *bool
	if cfg.IsProcessDetection() {
		ag := proc.Detect(cfg.Sources.WantAntigravity(), cfg.Sources.WantClaude())
		agyProc, claudeProc = ag.Antigravity, ag.Claude
		st.AgentProcessAlive = ag.Any
	}
	st.AntigravityActive, st.ClaudeActive = presence.ComputeActiveSources(sessions, nil, agyProc, claudeProc, cfg, now)
	st.SourcesComputed = true
	st.AgySession, st.ClaudeSession = presence.PickDualSnapshots(sessions, nil, cfg, now)
	st = format.ApplyIdle(st, cfg)
	st = format.ApplyShipped(st, cfg)
	st = format.ApplyTrigger(st, cfg)
	if presence.IsDualWorking(st) {
		st.Status = "dual"
	}
	return cfg, agg, st
}

// NewHandler builds the complete HTTP router for dashboard, settings, wrapped, and APIs.
func NewHandler() http.Handler {
	mux := http.NewServeMux()

	// Embedded CSS assets (no JS!)
	mux.HandleFunc("GET /assets/{file}", guard(func(w http.ResponseWriter, r *http.Request) {
		file := filepath.Base(r.PathValue("file"))
		if !strings.HasSuffix(file, ".css") {
			http.NotFound(w, r)
			return
		}
		b, err := assetFS.ReadFile("assets/" + file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}))

	// Embedded Antigravity animated GIFs
	mux.HandleFunc("GET /assets/gifs/{file}", guard(func(w http.ResponseWriter, r *http.Request) {
		file := filepath.Base(r.PathValue("file"))
		if !strings.HasSuffix(file, ".gif") {
			http.NotFound(w, r)
			return
		}
		b, err := assetFS.ReadFile("assets/gifs/" + file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/gif")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(b)
	}))

	// SSR HTML pages
	mux.HandleFunc("GET /{$}", guard(handleDashboard))
	mux.HandleFunc("GET /settings", guard(handleSettingsGet))
	mux.HandleFunc("POST /settings/save", guard(handleSettingsSave))
	mux.HandleFunc("POST /settings/gif/pick", guard(handleSettingsGifPick))
	mux.HandleFunc("POST /settings/frames/add", guard(handleSettingsFrameAdd))
	mux.HandleFunc("POST /settings/frames/delete", guard(handleSettingsFrameDelete))
	mux.HandleFunc("POST /settings/buttons/add", guard(handleSettingsButtonAdd))
	mux.HandleFunc("POST /settings/buttons/delete", guard(handleSettingsButtonDelete))
	mux.HandleFunc("POST /settings/triggers/add", guard(handleSettingsTriggerAdd))
	mux.HandleFunc("POST /settings/triggers/delete", guard(handleSettingsTriggerDelete))
	mux.HandleFunc("POST /settings/webhook/test", guard(handleSettingsWebhookTest))
	mux.HandleFunc("POST /settings/claude-statusline", guard(handleSettingsClaudeStatusLine))
	mux.HandleFunc("POST /settings/agy-statusline", guard(handleSettingsAgyStatusLine))
	mux.HandleFunc("POST /settings/daemon", guard(handleSettingsDaemon))
	mux.HandleFunc("POST /settings/privacy", guard(handleSettingsPrivacy))
	mux.HandleFunc("GET /wrapped", guard(handleWrapped))

	// JSON / SVG / CSV API endpoints
	mux.HandleFunc("GET /api/state", guard(handleAPIState))
	mux.HandleFunc("GET /api/aggregate", guard(handleAPIAggregate))
	mux.HandleFunc("GET /api/insights", guard(handleAPIInsights))
	mux.HandleFunc("GET /api/quotas", guard(handleAPIQuotas))
	mux.HandleFunc("GET /api/badge.svg", guard(handleAPIBadge))
	mux.HandleFunc("GET /api/card.svg", guard(handleAPICard))
	mux.HandleFunc("GET /api/card.png", guard(handleAPICardPNG))
	mux.HandleFunc("GET /api/heatmap.svg", guard(handleAPIHeatmap))
	mux.HandleFunc("GET /api/heatmap.png", guard(handleAPIHeatmapPNG))
	mux.HandleFunc("GET /api/session-card.svg", guard(handleAPISessionCard))
	mux.HandleFunc("GET /api/session-card.png", guard(handleAPISessionCardPNG))
	mux.HandleFunc("GET /api/export.json", guard(handleAPIAggregate))
	mux.HandleFunc("GET /api/export.csv", guard(handleAPIExportCSV))
	mux.HandleFunc("GET /api/settings", guard(handleAPISettings))
	mux.HandleFunc("GET /api/workspaces", guard(handleAPIWorkspaces))
	mux.HandleFunc("GET /api/log", guard(handleAPILog))
	mux.HandleFunc("GET /api/project/{name}", guard(handleAPIProject))
	mux.HandleFunc("GET /api/day/{day}", guard(handleAPIDay))

	return mux
}

// IsRunningAt reports whether a vibe-rpc dashboard already answers at addr,
// so `vibe-rpc serve` can reuse it instead of failing to bind.
func IsRunningAt(addr string) bool {
	c := &http.Client{Timeout: 2 * time.Second} // /api/state scans processes; allow for a busy machine
	resp, err := c.Get("http://" + addr + "/api/state")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var probe struct {
		State json.RawMessage `json:"state"`
		Vars  json.RawMessage `json:"vars"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&probe) == nil && len(probe.State) > 0 && len(probe.Vars) > 0
}

func Serve(addr string) error {
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", Port())
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

// ── Dashboard SSR ─────────────────────────────────────────────────────────────

type rangePill struct {
	Key    string
	Label  string
	Active bool
}

type heatCell struct {
	Day     string
	Label   string // e.g. "Thu Nov 26, 2026"
	Level   string
	Hours   string
	Prompts int
	Future  bool // placeholder after today in the current week
}

type modelRow struct {
	Name string
	Cost string
	Pct  int
}

type langRow struct {
	Name  string
	Edits int
	Pct   int
	Color string
}

type lbRow struct {
	Name  string
	Full  string
	Value string
}

type achView struct {
	Icon     string
	Title    string
	Sub      string
	Unlocked bool
}

type rotView struct {
	Details string
	State   string
	Passing bool
}

type dayModalView struct {
	Day      string
	Hours    string
	Sessions int
	Prompts  int
	Tools    int
	Tokens   string
	Cost     string
	Added    string
	Removed  string
	Ships    int
}

type projDrawerView struct {
	Name     string
	Hours    string
	Sessions int
	Prompts  int
	Tools    int
	Tokens   string
	Cost     string
	Added    string
	Removed  string
}

var langPalette = []string{"#f5f5f7", "#d1d5db", "#9ca3af", "#6b7280", "#4b5563", "#374151"}

func renderActivitySVG(rows []stats.WeekRow) template.HTML {
	const w, h = 480.0, 110.0
	if len(rows) == 0 {
		return template.HTML(`<svg class="chart" viewBox="0 0 480 110"><text x="240" y="60" text-anchor="middle" font-family="JetBrains Mono,monospace" font-size="11" fill="#8c9099">No activity yet</text></svg>`)
	}
	var maxMs int64 = 1
	for _, r := range rows {
		if r.ActiveMs > maxMs {
			maxMs = r.ActiveMs
		}
	}
	var pts []string
	var dots strings.Builder
	step := w / math.Max(1, float64(len(rows)-1))
	for i, r := range rows {
		x := float64(i) * step
		y := h - 8 - (float64(r.ActiveMs)/float64(maxMs))*(h-20)
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
		if r.ActiveMs > 0 {
			fmt.Fprintf(&dots, `<circle cx="%.1f" cy="%.1f" r="3.2" fill="#f5f5f7" stroke="#0c0d10" stroke-width="1.5"><title>%s: %s (%d prompts)</title></circle>`,
				x, y, html.EscapeString(r.Day), html.EscapeString(format.FmtHours(r.ActiveMs)), r.Prompts)
		}
	}
	poly := strings.Join(pts, " ")
	area := fmt.Sprintf("0,%.1f %s %.1f,%.1f", h, poly, w, h)
	svg := fmt.Sprintf(`<svg class="chart" viewBox="0 0 480 110" preserveAspectRatio="none">
  <defs><linearGradient id="areaGrad" x1="0" y1="0" x2="0" y2="1"><stop offset="0%%" stop-color="#ffffff" stop-opacity="0.22"/><stop offset="100%%" stop-color="#ffffff" stop-opacity="0.01"/></linearGradient></defs>
  <line x1="0" y1="30" x2="480" y2="30" stroke="#ffffff" stroke-dasharray="2 4" stroke-width="1" opacity="0.08"/>
  <line x1="0" y1="70" x2="480" y2="70" stroke="#ffffff" stroke-dasharray="2 4" stroke-width="1" opacity="0.08"/>
  <polygon points="%s" fill="url(#areaGrad)"/>
  <polyline points="%s" fill="none" stroke="#e5e7eb" stroke-width="2.2" stroke-linejoin="round" stroke-linecap="round"/>
  %s
</svg>`, area, poly, dots.String())
	return template.HTML(svg)
}

func renderChurnSVG(agg *scanner.Aggregate) template.HTML {
	rows := stats.WeekGrid(agg, 21)
	maxL := 1
	for _, r := range rows {
		if agg != nil && agg.ByDay[r.Day] != nil {
			d := agg.ByDay[r.Day]
			if d.LinesAdded > maxL {
				maxL = d.LinesAdded
			}
			if d.LinesRemoved > maxL {
				maxL = d.LinesRemoved
			}
		}
	}
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 420 64" preserveAspectRatio="none"><line x1="0" y1="32" x2="420" y2="32" stroke="#ffffff" opacity="0.14" stroke-width="1"/>`)
	for i, r := range rows {
		x := i*20 + 3
		var add, rem int
		if agg != nil && agg.ByDay[r.Day] != nil {
			add = agg.ByDay[r.Day].LinesAdded
			rem = agg.ByDay[r.Day].LinesRemoved
		}
		if add > 0 {
			ah := math.Max(3, float64(add)/float64(maxL)*28)
			fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="13" height="%.1f" rx="3" fill="#e5e7eb" opacity="0.9"><title>%s: +%d lines</title></rect>`, x, 32-ah, ah, r.Day, add)
		}
		if rem > 0 {
			rh := math.Max(3, float64(rem)/float64(maxL)*28)
			fmt.Fprintf(&b, `<rect x="%d" y="33" width="13" height="%.1f" rx="3" fill="#6b7280" opacity="0.8"><title>%s: -%d lines</title></rect>`, x, rh, r.Day, rem)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func topMapRows(m map[string]int, limit int, pretty func(string) string) []lbRow {
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range m {
		if v > 0 {
			list = append(list, kv{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v == list[j].v {
			return list[i].k < list[j].k
		}
		return list[i].v > list[j].v
	})
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]lbRow, 0, len(list))
	for _, item := range list {
		name := item.k
		if pretty != nil {
			name = pretty(item.k)
		}
		out = append(out, lbRow{Name: name, Full: item.k, Value: format.FmtNum(int64(item.v))})
	}
	return out
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg, agg, st := loadLive()
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "30d"
	}
	sum := stats.SummarizeRange(agg, rng)
	vars := format.BuildVars(st, cfg, agg)
	cursor := &presence.RotationCursor{}
	act, _ := daemon.BuildActivity(st, cfg, agg, cursor)

	ranges := []rangePill{
		{Key: "7d", Label: "7D", Active: rng == "7d"},
		{Key: "30d", Label: "30D", Active: rng == "30d"},
		{Key: "90d", Label: "90D", Active: rng == "90d"},
		{Key: "1y", Label: "1Y", Active: rng == "1y"},
		{Key: "all", Label: "All", Active: rng == "all"},
	}
	rangeTitles := map[string]string{"7d": "Last 7 days", "30d": "Last 30 days", "90d": "Last 90 days", "1y": "Last year", "all": "All time"}
	rangeTitle := rangeTitles[rng]
	if rangeTitle == "" {
		rangeTitle = "Last 30 days"
	}

	// Live preview
	liveApp := cfg.AppName
	liveDet := "Away — no active session"
	liveState := "Open " + agentsOr(cfg) + " to start tracking"
	liveImg := presence.ResolveLargeImageKeyForSource(cfg, st.Source, st.Status, st.Model)
	if act != nil {
		liveApp = act.Name
		if act.Details != "" {
			liveDet = act.Details
		}
		if act.State != "" {
			liveState = act.State
		}
	}

	// Heatmap: rolling 12 months, week columns aligned Sunday→Saturday
	heat := buildYearHeatmap(agg, time.Now())

	// Models
	var models []modelRow
	for i, m := range agg.ModelSplit {
		if i >= 6 {
			break
		}
		models = append(models, modelRow{
			Name: format.HumanModel(m.Model),
			Cost: pricing.FmtCost(m.Cost),
			Pct:  int(math.Round(m.CostPct * 100)),
		})
	}

	// Languages
	type lkv struct {
		name  string
		edits int
	}
	var langList []lkv
	totalLangEdits := 0
	for k, v := range agg.Languages {
		if v != nil && v.Edits > 0 {
			langList = append(langList, lkv{k, v.Edits})
			totalLangEdits += v.Edits
		}
	}
	sort.Slice(langList, func(i, j int) bool { return langList[i].edits > langList[j].edits })
	if len(langList) > 6 {
		langList = langList[:6]
	}
	langs := make([]langRow, 0, len(langList))
	for i, l := range langList {
		pct := 0
		if totalLangEdits > 0 {
			pct = int(math.Round(float64(l.edits) / float64(totalLangEdits) * 100))
		}
		langs = append(langs, langRow{Name: l.name, Edits: l.edits, Pct: pct, Color: langPalette[i%len(langPalette)]})
	}

	// Projects leaderboard
	type pkv struct {
		name string
		ms   int64
	}
	var plist []pkv
	for n, ps := range agg.Projects {
		if ps != nil {
			plist = append(plist, pkv{n, ps.ActiveMs})
		}
	}
	sort.Slice(plist, func(i, j int) bool { return plist[i].ms > plist[j].ms })
	if len(plist) > 8 {
		plist = plist[:8]
	}
	topProjs := make([]lbRow, 0, len(plist))
	for _, p := range plist {
		topProjs = append(topProjs, lbRow{Name: p.name, Full: p.name, Value: format.FmtHours(p.ms)})
	}

	// Files leaderboard
	topFiles := make([]lbRow, 0, 8)
	for i, f := range agg.TopEditedFiles {
		if i >= 8 {
			break
		}
		topFiles = append(topFiles, lbRow{Name: filepath.Base(f.Path), Full: f.Path, Value: format.FmtNum(int64(f.Count))})
	}

	// Achievements
	allTok := agg.InputTokens + agg.OutputTokens + agg.CacheReadTokens + agg.CacheWriteTokens
	agySrc := agg.BySource["antigravity"]
	ccSrc := agg.BySource["claude"]
	if agySrc == nil {
		agySrc = &scanner.SourceStats{}
	}
	if ccSrc == nil {
		ccSrc = &scanner.SourceStats{}
	}
	achs := []achView{
		{Icon: "🚀", Title: "Liftoff", Sub: "First session", Unlocked: agg.Sessions >= 1},
		{Icon: "🔥", Title: "7-Day Streak", Sub: "7 consecutive days", Unlocked: agg.LongestStreak >= 7},
		{Icon: "⚡", Title: "Token Millionaire", Sub: "1M+ tokens", Unlocked: allTok >= 1_000_000},
		{Icon: "🚢", Title: "Shipper", Sub: "10+ commits/pushes", Unlocked: agg.Ships >= 10},
		{Icon: "🤝", Title: "Dual Agent", Sub: "Agy + Claude Code", Unlocked: agySrc.Sessions > 0 && ccSrc.Sessions > 0},
		{Icon: "🏆", Title: "Centurion", Sub: "100+ hours coded", Unlocked: agg.ActiveMs >= 100*3_600_000},
	}
	unlocked := 0
	for _, a := range achs {
		if a.Unlocked {
			unlocked++
		}
	}

	// Rotation frames
	rawFrames, _ := presence.PickFrames(cfg.Presence, st.Status)
	rotViews := make([]rotView, 0, len(rawFrames))
	for _, f := range rawFrames {
		rotViews = append(rotViews, rotView{
			Details: format.FillTemplate(f.Details, vars),
			State:   format.FillTemplate(f.State, vars),
			Passing: format.FramePasses(f, vars),
		})
	}

	// Optional Day modal or Project drawer
	var selDay *dayModalView
	if dayQ := r.URL.Query().Get("day"); dayQ != "" {
		d := agg.ByDay[dayQ]
		if d == nil {
			d = &scanner.DayBucket{}
		}
		dt := d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
		selDay = &dayModalView{
			Day: dayQ, Hours: format.FmtHours(d.ActiveMs), Sessions: d.Sessions,
			Prompts: d.UserMessages, Tools: d.ToolCalls, Tokens: format.FmtNum(dt),
			Cost: pricing.FmtCost(d.Cost), Added: format.FmtNum(int64(d.LinesAdded)),
			Removed: format.FmtNum(int64(d.LinesRemoved)), Ships: d.Ships,
		}
	}
	var selProj *projDrawerView
	if projQ := r.URL.Query().Get("project"); projQ != "" {
		if ps := agg.Projects[projQ]; ps != nil {
			selProj = &projDrawerView{
				Name: projQ, Hours: format.FmtHours(ps.ActiveMs), Sessions: ps.Sessions,
				Prompts: ps.UserMessages, Tools: ps.ToolCalls, Tokens: format.FmtNum(ps.InputTokens + ps.OutputTokens),
				Cost: pricing.FmtCost(ps.Cost), Added: format.FmtNum(int64(ps.LinesAdded)),
				Removed: format.FmtNum(int64(ps.LinesRemoved)),
			}
		}
	}

	cachePct := "0%"
	if sum.Tokens > 0 {
		cachePct = fmt.Sprintf("%d%%", int(math.Round(float64(sum.CacheTokens)/float64(sum.Tokens)*100)))
	}
	todayB := agg.ByDay[scanner.DayKey(time.Now().UnixMilli())]
	todayCost := "$0.00"
	if todayB != nil {
		todayCost = pricing.FmtCost(todayB.Cost)
	}

	cmp := buildCompare(agg, rng, trackedAgents(cfg))
	quotas := buildDashboardQuotas(
		cfg, agg,
		limits.Load(), install.ClaudeStatusLineState(""),
		limits.LoadAntigravity(), install.AgyStatusLineState(""),
		time.Now(),
	)

	data := map[string]any{
		"Version":          paths.Version,
		"AutoRefresh":      selDay == nil && selProj == nil,
		"Range":            rng,
		"Ranges":           ranges,
		"RangeTitle":       rangeTitle,
		"Status":           st.Status,
		"StatusLabel":      format.StatusVerbose(st.Status, format.HumanTool(st.CurrentTool), 0),
		"LiveAvatar":       liveImg,
		"LiveAppName":      liveApp,
		"LiveDetails":      liveDet,
		"LiveState":        liveState,
		"AgentsOr":         agentsOr(cfg),
		"LiveSource":       vars["sourcePretty"],
		"LiveModel":        vars["modelPretty"],
		"LiveElapsed":      vars["duration"],
		"Insights":         stats.GenerateInsights(agg),
		"HeroHours":        format.FmtHours(sum.ActiveMs),
		"HeroCaption":      fmt.Sprintf("%s prompts · %s tool calls · %s tokens", format.FmtNum(int64(sum.Prompts)), format.FmtNum(int64(sum.ToolCalls)), format.FmtNum(sum.Tokens)),
		"RangeSessions":    sum.Sessions,
		"RangePrompts":     sum.Prompts,
		"ActivityChartSVG": renderActivitySVG(sum.DailySeries),
		"StatTokens":       format.FmtNum(sum.Tokens),
		"StatCachePct":     cachePct,
		"StatFreshTokens":  format.FmtNum(sum.FreshTokens),
		"StatCost":         pricing.FmtCost(sum.Cost),
		"TodayCost":        todayCost,
		"AllCost":          pricing.FmtCost(agg.EstimatedCost),
		"AllTokens":        format.FmtNum(allTok),
		"Streak":           agg.Streak,
		"LongestStreak":    agg.LongestStreak,
		"DaysSinceFirst":   agg.DaysSinceFirst,
		"Compare":          cmp,
		"Quotas":           quotas,
		"Running":          listRunning(cfg, st.SessionID),
		"Heatmap":          heat,
		"Models":           models,
		"Languages":        langs,
		"ChurnSparkSVG":    renderChurnSVG(agg),
		"LinesAdded":       format.FmtNum(int64(sum.LinesAdded)),
		"LinesRemoved":     format.FmtNum(int64(sum.LinesRemoved)),
		"LinesNet":         fmt.Sprintf("%+d", sum.LinesAdded-sum.LinesRemoved),
		"RangeShips":       sum.Ships,
		"Achievements":     achs,
		"UnlockedCount":    unlocked,
		"TopProjects":      topProjs,
		"TopTools":         topMapRows(agg.ToolBreakdown, 8, format.HumanTool),
		"TopFiles":         topFiles,
		"TopShell":         topMapRows(agg.BashCommands, 8, nil),
		"TopDomains":       topMapRows(agg.WebDomains, 8, nil),
		"TopSubagents":     topMapRows(agg.Subagents, 8, nil),
		"RotationFrames":   rotViews,
		"SelectedDay":      selDay,
		"SelectedProject":  selProj,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashTpl.Execute(w, data)
}

// ── Settings SSR + POST PRG Handlers ──────────────────────────────────────────

type statusTab struct {
	Key    string
	Label  string
	Active bool
}

type frameEditView struct {
	Index    int
	Details  string
	State    string
	Requires string
}

type agentTab struct {
	Key    string
	Label  string
	Active bool
}

var previewAgents = []struct{ Key, Label string }{
	{"antigravity", "Antigravity"}, {"claude", "Claude Code"}, {"dual", "Dual Wielding"},
}

func validAgent(a string) string {
	for _, x := range previewAgents {
		if x.Key == a {
			return a
		}
	}
	return ""
}

func agentTabs(sel string) []agentTab {
	out := make([]agentTab, 0, len(previewAgents))
	for _, a := range previewAgents {
		out = append(out, agentTab{Key: a.Key, Label: a.Label, Active: a.Key == sel})
	}
	return out
}

// previewImageInput names the form field whose value the live-preview script
// mirrors into the avatar, or "" when the shown GIF isn't edited on this tab.
func previewImageInput(agent, selStatus, effStatus string) string {
	switch {
	case effStatus != selStatus:
		return ""
	case agent == "claude" && selStatus != "dual":
		return "claude_status_image"
	default:
		return "status_image"
	}
}

// isClaudeFamily reports whether a model id is an Anthropic model. Stats store
// normalized ids ("opus-5-5"), so match on family names, not a "claude-" prefix.
func isClaudeFamily(model string) bool {
	m := strings.ToLower(model)
	for _, fam := range []string{"claude", "opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(m, fam) {
			return true
		}
	}
	return false
}

// topModelFor returns the most-used model id for a source from lifetime stats.
func topModelFor(agg *scanner.Aggregate, source string) string {
	if agg != nil {
		for _, m := range agg.ModelSplit {
			if (source == "claude") == isClaudeFamily(m.Model) {
				return m.Model
			}
		}
	}
	if source == "claude" {
		return "claude-opus-4-6"
	}
	return "gemini-4-neon-high"
}

// buildPreviewState shapes a session the way the daemon would see it for the
// chosen agent, and returns the status the daemon would actually display
// (busy statuses become "dual" when both agents are working).
func buildPreviewState(live state.SessionState, agg *scanner.Aggregate, status, agent string) (state.SessionState, string) {
	st := live
	st.Status = status
	st.SourcesComputed = true
	if st.Cwd == "" {
		st.Cwd, _ = os.Getwd()
	}
	if st.CurrentTool == "" {
		st.CurrentTool = "edit_file"
		st.CurrentFile = "main.go"
	}
	modelFor := func(src string, snap *state.SessionSnapshot) string {
		if snap != nil && snap.Model != "" {
			return snap.Model
		}
		if live.Source == src && live.Model != "" {
			return live.Model
		}
		return topModelFor(agg, src)
	}
	// Each agent's snapshot carries the previewed status, so non-busy statuses
	// (idle, shipped, waiting) stay single cards just like in the daemon.
	busy := status
	if status == "dual" {
		busy = "working"
	}
	switch agent {
	case "claude":
		st.Source, st.Model = "claude", modelFor("claude", live.ClaudeSession)
		st.AntigravityActive, st.ClaudeActive = false, true
		st.AgySession, st.ClaudeSession = nil, nil
	case "dual":
		st.Source, st.Model = "antigravity", modelFor("antigravity", live.AgySession)
		st.AntigravityActive, st.ClaudeActive = true, true
		agyFile, ccFile := "server.go", "app.go"
		if live.AgySession != nil && live.AgySession.CurrentFile != "" {
			agyFile = live.AgySession.CurrentFile
		}
		if live.ClaudeSession != nil && live.ClaudeSession.CurrentFile != "" {
			ccFile = live.ClaudeSession.CurrentFile
		}
		st.AgySession = &state.SessionSnapshot{Cwd: st.Cwd, Model: st.Model, CurrentFile: agyFile, Status: busy}
		st.ClaudeSession = &state.SessionSnapshot{Cwd: st.Cwd, Model: modelFor("claude", live.ClaudeSession), CurrentFile: ccFile, Status: busy}
	default:
		st.Source, st.Model = "antigravity", modelFor("antigravity", live.AgySession)
		st.AntigravityActive, st.ClaudeActive = true, false
		st.AgySession, st.ClaudeSession = nil, nil
	}
	if status == "dual" {
		// The Dual tab always previews both agents working together.
		st.AntigravityActive, st.ClaudeActive = true, true
		if st.AgySession == nil {
			st.AgySession = &state.SessionSnapshot{Cwd: st.Cwd, Model: modelFor("antigravity", live.AgySession), CurrentFile: "server.go", Status: "working"}
		}
		if st.ClaudeSession == nil {
			st.ClaudeSession = &state.SessionSnapshot{Cwd: st.Cwd, Model: modelFor("claude", live.ClaudeSession), CurrentFile: "app.go", Status: "working"}
		}
		return st, "dual"
	}
	if presence.IsDualWorking(st) {
		return st, "dual"
	}
	return st, status
}

type triggerEditView struct {
	Index          int
	Match          string
	Details        string
	State          string
	LargeImageText string
}

type webhookEventView struct {
	Key   string
	Label string
	On    bool
}

// webhookEvents are the status transitions a webhook can fire on.
var webhookEvents = []struct{ Key, Label string }{
	{"shipped", "Shipped"}, {"notification", "Waiting on input"}, {"working", "Working"},
	{"thinking", "Thinking"}, {"compacting", "Compacting"}, {"idle", "Idle"},
	{"stale", "Away"}, {"dual", "Dual wielding"}, {"trigger", "Trigger matched"},
}

// fmtNum renders a numeric setting for an input, leaving 0 (off) blank.
func fmtNum(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// monthToDateCost sums cost for the current calendar month.
// agentsOr names the tracked agents as "A, B or C" for empty-state copy.
func agentsOr(cfg config.Config) string {
	if names := config.JoinAgentNames(trackedAgents(cfg), "or"); names != "" {
		return names
	}
	return "a coding agent"
}

func monthToDateCost(agg *scanner.Aggregate) float64 {
	if agg == nil {
		return 0
	}
	pfx := scanner.DayKey(time.Now().UnixMilli())[:7]
	var mtd float64
	for k, d := range agg.ByDay {
		if strings.HasPrefix(k, pfx) && d != nil {
			mtd += d.Cost
		}
	}
	return mtd
}

type btnEditView struct {
	Index int
	Label string
	URL   string
}

type wsView struct {
	Name       string
	Path       string
	Visibility string
}

type varSample struct {
	Key   string
	Value string
}

func validStatus(s string) string {
	for _, st := range config.Statuses {
		if st == s {
			return s
		}
	}
	return "working"
}

func discoverWorkspaces(cfg config.Config) []wsView {
	cwds := state.ReadSessionCwds("")
	visMap := privacy.ListVisibility()
	seen := map[string]bool{}
	var pathsList []string
	for _, c := range cwds {
		if c != "" && !seen[c] {
			seen[c] = true
			pathsList = append(pathsList, c)
		}
	}
	for p := range visMap {
		if p != "" && !seen[p] {
			seen[p] = true
			pathsList = append(pathsList, p)
		}
	}
	sort.Strings(pathsList)
	out := make([]wsView, 0, len(pathsList))
	for _, p := range pathsList {
		res := privacy.Resolve(p, cfg)
		out = append(out, wsView{
			Name:       format.HumanProject(p),
			Path:       p,
			Visibility: res.Visibility,
		})
	}
	return out
}

func readLogTail(maxBytes int) string {
	b, err := os.ReadFile(paths.LogPath())
	if err != nil {
		return "(no daemon log yet)"
	}
	if len(b) > maxBytes {
		b = b[len(b)-maxBytes:]
	}
	return string(b)
}

func handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg, agg, liveSt := loadLive()
	selStatus := validStatus(r.URL.Query().Get("status"))

	// Daemon info
	pid := daemon.AlivePID()
	logTail := readLogTail(8192)
	ipcState, _ := doctor.IPCStateFromLog(logTail)
	dDot, dTitle, dSub := "down", "Daemon stopped", "Click Start or run `vibe-rpc start`"
	if pid > 0 {
		if ipcState == "connected" {
			dDot = "up"
			dTitle = fmt.Sprintf("Running & connected to Discord (pid %d)", pid)
			dSub = "Watching " + config.JoinAgentNames(trackedAgents(cfg), "and") + " sessions live"
		} else {
			dDot = "wait"
			dTitle = fmt.Sprintf("Running — waiting for Discord (pid %d)", pid)
			dSub = "Open Discord desktop (or Vesktop/arRPC) and it connects automatically"
		}
	}

	labels := map[string]string{
		"working": "Working", "thinking": "Thinking", "compacting": "Compacting",
		"shipped": "Shipped", "notification": "Waiting", "idle": "Idle", "dual": "Dual",
	}
	tabs := make([]statusTab, 0, len(config.Statuses))
	for _, k := range config.Statuses {
		tabs = append(tabs, statusTab{Key: k, Label: labels[k], Active: k == selStatus})
	}

	selAgent := validAgent(r.URL.Query().Get("agent"))
	if selAgent == "" {
		selAgent = "antigravity"
		if liveSt.Source == "claude" {
			selAgent = "claude"
		}
	}
	stTpl := cfg.Presence.ByStatus[selStatus]
	previewSt, effStatus := buildPreviewState(liveSt, agg, selStatus, selAgent)
	effTpl := cfg.Presence.ByStatus[effStatus]
	previewSource := selAgent
	if selAgent == "dual" {
		previewSource = "antigravity"
	}
	previewApp := presence.ResolveAppName(previewSt, cfg)
	vars := format.BuildVars(previewSt, cfg, agg)

	frames := make([]frameEditView, 0, len(stTpl.Rotation))
	for i, f := range stTpl.Rotation {
		frames = append(frames, frameEditView{
			Index:    i,
			Details:  f.Details,
			State:    f.State,
			Requires: strings.Join(f.Requires, ","),
		})
	}
	btns := make([]btnEditView, 0, len(cfg.Presence.Buttons))
	for i, b := range cfg.Presence.Buttons {
		btns = append(btns, btnEditView{Index: i, Label: b.Label, URL: b.URL})
	}

	sampleKeys := []string{
		"project", "modelPretty", "sourcePretty", "sourcesActiveLabel",
		"dualTargets", "dualModels", "dualProjects", "dualFiles",
		"agyFile", "claudeFile", "agyModel", "claudeModel",
		"currentToolPretty", "currentFilePretty", "fileLang", "gitBranch",
		"messagesLabel", "tokensLabel", "duration", "todayHours", "weekHours",
		"allHours", "allTokensFmt", "allCostFmt", "streakLabel", "linesNetFmt",
	}
	var samples []varSample
	for _, k := range sampleKeys {
		samples = append(samples, varSample{Key: k, Value: fmt.Sprint(vars[k])})
	}

	type gifView struct {
		Label  string
		Path   string
		Active bool
	}
	curImg := cfg.StatusAssets[selStatus]
	curClaudeImg := ""
	if cfg.ClaudeStatusAssets != nil {
		curClaudeImg = cfg.ClaudeStatusAssets[selStatus]
	}
	curOCImg := ""
	if cfg.OpenCodeStatusAssets != nil {
		curOCImg = cfg.OpenCodeStatusAssets[selStatus]
	}
	curCXImg := ""
	if cfg.CodexStatusAssets != nil {
		curCXImg = cfg.CodexStatusAssets[selStatus]
	}
	var agyGifs, claudeGifs, ocGifs, cxGifs, dualGifs []gifView
	for _, g := range BuiltinGifs {
		p := "/assets/gifs/" + g.File
		switch g.Group {
		case "antigravity":
			agyGifs = append(agyGifs, gifView{Label: g.Label, Path: p, Active: curImg == p})
			dualGifs = append(dualGifs, gifView{Label: "Agy " + g.Label, Path: p, Active: curImg == p})
		case "claude":
			claudeGifs = append(claudeGifs, gifView{Label: g.Label, Path: p, Active: curClaudeImg == p})
		case "opencode":
			ocGifs = append(ocGifs, gifView{Label: g.Label, Path: p, Active: curOCImg == p})
		case "codex":
			cxGifs = append(cxGifs, gifView{Label: g.Label, Path: p, Active: curCXImg == p})
		case "dual":
			// Place Dual Wielding GIF first in the Dual gallery
			dualGifs = append([]gifView{{Label: "Dual Wield", Path: p, Active: curImg == p}}, dualGifs...)
		}
	}

	trigs := make([]triggerEditView, 0, len(cfg.Triggers))
	for i, t := range cfg.Triggers {
		trigs = append(trigs, triggerEditView{Index: i, Match: t.Match, Details: t.Details, State: t.State, LargeImageText: t.LargeImageText})
	}
	whOn := map[string]bool{}
	for _, e := range cfg.Webhook.On {
		whOn[e] = true
	}
	whEvents := make([]webhookEventView, 0, len(webhookEvents))
	for _, e := range webhookEvents {
		whEvents = append(whEvents, webhookEventView{Key: e.Key, Label: e.Label, On: whOn[e.Key]})
	}
	warnPct := cfg.Budget.WarnAtPct
	if warnPct <= 0 {
		warnPct = 80
	}
	mtd := monthToDateCost(agg)
	budgetPct := 0
	if cfg.Budget.Monthly > 0 {
		budgetPct = int(math.Round(mtd / cfg.Budget.Monthly * 100))
	}
	budgetBar := budgetPct
	if budgetBar > 100 {
		budgetBar = 100
	}
	budgetLevel := "ok"
	if cfg.Budget.Monthly > 0 && budgetPct >= 100 {
		budgetLevel = "over"
	} else if cfg.Budget.Monthly > 0 && budgetPct >= warnPct {
		budgetLevel = "warn"
	}

	varsJSONBytes, _ := json.Marshal(vars)
	// Prevent accidental </script> breakout inside JSON data block
	varsJSONSafe := strings.ReplaceAll(string(varsJSONBytes), "</", `<\/`)

	data := map[string]any{
		"Saved":               r.URL.Query().Get("saved") == "1",
		"Error":               r.URL.Query().Get("err"),
		"ConfigPath":          paths.ConfigPath(),
		"LogPath":             paths.LogPath(),
		"LogTail":             logTail,
		"DaemonDot":           dDot,
		"DaemonTitle":         dTitle,
		"DaemonSub":           dSub,
		"AgentsOr":            agentsOr(cfg),
		"Status":              selStatus,
		"StatusTabs":          tabs,
		"PreviewAppName":      previewApp,
		"PreviewImage":        presence.ResolveLargeImageKeyForSource(cfg, previewSource, effStatus, previewSt.Model),
		"PreviewDetails":      format.FillTemplate(effTpl.Details, vars),
		"PreviewState":        format.FillTemplate(effTpl.State, vars),
		"Agent":               selAgent,
		"AgentTabs":           agentTabs(selAgent),
		"PreviewBecameDual":   effStatus == "dual" && selStatus != "dual",
		"PreviewImageInput":   previewImageInput(selAgent, selStatus, effStatus),
		"PreviewAppLive":      !previewSt.ClaudeActive || !cfg.IsDynamicAppName(),
		"PreviewVarsJSON":     template.JS(varsJSONSafe),
		"StatusDetails":       stTpl.Details,
		"StatusState":         stTpl.State,
		"StatusImage":         curImg,
		"ClaudeStatusImage":   curClaudeImg,
		"StatusLargeText":     stTpl.LargeImageText,
		"BuiltinAgyGifs":      agyGifs,
		"BuiltinClaudeGifs":   claudeGifs,
		"BuiltinOpenCodeGifs": ocGifs,
		"BuiltinCodexGifs":    cxGifs,
		"CodexStatusImage":    curCXImg,
		"ShowCodexGifs":       cfg.Sources.Tracks("codex"),
		"OpenCodeStatusImage": curOCImg,
		"ShowOpenCodeGifs":    cfg.Sources.Tracks("opencode"),
		"DualGifs":            dualGifs,
		"Frames":              frames,
		"Buttons":             btns,
		"VarSamples":          samples,
		"Cfg":                 cfg,
		"RotationSec":         cfg.RotationIntervalMs / 1000,
		"Workspaces":          discoverWorkspaces(cfg),
		"Notice":              r.URL.Query().Get("notice"),
		"Agents":              agentRows(cfg),
		"NotchSupported":      notch.Supported(),
		"NotchInstalled":      notch.Find(exeOrEmpty()) != "",
		"NotchRunning":        notch.Running(),
		"ClaudeStatusLine":    install.ClaudeStatusLineState(""),
		"AgyStatusLine":       install.AgyStatusLineState(""),
		"GoalDailyHours":      fmtNum(cfg.Goals.DailyHours),
		"GoalDailyPrompts":    fmtNum(float64(cfg.Goals.DailyPrompts)),
		"GoalWeeklyHours":     fmtNum(cfg.Goals.WeeklyHours),
		"GoalLabel":           fmt.Sprint(vars["goalLabel"]),
		"BudgetMonthly":       fmtNum(cfg.Budget.Monthly),
		"BudgetWarnPct":       warnPct,
		"BudgetMTD":           pricing.FmtCost(mtd),
		"BudgetPct":           budgetPct,
		"BudgetBar":           budgetBar,
		"BudgetLevel":         budgetLevel,
		"WebhookEvents":       whEvents,
		"Triggers":            trigs,
		"TriggerFrameSec":     cfg.TriggerFrameSec,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = settingsTpl.Execute(w, data)
}

func parseSettingsForm(r *http.Request, base config.Config) (config.Config, string) {
	_ = r.ParseForm()
	edited := base.Clone()
	selStatus := validStatus(r.FormValue("status"))

	if edited.Presence.ByStatus == nil {
		edited.Presence.ByStatus = map[string]config.StatusTemplate{}
	}
	if edited.StatusAssets == nil {
		edited.StatusAssets = map[string]string{}
	}
	stTpl := edited.Presence.ByStatus[selStatus]
	stTpl.Details = r.FormValue("status_details")
	stTpl.State = r.FormValue("status_state")
	stTpl.LargeImageText = r.FormValue("status_large_text")

	if img := strings.TrimSpace(r.FormValue("status_image")); img != "" {
		edited.StatusAssets[selStatus] = img
	}
	if xImg := strings.TrimSpace(r.FormValue("codex_status_image")); xImg != "" && selStatus != "dual" {
		if edited.CodexStatusAssets == nil {
			edited.CodexStatusAssets = map[string]string{}
		}
		edited.CodexStatusAssets[selStatus] = xImg
	}
	if oImg := strings.TrimSpace(r.FormValue("opencode_status_image")); oImg != "" && selStatus != "dual" {
		if edited.OpenCodeStatusAssets == nil {
			edited.OpenCodeStatusAssets = map[string]string{}
		}
		edited.OpenCodeStatusAssets[selStatus] = oImg
	}
	if cImg := strings.TrimSpace(r.FormValue("claude_status_image")); cImg != "" && selStatus != "dual" {
		if edited.ClaudeStatusAssets == nil {
			edited.ClaudeStatusAssets = map[string]string{}
		}
		edited.ClaudeStatusAssets[selStatus] = cImg
	}

	// Frames
	if fc, err := strconv.Atoi(r.FormValue("frame_count")); err == nil && fc >= 0 {
		var frames []config.Frame
		for i := 0; i < fc; i++ {
			det := strings.TrimSpace(r.FormValue(fmt.Sprintf("frame_details_%d", i)))
			st := strings.TrimSpace(r.FormValue(fmt.Sprintf("frame_state_%d", i)))
			reqRaw := strings.TrimSpace(r.FormValue(fmt.Sprintf("frame_requires_%d", i)))
			var reqs config.Requires
			if reqRaw != "" {
				for _, part := range strings.Split(reqRaw, ",") {
					if p := strings.TrimSpace(part); p != "" {
						reqs = append(reqs, p)
					}
				}
			}
			frames = append(frames, config.Frame{Details: det, State: st, Requires: reqs})
		}
		stTpl.Rotation = frames
	}
	edited.Presence.ByStatus[selStatus] = stTpl

	// Discord & sources
	if v := strings.TrimSpace(r.FormValue("appName")); v != "" {
		edited.AppName = v
	}
	if v := strings.TrimSpace(r.FormValue("clientId")); v != "" {
		edited.ClientID = v
	}
	edited.GifBaseURL = strings.TrimSpace(r.FormValue("gifBaseUrl"))
	if v, err := strconv.Atoi(r.FormValue("activityType")); err == nil {
		edited.ActivityType = v
	}
	edited.ShowElapsed = config.BoolPtr(r.FormValue("showElapsed") == "on")
	edited.Presence.GithubButton = config.BoolPtr(r.FormValue("githubButton") == "on")
	if r.FormValue("agents_section") == "1" {
		on := func(k string) *bool { return config.BoolPtr(r.FormValue(k) == "on") }
		edited.Sources = config.SourcesConfig{Antigravity: on("track_antigravity"), Claude: on("track_claude"), OpenCode: on("track_opencode"), Codex: on("track_codex")}
		edited.DiscordSources = config.DiscordSourcesConfig{Antigravity: on("discord_antigravity"), Claude: on("discord_claude"), OpenCode: on("discord_opencode"), Codex: on("discord_codex")}
	} else if r.FormValue("sourceAntigravity") != "" || r.FormValue("sourceClaude") != "" || r.FormValue("sourceOpenCode") != "" {
		// Older forms with plain "Track …" checkboxes.
		edited.Sources = config.SourcesConfig{
			Antigravity: config.BoolPtr(r.FormValue("sourceAntigravity") == "on"),
			Claude:      config.BoolPtr(r.FormValue("sourceClaude") == "on"),
			OpenCode:    config.BoolPtr(r.FormValue("sourceOpenCode") == "on"),
		}
	}
	edited.DynamicAppName = config.BoolPtr(r.FormValue("dynamicAppName") == "on")

	// Buttons
	if bc, err := strconv.Atoi(r.FormValue("btn_count")); err == nil && bc >= 0 {
		var btns []config.Button
		for i := 0; i < bc && len(btns) < 2; i++ {
			lbl := strings.TrimSpace(r.FormValue(fmt.Sprintf("btn_label_%d", i)))
			u := strings.TrimSpace(r.FormValue(fmt.Sprintf("btn_url_%d", i)))
			if lbl != "" || u != "" {
				btns = append(btns, config.Button{Label: lbl, URL: u})
			}
		}
		edited.Presence.Buttons = btns
	}

	// Behaviour
	if v, err := strconv.Atoi(r.FormValue("rotationSec")); err == nil && v >= 4 {
		edited.RotationIntervalMs = v * 1000
	}
	if v, err := strconv.Atoi(r.FormValue("idleThresholdSec")); err == nil && v >= 0 {
		edited.IdleThresholdSec = v
	}
	if v, err := strconv.Atoi(r.FormValue("staleSessionMin")); err == nil && v >= 0 {
		edited.StaleSessionMin = v
	}
	if v, err := strconv.Atoi(r.FormValue("shippedFrameSec")); err == nil && v >= 0 {
		edited.ShippedFrameSec = v
	}
	edited.HideWhenStale = config.BoolPtr(r.FormValue("hideWhenStale") == "on")
	edited.Notify.Enabled = r.FormValue("notifyEnabled") == "on"
	edited.ProcessDetection = config.BoolPtr(r.FormValue("processDetection") == "on")

	// Goals, budget, webhook & triggers. Gated on a sentinel so posts from
	// forms that don't render this section never wipe these settings.
	if r.FormValue("automation_section") == "1" {
		parseAutomation(r, &edited)
	}
	if r.FormValue("notch_section") == "1" {
		edited.Notch.Enabled = r.FormValue("notchEnabled") == "on"
		finish := r.FormValue("notchFinishAlerts") == "on"
		edited.Notch.FinishAlerts = &finish
		limitReset := r.FormValue("notchLimitResetAlerts") == "on"
		edited.Notch.LimitResetAlerts = &limitReset
	}

	return edited, selStatus
}

// formFloat parses a numeric field: blank means 0 (off), garbage keeps cur.
func formFloat(r *http.Request, name string, cur float64) float64 {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return cur
}

func parseAutomation(r *http.Request, edited *config.Config) {
	edited.Goals.DailyHours = formFloat(r, "goalDailyHours", edited.Goals.DailyHours)
	edited.Goals.DailyPrompts = int(math.Round(formFloat(r, "goalDailyPrompts", float64(edited.Goals.DailyPrompts))))
	edited.Goals.WeeklyHours = formFloat(r, "goalWeeklyHours", edited.Goals.WeeklyHours)
	edited.Budget.Monthly = formFloat(r, "budgetMonthly", edited.Budget.Monthly)
	edited.Budget.WarnAtPct = int(math.Round(formFloat(r, "budgetWarnPct", float64(edited.Budget.WarnAtPct))))

	edited.Webhook.URL = strings.TrimSpace(r.FormValue("webhookUrl"))
	known := map[string]bool{}
	for _, e := range webhookEvents {
		known[e.Key] = true
	}
	var on []string
	for _, e := range edited.Webhook.On { // keep hand-written events the UI doesn't list
		if !known[e] {
			on = append(on, e)
		}
	}
	picked := map[string]bool{}
	for _, e := range r.Form["webhookOn"] {
		picked[e] = true
	}
	for _, e := range webhookEvents {
		if picked[e.Key] {
			on = append(on, e.Key)
		}
	}
	edited.Webhook.On = on

	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("triggerFrameSec"))); err == nil && v >= 0 {
		edited.TriggerFrameSec = v
	}
	if tc, err := strconv.Atoi(r.FormValue("trigger_count")); err == nil && tc >= 0 {
		var trigs []config.TriggerConfig
		for i := 0; i < tc; i++ {
			t := config.TriggerConfig{
				Match:          strings.TrimSpace(r.FormValue(fmt.Sprintf("trigger_match_%d", i))),
				Details:        strings.TrimSpace(r.FormValue(fmt.Sprintf("trigger_details_%d", i))),
				State:          strings.TrimSpace(r.FormValue(fmt.Sprintf("trigger_state_%d", i))),
				LargeImageText: strings.TrimSpace(r.FormValue(fmt.Sprintf("trigger_large_%d", i))),
			}
			if t.Match == "" && t.Details == "" && t.State == "" && t.LargeImageText == "" {
				continue // fully blank row = removed
			}
			trigs = append(trigs, t)
		}
		edited.Triggers = trigs
	}
}

func redirectSettings(w http.ResponseWriter, r *http.Request, status string, saved bool, err error) {
	q := url.Values{"status": {status}}
	if a := validAgent(r.FormValue("agent")); a != "" {
		q.Set("agent", a)
	}
	if err != nil {
		q.Set("err", err.Error())
	} else if saved {
		q.Set("saved", "1")
	}
	http.Redirect(w, r, "/settings?"+q.Encode(), http.StatusSeeOther)
}

func handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	if err == nil && edited.Notch.Enabled != base.Notch.Enabled {
		err = applyNotchToggle(edited.Notch.Enabled)
	}
	redirectSettings(w, r, selStatus, err == nil, err)
}

// applyNotchToggle launches or quits VibeNotch right away when the setting
// changes (the daemon also keeps it running while enabled).
var applyNotchToggle = func(on bool) error {
	if !notch.Supported() {
		return nil
	}
	if !on {
		notch.Quit()
		return nil
	}
	if notch.Running() {
		return nil
	}
	_, err := notch.Launch(exeOrEmpty())
	if errors.Is(err, notch.ErrNotInstalled) {
		return fmt.Errorf("Saved, but VibeNotch.app isn't installed yet. Install it with: vibe-rpc notch install")
	}
	return err
}

func exeOrEmpty() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return install.StableBinaryPath(exe)
}

// handleSettingsClaudeStatusLine makes vibe-rpc Claude Code's status line
// (the only way Claude Code shares its 5-hour/weekly limits), or removes it.
func handleSettingsClaudeStatusLine(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	selStatus := validStatus(r.FormValue("status"))
	var err error
	if r.FormValue("action") == "remove" {
		_, err = install.UninstallClaudeStatusLine("")
	} else {
		_, err = install.InstallClaudeStatusLine("", exeOrEmpty())
	}
	redirectSettings(w, r, selStatus, err == nil, err)
}

// handleSettingsAgyStatusLine wires vibe-rpc into Antigravity CLI's status line
// (stacked with the default bar) to capture real 5-hour/weekly quotas, or removes it.
func handleSettingsAgyStatusLine(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	selStatus := validStatus(r.FormValue("status"))
	var err error
	if r.FormValue("action") == "remove" {
		_, err = install.UninstallAgyStatusLine("")
	} else {
		_, err = install.InstallAgyStatusLine("", exeOrEmpty())
	}
	redirectSettings(w, r, selStatus, err == nil, err)
}

func handleSettingsGifPick(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	pick := strings.TrimSpace(r.FormValue("gif_pick"))
	if pick != "" {
		target, gifPath, ok := strings.Cut(pick, ":")
		if ok && strings.HasPrefix(gifPath, "/assets/gifs/") {
			if target == "claude" && selStatus != "dual" {
				if edited.ClaudeStatusAssets == nil {
					edited.ClaudeStatusAssets = map[string]string{}
				}
				edited.ClaudeStatusAssets[selStatus] = gifPath
			} else if target == "codex" && selStatus != "dual" {
				if edited.CodexStatusAssets == nil {
					edited.CodexStatusAssets = map[string]string{}
				}
				edited.CodexStatusAssets[selStatus] = gifPath
			} else if target == "opencode" && selStatus != "dual" {
				if edited.OpenCodeStatusAssets == nil {
					edited.OpenCodeStatusAssets = map[string]string{}
				}
				edited.OpenCodeStatusAssets[selStatus] = gifPath
			} else {
				if edited.StatusAssets == nil {
					edited.StatusAssets = map[string]string{}
				}
				edited.StatusAssets[selStatus] = gifPath
			}
		}
	} else if gifPath := strings.TrimSpace(r.FormValue("gif_path")); strings.HasPrefix(gifPath, "/assets/gifs/") {
		if edited.StatusAssets == nil {
			edited.StatusAssets = map[string]string{}
		}
		edited.StatusAssets[selStatus] = gifPath
	}
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	redirectSettings(w, r, selStatus, err == nil, err)
}

func handleSettingsFrameAdd(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	stTpl := edited.Presence.ByStatus[selStatus]
	stTpl.Rotation = append(stTpl.Rotation, config.Frame{Details: "{allHours} coding", State: "{streakLabel}"})
	edited.Presence.ByStatus[selStatus] = stTpl
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	redirectSettings(w, r, selStatus, err == nil, err)
}

func handleSettingsFrameDelete(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	idx, err := strconv.Atoi(r.FormValue("delete_idx"))
	if err == nil {
		stTpl := edited.Presence.ByStatus[selStatus]
		if idx >= 0 && idx < len(stTpl.Rotation) {
			stTpl.Rotation = append(stTpl.Rotation[:idx], stTpl.Rotation[idx+1:]...)
			edited.Presence.ByStatus[selStatus] = stTpl
		}
	}
	_, saveErr := config.SaveDiff(paths.ConfigPath(), base, edited)
	redirectSettings(w, r, selStatus, saveErr == nil, saveErr)
}

func handleSettingsButtonAdd(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	if len(edited.Presence.Buttons) < 2 {
		edited.Presence.Buttons = append(edited.Presence.Buttons, config.Button{Label: "My Website", URL: "https://github.com"})
	}
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	redirectSettings(w, r, selStatus, err == nil, err)
}

func handleSettingsButtonDelete(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	idx, err := strconv.Atoi(r.FormValue("delete_btn_idx"))
	if err == nil && idx >= 0 && idx < len(edited.Presence.Buttons) {
		edited.Presence.Buttons = append(edited.Presence.Buttons[:idx], edited.Presence.Buttons[idx+1:]...)
	}
	_, saveErr := config.SaveDiff(paths.ConfigPath(), base, edited)
	redirectSettings(w, r, selStatus, saveErr == nil, saveErr)
}

func handleSettingsTriggerAdd(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	edited.Triggers = append(edited.Triggers, config.TriggerConfig{
		Match: `^(npm|pnpm|yarn|go) test`, Details: "Running tests in {project}", State: "Fingers crossed",
	})
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	redirectSettings(w, r, selStatus, err == nil, err)
}

func handleSettingsTriggerDelete(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	if idx, err := strconv.Atoi(r.FormValue("delete_trigger_idx")); err == nil && idx >= 0 && idx < len(edited.Triggers) {
		edited.Triggers = append(edited.Triggers[:idx], edited.Triggers[idx+1:]...)
	}
	_, err := config.SaveDiff(paths.ConfigPath(), base, edited)
	redirectSettings(w, r, selStatus, err == nil, err)
}

// handleSettingsWebhookTest saves the form, then sends a sample event to the
// webhook URL synchronously so the result can be shown in the banner.
func handleSettingsWebhookTest(w http.ResponseWriter, r *http.Request) {
	base := config.Load()
	edited, selStatus := parseSettingsForm(r, base)
	if _, err := config.SaveDiff(paths.ConfigPath(), base, edited); err != nil {
		redirectSettings(w, r, selStatus, false, err)
		return
	}
	if edited.Webhook.URL == "" {
		redirectSettings(w, r, selStatus, false, fmt.Errorf("Add a webhook URL first"))
		return
	}
	code, err := notify.PostWebhookSync(edited.Webhook.URL, map[string]any{
		"event": "test", "source": "vibe-rpc", "project": "vibe-rpc", "model": "", "test": true,
		"ts": time.Now().UnixMilli(),
	})
	if err != nil {
		redirectSettings(w, r, selStatus, false, fmt.Errorf("Test webhook failed: %v", err))
		return
	}
	q := url.Values{"status": {selStatus}, "notice": {fmt.Sprintf("Test webhook delivered (HTTP %d)", code)}}
	if a := validAgent(r.FormValue("agent")); a != "" {
		q.Set("agent", a)
	}
	http.Redirect(w, r, "/settings?"+q.Encode(), http.StatusSeeOther)
}

func handleSettingsDaemon(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	selStatus := validStatus(r.FormValue("status"))
	action := r.FormValue("action")
	var err error
	switch action {
	case "start":
		daemon.ClearStopped()
		if daemon.AlivePID() == 0 {
			_, err = daemon.SpawnDetached()
		}
	case "stop":
		daemon.MarkStopped()
		daemon.StopAndWait()
	case "restart":
		daemon.ClearStopped()
		daemon.StopAndWait()
		_, err = daemon.SpawnDetached()
	}
	redirectSettings(w, r, selStatus, err == nil, err)
}

func handleSettingsPrivacy(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	selStatus := validStatus(r.FormValue("status"))
	cwd := r.FormValue("cwd")
	vis := r.FormValue("visibility")
	_, err := privacy.SetCwdVisibility(cwd, vis)
	redirectSettings(w, r, selStatus, err == nil, err)
}

// ── Wrapped SSR ───────────────────────────────────────────────────────────────

type wrappedBar struct {
	Index int
	Class string
}

func handleWrapped(w http.ResponseWriter, r *http.Request) {
	_, agg, _ := loadLive()
	const totalSlides = 7
	idx, _ := strconv.Atoi(r.URL.Query().Get("slide"))
	if idx < 0 {
		idx = 0
	}
	if idx >= totalSlides {
		idx = totalSlides - 1
	}

	bars := make([]wrappedBar, totalSlides)
	for i := 0; i < totalSlides; i++ {
		cls := ""
		if i < idx {
			cls = "done"
		} else if i == idx {
			cls = "active"
		}
		bars[i] = wrappedBar{Index: i, Class: cls}
	}

	allTok := agg.InputTokens + agg.OutputTokens + agg.CacheReadTokens + agg.CacheWriteTokens
	active := format.ActiveAgents(agg)
	var split []string // "Antigravity 12h", one per agent with sessions
	for _, a := range active {
		split = append(split, config.AgentName(a)+" "+format.FmtHours(agg.BySource[a].ActiveMs))
	}

	data := map[string]any{
		"Bars":        bars,
		"SlideNum":    idx + 1,
		"TotalSlides": totalSlides,
		"HasPrev":     idx > 0,
		"PrevSlide":   idx - 1,
		"HasNext":     idx < totalSlides-1,
		"NextSlide":   idx + 1,
		"FigureClass": "huge",
		"Version":     paths.Version,
	}

	switch idx {
	case 0:
		data["Theme"] = "ink"
		data["Badge"] = "All-time recap"
		switch len(active) {
		case 0:
			data["Kicker"] = "You coded for"
		case 1:
			data["Kicker"] = "In " + config.AgentName(active[0]) + ", you coded for"
		default:
			data["Kicker"] = "Across " + config.JoinAgentNames(active, "and") + ", you coded for"
		}
		data["Figure"] = format.FmtHours(agg.ActiveMs)
		data["Watermark"] = format.FmtHours(agg.ActiveMs)
		data["Sub"] = fmt.Sprintf("%d sessions · %s prompts · %s tool calls", agg.Sessions, format.FmtNum(int64(agg.UserMessages)), format.FmtNum(int64(agg.ToolCalls)))
	case 1:
		data["Theme"] = "rust"
		data["Kicker"] = "You burned through"
		data["Figure"] = format.FmtNum(allTok)
		data["Watermark"] = "TOK"
		data["Sub"] = fmt.Sprintf("tokens (~%s API-equivalent value)", pricing.FmtCost(agg.EstimatedCost))
	case 2:
		data["Theme"] = "gold"
		data["Kicker"] = "Your longest coding streak"
		data["Figure"] = fmt.Sprintf("%dd", agg.LongestStreak)
		data["Watermark"] = fmt.Sprintf("%d", agg.LongestStreak)
		best := "No best day recorded yet"
		if agg.BestDay != nil {
			best = fmt.Sprintf("Biggest day: %s (%s)", agg.BestDay.Day, format.FmtHours(agg.BestDay.ActiveMs))
		}
		data["Sub"] = best
	case 3:
		data["Theme"] = "blurple"
		data["Kicker"] = "Your model mix"
		data["FigureClass"] = "big"
		topM := "None yet"
		if len(agg.ModelSplit) > 0 {
			topM = format.HumanModel(agg.ModelSplit[0].Model)
		}
		data["Figure"] = topM
		var mrows []modelRow
		for i, m := range agg.ModelSplit {
			if i >= 4 {
				break
			}
			mrows = append(mrows, modelRow{Name: format.HumanModel(m.Model), Pct: int(math.Round(m.CostPct * 100))})
		}
		data["ModelRows"] = mrows
		data["Sub"] = strings.Join(split, " · ")
	case 4:
		data["Theme"] = "grass"
		data["Kicker"] = "Code you wrote & shipped"
		data["Figure"] = "+" + format.FmtNum(int64(agg.LinesAdded))
		data["Watermark"] = "+"
		data["Sub"] = fmt.Sprintf("lines added · -%s removed · %d ships", format.FmtNum(int64(agg.LinesRemoved)), agg.Ships)
	case 5:
		data["Theme"] = "plum"
		data["Kicker"] = "Your most-edited file"
		data["FigureClass"] = "big"
		topFile, sub := "None yet", "No file edits recorded yet"
		if len(agg.TopEditedFiles) > 0 {
			topFile = filepath.Base(agg.TopEditedFiles[0].Path)
			sub = fmt.Sprintf("Edited %d times · %d files touched overall", agg.TopEditedFiles[0].Count, agg.UniqueFiles)
		}
		data["Figure"] = topFile
		data["Sub"] = sub
	default:
		data["Theme"] = "ink"
		data["IsFinale"] = true
		data["Sessions"] = agg.Sessions
		data["Hours"] = format.FmtHours(agg.ActiveMs)
		data["Tokens"] = format.FmtNum(allTok)
		data["Cost"] = pricing.FmtCost(agg.EstimatedCost)
		data["LongestStreak"] = agg.LongestStreak
		data["LinesAdded"] = format.FmtNum(int64(agg.LinesAdded))
		data["Ships"] = agg.Ships
		data["AgentSplit"] = strings.Join(split, ", ")
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = wrappedTpl.Execute(w, data)
}

// ── API Endpoints ─────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func handleAPIState(w http.ResponseWriter, _ *http.Request) {
	cfg, agg, st := loadLive()
	writeJSON(w, map[string]any{
		"state": st,
		"vars":  format.BuildVars(st, cfg, agg),
		"pid":   daemon.AlivePID(),
	})
}

func handleAPIAggregate(w http.ResponseWriter, _ *http.Request) {
	_, agg, _ := loadLive()
	writeJSON(w, agg)
}

func handleAPIInsights(w http.ResponseWriter, _ *http.Request) {
	_, agg, _ := loadLive()
	writeJSON(w, map[string]any{"insights": stats.GenerateInsights(agg)})
}

func handleAPIBadge(w http.ResponseWriter, r *http.Request) {
	_, agg, _ := loadLive()
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(stats.BadgeSVG(agg, r.URL.Query().Get("metric"))))
}

func handleAPICard(w http.ResponseWriter, _ *http.Request) {
	_, agg, _ := loadLive()
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(stats.PosterCardSVG(agg)))
}

func parsePNGScale(r *http.Request) float64 {
	scale := 2.0
	if v, err := strconv.ParseFloat(r.URL.Query().Get("scale"), 64); err == nil && v >= 1 && v <= 3 {
		scale = v
	}
	return scale
}

func writePNGResponse(w http.ResponseWriter, r *http.Request, b []byte, filename string) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}
	_, _ = w.Write(b)
}

// handleAPICardPNG serves the shareable stats card as a PNG. ?scale=1|2|3
// (default 2 → 2400×1260); ?download=1 sends it as an attachment.
func handleAPICardPNG(w http.ResponseWriter, r *http.Request) {
	_, agg, _ := loadLive()
	b, err := card.PNG(card.FromAggregate(agg), parsePNGScale(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writePNGResponse(w, r, b, "vibe-rpc-wrapped.png")
}

func handleAPIHeatmap(w http.ResponseWriter, _ *http.Request) {
	_, agg, _ := loadLive()
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(stats.HeatmapSVG(agg, 91)))
}

func handleAPIHeatmapPNG(w http.ResponseWriter, r *http.Request) {
	_, agg, _ := loadLive()
	b, err := card.HeatmapPNG(agg, time.Now(), parsePNGScale(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writePNGResponse(w, r, b, "vibe-rpc-heatmap.png")
}

func handleAPISessionCard(w http.ResponseWriter, _ *http.Request) {
	_, _, st := loadLive()
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(stats.SessionCardSVG(st)))
}

func handleAPISessionCardPNG(w http.ResponseWriter, r *http.Request) {
	_, _, st := loadLive()
	b, err := card.SessionPNG(card.SessionFromState(st, time.Now()), parsePNGScale(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writePNGResponse(w, r, b, "vibe-rpc-session.png")
}

func handleAPIExportCSV(w http.ResponseWriter, _ *http.Request) {
	_, agg, _ := loadLive()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="vibe-rpc-stats.csv"`)
	_, _ = w.Write([]byte(stats.AggregateToCSV(agg)))
}

func handleAPISettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, config.Load())
}

func handleAPIWorkspaces(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, discoverWorkspaces(config.Load()))
}

func handleAPILog(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(readLogTail(16384)))
}

func handleAPIProject(w http.ResponseWriter, r *http.Request) {
	_, agg, _ := loadLive()
	name := r.PathValue("name")
	ps := agg.Projects[name]
	if ps == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, ps)
}

func handleAPIDay(w http.ResponseWriter, r *http.Request) {
	_, agg, _ := loadLive()
	day := r.PathValue("day")
	d := agg.ByDay[day]
	if d == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, d)
}

type agentRow struct {
	Key, Name, Where          string
	Installed, Track, Discord bool
}

// agentRows feeds the Settings → Agents table.
func agentRows(cfg config.Config) []agentRow {
	var rows []agentRow
	for _, a := range detect.All() {
		rows = append(rows, agentRow{
			Key: a.Key, Name: a.Name, Where: a.Where, Installed: a.Installed,
			Track: cfg.Sources.Tracks(a.Key), Discord: cfg.ShowOnDiscord(a.Key),
		})
	}
	return rows
}
