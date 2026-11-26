package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDualAgentTranscripts(t *testing.T) {
	tmp := t.TempDir()

	// Antigravity transcript with JSON-quoted string args
	agyDir := filepath.Join(tmp, "brain", "conv-abc", ".system_generated", "logs")
	if err := os.MkdirAll(agyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agyPath := filepath.Join(agyDir, "transcript.jsonl")
	agyJSONL := `{"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-05-01T10:00:00Z"}
{"type":"PLANNER_RESPONSE","step_index":1,"model":"gemini-3-pro-high","created_at":"2026-05-01T10:00:30Z","input_tokens":1000,"output_tokens":500,"cache_read_tokens":2000,"tool_calls":[{"name":"write_file","args":{"Path":"\"/tmp/myproj/main.go\"","Content":"\"package main\\nfunc main() {}\\n\""}}]}
{"type":"PLANNER_RESPONSE","step_index":2,"model":"gemini-3-flash","created_at":"2026-05-01T10:01:00Z","input_tokens":500,"output_tokens":200,"tool_calls":[{"name":"run_command","args":{"CommandLine":"\"git push origin main\""}}]}
`
	if err := os.WriteFile(agyPath, []byte(agyJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	agySum, err := ParseTranscript(agyPath, map[string]string{"conv-abc": "/tmp/myproj"})
	if err != nil {
		t.Fatal(err)
	}
	if agySum.Source != "antigravity" || agySum.UserMessages != 1 || agySum.ToolCalls != 2 || agySum.Ships != 1 {
		t.Fatalf("unexpected agy summary: %+v", agySum)
	}
	if agySum.InputTokens != 1500 || agySum.OutputTokens != 700 || agySum.CacheReadTokens != 2000 {
		t.Fatalf("unexpected agy tokens: in=%d out=%d cr=%d", agySum.InputTokens, agySum.OutputTokens, agySum.CacheReadTokens)
	}
	if agySum.Model != "gemini-3-flash" {
		t.Fatalf("expected latest model gemini-3-flash, got %q", agySum.Model)
	}
	if agySum.LinesAdded != 2 {
		t.Fatalf("expected 2 lines added, got %d", agySum.LinesAdded)
	}

	// Claude Code transcript
	ccPath := filepath.Join(tmp, "cc-session.jsonl")
	ccJSONL := `{"type":"user","cwd":"/tmp/ccproj","timestamp":"2026-05-01T11:00:00Z","message":{"role":"user","content":"Add tests"}}
{"type":"assistant","cwd":"/tmp/ccproj","timestamp":"2026-05-01T11:01:00Z","message":{"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":2000,"output_tokens":400,"cache_read_input_tokens":1000},"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/tmp/ccproj/app.go","old_string":"a\n","new_string":"a\nb\nc\n"}}]}}
`
	if err := os.WriteFile(ccPath, []byte(ccJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	ccSum, err := ParseTranscript(ccPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ccSum.Source != "claude" || ccSum.UserMessages != 1 || ccSum.ToolCalls != 1 || ccSum.LinesAdded != 3 || ccSum.LinesRemoved != 1 {
		t.Fatalf("unexpected claude summary: %+v", ccSum)
	}

	agg := AggregateFrom(&ScanCache{Version: CacheVersion, Files: map[string]*TranscriptSummary{
		agyPath: agySum,
		ccPath:  ccSum,
	}})
	if agg.Sessions != 2 {
		t.Fatalf("expected 2 combined sessions, got %d", agg.Sessions)
	}
	if agg.BySource["antigravity"].Sessions != 1 || agg.BySource["claude"].Sessions != 1 {
		t.Fatalf("expected 1 agy + 1 claude session, got %+v", agg.BySource)
	}
	if agg.EstimatedCost <= 0 {
		t.Fatalf("expected positive estimated cost, got %f", agg.EstimatedCost)
	}
}
