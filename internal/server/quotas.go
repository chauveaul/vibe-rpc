package server

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/install"
	"github.com/chauveaul/vibe-rpc/internal/limits"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/stats"
)

type quotaBarView struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Reset string `json:"reset,omitempty"`
	Pct   int    `json:"pct"`
	Level string `json:"level"` // "" | "warn" | "crit"
}

type goalBarView struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Pct    int    `json:"pct"`
	Bar    int    `json:"bar"`
	Met    bool   `json:"met"`
}

type dashboardQuotasView struct {
	ClaudeBars  []quotaBarView `json:"claudeBars"`
	ClaudeHint  string         `json:"claudeHint,omitempty"`
	AgyBars     []quotaBarView `json:"agyBars"`
	AgyHint     string         `json:"agyHint,omitempty"`
	AgyPlan     string         `json:"agyPlan,omitempty"`
	Goals       []goalBarView  `json:"goals"`
	HasBudget   bool           `json:"hasBudget"`
	BudgetMTD   string         `json:"budgetMtd"`
	BudgetCap   string         `json:"budgetCap,omitempty"`
	BudgetPct   int            `json:"budgetPct"`
	BudgetBar   int            `json:"budgetBar"`
	BudgetLevel string         `json:"budgetLevel"` // "ok" | "warn" | "over"
}

func quotaLevel(pct int) string {
	if pct >= 95 {
		return "crit"
	}
	if pct >= 80 {
		return "warn"
	}
	return ""
}

func formatResetText(resetsAtSec int64, isWeek bool, now time.Time) string {
	if resetsAtSec <= 0 {
		return ""
	}
	t := time.Unix(resetsAtSec, 0)
	if !t.After(now) {
		return "resetting"
	}
	if isWeek {
		if t.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02") {
			return "resets " + t.In(now.Location()).Format("15:04")
		}
		return "resets " + t.In(now.Location()).Format("Mon 15:04")
	}
	d := t.Sub(now)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("resets in %dh %dm", h, m)
	}
	return fmt.Sprintf("resets in %dm", m)
}

func windowToQuotaBar(label string, w *limits.Window, isWeek bool, now time.Time) quotaBarView {
	if w == nil {
		return quotaBarView{Label: label, Value: "—"}
	}
	if w.Expired(now) {
		return quotaBarView{Label: label, Value: "0%", Reset: "fresh window", Pct: 0}
	}
	pct := int(math.Round(math.Max(0, math.Min(100, w.UsedPct))))
	return quotaBarView{
		Label: label,
		Value: fmt.Sprintf("%d%%", pct),
		Reset: formatResetText(w.ResetsAt, isWeek, now),
		Pct:   pct,
		Level: quotaLevel(pct),
	}
}

func buildDashboardQuotas(
	cfg config.Config,
	agg *scanner.Aggregate,
	ccLim *limits.Claude, ccStatusLine string,
	agyLim *limits.Antigravity, agyStatusLine string,
	now time.Time,
) dashboardQuotasView {
	var v dashboardQuotasView

	// Claude Code 5h / 7d quotas
	if ccLim != nil && (ccLim.FiveHour != nil || ccLim.SevenDay != nil) {
		v.ClaudeBars = []quotaBarView{
			windowToQuotaBar("Claude 5h", ccLim.FiveHour, false, now),
			windowToQuotaBar("Claude 7d", ccLim.SevenDay, true, now),
		}
	} else {
		switch ccStatusLine {
		case "vibe-rpc":
			v.ClaudeHint = "Connected. Limits appear after Claude Code's next reply."
		case "other":
			v.ClaudeHint = "Custom Claude Code status line in use."
		default:
			v.ClaudeHint = "Enable Claude Code usage limits in Settings to track 5h & 7d quotas."
		}
	}

	// Antigravity CLI quotas (Gemini + 3P pools when present), or rolling 5h active-time fallback
	if agyLim != nil && (agyLim.GeminiFiveHour != nil || agyLim.GeminiSevenDay != nil || agyLim.ThirdPartyFiveHour != nil || agyLim.ThirdPartySevenDay != nil) {
		v.AgyPlan = agyLim.PlanTier
		if agyLim.GeminiFiveHour != nil || agyLim.GeminiSevenDay != nil {
			v.AgyBars = append(v.AgyBars,
				windowToQuotaBar("Gemini 5h", agyLim.GeminiFiveHour, false, now),
				windowToQuotaBar("Gemini 7d", agyLim.GeminiSevenDay, true, now),
			)
		}
		if agyLim.ThirdPartyFiveHour != nil || agyLim.ThirdPartySevenDay != nil {
			v.AgyBars = append(v.AgyBars,
				windowToQuotaBar("Agy 3P 5h", agyLim.ThirdPartyFiveHour, false, now),
				windowToQuotaBar("Agy 3P 7d", agyLim.ThirdPartySevenDay, true, now),
			)
		}
	} else {
		var ms int64
		if agg != nil {
			ms = scanner.ActiveWithin(agg.RecentSpans["antigravity"], now.UnixMilli()-usageWindowMs, now.UnixMilli())
		}
		pct := int(math.Round(math.Min(1, float64(ms)/usageWindowMs) * 100))
		v.AgyBars = []quotaBarView{{
			Label: "Agy 5h active",
			Value: format.FmtHours(ms),
			Reset: "rolling 5h",
			Pct:   pct,
		}}
		switch agyStatusLine {
		case "vibe-rpc":
			v.AgyHint = "Connected. Quotas appear after Antigravity CLI's next reply."
		case "other":
			v.AgyHint = "Custom Antigravity CLI status line in use."
		default:
			v.AgyHint = "Enable Antigravity CLI usage limits in Settings for real 5h & 7d quotas."
		}
	}

	// Goals (daily hours, daily prompts, weekly hours)
	var todayMs int64
	var todayPrompts int
	if agg != nil && agg.ByDay != nil {
		if d := agg.ByDay[scanner.DayKey(now.UnixMilli())]; d != nil {
			todayMs = d.ActiveMs
			todayPrompts = d.UserMessages
		}
	}
	if cfg.Goals.DailyHours > 0 {
		curH := float64(todayMs) / 3_600_000
		pct := int(math.Round(curH / cfg.Goals.DailyHours * 100))
		v.Goals = append(v.Goals, goalBarView{
			Label:  "Daily hours",
			Detail: fmt.Sprintf("%s / %gh", format.FmtHours(todayMs), cfg.Goals.DailyHours),
			Pct:    pct,
			Bar:    min(pct, 100),
			Met:    pct >= 100,
		})
	}
	if cfg.Goals.DailyPrompts > 0 {
		pct := int(math.Round(float64(todayPrompts) / float64(cfg.Goals.DailyPrompts) * 100))
		v.Goals = append(v.Goals, goalBarView{
			Label:  "Daily prompts",
			Detail: fmt.Sprintf("%d / %d", todayPrompts, cfg.Goals.DailyPrompts),
			Pct:    pct,
			Bar:    min(pct, 100),
			Met:    pct >= 100,
		})
	}
	if cfg.Goals.WeeklyHours > 0 {
		week := stats.SummarizeRange(agg, "7d")
		curH := float64(week.ActiveMs) / 3_600_000
		pct := int(math.Round(curH / cfg.Goals.WeeklyHours * 100))
		v.Goals = append(v.Goals, goalBarView{
			Label:  "Weekly hours",
			Detail: fmt.Sprintf("%s / %gh", format.FmtHours(week.ActiveMs), cfg.Goals.WeeklyHours),
			Pct:    pct,
			Bar:    min(pct, 100),
			Met:    pct >= 100,
		})
	}

	// Monthly API-equivalent spend vs budget
	mtd := monthToDateCost(agg)
	v.BudgetMTD = pricing.FmtCost(mtd)
	v.BudgetLevel = "ok"
	if cfg.Budget.Monthly > 0 {
		v.HasBudget = true
		v.BudgetCap = fmt.Sprintf("$%s", fmtNum(cfg.Budget.Monthly))
		pct := int(math.Round(mtd / cfg.Budget.Monthly * 100))
		v.BudgetPct = pct
		v.BudgetBar = min(pct, 100)
		warnPct := cfg.Budget.WarnAtPct
		if warnPct <= 0 {
			warnPct = 80
		}
		if pct >= 100 {
			v.BudgetLevel = "over"
		} else if pct >= warnPct {
			v.BudgetLevel = "warn"
		}
	}

	return v
}

func handleAPIQuotas(w http.ResponseWriter, _ *http.Request) {
	cfg, agg, _ := loadLive()
	q := buildDashboardQuotas(
		cfg, agg,
		limits.Load(), install.ClaudeStatusLineState(""),
		limits.LoadAntigravity(), install.AgyStatusLineState(""),
		time.Now(),
	)
	writeJSON(w, q)
}
