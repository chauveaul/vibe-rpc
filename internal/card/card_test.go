package card

import (
	"bytes"
	"image/png"
	"os"
	"testing"

	"github.com/chauveaul/vibe-rpc/internal/scanner"
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
