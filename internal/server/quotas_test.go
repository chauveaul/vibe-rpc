package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
)

func TestBuildDashboardQuotasAndGoals(t *testing.T) {
	now := time.Date(2026, 11, 26, 22, 0, 0, 0, time.UTC)
	today := scanner.DayKey(now.UnixMilli())

	cfg := config.Default()
	cfg.Goals.DailyHours = 4
	cfg.Goals.DailyPrompts = 20
	cfg.Goals.WeeklyHours = 20
	cfg.Budget.Monthly = 100
	cfg.Budget.WarnAtPct = 80

	agg := &scanner.Aggregate{
		ByDay: map[string]*scanner.DayBucket{
			today: {ActiveMs: 2 * 3_600_000, UserMessages: 25, Cost: 85.0},
		},
	}
	ccLim := &limits.Claude{
		FiveHour: &limits.Window{UsedPct: 92, ResetsAt: now.Unix() + 5400},
		SevenDay: &limits.Window{UsedPct: 40, ResetsAt: now.Unix() + 86400},
	}
	agyLim := &limits.Antigravity{
		GeminiFiveHour:     &limits.Window{UsedPct: 12, ResetsAt: now.Unix() + 14400},
		GeminiSevenDay:     &limits.Window{UsedPct: 28, ResetsAt: now.Unix() + 3*86400},
		ThirdPartyFiveHour: &limits.Window{UsedPct: 100, ResetsAt: now.Unix() + 7200},
		ThirdPartySevenDay: &limits.Window{UsedPct: 50, ResetsAt: now.Unix() + 5*86400},
		PlanTier:           "Google AI Pro",
	}

	q := buildDashboardQuotas(cfg, agg, ccLim, "vibe-rpc", agyLim, "vibe-rpc", now)
	if len(q.ClaudeBars) != 2 || q.ClaudeBars[0].Pct != 92 || q.ClaudeBars[0].Level != "warn" {
		t.Fatalf("claude bars = %+v", q.ClaudeBars)
	}
	if len(q.AgyBars) != 4 || q.AgyPlan != "Google AI Pro" || q.AgyBars[2].Level != "crit" {
		t.Fatalf("agy bars = %+v", q.AgyBars)
	}
	if len(q.Goals) != 3 || q.Goals[0].Pct != 50 || q.Goals[0].Met || !q.Goals[1].Met {
		t.Fatalf("goals = %+v", q.Goals)
	}
	if !q.HasBudget || q.BudgetPct != 85 || q.BudgetLevel != "warn" {
		t.Fatalf("budget = %+v", q)
	}
}

func TestDashboardRendersQuotasAndAPIQuotas(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_STATE_DIR", filepath.Join(tmp, "state"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("CLAUDE_RPC_STATE_DIR", filepath.Join(tmp, "none"))

	now := time.Now()
	_, _ = limits.SaveFromStatusLine([]byte(`{"product":"antigravity","plan_tier":"Google AI Pro","quota":{"gemini-5h":{"remaining_fraction":0.85,"reset_in_seconds":3600},"gemini-weekly":{"remaining_fraction":0.7,"reset_in_seconds":86400}}}`), now)
	_ = os.WriteFile(filepath.Join(tmp, "config.json"), []byte(`{"goals":{"dailyHours":3},"budget":{"monthly":150}}`), 0o644)

	h := NewHandler()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Usage quotas", "Goals &amp; monthly budget", "Gemini 5h", "Google AI Pro", "Daily hours", "$150"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard HTML missing %q", want)
		}
	}

	apiReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47475/api/quotas", nil)
	apiRec := httptest.NewRecorder()
	h.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusOK {
		t.Fatalf("GET /api/quotas = %d", apiRec.Code)
	}
	var got dashboardQuotasView
	if err := json.Unmarshal(apiRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /api/quotas: %v", err)
	}
	if len(got.AgyBars) < 2 || got.AgyBars[0].Pct != 15 || !got.HasBudget {
		t.Fatalf("api quotas = %+v", got)
	}
}
