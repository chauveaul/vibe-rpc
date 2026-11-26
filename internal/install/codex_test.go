package install

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCodexHooksInstallRules(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	p := CodexHooksPath()
	mine := `{"description":"my hooks","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"~/bin/guard.sh"}]}]}}`
	_ = os.WriteFile(p, []byte(mine), 0o644)

	if changed, err := InstallCodexHooks("/opt/homebrew/bin/vibe-rpc"); !changed || err != nil {
		t.Fatalf("install = %v, %v", changed, err)
	}
	var root map[string]any
	b, _ := os.ReadFile(p)
	_ = json.Unmarshal(b, &root)
	hooks := root["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 || !strings.Contains(string(b), "~/bin/guard.sh") || root["description"] != "my hooks" {
		t.Fatalf("user hooks not preserved: %s", b)
	}
	if !strings.Contains(string(b), `/opt/homebrew/bin/vibe-rpc hook PermissionRequest --source codex`) || len(hooks) != len(codexEvents) {
		t.Fatalf("missing vibe-rpc events: %s", b)
	}
	if !CodexHooksInstalled() {
		t.Fatal("should report installed")
	}
	if changed, _ := InstallCodexHooks("/opt/homebrew/bin/vibe-rpc"); changed {
		t.Fatal("reinstall should be a no-op")
	}

	if ok, err := UninstallCodexHooks(); !ok || err != nil {
		t.Fatalf("uninstall = %v, %v", ok, err)
	}
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "vibe-rpc") || !strings.Contains(string(b), "~/bin/guard.sh") {
		t.Fatalf("uninstall should keep only the user's hooks: %s", b)
	}

	// With nothing else in it, the file goes away entirely.
	_ = os.Remove(p)
	_, _ = InstallCodexHooks("/x/vibe-rpc")
	if ok, _ := UninstallCodexHooks(); !ok {
		t.Fatal("uninstall should succeed")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("empty hooks.json should be removed")
	}
}
