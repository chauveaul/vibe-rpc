package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/chauveaul/vibe-rpc/internal/paths"
)

// ErrOtherStatusLine means Claude Code already has a status line that isn't
// vibe-rpc's; we never overwrite it.
var ErrOtherStatusLine = errors.New("Claude Code already has a custom status line; vibe-rpc won't replace it")

// ErrOtherAgyStatusLine means Antigravity CLI already has a custom status line
// that isn't vibe-rpc's; we never overwrite it.
var ErrOtherAgyStatusLine = errors.New("Antigravity CLI already has a custom status line; vibe-rpc won't replace it")

// Both Claude Code and Antigravity CLI pass their live subscription quotas
// (5-hour session and 7-day weekly windows) to their status line command on
// stdin, so `vibe-rpc statusline` records them and prints a compact status bar.

func isVibeStatusLine(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	return strings.Contains(cmd, "vibe-rpc") && strings.Contains(cmd, "statusline")
}

func statusLineCommand(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	cmd, _ := m["command"].(string)
	return strings.TrimSpace(cmd)
}

func readSettingsJSON(path string) (map[string]any, []byte, error) {
	root := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return root, nil, nil
		}
		return nil, nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return root, b, nil
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return root, b, nil
}

func writeSettingsJSON(path string, root map[string]any, orig []byte) (bool, error) {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	if bytes.Equal(orig, out) {
		return false, nil
	}
	return true, paths.WriteFileAtomic(path, out, 0o644)
}

// ClaudeStatusLineState reports "vibe-rpc", "other" or "none".
func ClaudeStatusLineState(settingsPath string) string {
	if settingsPath == "" {
		settingsPath = paths.ClaudeSettingsPath()
	}
	root, _, err := readSettingsJSON(settingsPath)
	if err != nil {
		return "none"
	}
	sl, ok := root["statusLine"]
	switch {
	case !ok || sl == nil:
		return "none"
	case isVibeStatusLine(sl):
		return "vibe-rpc"
	default:
		return "other"
	}
}

// defaultClaudeRefreshInterval asks Claude Code to re-run `vibe-rpc statusline`
// periodically (in seconds) even while the main thread is waiting on background
// subagents or sitting idle at the prompt. Without it, statusLine only fires on
// main-thread assistant messages and usage bars freeze during long subagents.
const defaultClaudeRefreshInterval = 30

func existingRefreshInterval(v any) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	switch n := m["refreshInterval"].(type) {
	case float64:
		if n >= 1 {
			return int(n)
		}
	case int:
		if n >= 1 {
			return n
		}
	}
	return 0
}

// InstallClaudeStatusLine makes `vibe-rpc statusline` Claude Code's status
// line. It refreshes an existing vibe-rpc entry (e.g. a new binary path or
// missing refreshInterval) but returns ErrOtherStatusLine rather than
// replacing someone else's.
func InstallClaudeStatusLine(settingsPath, binPath string) (bool, error) {
	if settingsPath == "" {
		settingsPath = paths.ClaudeSettingsPath()
	}
	if binPath == "" {
		binPath = ResolveBinary()
	}
	root, orig, err := readSettingsJSON(settingsPath)
	if err != nil {
		return false, err
	}
	if sl, ok := root["statusLine"]; ok && sl != nil && !isVibeStatusLine(sl) {
		return false, ErrOtherStatusLine
	}
	refresh := existingRefreshInterval(root["statusLine"])
	if refresh <= 0 {
		refresh = defaultClaudeRefreshInterval
	}
	root["statusLine"] = map[string]any{
		"type":            "command",
		"command":         shellQuote(binPath) + " statusline",
		"padding":         0,
		"refreshInterval": refresh,
	}
	return writeSettingsJSON(settingsPath, root, orig)
}

// UninstallClaudeStatusLine removes the status line only if it's vibe-rpc's.
func UninstallClaudeStatusLine(settingsPath string) (bool, error) {
	if settingsPath == "" {
		settingsPath = paths.ClaudeSettingsPath()
	}
	root, orig, err := readSettingsJSON(settingsPath)
	if err != nil {
		return false, err
	}
	if sl, ok := root["statusLine"]; !ok || !isVibeStatusLine(sl) {
		return false, nil
	}
	delete(root, "statusLine")
	return writeSettingsJSON(settingsPath, root, orig)
}

// AgyStatusLineState reports "vibe-rpc", "other" or "none" for Antigravity CLI's
// ~/.gemini/antigravity-cli/settings.json. An entry with no custom command
// (just toggling the built-in bar) is treated as "none".
func AgyStatusLineState(settingsPath string) string {
	if settingsPath == "" {
		settingsPath = paths.AgyCLISettingsPath()
	}
	root, _, err := readSettingsJSON(settingsPath)
	if err != nil {
		return "none"
	}
	sl, ok := root["statusLine"]
	if !ok || sl == nil {
		return "none"
	}
	if isVibeStatusLine(sl) {
		if m, ok := sl.(map[string]any); ok {
			if en, has := m["enabled"].(bool); has && !en {
				return "none"
			}
		}
		return "vibe-rpc"
	}
	if statusLineCommand(sl) == "" {
		return "none"
	}
	return "other"
}

// InstallAgyStatusLine wires `vibe-rpc statusline` into Antigravity CLI's
// settings.json with stack_with_default: true so the built-in status line stays
// visible alongside vibe-rpc. Never replaces another custom status line.
func InstallAgyStatusLine(settingsPath, binPath string) (bool, error) {
	if settingsPath == "" {
		settingsPath = paths.AgyCLISettingsPath()
	}
	if binPath == "" {
		binPath = ResolveBinary()
	}
	root, orig, err := readSettingsJSON(settingsPath)
	if err != nil {
		return false, err
	}
	if sl, ok := root["statusLine"]; ok && sl != nil && !isVibeStatusLine(sl) && statusLineCommand(sl) != "" {
		return false, ErrOtherAgyStatusLine
	}
	root["statusLine"] = map[string]any{
		"type":               "command",
		"command":            shellQuote(binPath) + " statusline",
		"enabled":            true,
		"padding":            0,
		"stack_with_default": true,
	}
	return writeSettingsJSON(settingsPath, root, orig)
}

// UninstallAgyStatusLine removes Antigravity CLI's status line only if it's vibe-rpc's.
func UninstallAgyStatusLine(settingsPath string) (bool, error) {
	if settingsPath == "" {
		settingsPath = paths.AgyCLISettingsPath()
	}
	root, orig, err := readSettingsJSON(settingsPath)
	if err != nil {
		return false, err
	}
	if sl, ok := root["statusLine"]; !ok || !isVibeStatusLine(sl) {
		return false, nil
	}
	delete(root, "statusLine")
	return writeSettingsJSON(settingsPath, root, orig)
}

// statusLineBinary returns the executable a status line command runs: the
// first word, or the quoted path shellQuote produced.
func statusLineBinary(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : end+1]
		}
		return ""
	}
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

// staleStatusLine reports whether a vibe-rpc status line should be pointed at
// binPath: always when force is set and the path differs, otherwise only when
// the binary it runs no longer exists (e.g. a deleted dev build), so two
// working installs don't keep overwriting each other.
func staleStatusLine(sl any, binPath string, force bool) bool {
	cur := statusLineBinary(statusLineCommand(sl))
	if cur == "" || cur == binPath {
		return false
	}
	if force {
		return true
	}
	if !strings.ContainsRune(cur, os.PathSeparator) {
		return false // bare "vibe-rpc", resolved through PATH
	}
	_, err := os.Stat(cur)
	return errors.Is(err, os.ErrNotExist)
}

// RepairStatusLines points vibe-rpc's Claude Code and Antigravity CLI status
// lines at binPath (ResolveBinary() when empty), and adds refreshInterval to
// existing Claude Code vibe-rpc status lines that predate it so usage keeps
// updating while waiting on background subagents. Without force it leaves a
// working binary path alone; status lines that belong to other tools, or that
// vibe-rpc doesn't manage, are never changed.
func RepairStatusLines(binPath string, force bool) (claude, agy bool) {
	if binPath == "" {
		binPath = ResolveBinary()
	}
	if ClaudeStatusLineState("") == "vibe-rpc" {
		if root, orig, err := readSettingsJSON(paths.ClaudeSettingsPath()); err == nil {
			sl := root["statusLine"]
			if staleStatusLine(sl, binPath, force) {
				claude, _ = InstallClaudeStatusLine("", binPath)
			} else if existingRefreshInterval(sl) <= 0 {
				if m, ok := sl.(map[string]any); ok {
					m["refreshInterval"] = defaultClaudeRefreshInterval
					claude, _ = writeSettingsJSON(paths.ClaudeSettingsPath(), root, orig)
				}
			}
		}
	}
	if AgyStatusLineState("") == "vibe-rpc" {
		if root, _, err := readSettingsJSON(paths.AgyCLISettingsPath()); err == nil && staleStatusLine(root["statusLine"], binPath, force) {
			agy, _ = InstallAgyStatusLine("", binPath)
		}
	}
	return claude, agy
}
