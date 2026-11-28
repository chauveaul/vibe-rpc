package scanner

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/chauveaul/vibe-rpc/internal/detect"
	"github.com/chauveaul/vibe-rpc/internal/ships"
)

// GitHub Copilot CLI keeps one directory per session under
// $COPILOT_HOME/session-state/<session id>/ (default ~/.copilot). Its
// events.jsonl holds one {"type", "data", "id", "parentId", "timestamp"}
// record per line: session.start, user.message, assistant.message,
// tool.execution_start, session.model_change, session.shutdown, …
//
// Tokens: each assistant.message carries its outputTokens, which add up to
// the session total, so output is bucketed by message time. Input and cache
// tokens only appear in session.shutdown's per-model modelMetrics, where
// inputTokens already includes cacheReadTokens. Copilot bills premium
// requests rather than tokens, so cost stays $0.

// CopilotSessionDir is where Copilot CLI keeps its sessions.
func CopilotSessionDir() string { return filepath.Join(detect.CopilotHome(), "session-state") }

// isCopilotEvents reports whether fp is a Copilot CLI session log.
func isCopilotEvents(fp string) bool {
	return filepath.Base(fp) == "events.jsonl" &&
		filepath.Dir(filepath.Dir(fp)) == CopilotSessionDir()
}

// listCopilotEvents returns every session-state/<id>/events.jsonl. The
// session folders also hold checkpoints and file snapshots, so they aren't
// walked like other transcript dirs.
func listCopilotEvents() []string {
	matches, _ := filepath.Glob(filepath.Join(CopilotSessionDir(), "*", "events.jsonl"))
	return matches
}

type copilotLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type copilotUsage struct {
	Input      int64 `json:"inputTokens"`
	Output     int64 `json:"outputTokens"`
	CacheRead  int64 `json:"cacheReadTokens"`
	CacheWrite int64 `json:"cacheWriteTokens"`
}

type copilotModelMetrics struct {
	Requests struct {
		Count int `json:"count"`
	} `json:"requests"`
	Usage copilotUsage `json:"usage"`
}

// copilotStrArg reads a string tool argument.
func copilotStrArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// ParseCopilotEvents turns one Copilot CLI events.jsonl into a TranscriptSummary.
func ParseCopilotEvents(fp string) (*TranscriptSummary, error) {
	f, err := os.Open(fp)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := newSummary()
	s.Source = "copilot"
	model := ""
	var records []tsRec
	files := map[string]bool{}
	// session.shutdown totals are per run; a resumed session may log another.
	// Counting the growth since the previous one never double-counts.
	prevByModel := map[string]copilotUsage{}
	sawMetrics := false
	msgOut := map[string]int64{} // output tokens per model, used without metrics
	msgTurns := map[string]int{}

	addTs := func(ts int64) {
		if ts > 0 {
			records = append(records, tsRec{ts: ts, day: DayKey(ts), week: WeekKey(ts), hour: HourKey(ts)})
		}
	}
	buckets := func(ts int64) []*DayBucket {
		if ts <= 0 {
			return nil
		}
		return []*DayBucket{ensureDay(s.ByDay, DayKey(ts)), ensureDay(s.ByWeek, WeekKey(ts)), ensureDay(s.ByHour, HourKey(ts))}
	}
	lines := func(ts int64, added, removed int) {
		s.LinesAdded += added
		s.LinesRemoved += removed
		if ts > 0 {
			b := ensureDay(s.ByDay, DayKey(ts))
			b.LinesAdded += added
			b.LinesRemoved += removed
		}
	}
	edited := func(path string, ts int64) {
		if path == "" {
			return
		}
		if !filepath.IsAbs(path) && s.Cwd != "" {
			path = filepath.Join(s.Cwd, path)
		}
		files[path] = true
		s.FileEdits[path]++
		if ts > s.FileEditTs[path] {
			s.FileEditTs[path] = ts
		}
	}

	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		raw, rerr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			var ln copilotLine
			if json.Unmarshal(raw, &ln) == nil && ln.Type != "" {
				ts := parseCodexTs(ln.Timestamp)
				addTs(ts)
				switch ln.Type {
				case "session.start", "session.resume", "session.context_changed":
					var d struct {
						SessionID string `json:"sessionId"`
						Cwd       string `json:"cwd"`
						Context   struct {
							Cwd string `json:"cwd"`
						} `json:"context"`
					}
					if json.Unmarshal(ln.Data, &d) == nil {
						if d.SessionID != "" && s.SessionID == "" {
							s.SessionID = d.SessionID
						}
						if c := firstNonEmpty(d.Context.Cwd, d.Cwd); c != "" && s.Cwd == "" {
							s.Cwd = c
						}
					}
				case "session.model_change":
					var d struct {
						NewModel string `json:"newModel"`
					}
					if json.Unmarshal(ln.Data, &d) == nil && d.NewModel != "" {
						model = d.NewModel
					}
				case "user.message":
					s.UserMessages++
					if ts > 0 {
						ensureDay(s.ByDay, DayKey(ts)).UserMessages++
					}
				case "assistant.message":
					var d struct {
						OutputTokens   int64  `json:"outputTokens"`
						ParentToolCall string `json:"parentToolCallId"`
					}
					if json.Unmarshal(ln.Data, &d) != nil || d.OutputTokens <= 0 {
						break
					}
					s.OutputTokens += d.OutputTokens
					for _, b := range buckets(ts) {
						b.OutputTokens += d.OutputTokens
					}
					if d.ParentToolCall == "" { // subagents may run on another model
						msgOut[model] += d.OutputTokens
						msgTurns[model]++
					}
				case "tool.execution_start":
					var d struct {
						ToolName  string `json:"toolName"`
						Arguments any    `json:"arguments"`
					}
					if json.Unmarshal(ln.Data, &d) != nil || d.ToolName == "" || d.ToolName == "report_intent" {
						break // report_intent only labels the step in Copilot's UI
					}
					s.ToolCalls++
					s.ToolBreakdown[d.ToolName]++
					if ts > 0 {
						ensureDay(s.ByDay, DayKey(ts)).ToolCalls++
					}
					args, _ := d.Arguments.(map[string]any)
					if p := copilotStrArg(args, "path"); s.Cwd == "" && filepath.IsAbs(p) {
						s.Cwd = inferProjectRoot(p) // older versions logged no cwd
					}
					switch d.ToolName {
					case "bash":
						cmd := copilotStrArg(args, "command")
						if tok := firstShellToken(cmd); tok != "" {
							s.BashCommands[tok]++
						}
						if sk := ships.Classify(cmd); sk != "" {
							s.Ships++
							s.ShipKinds[sk]++
							if ts > 0 {
								ensureDay(s.ByDay, DayKey(ts)).Ships++
							}
						}
					case "edit":
						edited(copilotStrArg(args, "path"), ts)
						lines(ts, countLines(copilotStrArg(args, "new_str")), countLines(copilotStrArg(args, "old_str")))
					case "create":
						edited(copilotStrArg(args, "path"), ts)
						lines(ts, countLines(copilotStrArg(args, "file_text")), 0)
					case "apply_patch":
						patch, _ := d.Arguments.(string)
						if patch == "" {
							patch = copilotStrArg(args, "input")
						}
						s.recordPatch(patch, ts, files)
					case "web_fetch":
						if host := domainOf(copilotStrArg(args, "url")); host != "" {
							s.WebDomains[host]++
						}
					case "task":
						s.Subagents[firstNonEmpty(copilotStrArg(args, "agent_type"), "subagent")]++
					}
				case "session.shutdown":
					var d struct {
						CurrentModel string                         `json:"currentModel"`
						ModelMetrics map[string]copilotModelMetrics `json:"modelMetrics"`
					}
					if json.Unmarshal(ln.Data, &d) != nil {
						break
					}
					if d.CurrentModel != "" {
						model = d.CurrentModel
					}
					for name, m := range d.ModelMetrics {
						sawMetrics = true
						cur, prev := m.Usage, prevByModel[name]
						delta := copilotUsage{Input: cur.Input - prev.Input, Output: cur.Output - prev.Output, CacheRead: cur.CacheRead - prev.CacheRead, CacheWrite: cur.CacheWrite - prev.CacheWrite}
						if delta.Input < 0 || delta.Output < 0 || delta.CacheRead < 0 || delta.CacheWrite < 0 {
							delta = cur // totals restarted: this run counts from zero
						}
						prevByModel[name] = cur
						fresh, cached := delta.Input-delta.CacheRead, delta.CacheRead
						if fresh < 0 {
							fresh, cached = 0, delta.Input
						}
						s.InputTokens += fresh
						s.CacheReadTokens += cached
						s.CacheWriteTokens += delta.CacheWrite
						for _, b := range buckets(ts) {
							b.InputTokens += fresh
							b.CacheReadTokens += cached
							b.CacheWriteTokens += delta.CacheWrite
						}
						mb := modelBucket(s, name)
						mb.Tokens += fresh + cached + delta.CacheWrite + delta.Output
						mb.Turns += m.Requests.Count
						s.ModelsUsed[name] += m.Requests.Count
					}
				}
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, rerr
		}
	}

	// A session that never shut down cleanly has no metrics: credit its
	// output tokens to whichever model was selected when each was written.
	if !sawMetrics {
		for name, out := range msgOut {
			if name == "" {
				name = "copilot" // never switched models: Copilot's default, unnamed
			}
			mb := modelBucket(s, name)
			mb.Tokens += out
			mb.Turns += msgTurns[name]
			s.ModelsUsed[name] += msgTurns[name]
		}
	}
	s.Model = model
	if s.Model == "" {
		s.Model = "copilot"
	}
	if s.Cwd != "" {
		s.Project = CleanProjectName(filepath.Base(s.Cwd))
	}
	if s.SessionID == "" {
		s.SessionID = filepath.Base(filepath.Dir(fp))
	}
	s.Files = make([]string, 0, len(files))
	for f := range files {
		s.Files = append(s.Files, f)
	}

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
	return s, nil
}

// modelBucket returns the summary's ByModel entry for a Copilot model name.
func modelBucket(s *TranscriptSummary, name string) *ModelBucket {
	key := openCodeModelKey(name)
	mb := s.ByModel[key]
	if mb == nil {
		mb = &ModelBucket{}
		s.ByModel[key] = mb
	}
	return mb
}
