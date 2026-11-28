package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCopilotEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	dir := filepath.Join(home, "session-state", "cp-123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := func(sec int) string {
		return time.Date(2026, 11, 28, 10, 0, sec, 0, time.UTC).Format(time.RFC3339Nano)
	}
	line := func(sec int, typ, data string) string {
		return `{"type":"` + typ + `","data":` + data + `,"id":"x","parentId":null,"timestamp":"` + ts(sec) + `"}`
	}
	p := filepath.Join(dir, "events.jsonl")
	body := strings.Join([]string{
		line(0, "session.start", `{"sessionId":"cp-123","version":1,"producer":"copilot-agent","copilotVersion":"1.0.117","startTime":"`+ts(0)+`","context":{"cwd":"/w/web","gitRoot":"/w/web","branch":"main"}}`),
		line(1, "session.model_change", `{"newModel":"claude-sonnet-4.5"}`),
		line(2, "user.message", `{"content":"fix the build","transformedContent":"<reminder>…</reminder> fix the build","attachments":[]}`),
		line(3, "assistant.turn_start", `{"turnId":"0"}`),
		line(4, "assistant.message", `{"messageId":"m1","content":"","toolRequests":[],"outputTokens":120}`),
		line(5, "tool.execution_start", `{"toolCallId":"t0","toolName":"report_intent","arguments":{"intent":"Fixing build"}}`),
		line(5, "tool.execution_start", `{"toolCallId":"t1","toolName":"bash","arguments":{"command":"git commit -m fix","description":"Commit"}}`),
		line(6, "tool.execution_start", `{"toolCallId":"t2","toolName":"edit","arguments":{"path":"/w/web/src/app.ts","old_str":"a\nb","new_str":"c\nd\ne"}}`),
		line(7, "tool.execution_start", `{"toolCallId":"t3","toolName":"apply_patch","arguments":"*** Begin Patch\n*** Update File: src/server.ts\n@@\n-old\n+new\n*** End Patch"}`),
		line(8, "tool.execution_start", `{"toolCallId":"t4","toolName":"create","arguments":{"path":"/w/web/README.md","file_text":"one\ntwo\n"}}`),
		line(9, "tool.execution_start", `{"toolCallId":"t5","toolName":"task","arguments":{"agent_type":"explore","description":"look","prompt":"find it"}}`),
		line(10, "assistant.message", `{"messageId":"m2","content":"","toolRequests":[],"outputTokens":30,"parentToolCallId":"t5"}`),
		line(20, "user.message", `{"content":"and lint","attachments":[]}`),
		line(30, "assistant.message", `{"messageId":"m3","content":"done","toolRequests":[],"outputTokens":50}`),
		line(40, "session.shutdown", `{"shutdownType":"routine","currentModel":"claude-sonnet-4.5","modelMetrics":{"claude-sonnet-4.5":{"requests":{"count":3,"cost":1},"usage":{"inputTokens":1000,"outputTokens":170,"cacheReadTokens":700,"cacheWriteTokens":0}},"claude-haiku-4.5":{"requests":{"count":1,"cost":0},"usage":{"inputTokens":100,"outputTokens":30,"cacheReadTokens":40,"cacheWriteTokens":0}}}}`),
		`not json`,
	}, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Other session files (checkpoints, snapshots) are not transcripts.
	_ = os.MkdirAll(filepath.Join(dir, "rewind-snapshots"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "rewind-snapshots", "x.jsonl"), []byte("{}\n"), 0o644)

	if got := listCopilotEvents(); len(got) != 1 || got[0] != p || !isCopilotEvents(p) {
		t.Fatalf("listCopilotEvents = %v", got)
	}
	s, err := ParseCopilotEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "copilot" || s.SessionID != "cp-123" || s.Project != "web" || s.Model != "claude-sonnet-4.5" {
		t.Fatalf("basics: %+v", s)
	}
	if s.UserMessages != 2 {
		t.Fatalf("prompts = %d", s.UserMessages)
	}
	// Output from every message, subagent included; input net of cache reads.
	if s.OutputTokens != 200 || s.InputTokens != 360 || s.CacheReadTokens != 740 {
		t.Fatalf("tokens: in=%d cached=%d out=%d", s.InputTokens, s.CacheReadTokens, s.OutputTokens)
	}
	if s.ToolCalls != 5 || s.ToolBreakdown["report_intent"] != 0 {
		t.Fatalf("tools = %d %v (report_intent isn't a real tool)", s.ToolCalls, s.ToolBreakdown)
	}
	if s.BashCommands["git"] != 1 || s.Ships != 1 || s.Subagents["explore"] != 1 {
		t.Fatalf("bash/ships/subagents: %v %d %v", s.BashCommands, s.Ships, s.Subagents)
	}
	// edit: +3 -2, patch: +1 -1, create: +2.
	if s.LinesAdded != 6 || s.LinesRemoved != 3 || len(s.Files) != 3 || s.FileEdits["/w/web/src/server.ts"] != 1 {
		t.Fatalf("edits: +%d -%d files=%v", s.LinesAdded, s.LinesRemoved, s.Files)
	}
	if s.ModelsUsed["claude-sonnet-4.5"] != 3 || len(s.ByModel) != 2 {
		t.Fatalf("models: %v %v", s.ModelsUsed, s.ByModel)
	}
	if s.ActiveMs != 40_000 || s.ByDay[DayKey(s.FirstTs)].UserMessages != 2 {
		t.Fatalf("active = %d", s.ActiveMs)
	}
}

// A session with no shutdown record still credits output to its model.
func TestParseCopilotEventsWithoutShutdown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	dir := filepath.Join(home, "session-state", "cp-9")
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "events.jsonl")
	_ = os.WriteFile(p, []byte(strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"cp-9","context":{"cwd":"/w/api"}},"timestamp":"2026-11-28T10:00:00Z"}`,
		`{"type":"session.model_change","data":{"newModel":"gpt-5.3-codex"},"timestamp":"2026-11-28T10:00:01Z"}`,
		`{"type":"assistant.message","data":{"outputTokens":80},"timestamp":"2026-11-28T10:00:05Z"}`,
	}, "\n")+"\n"), 0o644)
	s, err := ParseCopilotEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.OutputTokens != 80 || s.InputTokens != 0 || s.ModelsUsed["gpt-5.3-codex"] != 1 || s.ByModel["gpt-5.3-codex"].Tokens != 80 {
		t.Fatalf("got out=%d in=%d models=%v", s.OutputTokens, s.InputTokens, s.ModelsUsed)
	}
}
