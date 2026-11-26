package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
)

func TestZeroJSHTMLPagesAndSettingsPost(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))

	h := NewHandler()

	// Verify dashboard & wrapped pages contain ZERO <script> tags, and NO page uses inline JS handlers
	for _, route := range []string{"/", "/?range=7d", "/settings", "/settings?status=idle", "/settings?status=dual", "/wrapped", "/wrapped?slide=6"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475"+route, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", route, rec.Code)
		}
		body := strings.ToLower(rec.Body.String())
		if strings.HasPrefix(route, "/wrapped") && strings.Contains(body, "<script") {
			t.Fatalf("GET %s contains forbidden <script tag!", route)
		}
		if strings.Contains(body, "onclick=") || strings.Contains(body, "onchange=") {
			t.Fatalf("GET %s contains inline JS event handler!", route)
		}
	}

	// Verify API SVG / JSON / CSV / GIF endpoints
	for _, route := range []string{"/api/state", "/api/aggregate", "/api/insights", "/api/quotas", "/api/badge.svg", "/api/card.svg", "/api/card.png", "/api/card.png?scale=1&download=1", "/api/heatmap.svg", "/api/heatmap.png?scale=1", "/api/session-card.svg", "/api/session-card.png?scale=1", "/api/export.csv", "/assets/gifs/agy-working.gif", "/assets/gifs/agy-dual.gif", "/assets/gifs/claude-working.gif"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475"+route, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", route, rec.Code)
		}
	}

	// Verify POST /settings/save persists cleanly via PRG 303 redirect
	form := url.Values{
		"status":            {"working"},
		"status_details":    {"Building {project}"},
		"status_state":      {"{currentToolPretty} · {modelPretty}"},
		"appName":           {"Antigravity Pro"},
		"clientId":          {"1575595772760559656"},
		"activityType":      {"0"},
		"showElapsed":       {"on"},
		"githubButton":      {"on"},
		"sourceAntigravity": {"on"},
		"sourceClaude":      {"on"},
		"dynamicAppName":    {"on"},
		"rotationSec":       {"15"},
		"idleThresholdSec":  {"60"},
		"staleSessionMin":   {"5"},
		"shippedFrameSec":   {"60"},
		"hideWhenStale":     {"on"},
		"processDetection":  {"on"},
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47475/settings/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://127.0.0.1:47475")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/save expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "saved=1") {
		t.Fatalf("expected saved=1 redirect, got %q", loc)
	}
	cfg := config.Load()
	if cfg.AppName != "Antigravity Pro" || cfg.RotationIntervalMs != 15000 {
		t.Fatalf("settings not persisted: appName=%q rot=%d", cfg.AppName, cfg.RotationIntervalMs)
	}
	if cfg.Presence.ByStatus["working"].Details != "Building {project}" {
		t.Fatalf("status details not persisted: got %q", cfg.Presence.ByStatus["working"].Details)
	}

	// Verify POST /settings/gif/pick actually persists the chosen GIF
	gifForm := url.Values{
		"status":            {"working"},
		"status_details":    {"Building {project}"},
		"status_state":      {"{currentToolPretty} · {modelPretty}"},
		"status_image":      {"/assets/gifs/agy-working.gif"},
		"gif_path":          {"/assets/gifs/agy-shipped.gif"},
		"appName":           {"Antigravity Pro"},
		"clientId":          {"1575595772760559656"},
		"activityType":      {"0"},
		"showElapsed":       {"on"},
		"githubButton":      {"on"},
		"sourceAntigravity": {"on"},
		"sourceClaude":      {"on"},
		"dynamicAppName":    {"on"},
		"rotationSec":       {"15"},
		"idleThresholdSec":  {"60"},
		"staleSessionMin":   {"5"},
		"shippedFrameSec":   {"60"},
		"hideWhenStale":     {"on"},
		"processDetection":  {"on"},
	}
	gifReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47475/settings/gif/pick", strings.NewReader(gifForm.Encode()))
	gifReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	gifReq.Header.Set("Origin", "http://127.0.0.1:47475")
	gifRec := httptest.NewRecorder()
	h.ServeHTTP(gifRec, gifReq)
	if gifRec.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/gif/pick expected 303, got %d", gifRec.Code)
	}
	cfgAfterGif := config.Load()
	if got := cfgAfterGif.StatusAssets["working"]; got != "/assets/gifs/agy-shipped.gif" {
		t.Fatalf("GIF pick not persisted: expected /assets/gifs/agy-shipped.gif, got %q", got)
	}

	// Verify picking a Claude-only GIF via gif_pick=claude:... persists to ClaudeStatusAssets
	ccGifForm := url.Values{
		"status":            {"working"},
		"status_details":    {"Building {project}"},
		"status_state":      {"{currentToolPretty} · {modelPretty}"},
		"gif_pick":          {"claude:/assets/gifs/claude-shipped.gif"},
		"appName":           {"Antigravity Pro"},
		"clientId":          {"1575595772760559656"},
		"activityType":      {"0"},
		"showElapsed":       {"on"},
		"githubButton":      {"on"},
		"sourceAntigravity": {"on"},
		"sourceClaude":      {"on"},
		"dynamicAppName":    {"on"},
		"rotationSec":       {"15"},
		"idleThresholdSec":  {"60"},
		"staleSessionMin":   {"5"},
		"shippedFrameSec":   {"60"},
		"hideWhenStale":     {"on"},
		"processDetection":  {"on"},
	}
	ccGifReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47475/settings/gif/pick", strings.NewReader(ccGifForm.Encode()))
	ccGifReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ccGifReq.Header.Set("Origin", "http://127.0.0.1:47475")
	ccGifRec := httptest.NewRecorder()
	h.ServeHTTP(ccGifRec, ccGifReq)
	if ccGifRec.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings/gif/pick (claude) expected 303, got %d", ccGifRec.Code)
	}
	cfgAfterCCGif := config.Load()
	if got := cfgAfterCCGif.ClaudeStatusAssets["working"]; got != "/assets/gifs/claude-shipped.gif" {
		t.Fatalf("Claude GIF pick not persisted: expected /assets/gifs/claude-shipped.gif, got %q", got)
	}

	// Verify cross-origin POST is blocked
	evil := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47475/settings/save", strings.NewReader(form.Encode()))
	evil.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	evil.Header.Set("Origin", "https://evil.example")
	recEvil := httptest.NewRecorder()
	h.ServeHTTP(recEvil, evil)
	if recEvil.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin POST, got %d", recEvil.Code)
	}
}

func postSettings(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47475"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://127.0.0.1:47475")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST %s expected 303, got %d", path, rec.Code)
	}
	return rec
}

func baseSettingsForm() url.Values {
	return url.Values{
		"status": {"working"}, "status_details": {"Working in {project}"}, "status_state": {"{modelPretty}"},
		"appName": {"Antigravity"}, "clientId": {"1575595772760559656"}, "activityType": {"0"},
		"sourceAntigravity": {"on"}, "sourceClaude": {"on"}, "rotationSec": {"12"},
		"idleThresholdSec": {"60"}, "staleSessionMin": {"5"}, "shippedFrameSec": {"60"},
	}
}

func TestSettingsAutomationSections(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	h := NewHandler()

	var gotBody string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(strings.Builder)
		_, _ = io.Copy(b, r.Body)
		gotBody = b.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()

	form := baseSettingsForm()
	form.Set("automation_section", "1")
	form.Set("goalDailyHours", "4.5")
	form.Set("goalDailyPrompts", "")
	form.Set("goalWeeklyHours", "20")
	form.Set("budgetMonthly", "150")
	form.Set("budgetWarnPct", "75")
	form.Set("webhookUrl", hook.URL)
	form["webhookOn"] = []string{"shipped", "stale"}
	form.Set("triggerFrameSec", "30")
	form.Set("trigger_count", "2")
	form.Set("trigger_match_0", "^git push")
	form.Set("trigger_details_0", "Deploying {project}")
	form.Set("trigger_state_0", "{gitBranch}")
	form.Set("trigger_match_1", "") // blank row is dropped
	if loc := postSettings(t, h, "/settings/save", form).Header().Get("Location"); !strings.Contains(loc, "saved=1") {
		t.Fatalf("save failed: %s", loc)
	}
	cfg := config.Load()
	if cfg.Goals.DailyHours != 4.5 || cfg.Goals.DailyPrompts != 0 || cfg.Goals.WeeklyHours != 20 {
		t.Fatalf("goals not persisted: %+v", cfg.Goals)
	}
	if cfg.Budget.Monthly != 150 || cfg.Budget.WarnAtPct != 75 {
		t.Fatalf("budget not persisted: %+v", cfg.Budget)
	}
	if cfg.Webhook.URL != hook.URL || strings.Join(cfg.Webhook.On, ",") != "shipped,stale" {
		t.Fatalf("webhook not persisted: %+v", cfg.Webhook)
	}
	if cfg.TriggerFrameSec != 30 || len(cfg.Triggers) != 1 || cfg.Triggers[0].Match != "^git push" {
		t.Fatalf("triggers not persisted: %d %+v", cfg.TriggerFrameSec, cfg.Triggers)
	}

	// Page renders the saved values.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475/settings", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, want := range []string{`name="goalDailyHours" value="4.5"`, `value="^git push"`, `value="stale" checked`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("settings page missing %q", want)
		}
	}

	// A form without the sentinel (older clients) must not wipe these settings.
	postSettings(t, h, "/settings/save", baseSettingsForm())
	if c := config.Load(); c.Webhook.URL != hook.URL || len(c.Triggers) != 1 || c.Goals.DailyHours != 4.5 {
		t.Fatalf("settings wiped by post without automation section: %+v", c.Webhook)
	}

	// Add then delete a trigger.
	form.Set("trigger_count", "1")
	postSettings(t, h, "/settings/triggers/add", form)
	if n := len(config.Load().Triggers); n != 2 {
		t.Fatalf("expected 2 triggers after add, got %d", n)
	}
	del := url.Values{}
	for k, v := range form {
		del[k] = v
	}
	del.Set("trigger_count", "2")
	del.Set("trigger_match_1", "^(npm|go) test")
	del.Set("delete_trigger_idx", "0")
	postSettings(t, h, "/settings/triggers/delete", del)
	if tr := config.Load().Triggers; len(tr) != 1 || tr[0].Match != "^(npm|go) test" {
		t.Fatalf("delete removed the wrong trigger: %+v", tr)
	}

	// Invalid regex and bad webhook URL are rejected without saving.
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("trigger_match_0", "(unclosed")
	if loc := postSettings(t, h, "/settings/save", bad).Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("invalid regex accepted: %s", loc)
	}
	bad.Set("trigger_match_0", "ok")
	bad.Set("webhookUrl", "ftp://nope")
	if loc := postSettings(t, h, "/settings/save", bad).Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Fatalf("bad webhook URL accepted: %s", loc)
	}

	// Test webhook delivers synchronously and reports back.
	form.Set("trigger_count", "1")
	loc := postSettings(t, h, "/settings/webhook/test", form).Header().Get("Location")
	if !strings.Contains(loc, "notice=") || !strings.Contains(gotBody, `"event":"test"`) {
		t.Fatalf("test webhook not delivered: loc=%s body=%s", loc, gotBody)
	}
}

func TestSettingsPreviewSwitcher(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	h := NewHandler()

	get := func(route string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475"+route, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", route, rec.Code)
		}
		return rec.Body.String()
	}
	avatar := func(body string) string {
		i := strings.Index(body, `<div class="avatar">`)
		if i < 0 {
			t.Fatal("no preview avatar")
		}
		return body[i : i+200]
	}
	appName := func(body string) string {
		i := strings.Index(body, `<div class="pv-app">`)
		j := strings.Index(body[i:], "</div>")
		return body[i+len(`<div class="pv-app">`) : i+j]
	}

	cases := []struct {
		route, gif, app string
		becameDual      bool
	}{
		{"/settings?status=working&agent=antigravity", "agy-working.gif", "Antigravity", false},
		{"/settings?status=working&agent=claude", "claude-working.gif", "Claude Code", false},
		{"/settings?status=idle&agent=claude", "claude-idle.gif", "Claude Code", false},
		{"/settings?status=working&agent=dual", "agy-dual.gif", "Antigravity &amp; Claude", true},
		{"/settings?status=idle&agent=dual", "agy-idle.gif", "Antigravity &amp; Claude", false},
		{"/settings?status=dual&agent=claude", "agy-dual.gif", "Antigravity &amp; Claude", false},
	}
	for _, c := range cases {
		body := get(c.route)
		if a := avatar(body); !strings.Contains(a, c.gif) {
			t.Errorf("%s: expected %s in preview, got %q", c.route, c.gif, a)
		}
		if got := appName(body); got != c.app {
			t.Errorf("%s: expected app %q, got %q", c.route, c.app, got)
		}
		if got := strings.Contains(body, `class="pv-note"`); got != c.becameDual {
			t.Errorf("%s: dual note shown=%v, want %v", c.route, got, c.becameDual)
		}
	}

	// Status tabs and forms carry the chosen agent; saving keeps it.
	body := get("/settings?status=working&agent=claude")
	if !strings.Contains(body, `href="/settings?status=idle&amp;agent=claude"`) {
		t.Fatal("status tabs don't preserve the preview agent")
	}
	if strings.Contains(get("/settings?status=dual"), `class="seg sm"`) {
		t.Fatal("dual tab should not show the agent switcher")
	}
	form := baseSettingsForm()
	form.Set("agent", "claude")
	loc := postSettings(t, h, "/settings/save", form).Header().Get("Location")
	if !strings.Contains(loc, "agent=claude") {
		t.Fatalf("save redirect dropped agent: %s", loc)
	}
	form.Set("agent", "bogus")
	if loc := postSettings(t, h, "/settings/save", form).Header().Get("Location"); strings.Contains(loc, "agent=") {
		t.Fatalf("invalid agent leaked into redirect: %s", loc)
	}
}

func TestTopModelForSplitsByFamily(t *testing.T) {
	agg := &scanner.Aggregate{ModelSplit: []scanner.ModelSplitEntry{{Model: "opus-5-5"}, {Model: "sonnet-5"}, {Model: "gemini-4-neon"}}}
	if got := topModelFor(agg, "claude"); got != "opus-5-5" {
		t.Fatalf("claude: got %q", got)
	}
	if got := topModelFor(agg, "antigravity"); got != "gemini-4-neon" {
		t.Fatalf("antigravity: got %q", got)
	}
}

func TestSettingsAgentsTable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	h := NewHandler()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475/settings", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, want := range []string{`name="track_opencode"`, `name="discord_claude"`, "OpenCode"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("settings page missing %q", want)
		}
	}

	form := baseSettingsForm()
	form.Set("agents_section", "1")
	form.Set("track_antigravity", "on")
	form.Set("track_claude", "on")
	form.Set("discord_antigravity", "on")
	form.Set("discord_opencode", "on") // ignored: OpenCode isn't tracked
	postSettings(t, h, "/settings/save", form)
	c := config.Load()
	if !c.Sources.Tracks("claude") || c.Sources.Tracks("opencode") {
		t.Fatalf("tracking = %+v", c.Sources)
	}
	if c.ShowOnDiscord("claude") || !c.ShowOnDiscord("antigravity") || c.ShowOnDiscord("opencode") {
		t.Fatalf("discord = claude:%v agy:%v oc:%v", c.ShowOnDiscord("claude"), c.ShowOnDiscord("antigravity"), c.ShowOnDiscord("opencode"))
	}
}
