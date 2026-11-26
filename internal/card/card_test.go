package card

import (
	"bytes"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

func TestRenderPNG(t *testing.T) {
	d := Data{
		Hours: "128h", Subline: "412 sessions · 3.1k prompts", Since: "SINCE MAR 2026",
		AgyMs: 80 * 3_600_000, ClaudeMs: 48 * 3_600_000,
		Stats: []Stat{{"Tokens", "1.2B"}, {"API equiv", "$4,812"}, {"Best streak", "23 days"},
			{"Lines added", "+184k"}, {"Ships", "312"}, {"Top model", "Claude Opus 4.6"}},
		FooterLeft: "github.com/chauveaul/vibe-rpc", FooterRight: "ANTIGRAVITY + CLAUDE CODE",
	}
	b, err := PNG(d, 2)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != 2400 || got.Y != 1260 {
		t.Fatalf("unexpected size %v", got)
	}
	if out := os.Getenv("CARD_PREVIEW"); out != "" {
		_ = os.WriteFile(out, b, 0o644)
	}
}

func TestFromEmptyAggregate(t *testing.T) {
	d := FromAggregate(&scanner.Aggregate{})
	if d.Hours != "0h" || len(d.Stats) != 6 {
		t.Fatalf("unexpected data: %+v", d)
	}
	if _, err := PNG(d, 1); err != nil {
		t.Fatal(err)
	}
}

func TestRenderHeatmapPNG(t *testing.T) {
	now := time.Date(2026, 11, 26, 22, 0, 0, 0, time.UTC)
	agg := &scanner.Aggregate{
		Sessions:      42,
		Streak:        7,
		LongestStreak: 14,
		ByDay: map[string]*scanner.DayBucket{
			now.Format("2006-01-02"):                  {ActiveMs: 3 * 3_600_000},
			now.AddDate(0, 0, -1).Format("2006-01-02"): {ActiveMs: 90 * 60_000},
		},
	}
	b, err := HeatmapPNG(agg, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != HeatmapWidth || got.Y != HeatmapHeight {
		t.Fatalf("unexpected heatmap size %v", got)
	}
}

func TestRenderSessionPNG(t *testing.T) {
	now := time.Date(2026, 11, 26, 22, 0, 0, 0, time.UTC)
	st := state.SessionState{
		Source:       "antigravity",
		Status:       "working",
		Model:        "gemini-4-neon-high",
		Cwd:          "/Users/meze/Documents/vibe-rpc",
		CurrentTool:  "edit_file",
		CurrentFile:  "extras.go",
		SessionStart: now.Add(-42 * time.Minute).UnixMilli(),
		Messages:     18,
		Tools:        64,
		Tokens:       state.Tokens{Input: 120_000, Output: 18_000},
	}
	b, err := SessionPNG(SessionFromState(st, now), 1)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != SessionWidth || got.Y != SessionHeight {
		t.Fatalf("unexpected session size %v", got)
	}
}
