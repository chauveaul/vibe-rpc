package server

import (
	"testing"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/scanner"
)

func TestBuildYearHeatmapLayout(t *testing.T) {
	now := time.Date(2026, 11, 26, 15, 0, 0, 0, time.Local) // a Thursday
	h := int64(3_600_000)
	agg := &scanner.Aggregate{ByDay: map[string]*scanner.DayBucket{
		"2026-11-26": {ActiveMs: 4 * h, UserMessages: 9},
		"2026-11-20": {ActiveMs: 1 * h},
		"2025-01-01": {ActiveMs: 9 * h}, // outside the window: ignored
	}}
	hm := buildYearHeatmap(agg, now)

	if hm.Weeks != 53 || len(hm.Cells) != 53*7 {
		t.Fatalf("weeks=%d cells=%d", hm.Weeks, len(hm.Cells))
	}
	first, _ := time.ParseInLocation("2006-01-02", hm.Cells[0].Day, time.Local)
	if first.Weekday() != time.Sunday || hm.Cells[0].Day != "2025-11-23" {
		t.Fatalf("grid should start on a Sunday 52 weeks back, got %s (%s)", hm.Cells[0].Day, first.Weekday())
	}
	last := hm.Cells[52*7:]
	if last[4].Day != "2026-11-26" || last[4].Level != "l4" || last[4].Prompts != 9 {
		t.Fatalf("today should be Thursday of the last column: %+v", last[4])
	}
	if !last[5].Future || !last[6].Future || last[3].Future {
		t.Fatal("only days after today should be placeholders")
	}
	if hm.ActiveDays != 2 || hm.TotalHours != "5.0h" {
		t.Fatalf("active=%d total=%s (the out-of-window day must not count)", hm.ActiveDays, hm.TotalHours)
	}
	// Nov 23 start: "Dec" is in column 2, so column 1 stays unlabelled.
	if hm.Months[0].Name != "Dec" || hm.Months[0].Col != 2 || hm.Months[len(hm.Months)-1].Name != "Nov" {
		t.Fatalf("month labels = %+v", hm.Months)
	}
	if len(hm.Months) != 12 {
		t.Fatalf("expected 12 month labels, got %d: %+v", len(hm.Months), hm.Months)
	}
}

func TestMonthLabelsKeepFirstColumnWhenRoomy(t *testing.T) {
	start := time.Date(2026, 3, 8, 0, 0, 0, 0, time.Local) // Sunday; Apr 1 is 4 columns later
	got := monthLabels(start, start.AddDate(0, 0, 60), 9)
	if len(got) < 2 || got[0].Name != "Mar" || got[0].Col != 1 || got[1].Name != "Apr" {
		t.Fatalf("labels = %+v", got)
	}
}
