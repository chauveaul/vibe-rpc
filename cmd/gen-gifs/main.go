package main

import (
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"math"
	"os"
	"path/filepath"
)

const (
	size       = 256
	frameCount = 24
)

type rgb struct{ r, g, b float64 }

func lerpColor(a, b rgb, t float64) rgb {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return rgb{
		r: a.r + (b.r-a.r)*t,
		g: a.g + (b.g-a.g)*t,
		b: a.b + (b.b-a.b)*t,
	}
}

func addGlow(base, glow rgb, alpha float64) rgb {
	if alpha <= 0 {
		return base
	}
	if alpha > 1 {
		alpha = 1
	}
	return rgb{
		r: math.Min(255, base.r*(1-alpha)+glow.r*alpha),
		g: math.Min(255, base.g*(1-alpha)+glow.g*alpha),
		b: math.Min(255, base.b*(1-alpha)+glow.b*alpha),
	}
}

func screenGlow(base, glow rgb, strength float64) rgb {
	if strength <= 0 {
		return base
	}
	if strength > 1 {
		strength = 1
	}
	return rgb{
		r: math.Min(255, base.r+glow.r*strength),
		g: math.Min(255, base.g+glow.g*strength),
		b: math.Min(255, base.b+glow.b*strength),
	}
}

// Signed distance to a line segment AB from point P.
func sdSegment(px, py, ax, ay, bx, by float64) float64 {
	pax, pay := px-ax, py-ay
	bax, bay := bx-ax, by-ay
	denom := bax*bax + bay*bay
	if denom == 0 {
		return math.Hypot(pax, pay)
	}
	h := math.Max(0, math.Min(1, (pax*bax+pay*bay)/denom))
	return math.Hypot(pax-bax*h, pay-bay*h)
}

// Signed distance to a rounded box centered at origin.
func sdRoundedBox(px, py, bx, by, r float64) float64 {
	qx := math.Abs(px) - bx + r
	qy := math.Abs(py) - by + r
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func smoothstep(edge0, edge1, x float64) float64 {
	t := math.Max(0, math.Min(1, (x-edge0)/(edge1-edge0)))
	return t * t * (3 - 2*t)
}

type themeSpec struct {
	name      string
	bgTop     rgb
	bgBot     rgb
	accentA   rgb
	accentB   rgb
	badge     rgb
	mode      string
	delayCS   int // centiseconds per frame
	bobAmp    float64
	orbitSpd  float64
	starAlpha float64
}

func buildPalette(spec themeSpec) color.Palette {
	// 256-color smooth palette tailored to this status theme (dark slate + accents + whites)
	pal := make(color.Palette, 0, 256)
	// 32 background-to-surface dark slate tones
	for i := 0; i < 32; i++ {
		t := float64(i) / 31.0
		c := lerpColor(rgb{10, 12, 18}, rgb{46, 52, 70}, t)
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
	// 48 accentA ramp (dark -> accentA -> bright white)
	for i := 0; i < 48; i++ {
		t := float64(i) / 47.0
		var c rgb
		if t < 0.55 {
			c = lerpColor(spec.bgBot, spec.accentA, t/0.55)
		} else {
			c = lerpColor(spec.accentA, rgb{248, 251, 255}, (t-0.55)/0.45)
		}
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
	// 48 accentB ramp
	for i := 0; i < 48; i++ {
		t := float64(i) / 47.0
		var c rgb
		if t < 0.55 {
			c = lerpColor(spec.bgBot, spec.accentB, t/0.55)
		} else {
			c = lerpColor(spec.accentB, rgb{248, 251, 255}, (t-0.55)/0.45)
		}
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
	// 48 blend accentA <-> accentB across brightnesses
	for i := 0; i < 48; i++ {
		u := float64(i%8) / 7.0
		v := float64(i/8) / 5.0
		mid := lerpColor(spec.accentA, spec.accentB, u)
		c := lerpColor(spec.bgTop, lerpColor(mid, rgb{255, 255, 255}, 0.25), 0.2+0.8*v)
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
	// 40 badge color ramp
	for i := 0; i < 40; i++ {
		t := float64(i) / 39.0
		var c rgb
		if t < 0.6 {
			c = lerpColor(spec.bgBot, spec.badge, t/0.6)
		} else {
			c = lerpColor(spec.badge, rgb{255, 255, 255}, (t-0.6)/0.4)
		}
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
	// 40 neutral grayscale / white highlights
	for i := 0; i < 40; i++ {
		t := float64(i) / 39.0
		c := lerpColor(rgb{40, 45, 58}, rgb{253, 254, 255}, t)
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
	return pal
}

func renderFrame(spec themeSpec, frame int, pal color.Palette) *image.Paletted {
	img := image.NewPaletted(image.Rect(0, 0, size, size), pal)
	phase := float64(frame) / float64(frameCount)
	theta := phase * 2 * math.Pi

	cx, cy := float64(size)/2.0, float64(size)/2.0
	bobY := math.Sin(theta) * spec.bobAmp
	if spec.mode == "shipped" {
		bobY = -math.Abs(math.Sin(theta))*11.0 + 3.0
	}

	// Deterministic subtle background stars
	type star struct{ x, y, r, ph float64 }
	stars := []star{
		{38, 42, 1.8, 0.2}, {214, 52, 2.0, 1.5}, {52, 206, 1.6, 2.7},
		{208, 198, 2.1, 4.1}, {182, 34, 1.5, 5.0}, {32, 132, 1.4, 3.3},
		{226, 128, 1.7, 0.9}, {96, 30, 1.3, 2.1}, {158, 224, 1.5, 4.8},
	}

	// Precompute orbiting particles
	type orb struct {
		x, y, rad float64
		col       rgb
	}
	var orbs []orb
	switch spec.mode {
	case "working":
		for i := 0; i < 3; i++ {
			a := theta*spec.orbitSpd + float64(i)*2*math.Pi/3
			ox := cx + math.Cos(a)*82
			oy := cy + math.Sin(a)*34
			c := spec.accentA
			if i == 1 {
				c = spec.accentB
			} else if i == 2 {
				c = spec.badge
			}
			orbs = append(orbs, orb{ox, oy, 5.2 + 1.5*math.Sin(a), c})
		}
	case "shipped":
		for i := 0; i < 6; i++ {
			t := math.Mod(phase+float64(i)/6.0, 1.0)
			ang := -math.Pi/2 + (float64(i)-2.5)*0.48
			dist := 22 + t*88
			ox := cx + math.Cos(ang)*dist
			oy := (cy + bobY) + math.Sin(ang)*dist
			c := spec.accentA
			if i%2 == 1 {
				c = spec.badge
			}
			orbs = append(orbs, orb{ox, oy, (1.0 - t) * 5.8, c})
		}
	case "compacting":
		for i := 0; i < 4; i++ {
			t := math.Mod(phase+float64(i)*0.25, 1.0)
			dist := (1.0 - t) * 92
			ang := theta + float64(i)*math.Pi/2
			orbs = append(orbs, orb{cx + math.Cos(ang)*dist, cy + math.Sin(ang)*dist, 3.8 + t*2.5, spec.accentB})
		}
	default:
		for i := 0; i < 2; i++ {
			a := theta + float64(i)*math.Pi
			orbs = append(orbs, orb{cx + math.Cos(a)*76, cy + math.Sin(a)*28, 3.8, spec.accentB})
		}
	}

	// Antigravity sleek floating 'A' prism geometry (clean open hollow arch)
	ay := cy + bobY
	apexX, apexY := cx, ay-54
	leftX, leftY := cx-45, ay+44
	rightX, rightY := cx+45, ay+44
	// Low smooth crescent bridge that leaves a crisp hollow triangle inside the 'A'
	archLX, archLY := cx-23, ay+24
	archMX, archMY := cx, ay+14
	archRX, archRY := cx+23, ay+24

	// 4x4 Bayer ordered dither matrix for silky band-free 256-color gradients
	bayer := [4][4]float64{
		{0, 8, 2, 10},
		{12, 4, 14, 6},
		{3, 11, 1, 9},
		{15, 7, 13, 5},
	}

	// Build nearest-color lookup cache for speed
	palRGBA := make([]rgb, len(pal))
	for i, c := range pal {
		r, g, b, _ := c.RGBA()
		palRGBA[i] = rgb{float64(r >> 8), float64(g >> 8), float64(b >> 8)}
	}
	nearest := func(c rgb, x, y int) uint8 {
		dither := (bayer[y&3][x&3]/16.0 - 0.5) * 6.5
		cr := math.Max(0, math.Min(255, c.r+dither))
		cg := math.Max(0, math.Min(255, c.g+dither))
		cb := math.Max(0, math.Min(255, c.b+dither))
		bestIdx := 0
		bestDist := 1e18
		for i, p := range palRGBA {
			dr := cr - p.r
			dg := cg - p.g
			db := cb - p.b
			d := dr*dr + dg*dg + db*db
			if d < bestDist {
				bestDist = d
				bestIdx = i
			}
		}
		return uint8(bestIdx)
	}

	for y := 0; y < size; y++ {
		fy := float64(y)
		vGrad := fy / float64(size)
		for x := 0; x < size; x++ {
			fx := float64(x)

			// 1. Sleek dark slate background with soft atmospheric radial bloom
			col := lerpColor(spec.bgTop, spec.bgBot, vGrad)
			distC := math.Hypot(fx-cx, fy-ay)
			bloom := math.Exp(-(distC * distC) / (2 * 82 * 82))
			col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, 0.5+0.5*math.Sin(theta)), bloom*0.36)

			// 2. Subtle rounded frame rim (sleek Apple/Google app-icon silhouette)
			boxD := sdRoundedBox(fx-cx, fy-cy, 118, 118, 36)
			if boxD > 0 {
				// Soft vignette outside icon card
				col = lerpColor(col, rgb{10, 12, 17}, smoothstep(0, 8, boxD)*0.65)
			} else if math.Abs(boxD) < 2.2 {
				rim := 1.0 - math.Abs(boxD)/2.2
				col = addGlow(col, rgb{140, 165, 210}, rim*0.18)
			}

			// 3. Twinkling background stars
			for _, st := range stars {
				sd := math.Hypot(fx-st.x, fy-st.y)
				if sd < st.r*2.8 {
					twinkle := 0.45 + 0.55*math.Sin(theta+st.ph)
					a := math.Exp(-(sd*sd)/(2*st.r*st.r)) * twinkle * spec.starAlpha
					col = screenGlow(col, rgb{230, 240, 255}, a)
				}
			}

			// 4. Status-specific rings & waves
			switch spec.mode {
			case "working":
				// Tilted sleek orbital ring
				rx := (fx - cx) / 84.0
				ry := (fy - cy) / 34.0
				rd := math.Abs(math.Hypot(rx, ry) - 1.0)
				if rd < 0.08 {
					alpha := (1.0 - rd/0.08) * 0.55
					col = addGlow(col, spec.accentA, alpha)
				}
			case "thinking":
				// Expanding neural halo rings
				for k := 0; k < 2; k++ {
					p := math.Mod(phase+float64(k)*0.5, 1.0)
					rad := 34 + p*76
					d := math.Abs(distC - rad)
					if d < 4.0 {
						alpha := (1.0 - d/4.0) * (1.0 - p) * 0.72
						col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, p), alpha)
					}
				}
			case "compacting":
				// Contracting precision rings
				for k := 0; k < 3; k++ {
					p := math.Mod(phase+float64(k)/3.0, 1.0)
					rad := 104 - p*72
					d := math.Abs(distC - rad)
					if d < 3.5 {
						alpha := (1.0 - d/3.5) * math.Sin(p*math.Pi) * 0.75
						col = screenGlow(col, spec.accentB, alpha)
					}
				}
			case "notification":
				// Amber beacon ping rings
				for k := 0; k < 2; k++ {
					p := math.Mod(phase+float64(k)*0.5, 1.0)
					rad := 42 + p*68
					d := math.Abs(distC - rad)
					if d < 4.5 {
						alpha := (1.0 - d/4.5) * (1.0 - p) * 0.85
						col = screenGlow(col, spec.badge, alpha)
					}
				}
			case "idle", "stale":
				// Tranquil tilted halo
				rx := (fx - cx) / 76.0
				ry := (fy - cy) / 28.0
				rd := math.Abs(math.Hypot(rx, ry) - 1.0)
				if rd < 0.06 {
					alpha := (1.0 - rd/0.06) * 0.30
					col = addGlow(col, spec.accentA, alpha)
				}
			}

			// 5. Floating ground-shadow beneath the levitating Antigravity prism
			shX := (fx - cx) / (46.0 - bobY*0.4)
			shY := (fy - (cy + 70)) / 9.0
			shD := shX*shX + shY*shY
			if shD < 1.6 {
				sh := math.Exp(-shD*2.2) * 0.42
				col = addGlow(col, spec.accentA, sh*0.45)
			}

			// 6. Antigravity sleek sculpted 'A' prism (thick rounded anti-aliased strokes)
			dLeft := sdSegment(fx, fy, apexX, apexY, leftX, leftY)
			dRight := sdSegment(fx, fy, apexX, apexY, rightX, rightY)
			dArch1 := sdSegment(fx, fy, archLX, archLY, archMX, archMY)
			dArch2 := sdSegment(fx, fy, archMX, archMY, archRX, archRY)

			dOuter := math.Min(dLeft, dRight)
			dArch := math.Min(dArch1, dArch2)
			dGlyph := math.Min(dOuter-10.5, dArch-6.8)

			// Outer glow around the Antigravity glyph
			if dGlyph > 0 && dGlyph < 22 {
				glowA := math.Exp(-(dGlyph*dGlyph)/(2*9*9)) * 0.50
				gradT := math.Max(0, math.Min(1, (fx-(cx-50))/100.0))
				col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, gradT), glowA)
			}
			// Crisp anti-aliased glyph fill with vertical + diagonal sheen
			if dGlyph < 1.5 {
				cov := 1.0 - smoothstep(-1.2, 1.2, dGlyph)
				gradX := math.Max(0, math.Min(1, (fx-(cx-48))/96.0))
				gradY := math.Max(0, math.Min(1, (fy-(ay-54))/100.0))
				baseG := lerpColor(spec.accentA, spec.accentB, gradX*0.7+gradY*0.3)
				// Subtle top glossy highlight
				sheen := math.Max(0, 1.0-gradY*1.6) * 0.45
				glyphCol := lerpColor(baseG, rgb{248, 251, 255}, sheen)
				col = addGlow(col, glyphCol, cov)
			}

			// 7. Orbiting / launched glowing particles
			for _, o := range orbs {
				if o.rad <= 0.3 {
					continue
				}
				od := math.Hypot(fx-o.x, fy-o.y)
				if od < o.rad*3.5 {
					halo := math.Exp(-(od*od)/(2*o.rad*o.rad)) * 0.72
					col = screenGlow(col, o.col, halo)
					if od < o.rad {
						core := 1.0 - smoothstep(o.rad*0.4, o.rad, od)
						col = addGlow(col, rgb{255, 255, 255}, core*0.85)
					}
				}
			}

			// 8. Corner live status indicator pill/dot (bottom-right)
			bx, by := float64(size)-40, float64(size)-40
			bd := math.Hypot(fx-bx, fy-by)
			pulse := 0.75 + 0.25*math.Sin(theta*2)
			if bd < 22 {
				col = screenGlow(col, spec.badge, math.Exp(-(bd*bd)/(2*9*9))*0.55*pulse)
			}
			if bd < 10.5 {
				// Dark collar around status dot
				col = addGlow(col, spec.bgBot, 1.0-smoothstep(8.5, 10.5, bd))
			}
			if bd < 7.5 {
				dotCov := 1.0 - smoothstep(5.8, 7.5, bd)
				col = addGlow(col, spec.badge, dotCov)
			}

			img.SetColorIndex(x, y, nearest(col, x, y))
		}
	}
	return img
}

func main() {
	outDir := filepath.Join("internal", "server", "assets", "gifs")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	specs := []themeSpec{
		{
			name: "agy-working.gif", mode: "working", delayCS: 5, bobAmp: 6.5, orbitSpd: 1.0, starAlpha: 0.75,
			bgTop: rgb{18, 22, 34}, bgBot: rgb{12, 14, 21},
			accentA: rgb{66, 133, 244}, accentB: rgb{56, 189, 248}, badge: rgb{52, 168, 83},
		},
		{
			name: "agy-thinking.gif", mode: "thinking", delayCS: 6, bobAmp: 8.5, orbitSpd: 1.0, starAlpha: 0.85,
			bgTop: rgb{22, 20, 38}, bgBot: rgb{13, 14, 22},
			accentA: rgb{99, 102, 241}, accentB: rgb{167, 139, 250}, badge: rgb{138, 180, 248},
		},
		{
			name: "agy-compacting.gif", mode: "compacting", delayCS: 5, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{19, 22, 38}, bgBot: rgb{12, 13, 20},
			accentA: rgb{83, 127, 196}, accentB: rgb{129, 140, 248}, badge: rgb{96, 165, 250},
		},
		{
			name: "agy-shipped.gif", mode: "shipped", delayCS: 5, bobAmp: 9.0, orbitSpd: 1.0, starAlpha: 0.9,
			bgTop: rgb{15, 28, 26}, bgBot: rgb{12, 15, 20},
			accentA: rgb{52, 168, 83}, accentB: rgb{66, 133, 244}, badge: rgb{251, 188, 5},
		},
		{
			name: "agy-notification.gif", mode: "notification", delayCS: 5, bobAmp: 5.0, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{32, 24, 18}, bgBot: rgb{15, 14, 18},
			accentA: rgb{251, 188, 5}, accentB: rgb{249, 115, 22}, badge: rgb{251, 188, 5},
		},
		{
			name: "agy-idle.gif", mode: "idle", delayCS: 8, bobAmp: 4.5, orbitSpd: 0.5, starAlpha: 0.8,
			bgTop: rgb{17, 20, 30}, bgBot: rgb{11, 13, 19},
			accentA: rgb{83, 127, 196}, accentB: rgb{138, 180, 248}, badge: rgb{148, 163, 184},
		},
		{
			name: "agy-stale.gif", mode: "stale", delayCS: 10, bobAmp: 2.0, orbitSpd: 0.5, starAlpha: 0.45,
			bgTop: rgb{16, 18, 24}, bgBot: rgb{11, 12, 16},
			accentA: rgb{71, 85, 105}, accentB: rgb{100, 116, 139}, badge: rgb{71, 85, 105},
		},
	}

	for _, spec := range specs {
		pal := buildPalette(spec)
		g := &gif.GIF{LoopCount: 0}
		for f := 0; f < frameCount; f++ {
			g.Image = append(g.Image, renderFrame(spec, f, pal))
			g.Delay = append(g.Delay, spec.delayCS)
		}
		outPath := filepath.Join(outDir, spec.name)
		f, err := os.Create(outPath)
		if err != nil {
			panic(err)
		}
		if err := gif.EncodeAll(f, g); err != nil {
			_ = f.Close()
			panic(err)
		}
		_ = f.Close()
		info, _ := os.Stat(outPath)
		fmt.Printf("generated %s (%d KB)\n", outPath, info.Size()/1024)
	}
}
