package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/paths"
)

type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

type LiveSession struct {
	Source    string `json:"source"`
	Path      string `json:"path"`
	SessionID string `json:"sessionId"`
	Project   string `json:"project"`
	Cwd       string `json:"cwd"`
	Mtime     int64  `json:"mtime"`
	AgeSec    int    `json:"ageSec"`
}

type PrivacyVerdict struct {
	Visibility string `json:"visibility"`
	Alias      bool   `json:"alias"`
}

type TriggerFrame struct {
	Details        string `json:"details"`
	State          string `json:"state"`
	LargeImageText string `json:"largeImageText,omitempty"`
}

type SessionState struct {
	Source             string         `json:"source"`
	SessionID          string         `json:"sessionId,omitempty"`
	SessionStart       int64          `json:"sessionStart"`
	LastActivity       int64          `json:"lastActivity"`
	LastEventAt        int64          `json:"lastEventAt,omitempty"`
	LastUserPrompt     int64          `json:"lastUserPrompt,omitempty"`
	LastUserInputSig   string         `json:"lastUserInputSig,omitempty"`
	LastNotification   int64          `json:"lastNotification,omitempty"`
	Status             string         `json:"status"`
	CurrentTool        string         `json:"currentTool,omitempty"`
	CurrentFile        string         `json:"currentFile,omitempty"`
	TranscriptPath     string         `json:"transcriptPath,omitempty"`
	CompactStartedAt   int64          `json:"compactStartedAt,omitempty"`
	CompactTrigger     string         `json:"compactTrigger,omitempty"`
	ToolStartedAt      int64          `json:"toolStartedAt,omitempty"`
	JustShipped        int64          `json:"justShipped,omitempty"`
	JustShippedKind    string         `json:"justShippedKind,omitempty"`
	JustShippedSubject string         `json:"justShippedSubject,omitempty"`
	JustShippedBranch  string         `json:"justShippedBranch,omitempty"`
	LastBashCommand    string         `json:"lastBashCommand,omitempty"`
	LastBashAt         int64          `json:"lastBashAt,omitempty"`
	Model              string         `json:"model"`
	Cwd                string         `json:"cwd"`
	Messages           int            `json:"messages"`
	Tools              int            `json:"tools"`
	FilesOpened        []string       `json:"filesOpened"`
	FilesEdited        []string       `json:"filesEdited"`
	FilesRead          []string       `json:"filesRead"`
	Tokens             Tokens         `json:"tokens"`
	ToolBreakdown      map[string]int `json:"toolBreakdown"`
	AgentClosed        bool           `json:"agentClosed"`
	ClaudeClosed       bool           `json:"claudeClosed"`
	Borrowed           bool           `json:"borrowed,omitempty"`

	// Runtime-only overlays (not persisted as authoritative hook fields).
	LiveSessions      []LiveSession   `json:"liveSessions,omitempty"`
	LiveCount         *int            `json:"-"`
	AgentProcessAlive *bool           `json:"-"`
	AntigravityActive bool            `json:"antigravityActive,omitempty"`
	ClaudeActive      bool            `json:"claudeActive,omitempty"`
	SourcesComputed   bool            `json:"-"`
	AgySession        *SessionSnapshot `json:"agySession,omitempty"`
	ClaudeSession     *SessionSnapshot `json:"claudeSession,omitempty"`
	Privacy           *PrivacyVerdict `json:"-"`
	TriggerFrame      *TriggerFrame   `json:"-"`
}

// SessionSnapshot holds per-agent details when both Antigravity and Claude Code are active.
type SessionSnapshot struct {
	Cwd         string `json:"cwd,omitempty"`
	Model       string `json:"model,omitempty"`
	Status      string `json:"status,omitempty"`
	CurrentTool string `json:"currentTool,omitempty"`
	CurrentFile string `json:"currentFile,omitempty"`
}

func DefaultState() SessionState {
	cwd, _ := os.Getwd()
	return SessionState{
		Source:        "antigravity",
		Status:        "idle",
		Model:         "gemini",
		Cwd:           cwd,
		FilesOpened:   []string{},
		FilesEdited:   []string{},
		FilesRead:     []string{},
		ToolBreakdown: map[string]int{},
	}
}

var safeIDRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)
var stateFileRe = regexp.MustCompile(`^state-(.+)\.json$`)

func StatePathFor(sessionID string) string {
	if sessionID == "" {
		return paths.StatePath()
	}
	safe := safeIDRe.ReplaceAllString(sessionID, "")
	if len(safe) > 64 {
		safe = safe[:64]
	}
	if safe == "" {
		safe = "unknown"
	}
	return filepath.Join(paths.StateDir(), "state-"+safe+".json")
}

func Read(sessionID string) SessionState {
	st := DefaultState()
	b, err := os.ReadFile(StatePathFor(sessionID))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	if st.ToolBreakdown == nil {
		st.ToolBreakdown = map[string]int{}
	}
	if st.Source == "" {
		st.Source = "antigravity"
	}
	return st
}

func Write(st SessionState, sessionID string) error {
	if st.ToolBreakdown == nil {
		st.ToolBreakdown = map[string]int{}
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(StatePathFor(sessionID), b, 0o644)
}

func withLock(lockPath string, fn func() error) error {
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755)
	deadline := time.Now().Add(time.Second)
	var lockFile *os.File
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			lockFile = f
			break
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 2*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			break // proceed best-effort
		}
		time.Sleep(4 * time.Millisecond)
	}
	defer func() {
		if lockFile != nil {
			_ = lockFile.Close()
			_ = os.Remove(lockPath)
		}
	}()
	return fn()
}

func Update(sessionID string, mutator func(*SessionState)) (SessionState, error) {
	var out SessionState
	lockPath := StatePathFor(sessionID) + ".lock"
	err := withLock(lockPath, func() error {
		cur := Read(sessionID)
		mutator(&cur)
		out = cur
		return Write(cur, sessionID)
	})
	return out, err
}

func Reset(sessionID string, seed func(*SessionState)) (SessionState, error) {
	var out SessionState
	lockPath := StatePathFor(sessionID) + ".lock"
	err := withLock(lockPath, func() error {
		now := time.Now().UnixMilli()
		cur := DefaultState()
		cur.SessionID = sessionID
		cur.SessionStart = now
		cur.LastActivity = now
		if seed != nil {
			seed(&cur)
		}
		out = cur
		return Write(cur, sessionID)
	})
	return out, err
}

type ListOptions struct {
	IncludeAntigravity bool
	IncludeClaude      bool
	IncludeClaudeRPC   bool
	StateDir           string
	ClaudeStateDir     string
}

func readDirSessionStates(dir, defaultSource string) []SessionState {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SessionState
	for _, e := range entries {
		m := stateFileRe.FindStringSubmatch(e.Name())
		if len(m) < 2 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		st := DefaultState()
		st.Source = defaultSource
		if err := json.Unmarshal(b, &st); err != nil {
			continue
		}
		st.SessionID = m[1]
		if st.Source == "" {
			st.Source = defaultSource
		}
		out = append(out, st)
	}
	return out
}

func ListSessions(opts ListOptions) []SessionState {
	if opts.StateDir == "" {
		opts.StateDir = paths.StateDir()
	}
	if opts.ClaudeStateDir == "" {
		opts.ClaudeStateDir = paths.ClaudeRPCStateDir()
	}
	byID := map[string]SessionState{}

	for _, s := range readDirSessionStates(opts.StateDir, "antigravity") {
		if s.Source == "claude" && !opts.IncludeClaude {
			continue
		}
		if s.Source != "claude" && !opts.IncludeAntigravity {
			continue
		}
		byID[s.SessionID] = s
	}

	if opts.IncludeClaude && opts.IncludeClaudeRPC && opts.ClaudeStateDir != "" && opts.ClaudeStateDir != opts.StateDir {
		for _, s := range readDirSessionStates(opts.ClaudeStateDir, "claude") {
			s.Source = "claude"
			if prev, ok := byID[s.SessionID]; !ok || s.LastActivity > prev.LastActivity {
				byID[s.SessionID] = s
			}
		}
	}

	out := make([]SessionState, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	return out
}

func SweepStale(maxAge time.Duration) int {
	if maxAge <= 0 {
		maxAge = 6 * time.Hour
	}
	entries, err := os.ReadDir(paths.StateDir())
	if err != nil {
		return 0
	}
	removed := 0
	now := time.Now().UnixMilli()
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(paths.StateDir(), name)
		if strings.HasSuffix(name, ".tmp") {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Minute {
				if os.Remove(full) == nil {
					removed++
				}
			}
			continue
		}
		if !stateFileRe.MatchString(name) {
			continue
		}
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var stub struct {
			LastActivity int64 `json:"lastActivity"`
		}
		if json.Unmarshal(b, &stub) == nil && stub.LastActivity > 0 {
			if now-stub.LastActivity > maxAge.Milliseconds() {
				if os.Remove(full) == nil {
					removed++
				}
			}
		}
	}
	return removed
}

func PushUnique(arr []string, val string, maxLen int) []string {
	if val == "" {
		return arr
	}
	if maxLen <= 0 {
		maxLen = 50
	}
	out := make([]string, 0, len(arr)+1)
	out = append(out, val)
	for _, v := range arr {
		if v != val {
			out = append(out, v)
		}
		if len(out) >= maxLen {
			break
		}
	}
	return out
}

func ShortFile(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

func ReadSessionCwds(file string) map[string]string {
	if file == "" {
		file = paths.SessionCwdsPath()
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]string{}
	}
	return m
}

func RecordSessionCwd(sessionID, cwd, file string) {
	if sessionID == "" || cwd == "" {
		return
	}
	if file == "" {
		file = paths.SessionCwdsPath()
	}
	m := ReadSessionCwds(file)
	if m[sessionID] == cwd {
		return
	}
	m[sessionID] = cwd
	if b, err := json.Marshal(m); err == nil {
		_ = paths.WriteFileAtomic(file, b, 0o644)
	}
}
