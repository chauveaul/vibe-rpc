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

// GitHub Copilot CLI loads every *.json file in $COPILOT_HOME/hooks (default
// ~/.copilot/hooks) as user-level hooks: {"version":1,"hooks":{"<Event>":
// [{"type":"command","bash":…,"powershell":…,"timeoutSec":…}]}}. vibe-rpc
// owns one file there, so installing rewrites it and uninstalling deletes it
// without touching the user's other hook files.
//
// Event names are PascalCase on purpose: Copilot then sends the snake_case
// payload (session_id, tool_name, tool_input, …) that the hook already reads
// for Claude Code. "notification" has no PascalCase alias; its payload
// (message, notification_type) is snake_case anyway.
//
// Copilot fails closed when a preToolUse hook exits non-zero, so every
// command swallows its exit status: a missing or broken vibe-rpc binary must
// never block a tool call. The hook prints {}, which approves nothing.

// copilotEvents maps Copilot's hook event names to the event vibe-rpc's hook
// handler processes, with timeouts (seconds).
var copilotEvents = []struct {
	Name    string // key in the hooks file
	Event   string // passed to `vibe-rpc hook`
	Timeout int
}{
	{"SessionStart", "SessionStart", 5},
	{"UserPromptSubmit", "UserPromptSubmit", 5},
	{"PreToolUse", "PreToolUse", 5},
	{"PostToolUse", "PostToolUse", 5},
	{"PostToolUseFailure", "PostToolUseFailure", 5},
	{"PreCompact", "PreCompact", 5},
	{"notification", "Notification", 5},
	{"SubagentStop", "SubagentStop", 5},
	{"Stop", "Stop", 5},
	{"SessionEnd", "SessionEnd", 5},
}

// CopilotHooksPath is vibe-rpc's own file in Copilot's user hooks directory.
func CopilotHooksPath() string {
	return filepath.Join(detect.CopilotHome(), "hooks", "vibe-rpc.json")
}

func copilotHooksFile(binPath string) map[string]any {
	hooks := map[string]any{}
	for _, ev := range copilotEvents {
		args := fmt.Sprintf("hook %s --source copilot", ev.Event)
		hooks[ev.Name] = []any{map[string]any{
			"type":       "command",
			"bash":       fmt.Sprintf("%s %s || true", shellQuote(binPath), args),
			"powershell": fmt.Sprintf("& '%s' %s; exit 0", strings.ReplaceAll(binPath, "'", "''"), args),
			"timeoutSec": ev.Timeout,
		}}
	}
	return map[string]any{"version": 1, "hooks": hooks}
}

// InstallCopilotHooks writes (or refreshes) vibe-rpc's Copilot hooks file.
func InstallCopilotHooks(binPath string) (bool, error) {
	if binPath == "" {
		binPath = ResolveBinary()
	}
	out, err := json.MarshalIndent(copilotHooksFile(binPath), "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	p := CopilotHooksPath()
	if orig, err := os.ReadFile(p); err == nil && bytes.Equal(orig, out) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(p, out, 0o644)
}

// UninstallCopilotHooks deletes vibe-rpc's Copilot hooks file.
func UninstallCopilotHooks() (bool, error) {
	err := os.Remove(CopilotHooksPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// CopilotHooksInstalled reports whether vibe-rpc's Copilot hooks file exists
// and wires the PreToolUse event.
func CopilotHooksInstalled() bool {
	b, err := os.ReadFile(CopilotHooksPath())
	if err != nil {
		return false
	}
	var root struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	for _, h := range root.Hooks["PreToolUse"] {
		if cmd, _ := h["bash"].(string); strings.Contains(cmd, "--source copilot") {
			return true
		}
	}
	return false
}
