package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallAndUninstallIdempotency(t *testing.T) {
	tmp := t.TempDir()
	agyHooks := filepath.Join(tmp, "hooks.json")
	ccSettings := filepath.Join(tmp, "settings.json")

	// Seed pre-existing user hook to make sure we preserve it
	_ = os.WriteFile(ccSettings, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo custom"}]}]}}`), 0o644)

	changed, err := InstallAntigravityHooks(agyHooks, "/usr/local/bin/agy-rpc")
	if err != nil || !changed {
		t.Fatalf("first Antigravity install failed: changed=%v err=%v", changed, err)
	}
	changed, err = InstallAntigravityHooks(agyHooks, "/usr/local/bin/agy-rpc")
	if err != nil || changed {
		t.Fatalf("second Antigravity install must be idempotent: changed=%v err=%v", changed, err)
	}

	changed, err = InstallClaudeHooks(ccSettings, "/usr/local/bin/agy-rpc")
	if err != nil || !changed {
		t.Fatalf("first Claude install failed: changed=%v err=%v", changed, err)
	}
	changed, err = InstallClaudeHooks(ccSettings, "/usr/local/bin/agy-rpc")
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
