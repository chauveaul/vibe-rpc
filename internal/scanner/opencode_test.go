package scanner

import (
	"strings"
	"testing"
)

func TestScanOpenCodeBuildsAndCaches(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no real opencode; we stub the query instead
	calls := 0
	orig := openCodeQuery
	defer func() { openCodeQuery = orig }()
	openCodeQuery = func(bin, sql string) ([]map[string]any, error) {
		calls++
		switch {
		case strings.Contains(sql, "from session"):
			return []map[string]any{
				{"id": "ses_a", "parent_id": nil, "directory": "/w/api", "model": `{"id":"glm-5.3-flash-free","providerID":"opencode"}`,
					"summary_additions": 12.0, "summary_deletions": 3.0, "time_created": 1_000.0, "time_updated": 9_000.0},
				{"id": "ses_sub", "parent_id": "ses_a", "directory": "/w/api", "model": "", "time_created": 2_000.0, "time_updated": 3_000.0},
			}, nil
		case strings.Contains(sql, "from message"):
			return []map[string]any{
				{"session_id": "ses_a", "time_created": 1_000.0, "role": "user"},
				{"session_id": "ses_a", "time_created": 1_500.0, "done": 4_000.0, "role": "assistant", "model": "glm-5.3-flash-free",
					"cost": 0.25, "ti": 100.0, "tout": 20.0, "tr": 5.0, "tcr": 900.0, "tcw": 10.0},
			}, nil
		case strings.Contains(sql, "from part"):
			return []map[string]any{
				{"session_id": "ses_a", "time_created": 2_000.0, "type": "tool", "tool": "bash", "cmd": "git commit -m x"},
				{"session_id": "ses_a", "time_created": 3_000.0, "type": "tool", "tool": "edit", "file": "/w/api/main.go"},
				{"session_id": "ses_a", "time_created": 3_500.0, "type": "text"},
			}, nil
		}
		return nil, nil
	}
	// OpenCodeBinary needs *something*; point the fallback list at nothing and
	// fake LookPath by creating an executable named opencode on PATH.
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	writeExe(t, dir+"/opencode")

	cache := &ScanCache{Files: map[string]*TranscriptSummary{}}
	seen, n, ok := scanOpenCode(cache, false)
	if !ok || n != 2 || len(seen) != 2 {
		t.Fatalf("seen=%d n=%d ok=%v", len(seen), n, ok)
	}
	s := cache.Files["opencode://ses_a"]
	if s.Source != "opencode" || s.Project != "api" || s.IsSubagent {
		t.Fatalf("summary basics: %+v", s)
	}
	if s.InputTokens != 100 || s.OutputTokens != 25 || s.CacheReadTokens != 900 || s.CacheWriteTokens != 10 || s.Cost != 0.25 {
		t.Fatalf("tokens/cost: in=%d out=%d cr=%d cw=%d cost=%v", s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens, s.Cost)
	}
	if s.UserMessages != 1 || s.ToolCalls != 2 || s.ToolBreakdown["bash"] != 1 || s.FileEdits["/w/api/main.go"] != 1 || s.Ships != 1 {
		t.Fatalf("activity: %+v", s)
	}
	if s.ByModel["glm-5.3-flash-free"] == nil || s.LinesAdded != 12 || s.ActiveMs != 3_000 || len(s.Spans) != 1 {
		t.Fatalf("model/lines/active: models=%v lines=%d active=%d spans=%v", s.ByModel, s.LinesAdded, s.ActiveMs, s.Spans)
	}
	if !cache.Files["opencode://ses_sub"].IsSubagent {
		t.Fatal("child session should be a subagent")
	}

	// Unchanged sessions are skipped: only the session list is queried.
	calls = 0
	if _, n, _ := scanOpenCode(cache, false); n != 0 || calls != 1 {
		t.Fatalf("expected cached skip, n=%d calls=%d", n, calls)
	}

	agg := AggregateFrom(cache)
	if agg.BySource["opencode"].Sessions != 1 || agg.BySource["opencode"].Tokens != 1035 {
		t.Fatalf("aggregate opencode = %+v", agg.BySource["opencode"])
	}
}

func TestOpenCodeModelKey(t *testing.T) {
	if k := openCodeModelKey("glm-5.3-flash-free"); k != "glm-5.3-flash-free" {
		t.Fatalf("unknown models keep their id, got %q", k)
	}
	if k := openCodeModelKey("claude-opus-4-6"); k == "claude-opus-4-6" || strings.Contains(k, "gemini") {
		t.Fatalf("Claude ids should map to vibe-rpc's key, got %q", k)
	}
}
