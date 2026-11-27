package main

import (
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	size       = 256
	frameCount = 36
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

func sdRoundedBox(px, py, bx, by, r float64) float64 {
	qx := math.Abs(px) - bx + r
	qy := math.Abs(py) - by + r
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

func smoothstep(edge0, edge1, x float64) float64 {
	t := math.Max(0, math.Min(1, (x-edge0)/(edge1-edge0)))
	return t * t * (3 - 2*t)
}

// Signed distance to a sleek 8-ray geometric starburst emblem (Claude-inspired original emblem)
func sdStarburst(px, py, cx, cy, outerR, thickness, rot float64) float64 {
	best := 1e9
	const rays = 8
	for i := 0; i < rays; i++ {
		a := rot + float64(i)*math.Pi/float64(rays)
		dx := math.Cos(a) * outerR
		dy := math.Sin(a) * outerR
		d := sdSegment(px, py, cx-dx, cy-dy, cx+dx, cy+dy) - thickness
		if d < best {
			best = d
		}
	}
	core := math.Hypot(px-cx, py-cy) - thickness*2.1
	if core < best {
		best = core
	}
	return best
}

// Signed distance to the sleek hollow Antigravity 'A' prism
func sdAntigravityA(px, py, cx, ay, scale float64) float64 {
	apexX, apexY := cx, ay-54*scale
	leftX, leftY := cx-45*scale, ay+44*scale
	rightX, rightY := cx+45*scale, ay+44*scale
	archLX, archLY := cx-23*scale, ay+24*scale
	archMX, archMY := cx, ay+14*scale
	archRX, archRY := cx+23*scale, ay+24*scale

	dLeft := sdSegment(px, py, apexX, apexY, leftX, leftY)
	dRight := sdSegment(px, py, apexX, apexY, rightX, rightY)
	dArch1 := sdSegment(px, py, archLX, archLY, archMX, archMY)
	dArch2 := sdSegment(px, py, archMX, archMY, archRX, archRY)
	return math.Min(math.Min(dLeft, dRight)-10.5*scale, math.Min(dArch1, dArch2)-6.8*scale)
}

// Signed distance to a terminal prompt ">_" (OpenCode emblem; original art).
// cursorOn hides the underscore for the blink.
func sdPrompt(px, py, cx, cy, scale float64, cursorOn bool) float64 {
	tipX, tipY := cx-4*scale, cy
	top := sdSegment(px, py, cx-40*scale, cy-34*scale, tipX, tipY)
	bot := sdSegment(px, py, tipX, tipY, cx-40*scale, cy+34*scale)
	d := math.Min(top, bot) - 9*scale
	if cursorOn {
		under := sdSegment(px, py, cx+10*scale, cy+34*scale, cx+44*scale, cy+34*scale) - 8*scale
		d = math.Min(d, under)
	}
	return d
}

// Signed distance to a pair of curly braces "{ }" (Codex emblem; original art).
func sdBraces(px, py, cx, cy, scale float64) float64 {
	brace := func(dir float64) float64 {
		pts := [][2]float64{{-14, -40}, {-24, -34}, {-24, -8}, {-34, 0}, {-24, 8}, {-24, 34}, {-14, 40}}
		d := 1e9
		for i := 0; i+1 < len(pts); i++ {
			ax, ay := cx+dir*(pts[i][0]-12)*scale, cy+pts[i][1]*scale
			bx, by := cx+dir*(pts[i+1][0]-12)*scale, cy+pts[i+1][1]*scale
			d = math.Min(d, sdSegment(px, py, ax, ay, bx, by))
		}
		return d - 7.5*scale
	}
	return math.Min(brace(1), brace(-1))
}

type themeSpec struct {
	name      string
	kind      string // "antigravity", "claude", "dual", "opencode", "codex"
	bgTop     rgb
	bgBot     rgb
	accentA   rgb
	accentB   rgb
	badge     rgb
	mode      string
	delayCS   int
	bobAmp    float64
	orbitSpd  float64
	starAlpha float64
}

func buildPalette(spec themeSpec) color.Palette {
	pal := make(color.Palette, 0, 256)
	for i := 0; i < 32; i++ {
		t := float64(i) / 31.0
		c := lerpColor(rgb{10, 12, 18}, rgb{46, 52, 70}, t)
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
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
	for i := 0; i < 48; i++ {
		u := float64(i%8) / 7.0
		v := float64(i/8) / 5.0
		mid := lerpColor(spec.accentA, spec.accentB, u)
		c := lerpColor(spec.bgTop, lerpColor(mid, rgb{255, 255, 255}, 0.25), 0.2+0.8*v)
		pal = append(pal, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
	}
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
		bobY = -math.Abs(math.Sin(theta))*10.0 + 3.0
	}

	type star struct{ x, y, r, ph float64 }
	stars := []star{
		{38, 42, 1.8, 0.2}, {214, 52, 2.0, 1.5}, {52, 206, 1.6, 2.7},
		{208, 198, 2.1, 4.1}, {182, 34, 1.5, 5.0}, {32, 132, 1.4, 3.3},
		{226, 128, 1.7, 0.9}, {96, 30, 1.3, 2.1}, {158, 224, 1.5, 4.8},
	}

	type orb struct {
		x, y, rad float64
		col       rgb
	}
	var orbs []orb
	switch spec.mode {
	case "dual":
		for i := 0; i < 4; i++ {
			a := theta + float64(i)*math.Pi/2
			ox := cx + math.Cos(a)*88
			oy := cy + math.Sin(a)*46
			c := spec.accentA
			if i%2 == 1 {
				c = spec.accentB
			}
			orbs = append(orbs, orb{ox, oy, 4.8 + 1.2*math.Sin(a), c})
		}
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
			orbs = append(orbs, orb{ox, oy, 5.0 + 1.4*math.Sin(a), c})
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
			orbs = append(orbs, orb{ox, oy, (1.0 - t) * 5.6, c})
		}
	case "compacting":
		for i := 0; i < 4; i++ {
			t := math.Mod(phase+float64(i)*0.25, 1.0)
			dist := (1.0 - t) * 92
			ang := theta + float64(i)*math.Pi/2
			orbs = append(orbs, orb{cx + math.Cos(ang)*dist, cy + math.Sin(ang)*dist, 3.8 + t*2.4, spec.accentB})
		}
	default:
		for i := 0; i < 2; i++ {
			a := theta + float64(i)*math.Pi
			orbs = append(orbs, orb{cx + math.Cos(a)*76, cy + math.Sin(a)*28, 3.8, spec.accentB})
		}
	}

	ay := cy + bobY

	bayer := [4][4]float64{
		{0, 8, 2, 10},
		{12, 4, 14, 6},
		{3, 11, 1, 9},
		{15, 7, 13, 5},
	}

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

	// Dual mode positions: twin Antigravity 'A' on left-front + Claude starburst on right-back bobbing in harmony
	dualAgyX, dualAgyY := cx-34, cy+math.Sin(theta)*6.0
	dualCCX, dualCCY := cx+40, cy-math.Sin(theta)*6.0

	for y := 0; y < size; y++ {
		fy := float64(y)
		vGrad := fy / float64(size)
		for x := 0; x < size; x++ {
			fx := float64(x)

			// 1. Sleek dark slate background + atmospheric bloom
			col := lerpColor(spec.bgTop, spec.bgBot, vGrad)
			if spec.kind == "dual" {
				dA := math.Hypot(fx-dualAgyX, fy-dualAgyY)
				dC := math.Hypot(fx-dualCCX, fy-dualCCY)
				col = screenGlow(col, spec.accentA, math.Exp(-(dA*dA)/(2*72*72))*0.34)
				col = screenGlow(col, spec.accentB, math.Exp(-(dC*dC)/(2*72*72))*0.34)
			} else {
				distC := math.Hypot(fx-cx, fy-ay)
				bloom := math.Exp(-(distC * distC) / (2 * 82 * 82))
				col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, 0.5+0.5*math.Sin(theta)), bloom*0.36)
			}

			// 2. Sleek rounded app-icon card rim
			boxD := sdRoundedBox(fx-cx, fy-cy, 118, 118, 36)
			if boxD > 0 {
				col = lerpColor(col, rgb{10, 12, 17}, smoothstep(0, 8, boxD)*0.65)
			} else if math.Abs(boxD) < 2.2 {
				rim := 1.0 - math.Abs(boxD)/2.2
				col = addGlow(col, rgb{160, 175, 205}, rim*0.18)
			}

			// 3. Background stars
			for _, st := range stars {
				sd := math.Hypot(fx-st.x, fy-st.y)
				if sd < st.r*2.8 {
					twinkle := 0.45 + 0.55*math.Sin(theta+st.ph)
					a := math.Exp(-(sd*sd)/(2*st.r*st.r)) * twinkle * spec.starAlpha
					col = screenGlow(col, rgb{235, 240, 255}, a)
				}
			}

			// 4. Status rings & waves
			distC := math.Hypot(fx-cx, fy-ay)
			switch spec.mode {
			case "dual":
				// Twin counter-tilted intersecting orbital rings
				ang1 := 0.32
				rx1 := ((fx-cx)*math.Cos(ang1) - (fy-cy)*math.Sin(ang1)) / 92.0
				ry1 := ((fx-cx)*math.Sin(ang1) + (fy-cy)*math.Cos(ang1)) / 42.0
				rd1 := math.Abs(math.Hypot(rx1, ry1) - 1.0)
				if rd1 < 0.075 {
					col = addGlow(col, spec.accentA, (1.0-rd1/0.075)*0.52)
				}
				ang2 := -0.32
				rx2 := ((fx-cx)*math.Cos(ang2) - (fy-cy)*math.Sin(ang2)) / 92.0
				ry2 := ((fx-cx)*math.Sin(ang2) + (fy-cy)*math.Cos(ang2)) / 42.0
				rd2 := math.Abs(math.Hypot(rx2, ry2) - 1.0)
				if rd2 < 0.075 {
					col = addGlow(col, spec.accentB, (1.0-rd2/0.075)*0.52)
				}
			case "working":
				rx := (fx - cx) / 84.0
				ry := (fy - cy) / 34.0
				rd := math.Abs(math.Hypot(rx, ry) - 1.0)
				if rd < 0.08 {
					col = addGlow(col, spec.accentA, (1.0-rd/0.08)*0.55)
				}
			case "thinking":
				for k := 0; k < 2; k++ {
					p := math.Mod(phase+float64(k)*0.5, 1.0)
					rad := 34 + p*76
					d := math.Abs(distC - rad)
					if d < 4.0 {
						col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, p), (1.0-d/4.0)*(1.0-p)*0.72)
					}
				}
			case "compacting":
				for k := 0; k < 3; k++ {
					p := math.Mod(phase+float64(k)/3.0, 1.0)
					rad := 104 - p*72
					d := math.Abs(distC - rad)
					if d < 3.5 {
						col = screenGlow(col, spec.accentB, (1.0-d/3.5)*math.Sin(p*math.Pi)*0.75)
					}
				}
			case "notification":
				for k := 0; k < 2; k++ {
					p := math.Mod(phase+float64(k)*0.5, 1.0)
					rad := 42 + p*68
					d := math.Abs(distC - rad)
					if d < 4.5 {
						col = screenGlow(col, spec.badge, (1.0-d/4.5)*(1.0-p)*0.85)
					}
				}
			case "idle", "stale":
				rx := (fx - cx) / 76.0
				ry := (fy - cy) / 28.0
				rd := math.Abs(math.Hypot(rx, ry) - 1.0)
				if rd < 0.06 {
					col = addGlow(col, spec.accentA, (1.0-rd/0.06)*0.30)
				}
			}

			// 5. Floating ground shadow
			shX := (fx - cx) / (46.0 - bobY*0.4)
			shY := (fy - (cy + 70)) / 9.0
			shD := shX*shX + shY*shY
			if shD < 1.6 {
				col = addGlow(col, spec.accentA, math.Exp(-shD*2.2)*0.20)
			}

			// 6. Primary Emblem(s)
			switch spec.kind {
			case "antigravity":
				dGlyph := sdAntigravityA(fx, fy, cx, ay, 1.0)
				if dGlyph > 0 && dGlyph < 22 {
					glowA := math.Exp(-(dGlyph*dGlyph)/(2*9*9)) * 0.50
					gradT := math.Max(0, math.Min(1, (fx-(cx-50))/100.0))
					col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, gradT), glowA)
				}
				if dGlyph < 1.5 {
					cov := 1.0 - smoothstep(-1.2, 1.2, dGlyph)
					gradX := math.Max(0, math.Min(1, (fx-(cx-48))/96.0))
					gradY := math.Max(0, math.Min(1, (fy-(ay-54))/100.0))
					baseG := lerpColor(spec.accentA, spec.accentB, gradX*0.7+gradY*0.3)
					sheen := math.Max(0, 1.0-gradY*1.6) * 0.45
					col = addGlow(col, lerpColor(baseG, rgb{248, 251, 255}, sheen), cov)
				}

			case "claude":
				rot := theta * 0.25
				if spec.mode == "idle" || spec.mode == "stale" {
					rot = math.Sin(theta) * 0.14
				}
				dStar := sdStarburst(fx, fy, cx, ay, 48, 5.8, rot)
				if dStar > 0 && dStar < 22 {
					glowA := math.Exp(-(dStar*dStar)/(2*9*9)) * 0.52
					gradT := math.Max(0, math.Min(1, (fx-(cx-50))/100.0))
					col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, gradT), glowA)
				}
				if dStar < 1.5 {
					cov := 1.0 - smoothstep(-1.2, 1.2, dStar)
					gradY := math.Max(0, math.Min(1, (fy-(ay-50))/100.0))
					baseG := lerpColor(spec.accentB, spec.accentA, gradY)
					sheen := math.Max(0, 1.0-gradY*1.6) * 0.42
					col = addGlow(col, lerpColor(baseG, rgb{255, 248, 240}, sheen), cov)
				}

			case "codex":
				rot := math.Sin(theta) * 0.05
				if spec.mode == "idle" || spec.mode == "stale" {
					rot = 0
				}
				// A gentle breathing scale stands in for rotation on a symmetric glyph.
				dB := sdBraces(fx, fy, cx, ay, 1.0+rot)
				if dB > 0 && dB < 22 {
					glowA := math.Exp(-(dB*dB)/(2*9*9)) * 0.52
					gradT := math.Max(0, math.Min(1, (fx-(cx-50))/100.0))
					col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, gradT), glowA)
				}
				if dB < 1.5 {
					cov := 1.0 - smoothstep(-1.2, 1.2, dB)
					gradY := math.Max(0, math.Min(1, (fy-(ay-44))/90.0))
					baseG := lerpColor(spec.accentB, spec.accentA, gradY)
					sheen := math.Max(0, 1.0-gradY*1.6) * 0.45
					col = addGlow(col, lerpColor(baseG, rgb{246, 243, 255}, sheen), cov)
				}

			case "opencode":
				// Cursor blinks: on for the first half of the loop (always on when idle/away).
				cursorOn := frame < frameCount/2 || spec.mode == "idle" || spec.mode == "stale"
				dP := sdPrompt(fx, fy, cx, ay, 1.0, cursorOn)
				if dP > 0 && dP < 22 {
					glowA := math.Exp(-(dP*dP)/(2*9*9)) * 0.50
					gradT := math.Max(0, math.Min(1, (fx-(cx-50))/100.0))
					col = screenGlow(col, lerpColor(spec.accentA, spec.accentB, gradT), glowA)
				}
				if dP < 1.5 {
					cov := 1.0 - smoothstep(-1.2, 1.2, dP)
					gradY := math.Max(0, math.Min(1, (fy-(ay-40))/90.0))
					baseG := lerpColor(spec.accentA, spec.accentB, gradY)
					sheen := math.Max(0, 1.0-gradY*1.6) * 0.45
					col = addGlow(col, lerpColor(baseG, rgb{240, 255, 250}, sheen), cov)
				}

			case "dual":
				// Antigravity 'A' prism (left)
				dA := sdAntigravityA(fx, fy, dualAgyX, dualAgyY, 0.72)
				if dA > 0 && dA < 20 {
					col = screenGlow(col, spec.accentA, math.Exp(-(dA*dA)/(2*8*8))*0.52)
				}
				if dA < 1.5 {
					cov := 1.0 - smoothstep(-1.2, 1.2, dA)
					gy := math.Max(0, math.Min(1, (fy-(dualAgyY-40))/80.0))
					col = addGlow(col, lerpColor(spec.accentA, rgb{230, 246, 255}, math.Max(0, 1-gy*1.5)*0.55), cov)
				}
				// Claude geometric starburst (right)
				dC := sdStarburst(fx, fy, dualCCX, dualCCY, 34, 4.4, theta*0.25)
				if dC > 0 && dC < 20 {
					col = screenGlow(col, spec.accentB, math.Exp(-(dC*dC)/(2*8*8))*0.52)
				}
				if dC < 1.5 {
					cov := 1.0 - smoothstep(-1.2, 1.2, dC)
					gy := math.Max(0, math.Min(1, (fy-(dualCCY-36))/72.0))
					col = addGlow(col, lerpColor(spec.accentB, rgb{255, 244, 232}, math.Max(0, 1-gy*1.5)*0.5), cov)
				}
			}

			// 7. Orbiting particles
			for _, o := range orbs {
				if o.rad <= 0.3 {
					continue
				}
				od := math.Hypot(fx-o.x, fy-o.y)
				if od < o.rad*3.5 {
					col = screenGlow(col, o.col, math.Exp(-(od*od)/(2*o.rad*o.rad))*0.72)
					if od < o.rad {
						core := 1.0 - smoothstep(o.rad*0.4, o.rad, od)
						col = addGlow(col, rgb{255, 255, 255}, core*0.85)
					}
				}
			}

			// 8. Corner live status indicator dot
			bx, by := float64(size)-40, float64(size)-40
			bd := math.Hypot(fx-bx, fy-by)
			pulse := 0.75 + 0.25*math.Sin(theta*2)
			if bd < 22 {
				col = screenGlow(col, spec.badge, math.Exp(-(bd*bd)/(2*9*9))*0.55*pulse)
			}
			if bd < 10.5 {
				col = addGlow(col, spec.bgBot, 1.0-smoothstep(8.5, 10.5, bd))
			}
			if bd < 7.5 {
				col = addGlow(col, spec.badge, 1.0-smoothstep(5.8, 7.5, bd))
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
		// Antigravity GIFs (7 statuses)
		{
			name: "agy-working.gif", kind: "antigravity", mode: "working", delayCS: 9, bobAmp: 6.0, orbitSpd: 1.0, starAlpha: 0.75,
			bgTop: rgb{18, 22, 34}, bgBot: rgb{12, 14, 21},
			accentA: rgb{66, 133, 244}, accentB: rgb{56, 189, 248}, badge: rgb{52, 168, 83},
		},
		{
			name: "agy-thinking.gif", kind: "antigravity", mode: "thinking", delayCS: 10, bobAmp: 7.5, orbitSpd: 1.0, starAlpha: 0.85,
			bgTop: rgb{22, 20, 38}, bgBot: rgb{13, 14, 22},
			accentA: rgb{99, 102, 241}, accentB: rgb{167, 139, 250}, badge: rgb{138, 180, 248},
		},
		{
			name: "agy-compacting.gif", kind: "antigravity", mode: "compacting", delayCS: 9, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{19, 22, 38}, bgBot: rgb{12, 13, 20},
			accentA: rgb{83, 127, 196}, accentB: rgb{129, 140, 248}, badge: rgb{96, 165, 250},
		},
		{
			name: "agy-shipped.gif", kind: "antigravity", mode: "shipped", delayCS: 8, bobAmp: 8.5, orbitSpd: 1.0, starAlpha: 0.9,
			bgTop: rgb{15, 28, 26}, bgBot: rgb{12, 15, 20},
			accentA: rgb{52, 168, 83}, accentB: rgb{66, 133, 244}, badge: rgb{251, 188, 5},
		},
		{
			name: "agy-notification.gif", kind: "antigravity", mode: "notification", delayCS: 9, bobAmp: 5.0, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{32, 24, 18}, bgBot: rgb{15, 14, 18},
			accentA: rgb{251, 188, 5}, accentB: rgb{249, 115, 22}, badge: rgb{251, 188, 5},
		},
		{
			name: "agy-idle.gif", kind: "antigravity", mode: "idle", delayCS: 13, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.8,
			bgTop: rgb{17, 20, 30}, bgBot: rgb{11, 13, 19},
			accentA: rgb{83, 127, 196}, accentB: rgb{138, 180, 248}, badge: rgb{148, 163, 184},
		},
		{
			name: "agy-stale.gif", kind: "antigravity", mode: "stale", delayCS: 15, bobAmp: 2.0, orbitSpd: 1.0, starAlpha: 0.45,
			bgTop: rgb{16, 18, 24}, bgBot: rgb{11, 12, 16},
			accentA: rgb{71, 85, 105}, accentB: rgb{100, 116, 139}, badge: rgb{71, 85, 105},
		},

		// Dual Wielding GIF (Antigravity + Claude Code harmonic twin orbit)
		{
			name: "agy-dual.gif", kind: "dual", mode: "dual", delayCS: 9, bobAmp: 5.5, orbitSpd: 1.0, starAlpha: 0.85,
			bgTop: rgb{22, 20, 34}, bgBot: rgb{11, 12, 18},
			accentA: rgb{66, 133, 244}, accentB: rgb{217, 119, 87}, badge: rgb{52, 211, 153},
		},

		// Original Custom Claude Code GIFs (warm terracotta / amber geometric starburst)
		{
			name: "claude-working.gif", kind: "claude", mode: "working", delayCS: 9, bobAmp: 6.0, orbitSpd: 1.0, starAlpha: 0.75,
			bgTop: rgb{30, 20, 18}, bgBot: rgb{15, 12, 14},
			accentA: rgb{217, 119, 87}, accentB: rgb{245, 166, 126}, badge: rgb{52, 168, 83},
		},
		{
			name: "claude-thinking.gif", kind: "claude", mode: "thinking", delayCS: 10, bobAmp: 7.5, orbitSpd: 1.0, starAlpha: 0.85,
			bgTop: rgb{30, 20, 26}, bgBot: rgb{14, 12, 16},
			accentA: rgb{217, 119, 87}, accentB: rgb{251, 191, 140}, badge: rgb{245, 166, 126},
		},
		{
			name: "claude-compacting.gif", kind: "claude", mode: "compacting", delayCS: 9, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{28, 20, 22}, bgBot: rgb{14, 12, 15},
			accentA: rgb{204, 107, 73}, accentB: rgb{236, 154, 116}, badge: rgb{245, 166, 126},
		},
		{
			name: "claude-shipped.gif", kind: "claude", mode: "shipped", delayCS: 8, bobAmp: 8.5, orbitSpd: 1.0, starAlpha: 0.9,
			bgTop: rgb{22, 26, 20}, bgBot: rgb{12, 14, 15},
			accentA: rgb{52, 168, 83}, accentB: rgb{217, 119, 87}, badge: rgb{251, 188, 5},
		},
		{
			name: "claude-notification.gif", kind: "claude", mode: "notification", delayCS: 9, bobAmp: 5.0, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{34, 24, 16}, bgBot: rgb{15, 13, 14},
			accentA: rgb{251, 188, 5}, accentB: rgb{217, 119, 87}, badge: rgb{251, 188, 5},
		},
		{
			name: "claude-idle.gif", kind: "claude", mode: "idle", delayCS: 13, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.8,
			bgTop: rgb{24, 19, 19}, bgBot: rgb{12, 11, 13},
			accentA: rgb{190, 116, 92}, accentB: rgb{224, 158, 132}, badge: rgb{148, 163, 184},
		},
		{
			name: "claude-stale.gif", kind: "claude", mode: "stale", delayCS: 15, bobAmp: 2.0, orbitSpd: 1.0, starAlpha: 0.45,
			bgTop: rgb{20, 18, 19}, bgBot: rgb{11, 11, 13},
			accentA: rgb{110, 90, 86}, accentB: rgb{140, 120, 116}, badge: rgb{90, 95, 105},
		},
		// Codex GIFs (violet curly braces)
		{
			name: "codex-working.gif", kind: "codex", mode: "working", delayCS: 9, bobAmp: 6.0, orbitSpd: 1.0, starAlpha: 0.75,
			bgTop: rgb{24, 18, 36}, bgBot: rgb{12, 11, 18},
			accentA: rgb{139, 92, 246}, accentB: rgb{196, 181, 253}, badge: rgb{52, 168, 83},
		},
		{
			name: "codex-thinking.gif", kind: "codex", mode: "thinking", delayCS: 10, bobAmp: 7.5, orbitSpd: 1.0, starAlpha: 0.85,
			bgTop: rgb{22, 20, 40}, bgBot: rgb{12, 12, 20},
			accentA: rgb{124, 108, 246}, accentB: rgb{165, 180, 252}, badge: rgb{165, 180, 252},
		},
		{
			name: "codex-compacting.gif", kind: "codex", mode: "compacting", delayCS: 9, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{22, 18, 34}, bgBot: rgb{12, 11, 18},
			accentA: rgb{120, 90, 200}, accentB: rgb{170, 150, 240}, badge: rgb{196, 181, 253},
		},
		{
			name: "codex-shipped.gif", kind: "codex", mode: "shipped", delayCS: 8, bobAmp: 8.5, orbitSpd: 1.0, starAlpha: 0.9,
			bgTop: rgb{18, 28, 28}, bgBot: rgb{11, 14, 18},
			accentA: rgb{52, 211, 153}, accentB: rgb{167, 139, 250}, badge: rgb{251, 188, 5},
		},
		{
			name: "codex-notification.gif", kind: "codex", mode: "notification", delayCS: 9, bobAmp: 5.0, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{32, 24, 18}, bgBot: rgb{15, 14, 18},
			accentA: rgb{251, 188, 5}, accentB: rgb{249, 115, 22}, badge: rgb{251, 188, 5},
		},
		{
			name: "codex-idle.gif", kind: "codex", mode: "idle", delayCS: 13, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.8,
			bgTop: rgb{20, 18, 30}, bgBot: rgb{11, 11, 17},
			accentA: rgb{110, 95, 170}, accentB: rgb{160, 150, 220}, badge: rgb{148, 163, 184},
		},
		{
			name: "codex-stale.gif", kind: "codex", mode: "stale", delayCS: 15, bobAmp: 2.0, orbitSpd: 1.0, starAlpha: 0.45,
			bgTop: rgb{17, 17, 22}, bgBot: rgb{11, 11, 15},
			accentA: rgb{85, 80, 110}, accentB: rgb{120, 115, 145}, badge: rgb{71, 85, 105},
		},

		// OpenCode GIFs (teal terminal prompt with a blinking cursor)
		{
			name: "opencode-working.gif", kind: "opencode", mode: "working", delayCS: 9, bobAmp: 6.0, orbitSpd: 1.0, starAlpha: 0.75,
			bgTop: rgb{14, 26, 26}, bgBot: rgb{10, 14, 16},
			accentA: rgb{45, 212, 191}, accentB: rgb{125, 233, 214}, badge: rgb{52, 168, 83},
		},
		{
			name: "opencode-thinking.gif", kind: "opencode", mode: "thinking", delayCS: 10, bobAmp: 7.5, orbitSpd: 1.0, starAlpha: 0.85,
			bgTop: rgb{16, 24, 32}, bgBot: rgb{10, 13, 18},
			accentA: rgb{56, 189, 248}, accentB: rgb{94, 234, 212}, badge: rgb{125, 211, 252},
		},
		{
			name: "opencode-compacting.gif", kind: "opencode", mode: "compacting", delayCS: 9, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{15, 24, 28}, bgBot: rgb{10, 13, 17},
			accentA: rgb{45, 160, 170}, accentB: rgb{103, 199, 214}, badge: rgb{94, 234, 212},
		},
		{
			name: "opencode-shipped.gif", kind: "opencode", mode: "shipped", delayCS: 8, bobAmp: 8.5, orbitSpd: 1.0, starAlpha: 0.9,
			bgTop: rgb{14, 30, 24}, bgBot: rgb{10, 15, 16},
			accentA: rgb{52, 211, 153}, accentB: rgb{45, 212, 191}, badge: rgb{251, 188, 5},
		},
		{
			name: "opencode-notification.gif", kind: "opencode", mode: "notification", delayCS: 9, bobAmp: 5.0, orbitSpd: 1.0, starAlpha: 0.65,
			bgTop: rgb{32, 24, 18}, bgBot: rgb{15, 14, 18},
			accentA: rgb{251, 188, 5}, accentB: rgb{249, 115, 22}, badge: rgb{251, 188, 5},
		},
		{
			name: "opencode-idle.gif", kind: "opencode", mode: "idle", delayCS: 13, bobAmp: 4.5, orbitSpd: 1.0, starAlpha: 0.8,
			bgTop: rgb{15, 22, 24}, bgBot: rgb{10, 13, 16},
			accentA: rgb{56, 150, 150}, accentB: rgb{125, 200, 200}, badge: rgb{148, 163, 184},
		},
		{
			name: "opencode-stale.gif", kind: "opencode", mode: "stale", delayCS: 15, bobAmp: 2.0, orbitSpd: 1.0, starAlpha: 0.45,
			bgTop: rgb{16, 18, 20}, bgBot: rgb{11, 12, 14},
			accentA: rgb{71, 95, 100}, accentB: rgb{100, 125, 130}, badge: rgb{71, 85, 105},
		},
	}

	// Optional name prefix filter (e.g. "opencode-") so existing, possibly
	// hand-edited GIFs aren't regenerated.
	if len(os.Args) > 1 {
		var kept []themeSpec
		for _, sp := range specs {
			if strings.HasPrefix(sp.name, os.Args[1]) {
				kept = append(kept, sp)
			}
		}
		specs = kept
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
