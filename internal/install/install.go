package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/hook"
	"github.com/chauveaul/vibe-rpc/internal/paths"
)

const (
	AgyHookGroupKey   = "vibe-rpc"
	LaunchAgentLabel  = "com.vibe-rpc.daemon"
	SystemdUnitName   = "vibe-rpc.service"
	WindowsRunRegPath = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	WindowsRunValName = "VibeRPC"

	// Legacy (pre-rename agy-rpc) identifiers, cleaned up on install/uninstall
	// so upgrading never leaves duplicate hooks or a second autostarted daemon.
	LegacyHookKey           = "agy-rpc"
	LegacyLaunchAgentLabel  = "com.agy-rpc.daemon"
	LegacySystemdUnitName   = "agy-rpc.service"
	LegacyWindowsRunValName = "AgyRPC"
)

type AgyHookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type AgyMatcherGroup struct {
	Matcher string           `json:"matcher"`
	Hooks   []AgyHookHandler `json:"hooks"`
}

type AgyHookGroup struct {
	Enabled        bool              `json:"enabled"`
	SessionStart   []AgyHookHandler  `json:"SessionStart"`
	PreInvocation  []AgyHookHandler  `json:"PreInvocation"`
	PostInvocation []AgyHookHandler  `json:"PostInvocation"`
	PreToolUse     []AgyMatcherGroup `json:"PreToolUse"`
	PostToolUse    []AgyMatcherGroup `json:"PostToolUse"`
	Stop           []AgyHookHandler  `json:"Stop"`
}

type ClaudeHookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   bool   `json:"async,omitempty"`
	VibeRPC bool   `json:"_vibeRpc"`
}

type ClaudeHookGroup struct {
	Matcher string              `json:"matcher,omitempty"`
	Hooks   []ClaudeHookHandler `json:"hooks"`
}

var claudeEvents = []struct {
	Name    string
	Matcher string
	Async   bool
}{
	{"SessionStart", "", false},
	{"UserPromptSubmit", "", false},
	{"PreToolUse", "*", true},
	{"PostToolUse", "*", true},
	{"PostToolUseFailure", "*", true},
	{"Notification", "", false},
	{"PreCompact", "", false},
	{"PostCompact", "", false},
	{"Stop", "", false},
	{"StopFailure", "", false},
	{"SubagentStop", "", false},
	{"SessionEnd", "", false},
}

func ResolveBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return "vibe-rpc"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return StableBinaryPath(real)
	}
	return exe
}

// StableBinaryPath maps a Homebrew keg path such as
// /opt/homebrew/Cellar/vibe-rpc/2.1.0/bin/vibe-rpc to the prefix symlink
// /opt/homebrew/bin/vibe-rpc, which survives `brew upgrade` (the versioned
// keg is deleted on cleanup, which would break hooks and autostart). Any
// other path is returned unchanged.
func StableBinaryPath(real string) string {
	slash := filepath.ToSlash(real)
	i := strings.Index(slash, "/Cellar/")
	if i < 0 {
		return real
	}
	link := filepath.Join(filepath.FromSlash(slash[:i]), "bin", filepath.Base(real))
	if target, err := filepath.EvalSymlinks(link); err == nil {
		if a, errA := os.Stat(target); errA == nil {
			if b, errB := os.Stat(real); errB == nil && os.SameFile(a, b) {
				return link
			}
		}
	}
	return real
}

func shellQuote(p string) string {
	if strings.ContainsAny(p, " \t\"'") {
		return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
	}
	return p
}

func BuildAgyHookGroup(binPath string) AgyHookGroup {
	cmd := func(ev string) AgyHookHandler {
		return AgyHookHandler{
			Type:    "command",
			Command: fmt.Sprintf("%s hook %s --source antigravity", shellQuote(binPath), ev),
			Timeout: 5,
		}
	}
	return AgyHookGroup{
		Enabled:        true,
		SessionStart:   []AgyHookHandler{cmd("SessionStart")},
		PreInvocation:  []AgyHookHandler{cmd("PreInvocation")},
		PostInvocation: []AgyHookHandler{cmd("PostInvocation")},
		PreToolUse:     []AgyMatcherGroup{{Matcher: "*", Hooks: []AgyHookHandler{cmd("PreToolUse")}}},
		PostToolUse:    []AgyMatcherGroup{{Matcher: "*", Hooks: []AgyHookHandler{cmd("PostToolUse")}}},
		Stop:           []AgyHookHandler{cmd("Stop")},
	}
}

func InstallAntigravityHooks(hooksPath, binPath string) (bool, error) {
	if hooksPath == "" {
		hooksPath = paths.AgyHooksPath()
	}
	if binPath == "" {
		binPath = ResolveBinary()
	}
	root := map[string]any{}
	var origBytes []byte
	if b, err := os.ReadFile(hooksPath); err == nil && len(bytes.TrimSpace(b)) > 0 {
		origBytes = b
		if err := json.Unmarshal(b, &root); err != nil {
			return false, fmt.Errorf("parse %s: %w", hooksPath, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	delete(root, LegacyHookKey)
	root[AgyHookGroupKey] = BuildAgyHookGroup(binPath)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	if bytes.Equal(origBytes, out) {
		return false, nil
	}
	return true, paths.WriteFileAtomic(hooksPath, out, 0o644)
}

func UninstallAntigravityHooks(hooksPath string) (bool, error) {
	if hooksPath == "" {
		hooksPath = paths.AgyHooksPath()
	}
	b, err := os.ReadFile(hooksPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	root := map[string]any{}
	if err := json.Unmarshal(b, &root); err != nil {
		return false, err
	}
	_, hasNew := root[AgyHookGroupKey]
	_, hasLegacy := root[LegacyHookKey]
	if !hasNew && !hasLegacy {
		return false, nil
	}
	delete(root, AgyHookGroupKey)
	delete(root, LegacyHookKey)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(hooksPath, append(out, '\n'), 0o644)
}

func isAgyClaudeGroup(raw any) bool {
	m, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	hooks, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hooks {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"_vibeRpc", "_agyRpc"} {
			if flag, _ := hm[key].(bool); flag {
				return true
			}
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, "vibe-rpc") || strings.Contains(cmd, "agy-rpc") {
			return true
		}
	}
	return false
}

func InstallClaudeHooks(settingsPath, binPath string) (bool, error) {
	if settingsPath == "" {
		settingsPath = paths.ClaudeSettingsPath()
	}
	if binPath == "" {
		binPath = ResolveBinary()
	}
	root := map[string]any{}
	var origBytes []byte
	if b, err := os.ReadFile(settingsPath); err == nil && len(bytes.TrimSpace(b)) > 0 {
		origBytes = b
		if err := json.Unmarshal(b, &root); err != nil {
			return false, fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	hooksMap, ok := root["hooks"].(map[string]any)
	if !ok || hooksMap == nil {
		hooksMap = map[string]any{}
	}

	for _, ev := range claudeEvents {
		var kept []any
		if existing, ok := hooksMap[ev.Name].([]any); ok {
			for _, g := range existing {
				if !isAgyClaudeGroup(g) {
					kept = append(kept, g)
				}
			}
		}
		entry := ClaudeHookGroup{
			Matcher: ev.Matcher,
			Hooks: []ClaudeHookHandler{{
				Type:    "command",
				Command: fmt.Sprintf("%s hook %s --source claude", shellQuote(binPath), ev.Name),
				Async:   ev.Async,
				VibeRPC: true,
			}},
		}
		kept = append(kept, entry)
		hooksMap[ev.Name] = kept
	}
	root["hooks"] = hooksMap

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	if bytes.Equal(origBytes, out) {
		return false, nil
	}
	return true, paths.WriteFileAtomic(settingsPath, out, 0o644)
}

func UninstallClaudeHooks(settingsPath string) (bool, error) {
	if settingsPath == "" {
		settingsPath = paths.ClaudeSettingsPath()
	}
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	root := map[string]any{}
	if err := json.Unmarshal(b, &root); err != nil {
		return false, err
	}
	hooksMap, ok := root["hooks"].(map[string]any)
	if !ok || hooksMap == nil {
		return false, nil
	}
	changed := false
	for ev, rawList := range hooksMap {
		arr, ok := rawList.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, g := range arr {
			if isAgyClaudeGroup(g) {
				changed = true
				continue
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			delete(hooksMap, ev)
		} else {
			hooksMap[ev] = kept
		}
	}
	if !changed {
		return false, nil
	}
	if len(hooksMap) == 0 {
		delete(root, "hooks")
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(settingsPath, append(out, '\n'), 0o644)
}

func SeedConfig(cfgPath string) (bool, error) {
	if cfgPath == "" {
		cfgPath = paths.ConfigPath()
	}
	if _, err := os.Stat(cfgPath); err == nil {
		return false, nil
	}
	seed := map[string]any{
		"clientId": config.Default().ClientID,
		"appName":  config.Default().AppName,
	}
	b, _ := json.MarshalIndent(seed, "", "  ")
	return true, paths.WriteFileAtomic(cfgPath, append(b, '\n'), 0o644)
}

func InstallAutostart(binPath string) (string, error) {
	if binPath == "" {
		binPath = ResolveBinary()
	}
	removeLegacyAutostart()
	switch runtime.GOOS {
	case "darwin":
		plistPath := filepath.Join(paths.HomeDir(), "Library", "LaunchAgents", LaunchAgentLabel+".plist")
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, LaunchAgentLabel, binPath, paths.LogPath(), paths.LogPath())
		if err := paths.WriteFileAtomic(plistPath, []byte(plist), 0o644); err != nil {
			return "", err
		}
		_ = exec.Command("launchctl", "unload", plistPath).Run()
		_ = exec.Command("launchctl", "load", plistPath).Run()
		return plistPath, nil

	case "linux":
		unitDir := filepath.Join(paths.HomeDir(), ".config", "systemd", "user")
		unitPath := filepath.Join(unitDir, SystemdUnitName)
		unit := fmt.Sprintf(`[Unit]
Description=Antigravity + Claude Code Discord Rich Presence daemon
After=graphical-session.target

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, shellQuote(binPath))
		if err := paths.WriteFileAtomic(unitPath, []byte(unit), 0o644); err != nil {
			return "", err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		_ = exec.Command("systemctl", "--user", "enable", SystemdUnitName).Run()
		return unitPath, nil

	case "windows":
		val := fmt.Sprintf(`"%s" daemon`, binPath)
		err := exec.Command("reg", "add", WindowsRunRegPath, "/v", WindowsRunValName, "/t", "REG_SZ", "/d", val, "/f").Run()
		return WindowsRunRegPath + "\\" + WindowsRunValName, err

	default:
		return "", nil
	}
}

// removeLegacyAutostart unloads and deletes any pre-rename agy-rpc autostart entry.
func removeLegacyAutostart() {
	switch runtime.GOOS {
	case "darwin":
		plistPath := filepath.Join(paths.HomeDir(), "Library", "LaunchAgents", LegacyLaunchAgentLabel+".plist")
		if _, err := os.Stat(plistPath); err == nil {
			_ = exec.Command("launchctl", "unload", plistPath).Run()
			_ = os.Remove(plistPath)
		}
	case "linux":
		unitPath := filepath.Join(paths.HomeDir(), ".config", "systemd", "user", LegacySystemdUnitName)
		if _, err := os.Stat(unitPath); err == nil {
			_ = exec.Command("systemctl", "--user", "disable", "--now", LegacySystemdUnitName).Run()
			_ = os.Remove(unitPath)
		}
	case "windows":
		_ = exec.Command("reg", "delete", WindowsRunRegPath, "/v", LegacyWindowsRunValName, "/f").Run()
	}
}

func UninstallAutostart() error {
	removeLegacyAutostart()
	switch runtime.GOOS {
	case "darwin":
		plistPath := filepath.Join(paths.HomeDir(), "Library", "LaunchAgents", LaunchAgentLabel+".plist")
		_ = exec.Command("launchctl", "unload", plistPath).Run()
		_ = os.Remove(plistPath)
	case "linux":
		unitPath := filepath.Join(paths.HomeDir(), ".config", "systemd", "user", SystemdUnitName)
		_ = exec.Command("systemctl", "--user", "disable", "--now", SystemdUnitName).Run()
		_ = os.Remove(unitPath)
	case "windows":
		_ = exec.Command("reg", "delete", WindowsRunRegPath, "/v", WindowsRunValName, "/f").Run()
	}
	return nil
}

// SelfTestHooks verifies both Antigravity PreToolUse (fail-closed {"decision":"allow"})
// and Claude Code PreToolUse ({"continue":true}) in-process without mutating real state.
func SelfTestHooks() error {
	var agyOut bytes.Buffer
	agyIn := bytes.NewBufferString(`{"conversationId":"selftest","hookEventName":"PreToolUse","toolCall":{"name":"read_file","args":{"Path":"README.md"}}}`)
	// Use isolated temp state dir during self-test
	origState := os.Getenv("VIBE_RPC_STATE_DIR")
	tmpDir, err := os.MkdirTemp("", "vibe-rpc-selftest-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
		if origState == "" {
			_ = os.Unsetenv("VIBE_RPC_STATE_DIR")
		} else {
			_ = os.Setenv("VIBE_RPC_STATE_DIR", origState)
		}
	}()
	_ = os.Setenv("VIBE_RPC_STATE_DIR", tmpDir)
	_ = os.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	defer os.Unsetenv("VIBE_RPC_NO_AUTOSTART")

	hook.RunCLI("PreToolUse", "antigravity", agyIn, &agyOut)
	if !strings.Contains(agyOut.String(), `"decision":"allow"`) {
		return fmt.Errorf("Antigravity PreToolUse self-test failed: got %s", agyOut.String())
	}

	var ccOut bytes.Buffer
	ccIn := bytes.NewBufferString(`{"session_id":"selftest-cc","tool_name":"Read","tool_input":{"file_path":"README.md"}}`)
	hook.RunCLI("PreToolUse", "claude", ccIn, &ccOut)
	if !strings.Contains(ccOut.String(), `"continue":true`) {
		return fmt.Errorf("Claude Code PreToolUse self-test failed: got %s", ccOut.String())
	}
	return nil
}
