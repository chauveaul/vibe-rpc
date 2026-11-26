package scanner

import (
	"testing"
	"time"
)

func TestMergeSpans(t *testing.T) {
	got := mergeSpans([][2]int64{{50, 60}, {0, 10}, {5, 20}, {20, 30}})
	want := [][2]int64{{0, 30}, {50, 60}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestOverlapByDaySplitsAtMidnight(t *testing.T) {
	mid := time.Date(2026, 3, 10, 0, 0, 0, 0, time.Local).UnixMilli()
	min := int64(60_000)
	// Antigravity 23:30–00:30, Claude 23:50–00:10 and 00:20–01:00.
	agy := [][2]int64{{mid - 30*min, mid + 30*min}}
	cc := [][2]int64{{mid - 10*min, mid + 10*min}, {mid + 20*min, mid + 60*min}}
	byDay := map[string]int64{}
	total := overlapByDay(agy, cc, byDay)
	if total != 30*min {
		t.Fatalf("total overlap = %d min, want 30", total/min)
	}
	if byDay["2026-03-09"] != 10*min || byDay["2026-03-10"] != 20*min {
		t.Fatalf("byDay = %v", byDay)
	}
	if overlapByDay(agy, nil, nil) != 0 {
		t.Fatal("no overlap expected with one side empty")
	}
}

func TestAggregateFromSourceDaysAndDual(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local).UnixMilli()
	day := DayKey(base)
	min := int64(60_000)
	cache := &ScanCache{Files: map[string]*TranscriptSummary{
		"a.jsonl": {Source: "antigravity", FirstTs: base, LastTs: base + 60*min, ActiveMs: 60 * min, UserMessages: 3,
			Spans: [][2]int64{{base, base + 60*min}}, ToolBreakdown: map[string]int{"edit_file": 4},
			ByDay: map[string]*DayBucket{day: {ActiveMs: 60 * min, UserMessages: 3, InputTokens: 100}}},
		"c.jsonl": {Source: "claude", FirstTs: base + 30*min, LastTs: base + 90*min, ActiveMs: 60 * min, UserMessages: 5,
			Spans: [][2]int64{{base + 30*min, base + 90*min}}, ToolBreakdown: map[string]int{"Bash": 7},
			ByDay: map[string]*DayBucket{day: {ActiveMs: 60 * min, UserMessages: 5, OutputTokens: 40, Cost: 1.5}}},
	}}
	agg := AggregateFrom(cache)
	if agg.DualMs != 30*min || agg.DualByDay[day] != 30*min {
		t.Fatalf("dual = %d / %v", agg.DualMs, agg.DualByDay)
	}
	a, c := agg.SourceDays["antigravity"][day], agg.SourceDays["claude"][day]
	if a == nil || c == nil {
		t.Fatalf("missing source days: %+v", agg.SourceDays)
	}
	if a.Sessions != 1 || a.Prompts != 3 || a.Tokens != 100 || a.Tools["edit_file"] != 4 {
		t.Fatalf("antigravity day = %+v", a)
	}
	if c.Sessions != 1 || c.Cost != 1.5 || c.Tools["Bash"] != 7 {
		t.Fatalf("claude day = %+v", c)
	}
}
