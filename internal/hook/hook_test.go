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

func TestOpenCodeEvents(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	run := func(event, body string) {
		var out bytes.Buffer
		RunCLI(event, "opencode", bytes.NewBufferString(body), &out)
	}
	run("PreToolUse", `{"session_id":"ses_1","cwd":"/w/api","model":"glm-5.3-flash-free","tool_name":"edit","tool_input":{"filePath":"/w/api/main.go"}}`)
	st := state.Read("ses_1")
	if st.Source != "opencode" || st.Status != "working" || st.CurrentFile != "main.go" || st.Model != "glm-5.3-flash-free" {
		t.Fatalf("tool event: %+v", st)
	}
	run("PreToolUse", `{"session_id":"ses_1","cwd":"/w/api","tool_name":"bash","tool_input":{"command":"go test ./..."}}`)
	if st := state.Read("ses_1"); st.LastBashCommand != "go test ./..." {
		t.Fatalf("bash not recorded: %+v", st)
	}
	run("Notification", `{"session_id":"ses_1","cwd":"/w/api","message":"OpenCode needs permission: Run go test","notification_type":"permission_prompt"}`)
	if st := state.Read("ses_1"); st.Status != "notification" || st.AttentionKind != "permission" {
		t.Fatalf("permission: %+v", st)
	}
	run("PermissionReplied", `{"session_id":"ses_1","cwd":"/w/api"}`)
	if st := state.Read("ses_1"); st.Status == "notification" || st.AttentionKind != "" {
		t.Fatalf("permission reply should clear the alert: %+v", st)
	}
	run("PreToolUse", `{"session_id":"ses_1","cwd":"/w/api","tool_name":"question","tool_input":{"questions":[{"question":"Use Postgres?"}]}}`)
	if st := state.Read("ses_1"); st.AttentionKind != "question" || st.AttentionText != "Use Postgres?" {
		t.Fatalf("question: %+v", st)
	}
}

func TestCodexEvents(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	run := func(event, body string) string {
		var out bytes.Buffer
		RunCLI(event, "codex", bytes.NewBufferString(body), &out)
		return out.String()
	}
	// Codex must never get a decision back (an "allow" could skip its approval).
	if out := run("PreToolUse", `{"session_id":"cx1","cwd":"/w/api","model":"gpt-5.5-codex","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`); strings.TrimSpace(out) != "{}" {
		t.Fatalf("codex hook output = %q, want {}", out)
	}
	st := state.Read("cx1")
	if st.Source != "codex" || st.Status != "working" || st.LastBashCommand != "go test ./..." || st.Model != "gpt-5.5-codex" {
		t.Fatalf("bash event: %+v", st)
	}
	run("PreToolUse", `{"session_id":"cx1","cwd":"/w/api","tool_name":"apply_patch","tool_input":{"input":"*** Begin Patch\n*** Update File: internal/server.go\n+x\n*** End Patch"}}`)
	if st := state.Read("cx1"); st.CurrentFile != "server.go" {
		t.Fatalf("apply_patch file = %q", st.CurrentFile)
	}
	run("PermissionRequest", `{"session_id":"cx1","cwd":"/w/api","tool_name":"Bash","tool_input":{"command":"rm -rf build","description":"Delete the build folder"}}`)
	st = state.Read("cx1")
	if st.Status != "notification" || st.AttentionKind != "permission" || st.AttentionText != "Codex needs your permission: Delete the build folder" {
		t.Fatalf("permission: %+v", st)
	}
	run("PostToolUse", `{"session_id":"cx1","cwd":"/w/api","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`)
	run("Stop", `{"session_id":"cx1","cwd":"/w/api","stop_hook_active":false}`)
	if st := state.Read("cx1"); st.Status == "notification" {
		t.Fatalf("approval should clear the alert: %+v", st)
	}
}

// A Claude Code session first seen after SessionStart must not inherit the
// default Antigravity model ("gemini") from a freshly created state file.
func TestClaudeSessionFirstSeenMidStreamModel(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	var out bytes.Buffer
	RunCLI("PreToolUse", "claude", bytes.NewBufferString(`{"session_id":"cc-mid","cwd":"/tmp/app","tool_name":"Bash","tool_input":{"command":"ls"}}`), &out)
	if st := state.Read("cc-mid"); st.Model != "claude" {
		t.Fatalf("model = %q, want claude", st.Model)
	}
	// Codex too: it must not show up as Gemini.
	RunCLI("UserPromptSubmit", "codex", bytes.NewBufferString(`{"session_id":"cx-mid","cwd":"/tmp/app","prompt":"hi"}`), &out)
	if st := state.Read("cx-mid"); st.Model != "codex" {
		t.Fatalf("codex model = %q, want codex", st.Model)
	}
	// An Antigravity session keeps its default.
	RunCLI("PreToolUse", "antigravity", bytes.NewBufferString(`{"conversationId":"agy-mid","workspacePaths":["/tmp/app"],"hookEventName":"PreToolUse","toolCall":{"name":"view_file","args":{}}}`), &out)
	if st := state.Read("agy-mid"); st.Model != "gemini" {
		t.Fatalf("antigravity model = %q, want gemini", st.Model)
	}
}

// Stop on a busy session records when the turn finished and how long it ran;
// a repeated Stop on an idle session, or a failed stop, doesn't.
func TestStopRecordsFinishedTurn(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	body := `{"session_id":"cc-stop","cwd":"/tmp/app"}`
	ProcessEvent("UserPromptSubmit", mustPayload(t, body), 1_000_000, "claude")
	ProcessEvent("Stop", mustPayload(t, body), 1_045_000, "claude")
	st := state.Read("cc-stop")
	if st.Status != "idle" || st.LastStop != 1_045_000 || st.LastTurnMs != 45_000 {
		t.Fatalf("after Stop: status=%q lastStop=%d turn=%d", st.Status, st.LastStop, st.LastTurnMs)
	}
	ProcessEvent("Stop", mustPayload(t, body), 1_050_000, "claude")
	if st := state.Read("cc-stop"); st.LastStop != 1_045_000 {
		t.Fatalf("repeated Stop on an idle session moved lastStop to %d", st.LastStop)
	}
	ProcessEvent("UserPromptSubmit", mustPayload(t, body), 1_060_000, "claude")
	ProcessEvent("StopFailure", mustPayload(t, body), 1_070_000, "claude")
	if st := state.Read("cc-stop"); st.LastStop != 1_045_000 {
		t.Fatalf("StopFailure counted as a finished turn: lastStop=%d", st.LastStop)
	}
}

func mustPayload(t *testing.T, s string) Payload {
	t.Helper()
	var p Payload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCopilotEvents(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("VIBE_RPC_STATE_DIR", tmp)
	t.Setenv("VIBE_RPC_DATA_DIR", filepath.Join(tmp, "data"))
	t.Setenv("VIBE_RPC_NO_AUTOSTART", "1")
	run := func(event, body string) string {
		var out bytes.Buffer
		RunCLI(event, "copilot", bytes.NewBufferString(body), &out)
		return out.String()
	}
	run("SessionStart", `{"hook_event_name":"SessionStart","session_id":"cp1","timestamp":"2026-11-28T15:00:00.000Z","cwd":"/w/web","source":"new"}`)
	if st := state.Read("cp1"); st.Source != "copilot" || st.Model != "copilot" {
		t.Fatalf("start: %+v", st)
	}
	run("UserPromptSubmit", `{"session_id":"cp1","cwd":"/w/web","prompt":"fix it"}`)
	// Copilot reads permissionDecision from PreToolUse output: stay a no-op.
	if out := run("PreToolUse", `{"session_id":"cp1","cwd":"/w/web","tool_name":"bash","tool_input":{"command":"npm test","description":"Run tests"}}`); strings.TrimSpace(out) != "{}" {
		t.Fatalf("copilot hook output = %q, want {}", out)
	}
	st := state.Read("cp1")
	if st.Status != "working" || st.LastBashCommand != "npm test" || st.Messages != 1 {
		t.Fatalf("bash: %+v", st)
	}
	// Tool input may arrive JSON-encoded, or as apply_patch's bare patch text.
	run("PreToolUse", `{"session_id":"cp1","cwd":"/w/web","tool_name":"view","tool_input":"{\"path\":\"/w/web/src/app.ts\"}"}`)
	if st := state.Read("cp1"); st.CurrentFile != "app.ts" || len(st.FilesRead) != 1 {
		t.Fatalf("view: %+v", st)
	}
	run("PreToolUse", `{"session_id":"cp1","cwd":"/w/web","tool_name":"apply_patch","tool_input":"*** Begin Patch\n*** Update File: src/server.ts\n+x\n*** End Patch"}`)
	if st := state.Read("cp1"); st.CurrentFile != "server.ts" {
		t.Fatalf("apply_patch file = %q", st.CurrentFile)
	}
	run("PostToolUse", `{"session_id":"cp1","cwd":"/w/web","tool_name":"edit","tool_input":{"path":"/w/web/src/app.ts","old_str":"a","new_str":"b"}}`)
	if st := state.Read("cp1"); len(st.FilesEdited) != 1 || st.CurrentTool != "" {
		t.Fatalf("edit: %+v", st)
	}

	// A finished shell command isn't a reason to raise the notch.
	run("Notification", `{"session_id":"cp1","cwd":"/w/web","message":"Shell command completed","notification_type":"shell_completion"}`)
	if st := state.Read("cp1"); st.Status == "notification" {
		t.Fatalf("shell completion raised an alert: %+v", st)
	}
	run("Notification", `{"session_id":"cp1","cwd":"/w/web","message":"Allow bash: rm -rf dist?","notification_type":"permission_prompt"}`)
	st = state.Read("cp1")
	if st.Status != "notification" || st.AttentionKind != "permission" || st.AttentionText != "Allow bash: rm -rf dist?" {
		t.Fatalf("permission: %+v", st)
	}
	run("PreToolUse", `{"session_id":"cp1","cwd":"/w/web","tool_name":"ask_user","tool_input":{"question":"Which port?","choices":["3000","8080"]}}`)
	if st := state.Read("cp1"); st.AttentionKind != "question" || st.AttentionText != "Which port?" {
		t.Fatalf("ask_user: %+v", st)
	}
	run("Stop", `{"session_id":"cp1","cwd":"/w/web","stopReason":"end_turn"}`)
	if st := state.Read("cp1"); st.Status != "idle" || st.LastStop == 0 {
		t.Fatalf("stop: %+v", st)
	}
	run("SessionEnd", `{"session_id":"cp1","cwd":"/w/web","reason":"user_exit"}`)
	if st := state.Read("cp1"); st.Status != "stale" || !st.AgentClosed {
		t.Fatalf("end: %+v", st)
	}
}
