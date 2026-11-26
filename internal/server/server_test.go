package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meze/agy-rpc/internal/config"
)

func TestZeroJSHTMLPagesAndSettingsPost(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AGY_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("AGY_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("AGY_RPC_DATA_DIR", filepath.Join(tmp, "data"))

	h := NewHandler()

	// Verify all HTML pages render 200 OK and contain ZERO JavaScript (<script or on*=)
	for _, route := range []string{"/", "/?range=7d", "/settings", "/settings?status=idle", "/wrapped", "/wrapped?slide=6"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475"+route, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", route, rec.Code)
		}
		body := strings.ToLower(rec.Body.String())
		if strings.Contains(body, "<script") {
			t.Fatalf("GET %s contains forbidden <script tag!", route)
		}
		if strings.Contains(body, "onclick=") || strings.Contains(body, "onchange=") {
			t.Fatalf("GET %s contains inline JS event handler!", route)
		}
	}

	// Verify API SVG / JSON / CSV / GIF endpoints
	for _, route := range []string{"/api/state", "/api/aggregate", "/api/insights", "/api/badge.svg", "/api/card.svg", "/api/heatmap.svg", "/api/export.csv", "/assets/gifs/agy-working.gif", "/assets/gifs/agy-thinking.gif"} {
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
		"clientId":          {"1506443909406920948"},
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
		"clientId":          {"1506443909406920948"},
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
