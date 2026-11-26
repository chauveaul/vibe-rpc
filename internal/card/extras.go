package card

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/font"

	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

const (
	HeatmapWidth  = 1200
	HeatmapHeight = 460

	SessionWidth  = 960
	SessionHeight = 420
)

func (c *canvas) glassPanel(x, y, w, h, rad float64) {
	for i := 6; i >= 1; i-- {
		o := float64(i) * 4
		c.fill(path{rrect(x-o/2, y+o, w+o, h, rad+o/2)}, uni(rgba(0, 0, 0, 0.06)))
	}
	c.fill(path{rrect(x, y, w, h, rad)}, uni(rgba(0x0c, 0x0d, 0x11, 0.55)))
	c.fill(path{rrect(x, y, w, h, rad)}, vgrad{y * c.s, (y + h) * c.s, rgba(255, 255, 255, 0.10), rgba(255, 255, 255, 0.035)})
	c.fill(border(x, y, w, h, rad, 1), uni(rgba(255, 255, 255, 0.14)))
	c.fill(path{rrect(x+rad, y+0.5, w-2*rad, 1.2, 0.6)}, uni(rgba(255, 255, 255, 0.30)))
}

func (c *canvas) footerBar(designW, designH float64, left, right string) {
	y := designH - 36.0
	c.fill(path{rrect(pad, y-28, designW-2*pad, 1, 0.5)}, uni(rgba(255, 255, 255, 0.075)))
	f := c.face("JetBrainsMono-Regular", 13)
	c.text(left, pad, y, f, uni(colMute), 0.4)
	if right != "" {
		w := c.measure(right, f, 1.6)
		c.text(right, designW-pad-w, y, f, uni(colFaint), 1.6)
	}
}

// ── 12-Month Heatmap PNG ─────────────────────────────────────────────────────

type heatmapCell struct {
	level  int // 0..4
	future bool
}

type heatmapMonth struct {
	col  int
	name string
}

// RenderHeatmap renders a rolling 12-month coding heatmap PNG (1200×460 × scale).
func RenderHeatmap(agg *scanner.Aggregate, now time.Time, scale float64) (*image.RGBA, error) {
	if err := loadFonts(); err != nil {
		return nil, err
	}
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	if scale <= 0 {
		scale = 2
	}

	// Build rolling 12-month Sunday→Saturday grid (mirrors buildYearHeatmap).
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	yearAgo := today.AddDate(-1, 0, 1)
	start := yearAgo.AddDate(0, 0, -int(yearAgo.Weekday()))
	end := today.AddDate(0, 0, 6-int(today.Weekday()))
	totalDays := int(math.Round(end.Sub(start).Hours()/24)) + 1
	weeks := totalDays / 7
	if weeks < 1 {
		weeks = 1
	}

	var maxMs int64 = 1
	var totalMs int64
	activeDays := 0
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		if b := agg.ByDay[d.Format("2006-01-02")]; b != nil && b.ActiveMs > 0 {
			activeDays++
			totalMs += b.ActiveMs
			if b.ActiveMs > maxMs {
				maxMs = b.ActiveMs
			}
		}
	}

	cells := make([]heatmapCell, weeks*7)
	var months []heatmapMonth
	lastMonth := time.Month(0)
	for i := 0; i < weeks*7; i++ {
		d := start.AddDate(0, 0, i)
		col := i / 7
		if d.Month() != lastMonth {
			if len(months) == 0 {
				months = append(months, heatmapMonth{col: col, name: d.Format("Jan")})
			} else if col-months[len(months)-1].col >= 3 {
				months = append(months, heatmapMonth{col: col, name: d.Format("Jan")})
			} else if len(months) == 1 && months[0].col == 0 {
				// First partial week belongs mostly to the next month (e.g. Nov 29 → Dec 1).
				months[0] = heatmapMonth{col: col, name: d.Format("Jan")}
			}
			lastMonth = d.Month()
		}
		if d.After(today) {
			cells[i] = heatmapCell{future: true}
			continue
		}
		var ms int64
		if b := agg.ByDay[d.Format("2006-01-02")]; b != nil {
			ms = b.ActiveMs
		}
		lvl := 0
		if ms > 0 {
			r := float64(ms) / float64(maxMs)
			switch {
			case r > 0.72:
				lvl = 4
			case r > 0.45:
				lvl = 3
			case r > 0.20:
				lvl = 2
			default:
				lvl = 1
			}
		}
		cells[i] = heatmapCell{level: lvl}
	}

	W, H := int(math.Round(HeatmapWidth*scale)), int(math.Round(HeatmapHeight*scale))
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, W, H)), s: scale, faces: map[string]font.Face{}}
	c.backgroundFor(HeatmapWidth, HeatmapHeight)

	// Header
	c.logo(pad, 40, 40)
	tw := c.text("vibe-rpc", pad+54, 68, c.face("Inter-Bold", 24), uni(colText), -0.4)
	c.pill("LAST 12 MONTHS", pad+54+tw+14, 46)
	rightStat := fmt.Sprintf("%d ACTIVE DAYS · %s CODING", activeDays, strings.ToUpper(format.FmtHours(totalMs)))
	rf := c.face("JetBrainsMono-Regular", 13)
	rw := c.measure(rightStat, rf, 1.4)
	c.text(rightStat, HeatmapWidth-pad-rw, 66, rf, uni(colMute), 1.4)

	// Frosted glass heatmap panel
	px, py, pw, ph := pad, 108.0, HeatmapWidth-2*pad, 272.0
	c.glassPanel(px, py, pw, ph, 24)

	gridY := py + 52.0
	leftMargin, rightMargin := 62.0, 28.0
	availW := pw - leftMargin - rightMargin
	gap := 4.0
	cell := math.Floor((availW - gap*float64(weeks-1)) / float64(weeks))
	if cell > 16 {
		cell = 16
	}
	if cell < 8 {
		cell = 8
	}
	step := cell + gap
	gridW := float64(weeks)*cell + float64(weeks-1)*gap
	gridX := px + leftMargin + math.Floor((availW-gridW)/2)

	// Month labels
	mf := c.face("JetBrainsMono-Regular", 11)
	for _, m := range months {
		c.text(m.name, gridX+float64(m.col)*step, gridY-12, mf, uni(colMute), 0.3)
	}

	// Weekday labels (Mon, Wed, Fri)
	df := c.face("JetBrainsMono-Regular", 10.5)
	for _, day := range []struct {
		row  int
		name string
	}{{1, "Mon"}, {3, "Wed"}, {5, "Fri"}} {
		c.text(day.name, px+24, gridY+float64(day.row)*step+cell*0.78, df, uni(colFaint), 0.2)
	}

	// Monochrome platinum levels
	levelCols := [5]color.NRGBA{
		rgba(255, 255, 255, 0.06),
		rgba(255, 255, 255, 0.22),
		rgba(255, 255, 255, 0.44),
		rgba(255, 255, 255, 0.70),
		rgba(0xf5, 0xf5, 0xf7, 0.98),
	}
	for i, cellData := range cells {
		if cellData.future {
			continue
		}
		col, row := i/7, i%7
		cx := gridX + float64(col)*step
		cy := gridY + float64(row)*step
		c.fill(path{rrect(cx, cy, cell, cell, 3.2)}, uni(levelCols[cellData.level]))
	}

	// Panel footer: streak summary + legend
	fy := py + ph - 26
	summary := fmt.Sprintf("Streak: %dd current · %dd longest · %s sessions",
		agg.Streak, agg.LongestStreak, format.FmtNum(int64(agg.Sessions)))
	c.text(summary, px+28, fy, c.face("JetBrainsMono-Regular", 12), uni(colSoft), 0.2)

	// Legend on right
	legF := c.face("JetBrainsMono-Regular", 11)
	moreW := c.measure("More", legF, 0.2)
	lessW := c.measure("Less", legF, 0.2)
	swSize, swGap := 12.0, 4.0
	swTotal := 5*swSize + 4*swGap
	rx := px + pw - 28 - moreW
	c.text("More", rx, fy, legF, uni(colMute), 0.2)
	sx := rx - 8 - swTotal
	for i := 0; i < 5; i++ {
		c.fill(path{rrect(sx+float64(i)*(swSize+swGap), fy-10, swSize, swSize, 2.8)}, uni(levelCols[i]))
	}
	c.text("Less", sx-8-lessW, fy, legF, uni(colMute), 0.2)

	c.footerBar(HeatmapWidth, HeatmapHeight, "github.com/chauveaul/vibe-rpc", "ANTIGRAVITY + CLAUDE CODE")
	return c.img, nil
}

// WriteHeatmapPNG encodes the 12-month heatmap PNG to w.
func WriteHeatmapPNG(w io.Writer, agg *scanner.Aggregate, now time.Time, scale float64) error {
	img, err := RenderHeatmap(agg, now, scale)
	if err != nil {
		return err
	}
	return (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(w, img)
}

// HeatmapPNG renders the 12-month heatmap to PNG bytes.
func HeatmapPNG(agg *scanner.Aggregate, now time.Time, scale float64) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteHeatmapPNG(&buf, agg, now, scale); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── Live Session Snapshot PNG ────────────────────────────────────────────────

// SessionData holds formatted fields for the live session snapshot PNG.
type SessionData struct {
	Status      string // working|thinking|compacting|shipped|notification|idle|dual|stale
	StatusLabel string
	SourceLabel string // ANTIGRAVITY | CLAUDE CODE | ANTIGRAVITY + CLAUDE
	Project     string
	Activity    string // e.g. "Editing server.go" or "Dual wielding"
	Model       string
	Elapsed     string
	Prompts     string
	Tokens      string
	Timestamp   string
}

// SessionFromState extracts displayable SessionData from a live SessionState.
func SessionFromState(st state.SessionState, now time.Time) SessionData {
	if now.IsZero() {
		now = time.Now()
	}
	proj := "vibe-rpc"
	if st.Cwd != "" {
		proj = filepath.Base(st.Cwd)
	}
	if st.Status == "dual" && st.AgySession != nil && st.ClaudeSession != nil {
		ap, cp := filepath.Base(st.AgySession.Cwd), filepath.Base(st.ClaudeSession.Cwd)
		if ap != "" && cp != "" && ap != cp {
			proj = ap + " & " + cp
		}
	}
	src := "ANTIGRAVITY"
	if st.AntigravityActive && st.ClaudeActive || st.Status == "dual" {
		src = "ANTIGRAVITY + CLAUDE"
	} else if st.Source == "claude" {
		src = "CLAUDE CODE"
	} else if st.Source == "opencode" {
		src = "OPENCODE"
	}
	status := st.Status
	if status == "" {
		status = "idle"
	}
	statusLbl := strings.ToUpper(format.StatusVerbose(status, format.HumanTool(st.CurrentTool), 0))
	if status == "dual" {
		statusLbl = "DUAL WIELDING"
	}
	activity := format.HumanModel(st.Model)
	if st.CurrentTool != "" && st.CurrentFile != "" {
		activity = format.HumanTool(st.CurrentTool) + " · " + st.CurrentFile
	} else if st.CurrentTool != "" {
		activity = format.HumanTool(st.CurrentTool)
	} else if st.Status == "dual" && st.AgySession != nil && st.ClaudeSession != nil {
		activity = format.HumanModel(st.AgySession.Model) + " + " + format.HumanModel(st.ClaudeSession.Model)
	}
	elapsed := "—"
	if st.SessionStart > 0 {
		ms := now.UnixMilli() - st.SessionStart
		if ms < 0 {
			ms = 0
		}
		elapsed = format.FmtDuration(ms)
	}
	toks := st.Tokens.Input + st.Tokens.Output + st.Tokens.CacheRead + st.Tokens.CacheWrite
	tokStr := "—"
	if toks > 0 {
		tokStr = format.FmtNum(toks)
	}
	return SessionData{
		Status:      status,
		StatusLabel: statusLbl,
		SourceLabel: src,
		Project:     proj,
		Activity:    activity,
		Model:       format.HumanModel(st.Model),
		Elapsed:     elapsed,
		Prompts:     fmt.Sprintf("%d / %d", st.Messages, st.Tools),
		Tokens:      tokStr,
		Timestamp:   now.Format("Jan 2, 2006 · 15:04"),
	}
}

func sessionStatusColor(status string) color.NRGBA {
	switch status {
	case "working", "thinking", "compacting", "dual", "trigger":
		return rgba(0x4a, 0xde, 0x80, 1)
	case "notification":
		return rgba(0xfa, 0xcc, 0x15, 1)
	case "shipped":
		return rgba(0xff, 0xff, 0xff, 1)
	default:
		return rgba(0x8c, 0x90, 0x99, 1)
	}
}

// RenderSession renders a live session snapshot PNG (960×420 × scale).
func RenderSession(d SessionData, scale float64) (*image.RGBA, error) {
	if err := loadFonts(); err != nil {
		return nil, err
	}
	if scale <= 0 {
		scale = 2
	}
	W, H := int(math.Round(SessionWidth*scale)), int(math.Round(SessionHeight*scale))
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, W, H)), s: scale, faces: map[string]font.Face{}}
	c.backgroundFor(SessionWidth, SessionHeight)

	// Header
	c.logo(pad, 38, 38)
	tw := c.text("vibe-rpc", pad+52, 65, c.face("Inter-Bold", 23), uni(colText), -0.4)
	c.pill("LIVE SESSION", pad+52+tw+14, 44)

	sf := c.face("JetBrainsMono-Bold", 12.5)
	sw := c.measure(d.SourceLabel, sf, 1.6)
	c.text(d.SourceLabel, SessionWidth-pad-sw, 62, sf, uni(colSoft), 1.6)

	// Frosted glass session panel
	px, py, pw, ph := pad, 102.0, SessionWidth-2*pad, 240.0
	c.glassPanel(px, py, pw, ph, 24)

	// Status dot + status label
	ix := px + 36
	dotCol := sessionStatusColor(d.Status)
	c.fill(path{circle(ix+6, py+38, 6)}, uni(rgba(dotCol.R, dotCol.G, dotCol.B, 0.25)))
	c.fill(path{circle(ix+6, py+38, 4.5)}, uni(dotCol))
	c.text(d.StatusLabel, ix+20, py+43, c.face("JetBrainsMono-Bold", 12.5), uni(colSoft), 1.6)

	// Project hero title
	projSize := c.fit(d.Project, "Inter-ExtraBold", 48, pw-72, -1.2)
	projY := py + 104
	c.text(d.Project, ix, projY, c.face("Inter-ExtraBold", projSize),
		vgrad{(projY - projSize*0.75) * c.s, projY * c.s, rgba(255, 255, 255, 1), rgba(0xc0, 0xc5, 0xd0, 1)}, -1.2)

	// Activity subtitle
	if d.Activity != "" {
		actSize := c.fit(d.Activity, "JetBrainsMono-Regular", 15, pw-72, 0.2)
		c.text(d.Activity, ix, projY+30, c.face("JetBrainsMono-Regular", actSize), uni(colMute), 0.2)
	}

	// Divider + 4 stat columns
	divY := py + 154
	c.fill(path{rrect(ix, divY, pw-72, 1, 0.5)}, uni(rgba(255, 255, 255, 0.08)))

	cols := []Stat{
		{"MODEL", d.Model},
		{"ELAPSED", d.Elapsed},
		{"PROMPTS / TOOLS", d.Prompts},
		{"TOKENS", d.Tokens},
	}
	colW := (pw - 72) / float64(len(cols))
	for i, col := range cols {
		cx := ix + float64(i)*colW
		c.text(col.Label, cx, divY+28, c.face("JetBrainsMono-Bold", 11), uni(colFaint), 1.4)
		val := col.Value
		if val == "" {
			val = "—"
		}
		vs := c.fit(val, "Inter-Bold", 24, colW-18, -0.4)
		c.text(val, cx, divY+62, c.face("Inter-Bold", vs), uni(colText), -0.4)
	}

	c.footerBar(SessionWidth, SessionHeight, "github.com/chauveaul/vibe-rpc", d.Timestamp)
	return c.img, nil
}

// WriteSessionPNG encodes the live session snapshot PNG to w.
func WriteSessionPNG(w io.Writer, d SessionData, scale float64) error {
	img, err := RenderSession(d, scale)
	if err != nil {
		return err
	}
	return (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(w, img)
}

// SessionPNG renders the live session snapshot to PNG bytes.
func SessionPNG(d SessionData, scale float64) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteSessionPNG(&buf, d, scale); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
