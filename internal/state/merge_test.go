package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestListSessionsPrefersOwnStateOverClaudeRPC(t *testing.T) {
	own, rpc := t.TempDir(), t.TempDir()
	write := func(dir, id string, st SessionState) {
		b, _ := json.Marshal(st)
		_ = os.WriteFile(filepath.Join(dir, "state-"+id+".json"), b, 0o644)
	}
	write(own, "s1", SessionState{Source: "claude", Status: "notification", AttentionKind: "question", LastActivity: 100})
	write(rpc, "s1", SessionState{Source: "claude", Status: "working", LastActivity: 200}) // fresher, but claude-rpc's
	write(rpc, "s2", SessionState{Source: "claude", Status: "working", LastActivity: 300}) // only claude-rpc knows it

	got := map[string]SessionState{}
	for _, s := range ListSessions(ListOptions{IncludeClaude: true, IncludeAntigravity: true, IncludeClaudeRPC: true, StateDir: own, ClaudeStateDir: rpc}) {
		got[s.SessionID] = s
	}
	if got["s1"].Status != "notification" || got["s1"].AttentionKind != "question" {
		t.Fatalf("vibe-rpc's own state should win: %+v", got["s1"])
	}
	if got["s2"].Status != "working" {
		t.Fatalf("claude-rpc-only sessions should still appear: %+v", got)
	}
}
