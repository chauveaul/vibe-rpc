package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chauveaul/vibe-rpc/internal/state"
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
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")

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

func TestAttentionCapture(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	run := func(event, source, body string) {
		var out bytes.Buffer
		RunCLI(event, source, bytes.NewBufferString(body), &out)
	}

	run("Notification", "claude", `{"session_id":"cc-p","cwd":"/tmp/app","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`)
	st := state.Read("cc-p")
	if st.Status != "notification" || st.AttentionKind != "permission" || st.AttentionText != "Claude needs your permission to use Bash" {
		t.Fatalf("permission not captured: %+v", st)
	}

	// "waiting for your input" after a finished turn is not an alert.
	run("Notification", "claude", `{"session_id":"cc-i","cwd":"/tmp/app","message":"Claude is waiting for your input","notification_type":"idle_prompt"}`)
	if st := state.Read("cc-i"); st.AttentionKind != "idle" {
		t.Fatalf("idle prompt kind = %q", st.AttentionKind)
	}

	run("PreToolUse", "claude", `{"session_id":"cc-q","cwd":"/tmp/app","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which database should we use?","options":[]},{"question":"Second?"}]}}`)
	st = state.Read("cc-q")
	if st.Status != "notification" || st.AttentionKind != "question" || st.AttentionText != "Which database should we use? (+1 more)" {
		t.Fatalf("Claude question not captured: %+v", st)
	}

	run("PreToolUse", "antigravity", `{"conversationId":"agy-q","workspacePaths":["/tmp/app"],"hookEventName":"PreToolUse","toolCall":{"name":"ask_question","args":{"Question":"Deploy to prod?"}}}`)
	if st := state.Read("agy-q"); st.AttentionKind != "question" || st.AttentionText != "Deploy to prod?" {
		t.Fatalf("Antigravity question not captured: %+v", st)
	}

	// Answering moves the session on, which clears the alert.
	run("PostToolUse", "claude", `{"session_id":"cc-p","cwd":"/tmp/app","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	run("PreToolUse", "claude", `{"session_id":"cc-p","cwd":"/tmp/app","tool_name":"Read","tool_input":{"file_path":"/tmp/app/x.go"}}`)
	if st := state.Read("cc-p"); st.Status == "notification" {
		t.Fatalf("alert should clear once the agent continues: %+v", st)
	}
}

func TestQuestionTextSurvivesFollowUpPermissionNotification(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	var out bytes.Buffer
	RunCLI("PreToolUse", "claude", bytes.NewBufferString(`{"session_id":"cc-f","cwd":"/tmp/app","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Ship it?"}]}}`), &out)
	RunCLI("Notification", "claude", bytes.NewBufferString(`{"session_id":"cc-f","cwd":"/tmp/app","message":"Claude needs your permission","notification_type":"permission_prompt"}`), &out)
	if st := state.Read("cc-f"); st.AttentionKind != "question" || st.AttentionText != "Ship it?" {
		t.Fatalf("question text was overwritten: %+v", st)
	}
}
