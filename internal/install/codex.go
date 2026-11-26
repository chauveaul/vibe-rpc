package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chauveaul/vibe-rpc/internal/detect"
	"github.com/chauveaul/vibe-rpc/internal/paths"
)

// Codex CLI runs hooks from $CODEX_HOME/hooks.json (default ~/.codex). The
// format matches Claude Code's: {"hooks": {"<Event>": [{"matcher"?, "hooks":
// [{"type":"command","command":…,"timeout":…}]}]}}. vibe-rpc adds one group
// per event that runs `vibe-rpc hook <Event> --source codex`; the hook prints
// {} so it never approves or blocks anything.

// codexEvents are the Codex hook events vibe-rpc listens to, with timeouts
// (seconds). SessionEnd hooks are capped at 3s by Codex.
var codexEvents = []struct {
	Name    string
	Timeout int
}{
	{"SessionStart", 5},
	{"UserPromptSubmit", 5},
	{"PreToolUse", 5},
	{"PostToolUse", 5},
	{"PermissionRequest", 5},
	{"PreCompact", 5},
	{"PostCompact", 5},
	{"Stop", 5},
	{"SessionEnd", 3},
}

// CodexHooksPath is Codex's user-level hooks file.
func CodexHooksPath() string { return filepath.Join(detect.CodexHome(), "hooks.json") }

func isVibeCodexGroup(g any) bool {
	m, ok := g.(map[string]any)
	if !ok {
		return false
	}
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		hm, _ := h.(map[string]any)
		cmd, _ := hm["command"].(string)
		if strings.Contains(cmd, "vibe-rpc") && strings.Contains(cmd, "--source codex") {
			return true
		}
	}
	return false
}

func readCodexHooks(p string) (map[string]any, []byte, error) {
	root := map[string]any{}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return root, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return root, b, nil
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return root, b, nil
}

// InstallCodexHooks adds (or refreshes) vibe-rpc's groups, keeping any other
// hooks in the file.
func InstallCodexHooks(binPath string) (bool, error) {
	if binPath == "" {
		binPath = ResolveBinary()
	}
	p := CodexHooksPath()
	root, orig, err := readCodexHooks(p)
	if err != nil {
		return false, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, ev := range codexEvents {
		var kept []any
		if existing, ok := hooks[ev.Name].([]any); ok {
			for _, g := range existing {
				if !isVibeCodexGroup(g) {
					kept = append(kept, g)
				}
			}
		}
		kept = append(kept, map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": fmt.Sprintf("%s hook %s --source codex", shellQuote(binPath), ev.Name),
				"timeout": ev.Timeout,
			}},
		})
		hooks[ev.Name] = kept
	}
	root["hooks"] = hooks
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	if bytes.Equal(orig, out) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(p, out, 0o644)
}

// UninstallCodexHooks removes vibe-rpc's groups; the file is deleted only if
// nothing else is left in it.
func UninstallCodexHooks() (bool, error) {
	p := CodexHooksPath()
	root, orig, err := readCodexHooks(p)
	if err != nil || orig == nil {
		return false, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	changed := false
	for name, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, g := range groups {
			if isVibeCodexGroup(g) {
				changed = true
				continue
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			delete(hooks, name)
		} else {
			hooks[name] = kept
		}
	}
	if !changed {
		return false, nil
	}
	if len(hooks) == 0 && len(root) == 1 {
		return true, os.Remove(p)
	}
	root["hooks"] = hooks
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(p, append(out, '\n'), 0o644)
}

// CodexHooksInstalled reports whether vibe-rpc's Codex hooks are present.
func CodexHooksInstalled() bool {
	root, _, err := readCodexHooks(CodexHooksPath())
	if err != nil {
		return false
	}
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks["PreToolUse"].([]any)
	for _, g := range groups {
		if isVibeCodexGroup(g) {
			return true
		}
	}
	return false
}
