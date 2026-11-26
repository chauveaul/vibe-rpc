package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/detect"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/ships"
)

// OpenCode keeps its history in a SQLite database. Rather than bundle a
// SQLite driver, vibe-rpc asks OpenCode itself: `opencode db --pure --format
// json "<sql>"` runs a read-only query through OpenCode's own engine (and
// --pure keeps plugins, including vibe-rpc's, from loading for it).
//
// Each OpenCode session becomes one cache entry keyed "opencode://<id>",
// re-read only when the session's time_updated changes.

const openCodeKeyPrefix = "opencode://"

// OpenCodeBinary finds the opencode CLI, or "" if it isn't installed.
func OpenCodeBinary() string { return detect.OpenCodeBinary() }

// openCodeQuery runs one read-only SQL query and decodes the JSON rows.
//
// Output goes to a temp file, not a pipe: `opencode db` exits before
// draining a pipe, truncating results at 64 KiB.
var openCodeQuery = func(bin, sql string) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f, err := os.CreateTemp("", "vibe-rpc-opencode-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	cmd := exec.CommandContext(ctx, bin, "db", "--pure", "--format", "json", sql)
	cmd.Stdout = f
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("opencode db: %w", err)
	}
	out, err := os.ReadFile(f.Name())
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("opencode db output: %w", err)
	}
	return rows, nil
}

func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		var f float64
		_, _ = fmt.Sscan(t, &f)
		return f
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// openCodeModelKey keeps OpenCode's own model ids (glm-…, gpt-…, kimi-…)
// unless they're a Claude/Gemini model vibe-rpc already knows how to name.
func openCodeModelKey(model string) string {
	low := strings.ToLower(model)
	// Only Claude/Gemini family names map to vibe-rpc's keys; the pricing
	// parser would otherwise read e.g. "glm-5.3-flash" as Gemini Flash.
	for _, fam := range []string{"claude", "opus", "sonnet", "haiku", "fable", "gemini"} {
		if strings.Contains(low, fam) {
			return pricing.ModelKeyFor(model)
		}
	}
	return low
}

// openCodeModelID unwraps session.model, stored as JSON {"id":…,"providerID":…}.
func openCodeModelID(v any) string {
	s := str(v)
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		var m struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(s), &m) == nil {
			return m.ID
		}
	}
	return s
}

// scanOpenCode refreshes OpenCode sessions in cache and returns the keys it
// saw. ok is false when OpenCode couldn't be queried; callers then keep the
// existing OpenCode entries instead of marking them gone.
func scanOpenCode(cache *ScanCache, force bool) (seen map[string]bool, scanned int, ok bool) {
	seen = map[string]bool{}
	bin := OpenCodeBinary()
	if bin == "" {
		return seen, 0, false
	}
	sessions, err := openCodeQuery(bin, `select id, parent_id, directory, model, cost, tokens_input, tokens_output,
		tokens_reasoning, tokens_cache_read, tokens_cache_write, summary_additions, summary_deletions,
		time_created, time_updated from session`)
	if err != nil {
		return seen, 0, false
	}

	var changed []string
	rowsByID := map[string]map[string]any{}
	for _, r := range sessions {
		id := str(r["id"])
		if id == "" {
			continue
		}
		key := openCodeKeyPrefix + id
		seen[key] = true
		sig := fmt.Sprintf("%.0f", num(r["time_updated"]))
		if prev := cache.Files[key]; !force && prev != nil && prev.Sig == sig && !prev.Gone {
			continue
		}
		changed = append(changed, id)
		rowsByID[id] = r
	}
	if len(changed) == 0 {
		return seen, 0, true
	}

	for start := 0; start < len(changed); start += 150 {
		end := min(start+150, len(changed))
		batch := changed[start:end]
		quoted := make([]string, len(batch))
		for i, id := range batch {
			quoted[i] = sqlQuote(id)
		}
		in := strings.Join(quoted, ",")
		msgs, err := openCodeQuery(bin, `select session_id, time_created,
			json_extract(data,'$.role') role, json_extract(data,'$.modelID') model,
			json_extract(data,'$.cost') cost, json_extract(data,'$.time.completed') done,
			json_extract(data,'$.tokens.input') ti, json_extract(data,'$.tokens.output') tout,
			json_extract(data,'$.tokens.reasoning') tr, json_extract(data,'$.tokens.cache.read') tcr,
			json_extract(data,'$.tokens.cache.write') tcw
			from message where session_id in (`+in+`)`)
		if err != nil {
			return seen, scanned, false
		}
		parts, err := openCodeQuery(bin, `select session_id, time_created,
			json_extract(data,'$.type') type, json_extract(data,'$.tool') tool,
			json_extract(data,'$.state.input.filePath') file, json_extract(data,'$.state.input.command') cmd
			from part where session_id in (`+in+`)`)
		if err != nil {
			return seen, scanned, false
		}
		msgsBy := map[string][]map[string]any{}
		for _, m := range msgs {
			msgsBy[str(m["session_id"])] = append(msgsBy[str(m["session_id"])], m)
		}
		partsBy := map[string][]map[string]any{}
		for _, p := range parts {
			partsBy[str(p["session_id"])] = append(partsBy[str(p["session_id"])], p)
		}
		for _, id := range batch {
			row := rowsByID[id]
			sum := buildOpenCodeSummary(row, msgsBy[id], partsBy[id])
			sum.Sig = fmt.Sprintf("%.0f", num(row["time_updated"]))
			cache.Files[openCodeKeyPrefix+id] = sum
			scanned++
		}
	}
	return seen, scanned, true
}

// buildOpenCodeSummary turns one session's rows into a TranscriptSummary.
func buildOpenCodeSummary(row map[string]any, msgs, parts []map[string]any) *TranscriptSummary {
	s := newSummary()
	s.Source = "opencode"
	s.SessionID = str(row["id"])
	s.Cwd = str(row["directory"])
	if s.Cwd != "" {
		s.Project = CleanProjectName(filepath.Base(s.Cwd))
	}
	s.Model = openCodeModelID(row["model"])
	s.IsSubagent = str(row["parent_id"]) != ""
	s.LinesAdded = int(num(row["summary_additions"]))
	s.LinesRemoved = int(num(row["summary_deletions"]))

	var records []tsRec
	addTs := func(ts int64) {
		if ts > 0 {
			records = append(records, tsRec{ts: ts, day: DayKey(ts), week: WeekKey(ts), hour: HourKey(ts)})
		}
	}

	for _, m := range msgs {
		ts := int64(num(m["time_created"]))
		addTs(ts)
		addTs(int64(num(m["done"])))
		day := ensureDay(s.ByDay, DayKey(ts))
		switch str(m["role"]) {
		case "user":
			s.UserMessages++
			day.UserMessages++
			ensureDay(s.ByWeek, WeekKey(ts)).UserMessages++
			ensureDay(s.ByHour, HourKey(ts)).UserMessages++
		case "assistant":
			in, out := int64(num(m["ti"])), int64(num(m["tout"]))+int64(num(m["tr"]))
			cr, cw := int64(num(m["tcr"])), int64(num(m["tcw"]))
			cost := num(m["cost"])
			model := str(m["model"])
			s.InputTokens += in
			s.OutputTokens += out
			s.CacheReadTokens += cr
			s.CacheWriteTokens += cw
			s.Cost += cost
			for _, b := range []*DayBucket{day, ensureDay(s.ByWeek, WeekKey(ts)), ensureDay(s.ByHour, HourKey(ts))} {
				b.InputTokens += in
				b.OutputTokens += out
				b.CacheReadTokens += cr
				b.CacheWriteTokens += cw
				b.Cost += cost
			}
			if model != "" {
				s.Model = model
				mkey := openCodeModelKey(model)
				s.ModelsUsed[model]++
				mb := s.ByModel[mkey]
				if mb == nil {
					mb = &ModelBucket{}
					s.ByModel[mkey] = mb
				}
				mb.Turns++
				mb.Tokens += in + out + cr + cw
				mb.Cost += cost
				if cost > 0 {
					s.CostByModel[mkey] += cost
				}
			}
		}
	}

	files := map[string]bool{}
	for _, p := range parts {
		ts := int64(num(p["time_created"]))
		addTs(ts)
		if str(p["type"]) != "tool" {
			continue
		}
		tool := str(p["tool"])
		if tool == "" {
			continue
		}
		s.ToolCalls++
		s.ToolBreakdown[tool]++
		day := ensureDay(s.ByDay, DayKey(ts))
		day.ToolCalls++
		if f := str(p["file"]); f != "" {
			files[f] = true
			switch tool {
			case "edit", "write", "patch", "multiedit":
				s.FileEdits[f]++
				if ts > s.FileEditTs[f] {
					s.FileEditTs[f] = ts
				}
			}
		}
		if tool == "bash" {
			cmd := str(p["cmd"])
			if tok := firstShellToken(cmd); tok != "" {
				s.BashCommands[tok]++
			}
			if sk := ships.Classify(cmd); sk != "" {
				s.Ships++
				s.ShipKinds[sk]++
				day.Ships++
			}
		}
	}
	s.Files = make([]string, 0, len(files))
	for f := range files {
		s.Files = append(s.Files, f)
	}

	// Fall back to the session's own totals if per-message data was missing.
	if s.InputTokens+s.OutputTokens+s.CacheReadTokens+s.CacheWriteTokens == 0 {
		s.InputTokens = int64(num(row["tokens_input"]))
		s.OutputTokens = int64(num(row["tokens_output"])) + int64(num(row["tokens_reasoning"]))
		s.CacheReadTokens = int64(num(row["tokens_cache_read"]))
		s.CacheWriteTokens = int64(num(row["tokens_cache_write"]))
		s.Cost = num(row["cost"])
	}
	addTs(int64(num(row["time_created"])))

	// Active time and spans, with the same gap cap as transcripts.
	if len(records) > 0 {
		sort.Slice(records, func(i, j int) bool { return records[i].ts < records[j].ts })
		s.FirstTs, s.LastTs = records[0].ts, records[len(records)-1].ts
		for i := 1; i < len(records); i++ {
			prev := records[i-1]
			gap := records[i].ts - prev.ts
			if gap > 0 && gap < ActiveGapCapMs {
				s.ActiveMs += gap
				ensureDay(s.ByDay, prev.day).ActiveMs += gap
				ensureDay(s.ByWeek, prev.week).ActiveMs += gap
				ensureDay(s.ByHour, prev.hour).ActiveMs += gap
				if n := len(s.Spans); n > 0 && s.Spans[n-1][1] == prev.ts {
					s.Spans[n-1][1] = records[i].ts
				} else {
					s.Spans = append(s.Spans, [2]int64{prev.ts, records[i].ts})
				}
			}
		}
	}
	return s
}
