package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meze/agy-rpc/internal/state"
)

func TestOutputContracts(t *testing.T) {
	if got := OutputFor("PreToolUse", "antigravity"); got["decision"] != "allow" {
		t.Fatalf("Antigravity PreToolUse must be fail-closed allow, got %#v", got)
	}
	if got := OutputFor("PostToolUse", "antigravity"); len(got) != 0 {
		t.Fatalf("Antigravity PostToolUse should be empty {}, got %#v", got)
	}
	if got := OutputFor("PreToolUse", "claude"); got["continue"] != true {
		t.Fatalf("Claude Code PreToolUse must return continue:true, got %#v", got)
	}
	if got := OutputFor("Stop", "claude"); got["continue"] != true {
		t.Fatalf("Claude Code Stop must return continue:true, got %#v", got)
	}
}

func TestRunCLIAntigravityAndClaude(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AGY_RPC_STATE_DIR", tmp)
	t.Setenv("AGY_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("AGY_RPC_NO_AUTOSTART", "1")

	var out bytes.Buffer
	in := bytes.NewBufferString(`{"conversationId":"conv-1","workspacePaths":["/tmp/demo"],"hookEventName":"PreToolUse","toolCall":{"name":"edit_file","args":{"Path":"/tmp/demo/main.go"}}}`)
	RunCLI("PreToolUse", "antigravity", in, &out)
	if !strings.Contains(out.String(), `"decision":"allow"`) {
		t.Fatalf("unexpected stdout: %s", out.String())
	}

	st := state.Read("conv-1")
	if st.Source != "antigravity" || st.Status != "working" || st.CurrentTool != "edit_file" || st.CurrentFile != "main.go" {
		t.Fatalf("unexpected Antigravity state: %+v", st)
	}

	// Claude Code event
	out.Reset()
	inCC := bytes.NewBufferString(`{"session_id":"cc-1","cwd":"/tmp/demo2","tool_name":"Bash","tool_input":{"command":"git commit -m 'ship it'"}}`)
	RunCLI("PostToolUse", "claude", inCC, &out)
	var ack map[string]any
	if err := json.Unmarshal(out.Bytes(), &ack); err != nil || ack["continue"] != true {
		t.Fatalf("expected continue:true, got %s", out.String())
	}
	stCC := state.Read("cc-1")
	if stCC.Source != "claude" || stCC.JustShippedKind != "commit" {
		t.Fatalf("unexpected Claude state: %+v", stCC)
	}
	if _, err := os.Stat(filepath.Join(tmp, "state-conv-1.json")); err != nil {
		t.Fatalf("expected per-session state file: %v", err)
	}
}
