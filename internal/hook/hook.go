package hook

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/gitinfo"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/proc"
	"github.com/chauveaul/vibe-rpc/internal/ships"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type Payload struct {
	// Antigravity fields (camelCase)
	ConversationID        string   `json:"conversationId"`
	WorkspacePaths        []string `json:"workspacePaths"`
	TranscriptPath        string   `json:"transcriptPath"`
	ArtifactDirectoryPath string   `json:"artifactDirectoryPath"`
	ModelName             string   `json:"modelName"`
	LastUserInput         string   `json:"lastUserInput"`
	HookEventName         string   `json:"hookEventName"`
	InvocationNum         int      `json:"invocationNum"`
	ToolCall              ToolCall `json:"toolCall"`
	Error                 string   `json:"error"`
	TerminationReason     string   `json:"terminationReason"`

	// Claude Code fields (snake_case)
	SessionIDSnake      string         `json:"session_id"`
	SessionIDCamel      string         `json:"sessionId"`
	TranscriptPathSnake string         `json:"transcript_path"`
	Cwd                 string         `json:"cwd"`
	Source              string         `json:"source"`
	ToolNameSnake       string         `json:"tool_name"`
	ToolNameCamel       string         `json:"toolName"`
	ToolInputSnake      map[string]any `json:"tool_input"`
	ToolInputCamel      map[string]any `json:"toolInput"`
	Trigger             string         `json:"trigger"`
	Message             string         `json:"message"`           // Notification
	NotificationType    string         `json:"notification_type"` // Notification: permission_prompt | idle_prompt | …
	Matcher             string         `json:"matcher"`
	ModelRaw            any            `json:"model"`
}

var (
	readTools = map[string]bool{
		"read_file": true, "view_file": true, "view_file_outline": true,
		"view_code_item": true, "Read": true, "NotebookEdit": true,
	}
	editTools = map[string]bool{
		"write_file": true, "edit_file": true, "replace_file_content": true,
		"multi_replace_file_content": true, "write_to_file": true,
		"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true,
	}
)

// EnsureDaemonFn is wired by cmd/vibe-rpc so hook.go stays decoupled from daemon spawning.
var EnsureDaemonFn func(autostart bool)

func appendEvent(entry map[string]any) {
	p := paths.EventsLogPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if info, err := os.Stat(p); err == nil && info.Size() > 5*1024*1024 {
		_ = os.Rename(p, p+".1")
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

func promptSig(text string) string {
	if text == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	return fmt.Sprintf("%d:%x", len(text), h.Sum32())
}

func extractCwd(in Payload) string {
	if in.Cwd != "" {
		return in.Cwd
	}
	if len(in.WorkspacePaths) > 0 && in.WorkspacePaths[0] != "" {
		return in.WorkspacePaths[0]
	}
	return ""
}

func extractModel(in Payload) string {
	raw := in.ModelName
	if raw == "" && in.ModelRaw != nil {
		switch m := in.ModelRaw.(type) {
		case string:
			raw = m
		case map[string]any:
			if id, ok := m["id"].(string); ok {
				raw = id
			}
		}
	}
	if raw == "" || strings.EqualFold(raw, "auto") {
		return ""
	}
	return raw
}

func extractTool(in Payload) (string, map[string]any) {
	name := in.ToolCall.Name
	if name == "" {
		name = in.ToolNameSnake
	}
	if name == "" {
		name = in.ToolNameCamel
	}
	if name == "" {
		name = "tool"
	}
	args := in.ToolCall.Args
	if args == nil {
		args = in.ToolInputSnake
	}
	if args == nil {
		args = in.ToolInputCamel
	}
	if args == nil {
		args = map[string]any{}
	}
	return name, args
}

func strArg(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func maybeAutostart() {
	if os.Getenv("VIBE_RPC_NO_AUTOSTART") == "1" || EnsureDaemonFn == nil {
		return
	}
	cfg := config.Load()
	EnsureDaemonFn(cfg.IsAutostart())
}

// OutputFor returns the exact JSON stdout payload required by the calling host.
//   - Claude Code expects {"continue":true}.
//   - Antigravity PreToolUse is fail-closed and REQUIRES {"decision":"allow"}.
//   - All other Antigravity events expect {}.
func OutputFor(event, source string) map[string]any {
	if source == "claude" {
		return map[string]any{"continue": true}
	}
	// Codex reads decisions from hook output (an "allow" could skip its
	// approval prompt), and OpenCode's plugin ignores output: stay a no-op.
	if source == "codex" || source == "opencode" {
		return map[string]any{}
	}
	if event == "PreToolUse" {
		return map[string]any{"decision": "allow"}
	}
	return map[string]any{}
}

func ProcessEvent(event string, in Payload, nowMs int64, explicitSource string) {
	if nowMs == 0 {
		nowMs = time.Now().UnixMilli()
	}
	sid := in.ConversationID
	if sid == "" {
		sid = in.SessionIDSnake
	}
	if sid == "" {
		sid = in.SessionIDCamel
	}
	source := explicitSource
	if source == "" {
		if in.ConversationID != "" {
			source = "antigravity"
		} else if in.SessionIDSnake != "" {
			source = "claude"
		} else {
			source = "antigravity"
		}
	}

	cwd := extractCwd(in)
	model := extractModel(in)
	tpath := in.TranscriptPath
	if tpath == "" {
		tpath = in.TranscriptPathSnake
	}

	if sid != "" && cwd != "" {
		state.RecordSessionCwd(sid, cwd, "")
	}

	update := func(fn func(s *state.SessionState, stale bool)) {
		_, _ = state.Update(sid, func(s *state.SessionState) {
			stale := nowMs < s.LastEventAt
			if !stale {
				s.LastEventAt = nowMs
			}
			if cwd != "" {
				s.Cwd = cwd
			}
			if tpath != "" {
				s.TranscriptPath = tpath
			}
			if model != "" {
				s.Model = model
			}
			s.Source = source
			s.SessionID = sid
			fn(s, stale)
		})
	}

	switch event {
	case "SessionStart":
		if in.Source == "compact" {
			update(func(s *state.SessionState, _ bool) {
				s.CompactStartedAt = 0
				s.CompactTrigger = ""
				s.Status = "working"
				s.LastActivity = nowMs
				s.AgentClosed = false
				s.ClaudeClosed = false
				if s.SessionStart == 0 {
					s.SessionStart = nowMs
				}
			})
			return
		}
		_, _ = state.Reset(sid, func(s *state.SessionState) {
			s.Source = source
			s.LastEventAt = nowMs
			s.SessionStart = nowMs
			s.LastActivity = nowMs
			if cwd != "" {
				s.Cwd = cwd
			}
			if model != "" {
				s.Model = model
			} else if source == "claude" {
				s.Model = "claude"
			} else {
				s.Model = "gemini"
			}
			s.TranscriptPath = tpath
			s.Status = "idle"
		})
		maybeAutostart()

	case "PreInvocation":
		sig := promptSig(in.LastUserInput)
		update(func(s *state.SessionState, _ bool) {
			isNew := (sig != "" && sig != s.LastUserInputSig) || (in.InvocationNum == 1 && s.LastUserInputSig == "") || s.Messages == 0
			if isNew {
				s.Messages++
				s.LastUserPrompt = nowMs
				if sig != "" {
					s.LastUserInputSig = sig
				}
			}
			s.LastActivity = nowMs
			s.Status = "thinking"
			s.CurrentTool = ""
			s.ToolStartedAt = 0
			s.AgentClosed = false
			s.ClaudeClosed = false
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
		})
		maybeAutostart()

	case "PostInvocation", "SubagentStop":
		update(func(s *state.SessionState, _ bool) {
			s.LastActivity = nowMs
			s.AgentClosed = false
			s.ClaudeClosed = false
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
		})

	case "UserPromptSubmit":
		update(func(s *state.SessionState, _ bool) {
			s.Messages++
			s.LastUserPrompt = nowMs
			s.LastActivity = nowMs
			s.Status = "thinking"
			s.AgentClosed = false
			s.ClaudeClosed = false
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
		})

	case "PreToolUse":
		toolName, toolArgs := extractTool(in)
		file := strArg(toolArgs, "Path", "AbsolutePath", "TargetFile", "file_path", "filePath", "path", "notebook_path")
		if file == "" && toolName == "apply_patch" { // Codex: files live in the patch text
			file = firstPatchFile(strArg(toolArgs, "input", "patch", "command"))
		}
		shellCmd := ""
		if toolName == "run_command" || toolName == "Bash" || toolName == "bash" {
			shellCmd = strArg(toolArgs, "CommandLine", "command")
		}
		// Antigravity's ask_question, Claude Code's AskUserQuestion, OpenCode's question.
		isAsk := toolName == "ask_question" || toolName == "AskUserQuestion" || (toolName == "question" && source == "opencode")
		askText := ""
		if isAsk {
			askText = questionText(toolArgs)
		}
		update(func(s *state.SessionState, stale bool) {
			s.Tools++
			s.ToolBreakdown[toolName]++
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
			if !stale {
				if isAsk {
					s.Status = "notification"
					s.LastNotification = nowMs
					s.AttentionKind, s.AttentionText = "question", askText
					refreshHostApp(s)
					s.CurrentTool = ""
					s.CurrentFile = ""
					s.ToolStartedAt = 0
				} else {
					displayTool := toolName
					if toolName == "call_mcp_tool" {
						if srv, tn := strArg(toolArgs, "ServerName"), strArg(toolArgs, "ToolName"); srv != "" && tn != "" {
							displayTool = "mcp__" + srv + "__" + tn
						}
					}
					s.CurrentTool = displayTool
					s.CurrentFile = state.ShortFile(file)
					s.ToolStartedAt = nowMs
					s.Status = "working"
				}
				s.LastActivity = nowMs
				s.AgentClosed = false
				s.ClaudeClosed = false
				if shellCmd != "" {
					if len(shellCmd) > 500 {
						shellCmd = shellCmd[:500]
					}
					s.LastBashCommand = shellCmd
					s.LastBashAt = nowMs
				}
			}
			if file != "" && readTools[toolName] {
				s.FilesOpened = state.PushUnique(s.FilesOpened, file, 50)
				if toolName == "read_file" || toolName == "view_file" || toolName == "Read" {
					s.FilesRead = state.PushUnique(s.FilesRead, file, 50)
				}
			}
		})
		if isAsk {
			appendEvent(map[string]any{"type": "notification", "ts": nowMs, "cwd": cwd})
		}

	case "PostToolUse", "PostToolUseFailure":
		failed := event == "PostToolUseFailure" || in.Error != ""
		toolName, toolArgs := extractTool(in)
		file := strArg(toolArgs, "Path", "AbsolutePath", "TargetFile", "file_path", "filePath", "path", "notebook_path")
		shellCmd := ""
		if toolName == "run_command" || toolName == "Bash" || toolName == "bash" {
			shellCmd = strArg(toolArgs, "CommandLine", "command")
		}
		shipKind, shipBranch, shipSubject := "", "", ""
		if shellCmd != "" && !failed {
			if sk := ships.Classify(shellCmd); sk != "" {
				shipKind = sk
				shipCwd := cwd
				if shipCwd == "" {
					shipCwd, _ = os.Getwd()
				}
				shipBranch = gitinfo.Branch(shipCwd)
				if sk == "commit" || sk == "push" {
					shipSubject = gitinfo.LastCommitSubject(shipCwd, 80)
				}
			}
		}
		update(func(s *state.SessionState, stale bool) {
			if !stale {
				s.CurrentTool = ""
				s.ToolStartedAt = 0
				s.LastActivity = nowMs
				s.AgentClosed = false
				s.ClaudeClosed = false
			}
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
			if file != "" && editTools[toolName] {
				s.FilesEdited = state.PushUnique(s.FilesEdited, file, 50)
				s.FilesOpened = state.PushUnique(s.FilesOpened, file, 50)
			}
			if shipKind != "" {
				s.JustShipped = nowMs
				s.JustShippedKind = shipKind
				s.JustShippedSubject = shipSubject
				s.JustShippedBranch = shipBranch
			}
		})

	case "Notification":
		kind, text := attentionFromNotification(in.NotificationType, in.Message)
		update(func(s *state.SessionState, _ bool) {
			// Claude Code follows a question with a generic "needs your
			// permission" notification; keep the more useful question text.
			keepQuestion := s.Status == "notification" && s.AttentionKind == "question" &&
				s.AttentionText != "" && nowMs-s.LastNotification < 15_000 && kind != "idle"
			s.Status = "notification"
			s.LastNotification = nowMs
			if !keepQuestion {
				s.AttentionKind, s.AttentionText = kind, text
			}
			if kind != "idle" {
				refreshHostApp(s)
			}
			s.LastActivity = nowMs
			s.AgentClosed = false
			s.ClaudeClosed = false
			s.CurrentTool = ""
			s.CurrentFile = ""
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
		})
		appendEvent(map[string]any{"type": "notification", "ts": nowMs, "cwd": cwd})

	case "PreCompact":
		trig := in.Trigger
		if trig == "" {
			trig = in.Matcher
		}
		update(func(s *state.SessionState, _ bool) {
			s.Status = "compacting"
			s.CompactStartedAt = nowMs
			s.CompactTrigger = trig
			s.CurrentTool = ""
			s.CurrentFile = ""
			s.LastActivity = nowMs
			s.AgentClosed = false
			s.ClaudeClosed = false
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
		})
		appendEvent(map[string]any{"type": "precompact", "ts": nowMs, "trigger": trig, "cwd": cwd})

	case "PostCompact":
		update(func(s *state.SessionState, _ bool) {
			manual := in.Trigger == "manual" || s.CompactTrigger == "manual"
			s.CompactStartedAt = 0
			s.CompactTrigger = ""
			if manual {
				s.Status = "idle"
			} else {
				s.Status = "working"
			}
			s.LastActivity = nowMs
			s.AgentClosed = false
			s.ClaudeClosed = false
		})

	case "PermissionRequest": // Codex (and Claude Code): waiting on an approval
		toolName, toolArgs := extractTool(in)
		what := strArg(toolArgs, "description")
		if what == "" {
			what = strArg(toolArgs, "command", "cmd")
		}
		if what == "" {
			what = toolName
		}
		agent := "Codex"
		if source == "claude" {
			agent = "Claude"
		}
		msg := agent + " needs your permission"
		if what != "" {
			msg += ": " + what
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		update(func(s *state.SessionState, _ bool) {
			s.Status = "notification"
			s.LastNotification = nowMs
			s.LastActivity = nowMs
			s.AttentionKind, s.AttentionText = "permission", msg
			refreshHostApp(s)
			s.AgentClosed = false
			s.ClaudeClosed = false
		})
		appendEvent(map[string]any{"type": "notification", "ts": nowMs, "cwd": cwd})

	case "PermissionReplied": // OpenCode: the user answered a permission prompt
		update(func(s *state.SessionState, _ bool) {
			if s.Status == "notification" && s.AttentionKind == "permission" {
				s.Status = "working"
				s.AttentionKind, s.AttentionText = "", ""
			}
			s.LastActivity = nowMs
		})

	case "SessionEnd":
		update(func(s *state.SessionState, _ bool) {
			s.Status = "stale"
			s.AgentClosed = true
			s.ClaudeClosed = true
			s.CurrentTool = ""
			s.CurrentFile = ""
			s.LastActivity = nowMs
		})

	default: // Stop, StopFailure
		update(func(s *state.SessionState, _ bool) {
			s.Status = "idle"
			s.CurrentTool = ""
			s.CurrentFile = ""
			s.LastActivity = nowMs
			s.AgentClosed = false
			s.ClaudeClosed = false
			if s.SessionStart == 0 {
				s.SessionStart = nowMs
			}
		})
	}
}

// RunCLI reads JSON from stdin, processes the hook event, and ALWAYS prints the
// host-required JSON ack on stdout.
func RunCLI(event, sourceFlag string, stdin io.Reader, stdout io.Writer) {
	source := sourceFlag
	var in Payload
	if raw, err := io.ReadAll(stdin); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	if source == "" {
		if in.ConversationID != "" {
			source = "antigravity"
		} else if in.SessionIDSnake != "" {
			source = "claude"
		} else {
			source = "antigravity"
		}
	}
	resolvedEvent := event
	if resolvedEvent == "" || resolvedEvent == "unknown" {
		if in.HookEventName != "" {
			resolvedEvent = in.HookEventName
		}
	}
	ProcessEvent(resolvedEvent, in, time.Now().UnixMilli(), source)
	out, _ := json.Marshal(OutputFor(resolvedEvent, source))
	_, _ = stdout.Write(out)
}

// attentionFromNotification classifies Claude Code's Notification hook.
// "idle_prompt" fires after every finished turn ("waiting for your input"),
// so it's kept for Discord's Waiting status but not raised in the notch.
func attentionFromNotification(typ, msg string) (string, string) {
	msg = strings.TrimSpace(msg)
	switch {
	case typ == "idle_prompt":
		return "idle", msg
	case typ == "permission_prompt" || strings.Contains(strings.ToLower(msg), "permission"):
		return "permission", msg
	case typ == "auth_success":
		return "idle", msg
	default: // elicitation dialogs and anything new
		if msg == "" {
			msg = "Needs your input"
		}
		return "question", msg
	}
}

// questionText pulls the question out of Antigravity's ask_question args or
// Claude Code's AskUserQuestion input ({"questions":[{"question":…}]}).
func questionText(args map[string]any) string {
	if qs, ok := args["questions"].([]any); ok && len(qs) > 0 {
		if q, ok := qs[0].(map[string]any); ok {
			if t, _ := q["question"].(string); t != "" {
				if len(qs) > 1 {
					return fmt.Sprintf("%s (+%d more)", t, len(qs)-1)
				}
				return t
			}
		}
	}
	if t := strArg(args, "Question", "question", "Prompt", "prompt", "Message", "message", "Text", "text"); t != "" {
		return t
	}
	return "Has a question for you"
}

// refreshHostApp records which GUI app hosts this agent (once, or again if
// that app has quit). The hook's parent is the agent process.
func refreshHostApp(s *state.SessionState) {
	if s.HostAppPID > 0 && proc.Alive(s.HostAppPID) {
		return
	}
	s.HostApp, s.HostAppPID = proc.HostApp(int32(os.Getppid()))
}

// firstPatchFile returns the first file named in a Codex apply_patch body.
func firstPatchFile(patch string) string {
	for _, line := range strings.Split(patch, "\n") {
		for _, p := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
			if strings.HasPrefix(line, p) {
				return strings.TrimSpace(strings.TrimPrefix(line, p))
			}
		}
	}
	return ""
}
