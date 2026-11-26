package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAndUninstallIdempotency(t *testing.T) {
	tmp := t.TempDir()
	agyHooks := filepath.Join(tmp, "hooks.json")
	ccSettings := filepath.Join(tmp, "settings.json")

	// Seed pre-existing user hook to make sure we preserve it
	_ = os.WriteFile(ccSettings, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo custom"}]}]}}`), 0o644)

	changed, err := InstallAntigravityHooks(agyHooks, "/usr/local/bin/vibe-rpc")
	if err != nil || !changed {
		t.Fatalf("first Antigravity install failed: changed=%v err=%v", changed, err)
	}
	changed, err = InstallAntigravityHooks(agyHooks, "/usr/local/bin/vibe-rpc")
	if err != nil || changed {
		t.Fatalf("second Antigravity install must be idempotent: changed=%v err=%v", changed, err)
	}

	changed, err = InstallClaudeHooks(ccSettings, "/usr/local/bin/vibe-rpc")
	if err != nil || !changed {
		t.Fatalf("first Claude install failed: changed=%v err=%v", changed, err)
	}
	changed, err = InstallClaudeHooks(ccSettings, "/usr/local/bin/vibe-rpc")
	if err != nil || changed {
		t.Fatalf("second Claude install must be idempotent: changed=%v err=%v", changed, err)
	}

	if err := SelfTestHooks(); err != nil {
		t.Fatalf("SelfTestHooks failed: %v", err)
	}

	if removed, err := UninstallAntigravityHooks(agyHooks); err != nil || !removed {
		t.Fatalf("UninstallAntigravityHooks failed: removed=%v err=%v", removed, err)
	}
	if removed, err := UninstallClaudeHooks(ccSettings); err != nil || !removed {
		t.Fatalf("UninstallClaudeHooks failed: removed=%v err=%v", removed, err)
	}
	after, _ := os.ReadFile(ccSettings)
	if !contains(string(after), "echo custom") {
		t.Fatalf("UninstallClaudeHooks clobbered user hook: %s", string(after))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexStr(s, sub) >= 0)
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestInstallMigratesLegacyAgyRpcHooks(t *testing.T) {
	tmp := t.TempDir()
	agyHooks := filepath.Join(tmp, "hooks.json")
	ccSettings := filepath.Join(tmp, "settings.json")
	_ = os.WriteFile(agyHooks, []byte(`{"agy-rpc":{"enabled":true},"other":{"enabled":true}}`), 0o644)
	_ = os.WriteFile(ccSettings, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/agy-rpc hook Stop --source claude","_agyRpc":true}]}]}}`), 0o644)

	if _, err := InstallAntigravityHooks(agyHooks, "/usr/local/bin/vibe-rpc"); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallClaudeHooks(ccSettings, "/usr/local/bin/vibe-rpc"); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(agyHooks)
	c, _ := os.ReadFile(ccSettings)
	if strings.Contains(string(a), `"agy-rpc"`) || !strings.Contains(string(a), `"vibe-rpc"`) || !strings.Contains(string(a), `"other"`) {
		t.Fatalf("legacy Antigravity hook not migrated: %s", a)
	}
	if strings.Contains(string(c), "agy-rpc") || strings.Count(string(c), "vibe-rpc hook Stop --source") != 1 {
		t.Fatalf("legacy Claude hook not migrated: %s", c)
	}
}

func TestStableBinaryPathPrefersHomebrewLink(t *testing.T) {
	root := t.TempDir()
	keg := filepath.Join(root, "Cellar", "vibe-rpc", "2.1.0", "bin", "vibe-rpc")
	if err := os.MkdirAll(filepath.Dir(keg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keg, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No prefix link yet: keep the keg path.
	if got := StableBinaryPath(keg); got != keg {
		t.Fatalf("without link got %q", got)
	}
	link := filepath.Join(root, "bin", "vibe-rpc")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(keg, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if got := StableBinaryPath(keg); got != link {
		t.Fatalf("expected prefix link %q, got %q", link, got)
	}
	if got := StableBinaryPath("/usr/local/bin/vibe-rpc"); got != "/usr/local/bin/vibe-rpc" {
		t.Fatalf("non-Homebrew path changed: %q", got)
	}
}
