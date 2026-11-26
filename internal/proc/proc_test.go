package proc

import "testing"

func TestProcessMatchers(t *testing.T) {
	if !LooksLikeAntigravity("agy", "/usr/local/bin/agy") {
		t.Fatal("should match agy binary")
	}
	if !LooksLikeAntigravity("Antigravity", "/Applications/Antigravity.app/Contents/MacOS/Antigravity") {
		t.Fatal("should match Antigravity.app")
	}
	if LooksLikeAntigravity("agy-rpc", "/Users/meze/agy-rpc daemon") {
		t.Fatal("must not match agy-rpc itself")
	}
	if !LooksLikeClaudeCode("claude", "/opt/homebrew/bin/claude --resume") {
		t.Fatal("should match claude CLI")
	}
	if LooksLikeClaudeCode("Claude", "/Applications/Claude.app/Contents/MacOS/Claude") {
		t.Fatal("must not match Claude desktop app")
	}
	if LooksLikeClaudeCode("node", "node /usr/local/bin/claude-rpc daemon") {
		t.Fatal("must not match claude-rpc daemon")
	}
}
