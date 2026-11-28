package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopilotHooksInstallRules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	mine := filepath.Join(home, "hooks", "mine.json")
	_ = os.MkdirAll(filepath.Dir(mine), 0o755)
	_ = os.WriteFile(mine, []byte(`{"version":1,"hooks":{"preToolUse":[{"bash":"~/bin/guard.sh"}]}}`), 0o644)

	if CopilotHooksInstalled() {
		t.Fatal("nothing installed yet")
	}
	if changed, err := InstallCopilotHooks("/opt/homebrew/bin/vibe-rpc"); !changed || err != nil {
		t.Fatalf("install = %v, %v", changed, err)
	}
	b, err := os.ReadFile(CopilotHooksPath())
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Version int                         `json:"version"`
		Hooks   map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &root); err != nil || root.Version != 1 || len(root.Hooks) != len(copilotEvents) {
		t.Fatalf("bad hooks file (%v): %s", err, b)
	}
	pre := root.Hooks["PreToolUse"][0]
	if pre["bash"] != "/opt/homebrew/bin/vibe-rpc hook PreToolUse --source copilot || true" {
		t.Fatalf("PreToolUse must never fail closed: %v", pre["bash"])
	}
	if ps, _ := pre["powershell"].(string); !strings.HasSuffix(ps, "; exit 0") {
		t.Fatalf("powershell must exit 0: %q", ps)
	}
	if n := root.Hooks["notification"][0]["bash"]; !strings.Contains(n.(string), "hook Notification --source copilot") {
		t.Fatalf("notification maps to vibe-rpc's Notification: %v", n)
	}
	if !CopilotHooksInstalled() {
		t.Fatal("should report installed")
	}
	if changed, _ := InstallCopilotHooks("/opt/homebrew/bin/vibe-rpc"); changed {
		t.Fatal("reinstall should be a no-op")
	}
	if changed, _ := InstallCopilotHooks("/usr/local/bin/vibe-rpc"); !changed {
		t.Fatal("a moved binary should refresh the file")
	}

	if ok, err := UninstallCopilotHooks(); !ok || err != nil {
		t.Fatalf("uninstall = %v, %v", ok, err)
	}
	if _, err := os.Stat(CopilotHooksPath()); !os.IsNotExist(err) {
		t.Fatal("vibe-rpc's hooks file should be gone")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatal("the user's own hooks file must survive")
	}
	if ok, _ := UninstallCopilotHooks(); ok {
		t.Fatal("second uninstall has nothing to remove")
	}
}
