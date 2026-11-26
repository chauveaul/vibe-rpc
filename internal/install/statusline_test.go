package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeStatusLineInstallRules(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	_ = os.WriteFile(p, []byte(`{"theme":"dark"}`), 0o644)

	if ClaudeStatusLineState(p) != "none" {
		t.Fatal("expected none")
	}
	if changed, err := InstallClaudeStatusLine(p, "/opt/homebrew/bin/vibe-rpc"); !changed || err != nil {
		t.Fatalf("install = %v, %v", changed, err)
	}
	var root map[string]any
	b, _ := os.ReadFile(p)
	_ = json.Unmarshal(b, &root)
	sl := root["statusLine"].(map[string]any)
	if sl["command"] != "/opt/homebrew/bin/vibe-rpc statusline" || root["theme"] != "dark" {
		t.Fatalf("settings = %s", b)
	}
	if ClaudeStatusLineState(p) != "vibe-rpc" {
		t.Fatal("expected vibe-rpc")
	}
	if changed, _ := InstallClaudeStatusLine(p, "/opt/homebrew/bin/vibe-rpc"); changed {
		t.Fatal("reinstall should be a no-op")
	}
	if changed, err := UninstallClaudeStatusLine(p); !changed || err != nil || ClaudeStatusLineState(p) != "none" {
		t.Fatalf("uninstall = %v, %v", changed, err)
	}

	// Someone else's status line is never replaced or removed.
	other := `{"statusLine":{"type":"command","command":"~/bin/my-status.sh"}}`
	_ = os.WriteFile(p, []byte(other), 0o644)
	if _, err := InstallClaudeStatusLine(p, "/x/vibe-rpc"); err != ErrOtherStatusLine {
		t.Fatalf("expected ErrOtherStatusLine, got %v", err)
	}
	if changed, _ := UninstallClaudeStatusLine(p); changed {
		t.Fatal("must not remove another tool's status line")
	}
	if b, _ := os.ReadFile(p); string(b) != other || ClaudeStatusLineState(p) != "other" {
		t.Fatalf("other status line was modified: %s", b)
	}
}

func TestAgyStatusLineInstallRules(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	// Built-in statusLine toggle without a custom command counts as "none".
	_ = os.WriteFile(p, []byte(`{"statusLine":{"enabled":true},"trustedWorkspaces":["/w"]}`), 0o644)

	if AgyStatusLineState(p) != "none" {
		t.Fatalf("expected none for default statusLine, got %s", AgyStatusLineState(p))
	}
	if changed, err := InstallAgyStatusLine(p, "/opt/homebrew/bin/vibe-rpc"); !changed || err != nil {
		t.Fatalf("install = %v, %v", changed, err)
	}
	var root map[string]any
	b, _ := os.ReadFile(p)
	_ = json.Unmarshal(b, &root)
	sl := root["statusLine"].(map[string]any)
	if sl["command"] != "/opt/homebrew/bin/vibe-rpc statusline" || sl["stack_with_default"] != true || sl["enabled"] != true {
		t.Fatalf("settings = %s", b)
	}
	if AgyStatusLineState(p) != "vibe-rpc" {
		t.Fatal("expected vibe-rpc")
	}
	if changed, _ := InstallAgyStatusLine(p, "/opt/homebrew/bin/vibe-rpc"); changed {
		t.Fatal("reinstall should be a no-op")
	}
	if changed, err := UninstallAgyStatusLine(p); !changed || err != nil || AgyStatusLineState(p) != "none" {
		t.Fatalf("uninstall = %v, %v", changed, err)
	}

	other := `{"statusLine":{"type":"command","command":"~/bin/agy-powerline"}}`
	_ = os.WriteFile(p, []byte(other), 0o644)
	if _, err := InstallAgyStatusLine(p, "/x/vibe-rpc"); err != ErrOtherAgyStatusLine {
		t.Fatalf("expected ErrOtherAgyStatusLine, got %v", err)
	}
	if changed, _ := UninstallAgyStatusLine(p); changed {
		t.Fatal("must not remove another tool's status line")
	}
	if b, _ := os.ReadFile(p); string(b) != other || AgyStatusLineState(p) != "other" {
		t.Fatalf("other status line was modified: %s", b)
	}
}
