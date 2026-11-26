// Package card renders the shareable vibe-rpc stats card as a PNG in pure Go,
// so exporting works anywhere the binary runs (no browser or rsvg needed).
//
// The look mirrors the dashboard's monochrome liquid-glass theme: a near-black
// radial backdrop with a vignetted dot field, a frosted glass stat panel, and
// Inter / JetBrains Mono type. Layout is authored at 1200×630 (the Open Graph
// size Discord, X and LinkedIn all preview well) and multiplied by Scale.
package card

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
)

const (
	Width  = 1200
	Height = 630
)

//go:embed fonts/*.ttf
var fontFS embed.FS

// Stat is one tile in the glass panel.
type Stat struct {
	Label string
	Value string
}

// Data is everything the card shows, already formatted for display.
type Data struct {
	Hours       string // hero figure, e.g. "128h"
	Subline     string // e.g. "412 sessions · 3.1k prompts"
	Since       string // e.g. "SINCE MAR 2026"; empty hides it
	AgyMs       int64
	ClaudeMs    int64
	Stats       []Stat // up to 6, laid out 2 per row
	FooterLeft  string
	FooterRight string
}

// FromAggregate builds card Data from lifetime stats.
func FromAggregate(agg *scanner.Aggregate) Data {
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	toks := agg.InputTokens + agg.OutputTokens + agg.CacheReadTokens + agg.CacheWriteTokens
	var agyMs, ccMs int64
	if s := agg.BySource["antigravity"]; s != nil {
		agyMs = s.ActiveMs
	}
	if s := agg.BySource["claude"]; s != nil {
		ccMs = s.ActiveMs
	}
	topModel := "—"
	if len(agg.ModelSplit) > 0 {
		topModel = format.HumanModel(agg.ModelSplit[0].Model)
	}
	since := ""
	if agg.FirstTs > 0 {
		since = "SINCE " + strings.ToUpper(time.UnixMilli(agg.FirstTs).Format("Jan 2006"))
	}
	streak := fmt.Sprintf("%d days", agg.LongestStreak)
	if agg.LongestStreak == 1 {
		streak = "1 day"
	}
	return Data{
		Hours:    format.FmtHours(agg.ActiveMs),
		Subline:  fmt.Sprintf("%s sessions · %s prompts", format.FmtNum(int64(agg.Sessions)), format.FmtNum(int64(agg.UserMessages))),
		Since:    since,
		AgyMs:    agyMs,
		ClaudeMs: ccMs,
		Stats: []Stat{
			{"Tokens", format.FmtNum(toks)},
			{"API equiv", pricing.FmtCost(agg.EstimatedCost)},
			{"Best streak", streak},
			{"Lines added", "+" + format.FmtNum(int64(agg.LinesAdded))},
			{"Ships", format.FmtNum(int64(agg.Ships))},
			{"Top model", topModel},
		},
		FooterLeft:  "github.com/chauveaul/vibe-rpc",
		FooterRight: "ANTIGRAVITY + CLAUDE CODE",
	}
}

// ── Fonts ────────────────────────────────────────────────────────────────────

var (
	fontsOnce sync.Once
	fontsErr  error
	fonts     = map[string]*opentype.Font{}
)

func loadFonts() error {
	fontsOnce.Do(func() {
		for _, name := range []string{"Inter-Medium", "Inter-Bold", "Inter-ExtraBold", "JetBrainsMono-Regular", "JetBrainsMono-Bold"} {
			b, err := fontFS.ReadFile("fonts/" + name + ".ttf")
			if err != nil {
				fontsErr = err
				return
			}
			f, err := opentype.Parse(b)
			if err != nil {
				fontsErr = fmt.Errorf("parse %s: %w", name, err)
				return
			}
			fonts[name] = f
		}
	})
	return fontsErr
}

// ── Canvas ───────────────────────────────────────────────────────────────────

type canvas struct {
	img   *image.RGBA
	s     float64 // scale from design units to pixels
	faces map[string]font.Face
}

func (c *canvas) face(name string, size float64) font.Face {
	key := fmt.Sprintf("%s@%.2f", name, size)
	if f, ok := c.faces[key]; ok {
		return f
	}
	f, err := opentype.NewFace(fonts[name], &opentype.FaceOptions{Size: size * c.s, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err) // fonts are embedded and validated by loadFonts
	}
	c.faces[key] = f
	return f
}

// path is a closed polygon list in design units; fill rasterizes the union
// (all subpaths must share orientation unless a hole is intended).
type path [][][2]float64

func (c *canvas) fill(p path, src image.Image) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, sp := range p {
		for _, pt := range sp {
			minX, minY = math.Min(minX, pt[0]), math.Min(minY, pt[1])
			maxX, maxY = math.Max(maxX, pt[0]), math.Max(maxY, pt[1])
		}
	}
	x0, y0 := int(math.Floor(minX*c.s)), int(math.Floor(minY*c.s))
	x1, y1 := int(math.Ceil(maxX*c.s)), int(math.Ceil(maxY*c.s))
	r := image.Rect(x0, y0, x1, y1).Intersect(c.img.Bounds())
	if r.Empty() {
		return
	}
	z := vector.NewRasterizer(r.Dx(), r.Dy())
	for _, sp := range p {
		for i, pt := range sp {
			x, y := float32(pt[0]*c.s-float64(r.Min.X)), float32(pt[1]*c.s-float64(r.Min.Y))
			if i == 0 {
				z.MoveTo(x, y)
			} else {
				z.LineTo(x, y)
			}
		}
		z.ClosePath()
	}
	z.Draw(c.img, r, src, r.Min)
}

// rrect returns a rounded rectangle polygon (clockwise on screen).
func rrect(x, y, w, h, rad float64) [][2]float64 {
	rad = math.Min(rad, math.Min(w, h)/2)
	var pts [][2]float64
	corner := func(cx, cy, a0 float64) {
		for i := 0; i <= 10; i++ {
			a := a0 + float64(i)*math.Pi/2/10
			pts = append(pts, [2]float64{cx + rad*math.Cos(a), cy + rad*math.Sin(a)})
		}
	}
	corner(x+w-rad, y+rad, -math.Pi/2)
	corner(x+w-rad, y+h-rad, 0)
	corner(x+rad, y+h-rad, math.Pi/2)
	corner(x+rad, y+rad, math.Pi)
	return pts
}

func circle(cx, cy, r float64) [][2]float64 {
	n := 24
	pts := make([][2]float64, n)
	for i := range pts {
		a := float64(i) / float64(n) * 2 * math.Pi
		pts[i] = [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)}
	}
	return pts
}

func reversed(p [][2]float64) [][2]float64 {
	out := make([][2]float64, len(p))
	for i := range p {
		out[i] = p[len(p)-1-i]
	}
	return out
}

func orient(p [][2]float64) [][2]float64 {
	var a float64
	for i := range p {
		j := (i + 1) % len(p)
		a += p[i][0]*p[j][1] - p[j][0]*p[i][1]
	}
	if a < 0 {
		return reversed(p)
	}
	return p
}

// stroke approximates a round-capped, round-joined polyline.
func stroke(pts [][2]float64, w float64) path {
	var p path
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		dx, dy := b[0]-a[0], b[1]-a[1]
		l := math.Hypot(dx, dy)
		nx, ny := -dy/l*w/2, dx/l*w/2
		p = append(p, orient([][2]float64{{a[0] + nx, a[1] + ny}, {b[0] + nx, b[1] + ny}, {b[0] - nx, b[1] - ny}, {a[0] - nx, a[1] - ny}}))
	}
	for _, pt := range pts {
		p = append(p, orient(circle(pt[0], pt[1], w/2)))
	}
	return p
}

// border fills the ring between a rounded rect and its 1-unit inset.
func border(x, y, w, h, rad, t float64) path {
	return path{rrect(x, y, w, h, rad), reversed(rrect(x+t, y+t, w-2*t, h-2*t, rad-t))}
}

func rgba(r, g, b uint8, a float64) color.NRGBA {
	return color.NRGBA{r, g, b, uint8(math.Round(a * 255))}
}

func uni(c color.Color) image.Image { return image.NewUniform(c) }

// vgrad is a vertical two-stop gradient in pixel space.
type vgrad struct {
	y0, y1 float64
	a, b   color.NRGBA
}

func (g vgrad) ColorModel() color.Model { return color.NRGBAModel }
func (g vgrad) Bounds() image.Rectangle {
	return image.Rect(-1e9, -1e9, 1e9, 1e9)
}
func (g vgrad) At(_, y int) color.Color {
	t := (float64(y) - g.y0) / (g.y1 - g.y0)
	t = math.Max(0, math.Min(1, t))
	lerp := func(a, b uint8) uint8 { return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t)) }
	return color.NRGBA{lerp(g.a.R, g.b.R), lerp(g.a.G, g.b.G), lerp(g.a.B, g.b.B), lerp(g.a.A, g.b.A)}
}

// text draws s with its baseline at (x, y) in design units and returns the
// advance width in design units. tracking is extra space per glyph.
func (c *canvas) text(s string, x, y float64, face font.Face, src image.Image, tracking float64) float64 {
	d := &font.Drawer{Dst: c.img, Src: src, Face: face, Dot: fixed.Point26_6{X: fixed.Int26_6(x * c.s * 64), Y: fixed.Int26_6(y * c.s * 64)}}
	if tracking == 0 {
		d.DrawString(s)
		return float64(d.Dot.X)/64/c.s - x
	}
	prev := rune(-1)
	for _, r := range s {
		if prev >= 0 {
			d.Dot.X += face.Kern(prev, r)
		}
		d.DrawString(string(r))
		d.Dot.X += fixed.Int26_6(tracking * c.s * 64)
		prev = r
	}
	return float64(d.Dot.X)/64/c.s - x - tracking
}

func (c *canvas) measure(s string, face font.Face, tracking float64) float64 {
	w := float64(font.MeasureString(face, s)) / 64 / c.s
	if n := len([]rune(s)); tracking != 0 && n > 1 {
		w += tracking * float64(n-1)
	}
	return w
}

// fit returns the largest size ≤ size at which s fits in maxW.
func (c *canvas) fit(s, name string, size, maxW, tracking float64) float64 {
	for size > 10 && c.measure(s, c.face(name, size), tracking) > maxW {
		size -= 1
	}
	return size
}

// ── Palette (matches dashboard.css tokens) ───────────────────────────────────

var (
	colText  = rgba(0xf5, 0xf5, 0xf7, 1)
	colSoft  = rgba(0xc7, 0xca, 0xd1, 1)
	colMute  = rgba(0x8c, 0x90, 0x99, 1)
	colFaint = rgba(0x5e, 0x62, 0x6b, 1)
)

// ── Render ───────────────────────────────────────────────────────────────────

// Render draws the card at scale (1 = 1200×630, 2 = 2400×1260).
func Render(d Data, scale float64) (*image.RGBA, error) {
	if err := loadFonts(); err != nil {
		return nil, err
	}
	if scale <= 0 {
		scale = 2
	}
	W, H := int(math.Round(Width*scale)), int(math.Round(Height*scale))
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, W, H)), s: scale, faces: map[string]font.Face{}}

	c.background()
	c.header(d)
	c.hero(d)
	c.panel(d)
	c.footer(d)
	return c.img, nil
}

// WritePNG renders the card and encodes it to w.
func WritePNG(w io.Writer, d Data, scale float64) error {
	img, err := Render(d, scale)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	return enc.Encode(w, img)
}

// PNG renders the card to PNG bytes.
func PNG(d Data, scale float64) ([]byte, error) {
	var buf bytes.Buffer
	if err := WritePNG(&buf, d, scale); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *canvas) background() {
	c.backgroundFor(Width, Height)
}

func (c *canvas) backgroundFor(designW, designH float64) {
	b := c.img.Bounds()
	W, H := float64(b.Dx()), float64(b.Dy())
	// radial-gradient(120% 95% at 50% 0%, #15171c 0%, #0b0c0f 56%, #060709 100%)
	// plus a soft light pool top-left and an elliptical vignette.
	stops := []struct {
		t       float64
		r, g, b float64
	}{{0, 0x15, 0x17, 0x1c}, {0.56, 0x0b, 0x0c, 0x0f}, {1, 0x06, 0x07, 0x09}}
	rx, ry := 1.2*W, 0.95*H
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			fx, fy := float64(x), float64(y)
			t := math.Min(1, math.Hypot((fx-W/2)/rx, fy/ry))
			var r, g, bl float64
			for i := 0; i+1 < len(stops); i++ {
				if t <= stops[i+1].t {
					k := (t - stops[i].t) / (stops[i+1].t - stops[i].t)
					r = stops[i].r + (stops[i+1].r-stops[i].r)*k
					g = stops[i].g + (stops[i+1].g-stops[i].g)*k
					bl = stops[i].b + (stops[i+1].b-stops[i].b)*k
					break
				}
			}
			// light pool, top-left
			pd := math.Hypot((fx-0.12*W)/(0.55*W), (fy+0.1*H)/(0.9*H))
			pool := math.Max(0, 1-pd) * 0.10 * 255
			r, g, bl = r+pool, g+pool, bl+pool*1.04
			i := c.img.PixOffset(x, y)
			c.img.Pix[i+0] = uint8(math.Min(255, r))
			c.img.Pix[i+1] = uint8(math.Min(255, g))
			c.img.Pix[i+2] = uint8(math.Min(255, bl))
			c.img.Pix[i+3] = 255
		}
	}
	// Dot field (48px grid), faded toward the edges like the dashboard's vignette.
	for gy := 24.0; gy < designH; gy += 48 {
		for gx := 24.0; gx < designW; gx += 48 {
			v := math.Hypot((gx-designW/2)/(0.8*designW/2), (gy-designH*0.42)/(0.75*designH/2))
			a := 0.20 * math.Max(0, 1-math.Max(0, v-0.32)/0.68)
			if a < 0.01 {
				continue
			}
			c.fill(path{circle(gx, gy, 1.4)}, uni(rgba(255, 255, 255, a)))
		}
	}
}

func (c *canvas) logo(x, y, size float64) {
	// Rounded square with the silver gradient, like .brand .mark
	c.fill(path{rrect(x, y, size, size, size*0.31)}, vgrad{y * c.s, (y + size) * c.s, rgba(255, 255, 255, 1), rgba(0xc8, 0xcc, 0xd4, 1)})
	k := size * 0.57 / 24 // icon is 20/35 of the mark, authored on a 24 grid
	ox, oy := x+(size-24*k)/2, y+(size-24*k)/2
	pt := func(px, py float64) [2]float64 { return [2]float64{ox + px*k, oy + py*k} }
	ink := rgba(0x0a, 0x0b, 0x0e, 1)
	c.fill(path{circle(ox+12*k, oy+4.2*k, 1.4*k)}, uni(ink))
	c.fill(stroke([][2]float64{pt(9.4, 7.5), pt(12, 13.2), pt(14.6, 7.5)}, 1.6*k), uni(rgba(0x0a, 0x0b, 0x0e, 0.65)))
	c.fill(stroke([][2]float64{pt(5.5, 7.5), pt(12, 20), pt(18.5, 7.5)}, 2.2*k), uni(ink))
}

func (c *canvas) pill(label string, x, y float64) float64 {
	f := c.face("JetBrainsMono-Bold", 13)
	w := c.measure(label, f, 1.6) + 28
	c.fill(path{rrect(x, y, w, 28, 14)}, uni(rgba(255, 255, 255, 0.08)))
	c.fill(border(x, y, w, 28, 14, 1), uni(rgba(255, 255, 255, 0.16)))
	c.text(label, x+14, y+19, f, uni(colSoft), 1.6)
	return w
}

const pad = 64.0

func (c *canvas) header(d Data) {
	c.logo(pad, 52, 44)
	w := c.text("vibe-rpc", pad+58, 82, c.face("Inter-Bold", 26), uni(colText), -0.4)
	c.pill("WRAPPED", pad+58+w+14, 60)
	if d.Since != "" {
		f := c.face("JetBrainsMono-Regular", 14)
		sw := c.measure(d.Since, f, 1.8)
		c.text(d.Since, Width-pad-sw, 80, f, uni(colMute), 1.8)
	}
}

func (c *canvas) hero(d Data) {
	x := pad
	c.text("ACTIVE CODING TIME", x, 196, c.face("JetBrainsMono-Bold", 15), uni(colMute), 2.4)

	size := c.fit(d.Hours, "Inter-ExtraBold", 170, 560, -6)
	base := 196 + 24 + size*0.76
	c.text(d.Hours, x-6, base, c.face("Inter-ExtraBold", size), vgrad{(base - size*0.75) * c.s, base * c.s, rgba(255, 255, 255, 1), rgba(0xb4, 0xb9, 0xc4, 1)}, -6)

	sub := d.Subline
	c.text(sub, x, base+50, c.face("Inter-Medium", 24), uni(colSoft), 0)

	// Antigravity vs Claude Code split bar
	barY, barW := base+92, 520.0
	total := d.AgyMs + d.ClaudeMs
	c.fill(path{rrect(x, barY, barW, 10, 5)}, uni(rgba(255, 255, 255, 0.08)))
	if total > 0 {
		aw := barW * float64(d.AgyMs) / float64(total)
		if aw > 0 {
			c.fill(path{rrect(x, barY, aw, 10, 5)}, uni(rgba(0xf5, 0xf5, 0xf7, 0.95)))
		}
		if cw := barW - aw; cw > 0 {
			c.fill(path{rrect(x+aw+math.Min(aw, 3), barY, cw-math.Min(aw, 3), 10, 5)}, uni(rgba(0x8c, 0x90, 0x99, 0.9)))
		}
	}
	f := c.face("JetBrainsMono-Regular", 14)
	ly := barY + 38
	c.fill(path{circle(x+5, ly-5, 5)}, uni(colText))
	lx := x + 18 + c.text("Antigravity "+format.FmtHours(d.AgyMs), x+18, ly, f, uni(colSoft), 0.2)
	c.fill(path{circle(lx+28, ly-5, 5)}, uni(colMute))
	c.text("Claude Code "+format.FmtHours(d.ClaudeMs), lx+41, ly, f, uni(colSoft), 0.2)
}

func (c *canvas) panel(d Data) {
	x, y, w, h := 704.0, 152.0, Width-pad-704.0, 374.0
	// Drop shadow: a few stacked translucent rects fake a soft blur.
	for i := 6; i >= 1; i-- {
		o := float64(i) * 4
		c.fill(path{rrect(x-o/2, y+o, w+o, h, 26+o/2)}, uni(rgba(0, 0, 0, 0.06)))
	}
	// Frosted glass fill + rim, like --glass-bg-strong / --glass-shadow.
	c.fill(path{rrect(x, y, w, h, 26)}, uni(rgba(0x0c, 0x0d, 0x11, 0.55)))
	c.fill(path{rrect(x, y, w, h, 26)}, vgrad{y * c.s, (y + h) * c.s, rgba(255, 255, 255, 0.10), rgba(255, 255, 255, 0.035)})
	c.fill(border(x, y, w, h, 26, 1), uni(rgba(255, 255, 255, 0.14)))
	c.fill(path{rrect(x+26, y+0.5, w-52, 1.2, 0.6)}, uni(rgba(255, 255, 255, 0.30))) // top highlight

	stats := d.Stats
	if len(stats) > 6 {
		stats = stats[:6]
	}
	rows := (len(stats) + 1) / 2
	if rows == 0 {
		return
	}
	ip := 34.0
	cw := (w - ip*2) / 2
	rh := (h - ip*2) / float64(rows)
	for i, st := range stats {
		col, row := i%2, i/2
		cx := x + ip + float64(col)*cw
		cy := y + ip + float64(row)*rh
		if row > 0 {
			lineW := cw
			if col == 0 {
				lineW -= 24
			}
			c.fill(path{rrect(cx, cy-2, lineW, 1, 0.5)}, uni(rgba(255, 255, 255, 0.075)))
		}
		c.text(strings.ToUpper(st.Label), cx, cy+30, c.face("JetBrainsMono-Bold", 12.5), uni(colFaint), 1.8)
		vs := c.fit(st.Value, "Inter-Bold", 38, cw-28, -0.8)
		c.text(st.Value, cx, cy+30+12+38*0.9, c.face("Inter-Bold", vs), uni(colText), -0.8)
	}
}

func (c *canvas) footer(d Data) {
	y := Height - 46.0
	c.fill(path{rrect(pad, y-34, Width-2*pad, 1, 0.5)}, uni(rgba(255, 255, 255, 0.075)))
	f := c.face("JetBrainsMono-Regular", 14)
	c.text(d.FooterLeft, pad, y, f, uni(colMute), 0.4)
	if d.FooterRight != "" {
		w := c.measure(d.FooterRight, f, 1.8)
		c.text(d.FooterRight, Width-pad-w, y, f, uni(colFaint), 1.8)
	}
}
