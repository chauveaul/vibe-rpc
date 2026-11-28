package install

import (
	"os"
	"strings"
	"testing"
)

func pinOpenCodeMajor(t *testing.T, major int) {
	t.Helper()
	prev := openCodeMajor
	openCodeMajor = func() int { return major }
	t.Cleanup(func() { openCodeMajor = prev })
}

func TestOpenCodePluginInstallRules(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	pinOpenCodeMajor(t, 2)
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

func TestParseOpenCodeMajor(t *testing.T) {
	for in, want := range map[string]int{
		"1.18.20\n":         1,
		"opencode v2.4.1\n": 2,
		"3.0.0-beta.1":      3,
		"":                  0,
		"dev":               0,
	} {
		if got := parseOpenCodeMajor(in); got != want {
			t.Errorf("parseOpenCodeMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

// OpenCode 2.x rejects a plugin without a default { id, setup } export
// ("Plugin must export a default definition with an id and an effect or setup
// function"); 1.x calls every exported function, so it must get the
// named-factory template with nothing else exported.
func TestOpenCodePluginMatchesInstalledAPI(t *testing.T) {
	bin := "/opt/homebrew/bin/vibe-rpc"
	for _, major := range []int{0, 2, 3} {
		src := renderOpenCodePlugin(bin, major)
		if !strings.Contains(src, `export default { id: "vibe-rpc", setup }`) {
			t.Fatalf("major %d: missing 2.x default export", major)
		}
		if strings.Contains(src, "export const") {
			t.Fatalf("major %d: 2.x plugin must have no named exports", major)
		}
	}
	v1 := renderOpenCodePlugin(bin, 1)
	if !strings.Contains(v1, "export const VibeRpc = async") || strings.Contains(v1, "export default") {
		t.Fatal("1.x plugin must export only the VibeRpc factory")
	}
	for _, major := range []int{1, 2} {
		src := renderOpenCodePlugin(bin, major)
		if !strings.HasPrefix(src, "// "+openCodePluginMarker+"\n") || !strings.Contains(src, `const BIN = "/opt/homebrew/bin/vibe-rpc"`) {
			t.Fatalf("major %d: marker or binary path missing", major)
		}
		if strings.Contains(src, "__VIBE_RPC_BIN__") {
			t.Fatalf("major %d: placeholder left in output", major)
		}
	}
}

// A major OpenCode upgrade changes the rendered plugin, so re-running install
// rewrites a plugin written for the old API.
func TestOpenCodePluginRefreshesAcrossMajorVersions(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	pinOpenCodeMajor(t, 1)
	if changed, err := InstallOpenCodePlugin("/x/vibe-rpc"); !changed || err != nil {
		t.Fatalf("install 1.x = %v, %v", changed, err)
	}
	pinOpenCodeMajor(t, 2)
	if changed, err := InstallOpenCodePlugin("/x/vibe-rpc"); !changed || err != nil {
		t.Fatalf("install 2.x over 1.x = %v, %v", changed, err)
	}
	b, _ := os.ReadFile(OpenCodePluginPath())
	if !strings.Contains(string(b), "export default { id:") {
		t.Fatalf("plugin not upgraded: %s", b)
	}
}
