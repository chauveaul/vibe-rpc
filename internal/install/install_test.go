package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chauveaul/vibe-rpc/internal/paths"
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

func TestPathLinkTo(t *testing.T) {
	store := filepath.Join(t.TempDir(), "store", "abc-vibe-rpc-2.4.1", "bin")
	profile := filepath.Join(t.TempDir(), "profile", "bin")
	for _, d := range []string{store, profile} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	real := filepath.Join(store, "vibe-rpc")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", profile)
	// Nothing on PATH yet: keep the real path.
	if got := pathLinkTo(real); got != real {
		t.Fatalf("without link got %q", got)
	}
	link := filepath.Join(profile, "vibe-rpc")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if got := pathLinkTo(real); got != link {
		t.Fatalf("expected profile link %q, got %q", link, got)
	}
	// A PATH entry for a different build is not a match.
	other := filepath.Join(store, "other")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if got := pathLinkTo(real); got != real {
		t.Fatalf("mismatched link should keep real path, got %q", got)
	}
}

func TestStatusLineBinary(t *testing.T) {
	for cmd, want := range map[string]string{
		"/opt/homebrew/bin/vibe-rpc statusline":        "/opt/homebrew/bin/vibe-rpc",
		`"/Users/a b/vibe-rpc" statusline`:             "/Users/a b/vibe-rpc",
		"vibe-rpc statusline":                          "vibe-rpc",
		"  /usr/bin/vibe-rpc   statusline --compact  ": "/usr/bin/vibe-rpc",
	} {
		if got := statusLineBinary(cmd); got != want {
			t.Errorf("statusLineBinary(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestStaleStatusLine(t *testing.T) {
	dir := t.TempDir()
	alive := filepath.Join(dir, "vibe-rpc")
	if err := os.WriteFile(alive, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "deleted", "vibe-rpc")
	sl := func(bin string) any { return map[string]any{"command": shellQuote(bin) + " statusline"} }
	const cur = "/opt/homebrew/bin/vibe-rpc"

	if !staleStatusLine(sl(gone), cur, false) {
		t.Error("missing binary should be repaired")
	}
	if staleStatusLine(sl(alive), cur, false) {
		t.Error("working binary should be left alone without force")
	}
	if !staleStatusLine(sl(alive), cur, true) {
		t.Error("force should repoint a different binary")
	}
	if staleStatusLine(sl(cur), cur, true) {
		t.Error("same binary is never stale")
	}
	if staleStatusLine(sl("vibe-rpc"), cur, false) {
		t.Error("bare command resolved via PATH should be left alone")
	}
}

func TestRepairStatusLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudePath := paths.ClaudeSettingsPath()
	if !strings.HasPrefix(claudePath, home) {
		t.Skip("settings path does not follow HOME on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(home, "old-build", "vibe-rpc")
	if err := os.WriteFile(claudePath, []byte(`{"statusLine":{"type":"command","command":"`+gone+` statusline","padding":0},"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(home, "bin", "vibe-rpc")
	claude, agy := RepairStatusLines(newBin, false)
	if !claude || agy {
		t.Fatalf("RepairStatusLines = %v, %v; want true, false", claude, agy)
	}
	b, _ := os.ReadFile(claudePath)
	if !strings.Contains(string(b), newBin+" statusline") || !strings.Contains(string(b), `"theme": "dark"`) || !strings.Contains(string(b), `"refreshInterval": 30`) {
		t.Fatalf("settings not repaired as expected:\n%s", b)
	}
	// Running again changes nothing.
	if claude, _ := RepairStatusLines(newBin, false); claude {
		t.Error("second repair should be a no-op")
	}

	// A working binary missing refreshInterval gets refreshInterval added without changing its binary path.
	alive := filepath.Join(home, "custom-bin", "vibe-rpc")
	if err := os.MkdirAll(filepath.Dir(alive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alive, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte(`{"statusLine":{"type":"command","command":"`+alive+` statusline","padding":0}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if claude, _ := RepairStatusLines(newBin, false); !claude {
		t.Fatal("expected RepairStatusLines to add missing refreshInterval")
	}
	b, _ = os.ReadFile(claudePath)
	if !strings.Contains(string(b), alive+" statusline") || !strings.Contains(string(b), `"refreshInterval": 30`) {
		t.Fatalf("repair should preserve working binary while adding refreshInterval:\n%s", b)
	}
}
