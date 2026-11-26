package server

import (
	"fmt"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
)

// heatWeeks is how many full weeks precede the current one: 52 + the current
// partial week gives a rolling 12 months, like GitHub's contribution graph.
const heatWeeks = 52

type monthLabel struct {
	Col  int // 1-based grid column
	Name string
}

type yearHeatmap struct {
	Weeks      int // number of columns
	Cells      []heatCell
	Months     []monthLabel
	ActiveDays int
	TotalHours string
}

// buildYearHeatmap lays out the last 12 months as week columns (Sunday at the
// top) ending with the current week. Days after today are placeholders so
// every row stays aligned to its weekday.
func buildYearHeatmap(agg *scanner.Aggregate, now time.Time) yearHeatmap {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	thisSunday := today.AddDate(0, 0, -int(today.Weekday()))
	start := thisSunday.AddDate(0, 0, -7*heatWeeks)
	weeks := heatWeeks + 1

	var maxMs, totalMs int64
	dayMs := func(d time.Time) (*scanner.DayBucket, int64) {
		if agg == nil || agg.ByDay == nil {
			return nil, 0
		}
		b := agg.ByDay[d.Format("2006-01-02")]
		if b == nil {
			return nil, 0
		}
		return b, b.ActiveMs
	}
	for d := start; !d.After(today); d = d.AddDate(0, 0, 1) {
		if _, ms := dayMs(d); ms > maxMs {
			maxMs = ms
		}
	}

	hm := yearHeatmap{Weeks: weeks, Cells: make([]heatCell, 0, weeks*7)}
	hm.Months = monthLabels(start, today, weeks)
	for w := 0; w < weeks; w++ {
		weekStart := start.AddDate(0, 0, 7*w)
		for i := 0; i < 7; i++ {
			d := weekStart.AddDate(0, 0, i)
			if d.After(today) {
				hm.Cells = append(hm.Cells, heatCell{Future: true})
				continue
			}
			b, ms := dayMs(d)
			c := heatCell{Day: d.Format("2006-01-02"), Label: d.Format("Mon Jan 2, 2006"), Hours: format.FmtHours(ms), Level: heatLevel(ms, maxMs)}
			if b != nil {
				c.Prompts = b.UserMessages
			}
			if ms > 0 {
				hm.ActiveDays++
				totalMs += ms
			}
			hm.Cells = append(hm.Cells, c)
		}
	}
	hm.TotalHours = format.FmtHours(totalMs)
	return hm
}

// monthLabels marks the column holding each month's 1st. The first column is
// labelled with its own month only when the next label is far enough away
// not to collide (GitHub-style).
func monthLabels(start, today time.Time, weeks int) []monthLabel {
	var out []monthLabel
	for w := 0; w < weeks; w++ {
		for i := 0; i < 7; i++ {
			d := start.AddDate(0, 0, 7*w+i)
			if d.Day() == 1 && !d.After(today) {
				out = append(out, monthLabel{Col: w + 1, Name: d.Format("Jan")})
			}
		}
	}
	if len(out) == 0 || out[0].Col >= 4 {
		out = append([]monthLabel{{Col: 1, Name: start.Format("Jan")}}, out...)
	}
	return out
}

func heatLevel(ms, maxMs int64) string {
	if ms <= 0 || maxMs <= 0 {
		return ""
	}
	switch r := float64(ms) / float64(maxMs); {
	case r >= 0.75:
		return "l4"
	case r >= 0.5:
		return "l3"
	case r >= 0.25:
		return "l2"
	default:
		return "l1"
	}
}

func (c heatCell) Title() string {
	if c.Future {
		return ""
	}
	return fmt.Sprintf("%s: %s (%d prompts)", c.Label, c.Hours, c.Prompts)
}
