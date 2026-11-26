package scanner

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/detect"
	"github.com/chauveaul/vibe-rpc/internal/ships"
)

// Codex CLI writes one "rollout" JSONL file per session under
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl (archived ones under
// archived_sessions/). Each line is {"timestamp", "type", "payload"} where
// type is session_meta | turn_context | event_msg | response_item | compacted.
//
// Token usage comes from event_msg token_count records, whose
// info.total_token_usage is the session's running total. Per Codex's
// protocol, cached_input_tokens is a subset of input_tokens and reasoning
// tokens are part of output_tokens, so neither is added on top.

// CodexDirs lists where Codex keeps rollouts.
func CodexDirs() []string {
	h := detect.CodexHome()
	return []string{filepath.Join(h, "sessions"), filepath.Join(h, "archived_sessions")}
}

// isCodexRollout reports whether fp is a Codex rollout file.
func isCodexRollout(fp string) bool {
	if !strings.HasPrefix(filepath.Base(fp), "rollout-") {
		return false
	}
	for _, d := range CodexDirs() {
		if strings.HasPrefix(fp, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexTokenUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
}

func parseCodexTs(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// codexShellCommand flattens a Codex shell invocation: ["bash","-lc","git
// commit -m x"] -> "git commit -m x"; plain strings pass through.
func codexShellCommand(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, p := range c {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		if n := len(parts); n >= 3 && (parts[n-2] == "-lc" || parts[n-2] == "-c") {
			return parts[n-1]
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// parseApplyPatch extracts edited files and +/- line counts from Codex's
// apply_patch format (*** Update File: / *** Add File: / *** Delete File:).
func parseApplyPatch(patch string) (files []string, added, removed int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Update File: "):
			files = append(files, strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: ")))
		case strings.HasPrefix(line, "*** Add File: "):
			files = append(files, strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: ")))
		case strings.HasPrefix(line, "***"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return files, added, removed
}

// ParseCodexRollout turns one Codex rollout file into a TranscriptSummary.
func ParseCodexRollout(fp string) (*TranscriptSummary, error) {
	f, err := os.Open(fp)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := newSummary()
	s.Source = "codex"
	model := ""
	var prev codexTokenUsage
	var records []tsRec
	files := map[string]bool{}
	// Codex mirrors user prompts as event_msg user_message; newer versions
	// sometimes drop that stream, so fall back to response_item messages.
	eventPrompts, itemPrompts := map[string]int{}, map[string]int{}

	addTs := func(ts int64) {
		if ts > 0 {
			records = append(records, tsRec{ts: ts, day: DayKey(ts), week: WeekKey(ts), hour: HourKey(ts)})
		}
	}
	shell := func(cmd string, ts int64) {
		if cmd == "" {
			return
		}
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
	}
	tool := func(name string, ts int64) {
		s.ToolCalls++
		s.ToolBreakdown[name]++
		if ts > 0 {
			ensureDay(s.ByDay, DayKey(ts)).ToolCalls++
		}
	}

	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		raw, rerr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			var ln codexLine
			if json.Unmarshal(raw, &ln) == nil && ln.Type != "" {
				ts := parseCodexTs(ln.Timestamp)
				addTs(ts)
				switch ln.Type {
				case "session_meta":
					var m struct {
						ID             string `json:"id"`
						Cwd            string `json:"cwd"`
						Timestamp      string `json:"timestamp"`
						Source         any    `json:"source"`
						ThreadSource   string `json:"thread_source"`
						ParentThreadID string `json:"parent_thread_id"`
					}
					if json.Unmarshal(ln.Payload, &m) == nil {
						if m.ID != "" && s.SessionID == "" {
							s.SessionID = m.ID
						}
						if m.Cwd != "" && s.Cwd == "" {
							s.Cwd = m.Cwd
						}
						addTs(parseCodexTs(m.Timestamp))
						if m.ThreadSource == "subagent" || m.ParentThreadID != "" {
							s.IsSubagent = true
						}
						if src, ok := m.Source.(map[string]any); ok {
							if _, sub := src["subagent"]; sub {
								s.IsSubagent = true
							}
						}
					}
				case "turn_context":
					var tc struct {
						Cwd   string `json:"cwd"`
						Model string `json:"model"`
					}
					if json.Unmarshal(ln.Payload, &tc) == nil {
						if tc.Model != "" {
							model = tc.Model
							s.Model = tc.Model
						}
						if tc.Cwd != "" && s.Cwd == "" {
							s.Cwd = tc.Cwd
						}
					}
				case "event_msg":
					var ev struct {
						Type string `json:"type"`
						Info *struct {
							Total *codexTokenUsage `json:"total_token_usage"`
						} `json:"info"`
					}
					if json.Unmarshal(ln.Payload, &ev) != nil {
						break
					}
					switch ev.Type {
					case "user_message":
						if ts > 0 {
							eventPrompts[DayKey(ts)]++
						} else {
							eventPrompts[""]++
						}
					case "token_count":
						if ev.Info == nil || ev.Info.Total == nil {
							break
						}
						cur := *ev.Info.Total
						d := codexTokenUsage{Input: cur.Input - prev.Input, Cached: cur.Cached - prev.Cached, CacheWrite: cur.CacheWrite - prev.CacheWrite, Output: cur.Output - prev.Output}
						if d.Input < 0 || d.Output < 0 || d.Cached < 0 || d.CacheWrite < 0 {
							d = cur // totals restarted (e.g. a fork): count from zero
						}
						prev = cur
						fresh, cached := d.Input-d.Cached, d.Cached
						if fresh < 0 {
							fresh, cached = 0, d.Input
						}
						s.InputTokens += fresh
						s.CacheReadTokens += cached
						s.CacheWriteTokens += d.CacheWrite
						s.OutputTokens += d.Output
						if ts > 0 {
							for _, b := range []*DayBucket{ensureDay(s.ByDay, DayKey(ts)), ensureDay(s.ByWeek, WeekKey(ts)), ensureDay(s.ByHour, HourKey(ts))} {
								b.InputTokens += fresh
								b.CacheReadTokens += cached
								b.CacheWriteTokens += d.CacheWrite
								b.OutputTokens += d.Output
							}
						}
						if model != "" {
							mkey := openCodeModelKey(model)
							mb := s.ByModel[mkey]
							if mb == nil {
								mb = &ModelBucket{}
								s.ByModel[mkey] = mb
							}
							mb.Tokens += fresh + cached + d.CacheWrite + d.Output
							if d.Output > 0 {
								mb.Turns++
								s.ModelsUsed[model]++
							}
						}
					}
				case "response_item":
					var it struct {
						Type      string                  `json:"type"`
						Role      string                  `json:"role"`
						Name      string                  `json:"name"`
						Arguments string                  `json:"arguments"`
						Input     string                  `json:"input"`
						Content   []struct{ Text string } `json:"content"`
						Action    *struct {
							Command any `json:"command"`
						} `json:"action"`
					}
					if json.Unmarshal(ln.Payload, &it) != nil {
						break
					}
					switch it.Type {
					case "message":
						if it.Role != "user" || len(it.Content) == 0 {
							break
						}
						// Skip injected context (<environment_context>, <user_instructions>, AGENTS.md…).
						if txt := strings.TrimSpace(it.Content[0].Text); txt == "" || strings.HasPrefix(txt, "<") || strings.HasPrefix(txt, "# AGENTS.md") {
							break
						}
						if ts > 0 {
							itemPrompts[DayKey(ts)]++
						} else {
							itemPrompts[""]++
						}
					case "function_call":
						tool(it.Name, ts)
						var args map[string]any
						_ = json.Unmarshal([]byte(it.Arguments), &args)
						switch it.Name {
						case "shell", "exec_command", "container.exec", "local_shell":
							c := args["command"]
							if c == nil {
								c = args["cmd"]
							}
							shell(codexShellCommand(c), ts)
						case "apply_patch":
							patch, _ := args["input"].(string)
							if patch == "" {
								patch, _ = args["patch"].(string)
							}
							s.recordPatch(patch, ts, files)
						}
					case "local_shell_call":
						tool("shell", ts)
						if it.Action != nil {
							shell(codexShellCommand(it.Action.Command), ts)
						}
					case "custom_tool_call":
						tool(it.Name, ts)
						if it.Name == "apply_patch" {
							s.recordPatch(it.Input, ts, files)
						}
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

	prompts := eventPrompts
	if len(prompts) == 0 {
		prompts = itemPrompts
	}
	for day, n := range prompts {
		s.UserMessages += n
		if day != "" {
			ensureDay(s.ByDay, day).UserMessages += n
		}
	}
	if s.Cwd != "" {
		s.Project = CleanProjectName(filepath.Base(s.Cwd))
	}
	if s.SessionID == "" {
		s.SessionID = strings.TrimSuffix(filepath.Base(fp), ".jsonl")
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

// recordPatch credits an apply_patch call's edited files and line counts.
func (s *TranscriptSummary) recordPatch(patch string, ts int64, files map[string]bool) {
	edited, added, removed := parseApplyPatch(patch)
	for _, f := range edited {
		if !filepath.IsAbs(f) && s.Cwd != "" {
			f = filepath.Join(s.Cwd, f)
		}
		files[f] = true
		s.FileEdits[f]++
		if ts > s.FileEditTs[f] {
			s.FileEditTs[f] = ts
		}
	}
	s.LinesAdded += added
	s.LinesRemoved += removed
	if ts > 0 {
		b := ensureDay(s.ByDay, DayKey(ts))
		b.LinesAdded += added
		b.LinesRemoved += removed
	}
}
