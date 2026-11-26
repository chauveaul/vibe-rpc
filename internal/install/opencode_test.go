package install

import (
	"os"
	"strings"
	"testing"
)

func TestOpenCodePluginInstallRules(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	p := OpenCodePluginPath()

	if changed, err := InstallOpenCodePlugin("/opt/homebrew/bin/vibe-rpc"); !changed || err != nil {
		t.Fatalf("install = %v, %v", changed, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `const BIN = "/opt/homebrew/bin/vibe-rpc"`) || !OpenCodePluginInstalled() {
		t.Fatalf("plugin content: %s", b)
	}
	if changed, _ := InstallOpenCodePlugin("/opt/homebrew/bin/vibe-rpc"); changed {
		t.Fatal("reinstall should be a no-op")
	}
	if changed, _ := InstallOpenCodePlugin("/new/path/vibe-rpc"); !changed {
		t.Fatal("a new binary path should refresh the plugin")
	}
	if ok, err := UninstallOpenCodePlugin(); !ok || err != nil {
		t.Fatalf("uninstall = %v, %v", ok, err)
	}

	// A user's own file at that path is never touched.
	_ = os.WriteFile(p, []byte("export const Mine = async () => ({})\n"), 0o644)
	if _, err := InstallOpenCodePlugin("/x/vibe-rpc"); err == nil {
		t.Fatal("must refuse to overwrite a foreign plugin")
	}
	if ok, _ := UninstallOpenCodePlugin(); ok {
		t.Fatal("must not remove a foreign plugin")
	}
}
