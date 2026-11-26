package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRollout(t *testing.T, home, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "11", "27")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseCodexRollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	ts := func(sec int) string {
		return time.Date(2026, 11, 27, 10, 0, sec, 0, time.UTC).Format(time.RFC3339Nano)
	}
	p := writeRollout(t, home, "rollout-2026-11-27T10-00-00-abc.jsonl",
		`{"timestamp":"`+ts(0)+`","type":"session_meta","payload":{"id":"abc","cwd":"/w/api","timestamp":"`+ts(0)+`","cli_version":"0.150.0","source":"cli"}}`,
		`{"timestamp":"`+ts(1)+`","type":"turn_context","payload":{"cwd":"/w/api","model":"gpt-5.5-codex","approval_policy":"on-request"}}`,
		`{"timestamp":"`+ts(2)+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"}]}}`,
		`{"timestamp":"`+ts(3)+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the tests"}]}}`,
		`{"timestamp":"`+ts(3)+`","type":"event_msg","payload":{"type":"user_message","message":"fix the tests"}}`,
		`{"timestamp":"`+ts(10)+`","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"git commit -m fix\"]}","call_id":"c1"}}`,
		`{"timestamp":"`+ts(20)+`","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c2","input":"*** Begin Patch\n*** Update File: main.go\n@@\n-old\n+new\n+more\n*** End Patch"}}`,
		`{"timestamp":"`+ts(25)+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":200,"reasoning_output_tokens":50,"total_tokens":1200}}}}`,
		`{"timestamp":"`+ts(40)+`","type":"event_msg","payload":{"type":"user_message","message":"and lint"}}`,
		`{"timestamp":"`+ts(50)+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1500,"cached_input_tokens":900,"output_tokens":260,"reasoning_output_tokens":60,"total_tokens":1760}}}}`,
		`not json`,
	)
	if !isCodexRollout(p) {
		t.Fatal("rollout under CODEX_HOME/sessions should be recognised")
	}
	s, err := ParseCodexRollout(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "codex" || s.SessionID != "abc" || s.Project != "api" || s.Model != "gpt-5.5-codex" || s.IsSubagent {
		t.Fatalf("basics: %+v", s)
	}
	// input 1500 of which 900 cached -> 600 fresh; output 260 (reasoning included).
	if s.InputTokens != 600 || s.CacheReadTokens != 900 || s.OutputTokens != 260 {
		t.Fatalf("tokens: in=%d cached=%d out=%d", s.InputTokens, s.CacheReadTokens, s.OutputTokens)
	}
	if s.UserMessages != 2 {
		t.Fatalf("prompts = %d (event stream preferred, context skipped)", s.UserMessages)
	}
	if s.ToolCalls != 2 || s.BashCommands["git"] != 1 || s.Ships != 1 {
		t.Fatalf("tools: calls=%d bash=%v ships=%d", s.ToolCalls, s.BashCommands, s.Ships)
	}
	if s.FileEdits["/w/api/main.go"] != 1 || s.LinesAdded != 2 || s.LinesRemoved != 1 {
		t.Fatalf("patch: edits=%v +%d -%d", s.FileEdits, s.LinesAdded, s.LinesRemoved)
	}
	if s.ActiveMs != 50_000 || s.Cost != 0 || s.ByModel["gpt-5.5-codex"] == nil {
		t.Fatalf("active=%d cost=%v models=%v", s.ActiveMs, s.Cost, s.ByModel)
	}

	sub := writeRollout(t, home, "rollout-2026-11-27T10-05-00-sub.jsonl",
		`{"timestamp":"`+ts(0)+`","type":"session_meta","payload":{"id":"sub","cwd":"/w/api","thread_source":"subagent","parent_thread_id":"abc"}}`,
		`{"timestamp":"`+ts(5)+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"look up docs"}]}}`,
	)
	ss, _ := ParseCodexRollout(sub)
	if !ss.IsSubagent || ss.UserMessages != 1 {
		t.Fatalf("subagent rollout: sub=%v prompts=%d (falls back to response items)", ss.IsSubagent, ss.UserMessages)
	}
	if isCodexRollout(filepath.Join(home, "elsewhere", "rollout-x.jsonl")) {
		t.Fatal("files outside Codex dirs aren't rollouts")
	}
}

func TestScanPicksUpCodex(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "codex")
	t.Setenv("CODEX_HOME", home)
	t.Setenv("VIBE_RPC_CONFIG", filepath.Join(tmp, "config.json"))
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("HOME", tmp) // no real Antigravity/Claude/OpenCode data
	t.Setenv("PATH", tmp)
	ts := time.Now().UTC()
	at := func(s int) string { return ts.Add(time.Duration(s) * time.Second).Format(time.RFC3339Nano) }
	writeRollout(t, home, "rollout-x-1.jsonl",
		`{"timestamp":"`+at(0)+`","type":"session_meta","payload":{"id":"s1","cwd":"/w/app"}}`,
		`{"timestamp":"`+at(1)+`","type":"turn_context","payload":{"model":"gpt-5.5-codex"}}`,
		`{"timestamp":"`+at(2)+`","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		`{"timestamp":"`+at(30)+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10}}}}`,
	)
	res := Scan(ScanOptions{})
	cx := res.Aggregate.BySource["codex"]
	if cx == nil || cx.Sessions != 1 || cx.Tokens != 110 || cx.UserMessages != 1 {
		t.Fatalf("codex bySource = %+v", cx)
	}
	if len(res.Aggregate.SourceDays["codex"]) == 0 {
		t.Fatal("codex should have daily buckets")
	}
}
