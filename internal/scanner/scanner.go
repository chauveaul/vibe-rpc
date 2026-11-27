package scanner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/languages"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/ships"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

const (
	CacheVersion   = 2 // v2: per-session active spans (dual-wielding time)
	ActiveGapCapMs = 5 * 60 * 1000
)

var (
	dateSuffixRe = regexp.MustCompile(`[-_.]\d{4}[-_.]?\d{2}[-_.]?\d{2}(?:[Tt._-]?\d{0,6})?Z?$`)
	envLeadRe    = regexp.MustCompile(`^\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S+\s+)+`)
	agyPathRe    = regexp.MustCompile(`(?i)/brain/([^/]+)/\.system_generated/logs/transcript(?:_full)?\.jsonl$`)
	editingTools = map[string]bool{
		"write_file": true, "edit_file": true, "replace_file_content": true,
		"multi_replace_file_content": true, "write_to_file": true,
		"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true,
	}
)

type ModelBucket struct {
	Turns  int     `json:"turns"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

type ProjectDay struct {
	ActiveMs int64 `json:"activeMs"`
	Tokens   int64 `json:"tokens"`
}

type DayBucket struct {
	ActiveMs         int64                   `json:"activeMs"`
	UserMessages     int                     `json:"userMessages"`
	ToolCalls        int                     `json:"toolCalls"`
	InputTokens      int64                   `json:"inputTokens"`
	OutputTokens     int64                   `json:"outputTokens"`
	CacheReadTokens  int64                   `json:"cacheReadTokens"`
	CacheWriteTokens int64                   `json:"cacheWriteTokens"`
	Sessions         int                     `json:"sessions"`
	LinesAdded       int                     `json:"linesAdded"`
	LinesRemoved     int                     `json:"linesRemoved"`
	Cost             float64                 `json:"cost"`
	Notifications    int                     `json:"notifications"`
	Ships            int                     `json:"ships"`
	FirstTs          int64                   `json:"firstTs,omitempty"`
	LastTs           int64                   `json:"lastTs,omitempty"`
	ShipKinds        map[string]int          `json:"shipKinds,omitempty"`
	Projects         map[string]*ProjectDay  `json:"projects,omitempty"`
	ByModel          map[string]*ModelBucket `json:"byModel,omitempty"`
}

type ProjectStats struct {
	Sessions     int     `json:"sessions"`
	ActiveMs     int64   `json:"activeMs"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	UserMessages int     `json:"userMessages"`
	ToolCalls    int     `json:"toolCalls"`
	LinesAdded   int     `json:"linesAdded"`
	LinesRemoved int     `json:"linesRemoved"`
	Cost         float64 `json:"cost"`
}

type HotspotFile struct {
	Path              string `json:"path"`
	Count             int    `json:"count"`
	LastEditedTs      int64  `json:"lastEditedTs,omitempty"`
	DaysSinceLastEdit *int   `json:"daysSinceLastEdit,omitempty"`
}

type ModelSplitEntry struct {
	Model    string  `json:"model"`
	Turns    int     `json:"turns"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	CostPct  float64 `json:"costPct"`
	TokenPct float64 `json:"tokenPct"`
}

type LangStats struct {
	Files int `json:"files"`
	Edits int `json:"edits"`
}

type SourceStats struct {
	Sessions     int     `json:"sessions"`
	ActiveMs     int64   `json:"activeMs"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	UserMessages int     `json:"userMessages"`
}

// SourceDay is one agent's activity on one local day.
type SourceDay struct {
	ActiveMs int64          `json:"activeMs"`
	Sessions int            `json:"sessions"`
	Prompts  int            `json:"prompts"`
	Tokens   int64          `json:"tokens"`
	Cost     float64        `json:"cost"`
	Tools    map[string]int `json:"tools,omitempty"` // attributed to the session's first day
}

type BestDay struct {
	Day string `json:"day"`
	DayBucket
}

type PeakHour struct {
	Hour int `json:"hour"`
	DayBucket
}

type TranscriptSummary struct {
	Source           string                  `json:"source"`
	SessionID        string                  `json:"sessionId"`
	Project          string                  `json:"project"`
	Cwd              string                  `json:"cwd"`
	Model            string                  `json:"model"`
	InputTokens      int64                   `json:"inputTokens"`
	OutputTokens     int64                   `json:"outputTokens"`
	CacheReadTokens  int64                   `json:"cacheReadTokens"`
	CacheWriteTokens int64                   `json:"cacheWriteTokens"`
	UserMessages     int                     `json:"userMessages"`
	ToolCalls        int                     `json:"toolCalls"`
	ToolBreakdown    map[string]int          `json:"toolBreakdown"`
	Files            []string                `json:"files"`
	FirstTs          int64                   `json:"firstTs"`
	LastTs           int64                   `json:"lastTs"`
	ActiveMs         int64                   `json:"activeMs"`
	ByDay            map[string]*DayBucket   `json:"byDay"`
	ByWeek           map[string]*DayBucket   `json:"byWeek"`
	ByHour           map[string]*DayBucket   `json:"byHour"`
	Spans            [][2]int64              `json:"spans,omitempty"` // merged active [start,end) ms intervals
	FileEdits        map[string]int          `json:"fileEdits"`
	FileEditTs       map[string]int64        `json:"fileEditTs"`
	LinesAdded       int                     `json:"linesAdded"`
	LinesRemoved     int                     `json:"linesRemoved"`
	BashCommands     map[string]int          `json:"bashCommands"`
	WebDomains       map[string]int          `json:"webDomains"`
	Subagents        map[string]int          `json:"subagents"`
	Ships            int                     `json:"ships"`
	ShipKinds        map[string]int          `json:"shipKinds"`
	Cost             float64                 `json:"cost"`
	CostByModel      map[string]float64      `json:"costByModel"`
	ModelsUsed       map[string]int          `json:"modelsUsed"`
	ByModel          map[string]*ModelBucket `json:"byModel"`
	Sig              string                  `json:"_sig,omitempty"`
	IsSubagent       bool                    `json:"isSubagent,omitempty"`
	Gone             bool                    `json:"_gone,omitempty"`
}

type ScanCache struct {
	Version int                           `json:"_v"`
	Files   map[string]*TranscriptSummary `json:"files"`
}

type Aggregate struct {
	Sessions         int                              `json:"sessions"`
	SubagentRuns     int                              `json:"subagentRuns"`
	SubagentActiveMs int64                            `json:"subagentActiveMs"`
	InputTokens      int64                            `json:"inputTokens"`
	OutputTokens     int64                            `json:"outputTokens"`
	CacheReadTokens  int64                            `json:"cacheReadTokens"`
	CacheWriteTokens int64                            `json:"cacheWriteTokens"`
	UserMessages     int                              `json:"userMessages"`
	ToolCalls        int                              `json:"toolCalls"`
	ToolBreakdown    map[string]int                   `json:"toolBreakdown"`
	Projects         map[string]*ProjectStats         `json:"projects"`
	ActiveMs         int64                            `json:"activeMs"`
	WallMs           int64                            `json:"wallMs"`
	UniqueFiles      int                              `json:"uniqueFiles"`
	FirstTs          int64                            `json:"firstTs,omitempty"`
	LastTs           int64                            `json:"lastTs,omitempty"`
	ByDay            map[string]*DayBucket            `json:"byDay"`
	ByWeek           map[string]*DayBucket            `json:"byWeek"`
	ByHour           map[string]*DayBucket            `json:"byHour"`
	ByWeekday        map[string]*DayBucket            `json:"byWeekday"`
	FileEdits        map[string]int                   `json:"fileEdits"`
	FileEditTs       map[string]int64                 `json:"fileEditTs"`
	Streak           int                              `json:"streak"`
	LongestStreak    int                              `json:"longestStreak"`
	DaysSinceFirst   int                              `json:"daysSinceFirst"`
	BestDay          *BestDay                         `json:"bestDay,omitempty"`
	PeakHour         *PeakHour                        `json:"peakHour,omitempty"`
	TopEditedFiles   []HotspotFile                    `json:"topEditedFiles"`
	LinesAdded       int                              `json:"linesAdded"`
	LinesRemoved     int                              `json:"linesRemoved"`
	LinesNet         int                              `json:"linesNet"`
	Ships            int                              `json:"ships"`
	ShipKinds        map[string]int                   `json:"shipKinds"`
	BashCommands     map[string]int                   `json:"bashCommands"`
	WebDomains       map[string]int                   `json:"webDomains"`
	Subagents        map[string]int                   `json:"subagents"`
	Languages        map[string]*LangStats            `json:"languages"`
	MCPToolCalls     int                              `json:"mcpToolCalls"`
	BuiltinToolCalls int                              `json:"builtinToolCalls"`
	EstimatedCost    float64                          `json:"estimatedCost"`
	CostByModel      map[string]float64               `json:"costByModel"`
	ModelsUsed       map[string]int                   `json:"modelsUsed"`
	ByModel          map[string]*ModelBucket          `json:"byModel"`
	ModelSplit       []ModelSplitEntry                `json:"modelSplit"`
	BySource         map[string]*SourceStats          `json:"bySource"`
	SourceDays       map[string]map[string]*SourceDay `json:"sourceDays,omitempty"` // source -> day -> stats
	DualMs           int64                            `json:"dualMs"`               // time both agents were active at once
	DualByDay        map[string]int64                 `json:"dualByDay,omitempty"`
	RecentSpans      map[string][][2]int64            `json:"recentSpans,omitempty"` // source -> merged active spans from the last 24h
	Notifications    int                              `json:"notifications"`
	Compactions      int                              `json:"compactions"`
	CompactionsByDay map[string]int                   `json:"compactionsByDay"`
	GeneratedAt      int64                            `json:"generatedAt"`
	Version          int                              `json:"_v"`
}

func DayKey(tsMs int64) string {
	return time.UnixMilli(tsMs).Local().Format("2006-01-02")
}

func DayKeyAt(now time.Time, offsetDays int) string {
	y, m, d := now.Local().Date()
	t := time.Date(y, m, d-offsetDays, 0, 0, 0, 0, time.Local)
	return t.Format("2006-01-02")
}

func WeekKey(tsMs int64) string {
	y, w := time.UnixMilli(tsMs).Local().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

func HourKey(tsMs int64) string {
	return fmt.Sprintf("%d", time.UnixMilli(tsMs).Local().Hour())
}

func CleanProjectName(name string) string {
	if name == "" {
		return name
	}
	cleaned := dateSuffixRe.ReplaceAllString(name, "")
	if cleaned == "" {
		return name
	}
	return cleaned
}

// UnquoteArg unwraps Antigravity transcript JSON-quoted string args (e.g. "\"/tmp/a.go\"").
func UnquoteArg(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var u string
		if err := json.Unmarshal([]byte(s), &u); err == nil {
			return u
		}
	}
	return s
}

func SessionIDFromPath(p string) string {
	norm := strings.ReplaceAll(p, "\\", "/")
	if m := agyPathRe.FindStringSubmatch(norm); len(m) >= 2 {
		return m[1]
	}
	return strings.TrimSuffix(filepath.Base(norm), ".jsonl")
}

func firstShellToken(cmd string) string {
	if cmd == "" {
		return ""
	}
	stripped := strings.TrimSpace(envLeadRe.ReplaceAllString(cmd, ""))
	if stripped == "" {
		return ""
	}
	fields := strings.Fields(stripped)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	if first == "sudo" || first == "time" {
		if len(fields) > 1 {
			return firstShellToken(strings.Join(fields[1:], " "))
		}
		return ""
	}
	first = strings.Trim(first, "`()")
	if idx := strings.LastIndex(first, "/"); idx != -1 {
		first = first[idx+1:]
	}
	return strings.ToLower(first)
}

func domainOf(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err == nil && u.Hostname() != "" {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return ""
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return len(lines)
}

func collectFilePath(args map[string]any) string {
	for _, k := range []string{"Path", "AbsolutePath", "TargetFile", "file_path", "path", "notebook_path"} {
		if v := UnquoteArg(args[k]); v != "" {
			return v
		}
	}
	return ""
}

func inferProjectRoot(p string) string {
	if p == "" {
		return ""
	}
	norm := strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(norm, "/") && !(len(norm) > 2 && norm[1] == ':') {
		return ""
	}
	if strings.Contains(norm, "/.gemini/") || strings.Contains(norm, "/node_modules/") {
		return ""
	}
	home := paths.HomeDir()
	cur := filepath.Dir(p)
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		cur = p
	}
	for depth := 0; depth < 8; depth++ {
		if cur == "" || cur == "/" || cur == home || cur == filepath.Dir(cur) {
			break
		}
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		if _, err := os.Stat(filepath.Join(cur, "package.json")); err == nil {
			return cur
		}
		cur = filepath.Dir(cur)
	}
	parent := filepath.Dir(p)
	if parent != "" && parent != "/" && parent != home {
		return parent
	}
	return ""
}

func parseTs(raw string) int64 {
	if raw == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return 0
		}
	}
	ms := t.UnixMilli()
	if ms < time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		return 0
	}
	if ms > time.Now().Add(48*time.Hour).UnixMilli() {
		return 0
	}
	return ms
}

func newSummary() *TranscriptSummary {
	return &TranscriptSummary{
		Source:        "antigravity",
		ToolBreakdown: map[string]int{},
		ByDay:         map[string]*DayBucket{},
		ByWeek:        map[string]*DayBucket{},
		ByHour:        map[string]*DayBucket{},
		FileEdits:     map[string]int{},
		FileEditTs:    map[string]int64{},
		BashCommands:  map[string]int{},
		WebDomains:    map[string]int{},
		Subagents:     map[string]int{},
		ShipKinds:     map[string]int{},
		CostByModel:   map[string]float64{},
		ModelsUsed:    map[string]int{},
		ByModel:       map[string]*ModelBucket{},
	}
}

func ensureDay(m map[string]*DayBucket, k string) *DayBucket {
	b, ok := m[k]
	if !ok {
		b = &DayBucket{}
		m[k] = b
	}
	return b
}

func mergeDay(target, src *DayBucket) {
	target.ActiveMs += src.ActiveMs
	target.UserMessages += src.UserMessages
	target.ToolCalls += src.ToolCalls
	target.InputTokens += src.InputTokens
	target.OutputTokens += src.OutputTokens
	target.CacheReadTokens += src.CacheReadTokens
	target.CacheWriteTokens += src.CacheWriteTokens
	target.Sessions += src.Sessions
	target.LinesAdded += src.LinesAdded
	target.LinesRemoved += src.LinesRemoved
	target.Cost += src.Cost
	target.Notifications += src.Notifications
	target.Ships += src.Ships
	for k, n := range src.ShipKinds {
		if target.ShipKinds == nil {
			target.ShipKinds = map[string]int{}
		}
		target.ShipKinds[k] += n
	}
	for m, v := range src.ByModel {
		if target.ByModel == nil {
			target.ByModel = map[string]*ModelBucket{}
		}
		tb := target.ByModel[m]
		if tb == nil {
			tb = &ModelBucket{}
			target.ByModel[m] = tb
		}
		tb.Turns += v.Turns
		tb.Tokens += v.Tokens
		tb.Cost += v.Cost
	}
	if src.FirstTs > 0 && (target.FirstTs == 0 || src.FirstTs < target.FirstTs) {
		target.FirstTs = src.FirstTs
	}
	if src.LastTs > 0 && src.LastTs > target.LastTs {
		target.LastTs = src.LastTs
	}
}

type seenUsage struct {
	id         string
	in, out    int64
	cRead, cWr int64
}

func usageDelta(recent *[]seenUsage, id string, u pricing.Usage) (first bool, delta pricing.Usage, ok bool) {
	if id == "" {
		return true, u, true
	}
	for i := range *recent {
		if (*recent)[i].id == id {
			hit := &(*recent)[i]
			d := pricing.Usage{
				InputTokens:      max64(0, u.InputTokens-hit.in),
				OutputTokens:     max64(0, u.OutputTokens-hit.out),
				CacheReadTokens:  max64(0, u.CacheReadTokens-hit.cRead),
				CacheWriteTokens: max64(0, u.CacheWriteTokens-hit.cWr),
			}
			if d.InputTokens == 0 && d.OutputTokens == 0 && d.CacheReadTokens == 0 && d.CacheWriteTokens == 0 {
				return false, pricing.Usage{}, false
			}
			hit.in = max64(hit.in, u.InputTokens)
			hit.out = max64(hit.out, u.OutputTokens)
			hit.cRead = max64(hit.cRead, u.CacheReadTokens)
			hit.cWr = max64(hit.cWr, u.CacheWriteTokens)
			return false, d, true
		}
	}
	*recent = append(*recent, seenUsage{id: id, in: u.InputTokens, out: u.OutputTokens, cRead: u.CacheReadTokens, cWr: u.CacheWriteTokens})
	if len(*recent) > 200 {
		*recent = (*recent)[1:]
	}
	return true, u, true
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func recordToolUse(name string, args map[string]any, s *TranscriptSummary, files map[string]bool, ts int64, dayB *DayBucket, allB []*DayBucket) {
	toolKey := name
	if name == "call_mcp_tool" {
		srv := UnquoteArg(args["ServerName"])
		tool := UnquoteArg(args["ToolName"])
		if srv != "" && tool != "" {
			toolKey = "mcp__" + srv + "__" + tool
		}
	}
	s.ToolCalls++
	s.ToolBreakdown[toolKey]++
	for _, b := range allB {
		b.ToolCalls++
	}

	if f := collectFilePath(args); f != "" {
		files[f] = true
		if s.Cwd == "" {
			if inferred := inferProjectRoot(f); inferred != "" {
				s.Cwd = inferred
				s.Project = CleanProjectName(filepath.Base(inferred))
			}
		}
		if editingTools[name] {
			s.FileEdits[f]++
			if ts > s.FileEditTs[f] {
				s.FileEditTs[f] = ts
			}
		}
	}

	switch name {
	case "edit_file", "Edit":
		adds := countLines(firstNonEmpty(UnquoteArg(args["Replacement"]), UnquoteArg(args["new_string"])))
		rems := countLines(firstNonEmpty(UnquoteArg(args["Target"]), UnquoteArg(args["old_string"])))
		s.LinesAdded += adds
		s.LinesRemoved += rems
		for _, b := range allB {
			b.LinesAdded += adds
			b.LinesRemoved += rems
		}
	case "replace_file_content":
		adds := countLines(UnquoteArg(args["ReplacementContent"]))
		rems := countLines(UnquoteArg(args["TargetContent"]))
		s.LinesAdded += adds
		s.LinesRemoved += rems
		for _, b := range allB {
			b.LinesAdded += adds
			b.LinesRemoved += rems
		}
	case "write_file", "write_to_file", "Write":
		adds := countLines(firstNonEmpty(UnquoteArg(args["Content"]), UnquoteArg(args["CodeContent"]), UnquoteArg(args["content"])))
		s.LinesAdded += adds
		for _, b := range allB {
			b.LinesAdded += adds
		}
	case "NotebookEdit":
		adds := countLines(UnquoteArg(args["new_source"]))
		s.LinesAdded += adds
		for _, b := range allB {
			b.LinesAdded += adds
		}
	case "run_command", "Bash":
		cmdLine := firstNonEmpty(UnquoteArg(args["CommandLine"]), UnquoteArg(args["command"]))
		if tok := firstShellToken(cmdLine); tok != "" {
			s.BashCommands[tok]++
		}
		if sk := ships.Classify(cmdLine); sk != "" {
			s.Ships++
			s.ShipKinds[sk]++
			for _, b := range allB {
				b.Ships++
			}
			if dayB != nil {
				if dayB.ShipKinds == nil {
					dayB.ShipKinds = map[string]int{}
				}
				dayB.ShipKinds[sk]++
			}
		}
	case "read_url_content", "browser_navigate", "WebFetch":
		if host := domainOf(firstNonEmpty(UnquoteArg(args["Url"]), UnquoteArg(args["url"]))); host != "" {
			s.WebDomains[host]++
		}
	case "search_web":
		if host := strings.TrimPrefix(UnquoteArg(args["domain"]), "www."); host != "" {
			s.WebDomains[host]++
		}
	case "invoke_subagent", "Agent", "Task":
		kind := firstNonEmpty(UnquoteArg(args["TypeName"]), UnquoteArg(args["Role"]), UnquoteArg(args["subagent_type"]), "subagent")
		s.Subagents[kind]++
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func recordModelUsage(turnModel, msgID string, rawUsage *pricing.Usage, s *TranscriptSummary, recent *[]seenUsage, dayB *DayBucket, allB []*DayBucket) {
	var firstSeen bool
	var u pricing.Usage
	var hasDelta bool
	if rawUsage != nil {
		firstSeen, u, hasDelta = usageDelta(recent, msgID, *rawUsage)
	} else {
		firstSeen = true
	}

	mkey := ""
	if turnModel != "" {
		mkey = pricing.ModelKeyFor(turnModel)
	}
	var mb *ModelBucket
	if mkey != "" {
		mb = s.ByModel[mkey]
		if mb == nil {
			mb = &ModelBucket{}
			s.ByModel[mkey] = mb
		}
	}
	var dm *ModelBucket
	if (firstSeen || hasDelta) && mkey != "" && dayB != nil {
		if dayB.ByModel == nil {
			dayB.ByModel = map[string]*ModelBucket{}
		}
		dm = dayB.ByModel[mkey]
		if dm == nil {
			dm = &ModelBucket{}
			dayB.ByModel[mkey] = dm
		}
	}
	if hasDelta {
		s.InputTokens += u.InputTokens
		s.OutputTokens += u.OutputTokens
		s.CacheReadTokens += u.CacheReadTokens
		s.CacheWriteTokens += u.CacheWriteTokens
		for _, b := range allB {
			b.InputTokens += u.InputTokens
			b.OutputTokens += u.OutputTokens
			b.CacheReadTokens += u.CacheReadTokens
			b.CacheWriteTokens += u.CacheWriteTokens
		}
		turnTokens := u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
		if mb != nil {
			mb.Tokens += turnTokens
		}
		if dm != nil {
			dm.Tokens += turnTokens
		}
		cost := pricing.CostFor(turnModel, u)
		if cost > 0 {
			s.Cost += cost
			ck := mkey
			if ck == "" {
				ck = "gemini-pro"
			}
			s.CostByModel[ck] += cost
			if mb != nil {
				mb.Cost += cost
			}
			if dayB != nil {
				if dayB.ByModel == nil {
					dayB.ByModel = map[string]*ModelBucket{}
				}
				db := dayB.ByModel[ck]
				if db == nil {
					db = &ModelBucket{}
					dayB.ByModel[ck] = db
				}
				db.Cost += cost
			}
			for _, b := range allB {
				b.Cost += cost
			}
		}
	}
	if turnModel != "" {
		s.Model = turnModel
		if firstSeen {
			s.ModelsUsed[turnModel]++
			if mb != nil {
				mb.Turns++
			}
			if dm != nil {
				dm.Turns++
			}
		}
	}
}

type tsRec struct {
	ts   int64
	day  string
	week string
	hour string
}

// ParseTranscript parses either an Antigravity transcript.jsonl or a Claude Code .jsonl file.
func ParseTranscript(filePath string, sessionCwds map[string]string) (*TranscriptSummary, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := newSummary()
	s.SessionID = SessionIDFromPath(filePath)
	if sessionCwds != nil && s.SessionID != "" {
		if c := sessionCwds[s.SessionID]; c != "" {
			s.Cwd = c
			s.Project = CleanProjectName(filepath.Base(c))
		}
	}
	files := map[string]bool{}
	var recent []seenUsage
	var records []tsRec

	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			parseLine(line, s, files, &recent, &records)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}

	s.Files = make([]string, 0, len(files))
	for fp := range files {
		s.Files = append(s.Files, fp)
	}
	if len(records) > 0 {
		sort.Slice(records, func(i, j int) bool { return records[i].ts < records[j].ts })
		s.FirstTs = records[0].ts
		s.LastTs = records[len(records)-1].ts
		for i := 1; i < len(records); i++ {
			prev := records[i-1]
			gap := records[i].ts - prev.ts
			if gap > 0 && gap < ActiveGapCapMs {
				s.ActiveMs += gap
				ensureDay(s.ByDay, prev.day).ActiveMs += gap
				ensureDay(s.ByWeek, prev.week).ActiveMs += gap
				ensureDay(s.ByHour, prev.hour).ActiveMs += gap
				if n := len(s.Spans); n > 0 && s.Spans[n-1][1] == prev.ts {
					s.Spans[n-1][1] = records[i].ts
				} else {
					s.Spans = append(s.Spans, [2]int64{prev.ts, records[i].ts})
				}
			}
		}
	}
	return s, nil
}

func parseLine(raw []byte, s *TranscriptSummary, files map[string]bool, recent *[]seenUsage, records *[]tsRec) {
	var r struct {
		Type            string `json:"type"`
		Source          string `json:"source"`
		StepIndex       *int   `json:"step_index"`
		SessionID       string `json:"sessionId"`
		Cwd             string `json:"cwd"`
		CreatedAt       string `json:"created_at"`
		Timestamp       string `json:"timestamp"`
		IsMeta          bool   `json:"isMeta"`
		Model           string `json:"model"`
		InputTokens     *int64 `json:"input_tokens"`
		OutputTokens    *int64 `json:"output_tokens"`
		CacheReadTokens *int64 `json:"cache_read_tokens"`
		CacheWriteToks  *int64 `json:"cache_write_tokens"`
		ToolCalls       []struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"tool_calls"`
		Message *struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   *struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return
	}
	if r.SessionID != "" && s.SessionID == "" {
		s.SessionID = r.SessionID
	}
	if r.Cwd != "" && s.Cwd == "" {
		s.Cwd = r.Cwd
		s.Project = CleanProjectName(filepath.Base(r.Cwd))
	}

	ts := parseTs(firstNonEmpty(r.CreatedAt, r.Timestamp))
	var dayB *DayBucket
	var allB []*DayBucket
	if ts > 0 {
		dk, wk, hk := DayKey(ts), WeekKey(ts), HourKey(ts)
		dayB = ensureDay(s.ByDay, dk)
		weekB := ensureDay(s.ByWeek, wk)
		hourB := ensureDay(s.ByHour, hk)
		allB = []*DayBucket{dayB, weekB, hourB}
		for _, b := range allB {
			if b.FirstTs == 0 || ts < b.FirstTs {
				b.FirstTs = ts
			}
			if ts > b.LastTs {
				b.LastTs = ts
			}
		}
		*records = append(*records, tsRec{ts: ts, day: dk, week: wk, hour: hk})
	}

	switch {
	// Antigravity transcript format
	case r.Type == "PLANNER_RESPONSE":
		s.Source = "antigravity"
		turnModel := firstNonEmpty(r.Model, s.Model)
		var u *pricing.Usage
		if r.InputTokens != nil || r.OutputTokens != nil || r.CacheReadTokens != nil {
			u = &pricing.Usage{}
			if r.InputTokens != nil {
				u.InputTokens = *r.InputTokens
			}
			if r.OutputTokens != nil {
				u.OutputTokens = *r.OutputTokens
			}
			if r.CacheReadTokens != nil {
				u.CacheReadTokens = *r.CacheReadTokens
			}
			if r.CacheWriteToks != nil {
				u.CacheWriteTokens = *r.CacheWriteToks
			}
		}
		msgID := ""
		if r.StepIndex != nil {
			msgID = fmt.Sprintf("step:%d", *r.StepIndex)
		}
		recordModelUsage(turnModel, msgID, u, s, recent, dayB, allB)
		for _, tc := range r.ToolCalls {
			if tc.Name != "" {
				recordToolUse(tc.Name, tc.Args, s, files, ts, dayB, allB)
			}
		}

	case r.Type == "USER_INPUT" && r.Source == "USER_EXPLICIT":
		s.Source = "antigravity"
		s.UserMessages++
		for _, b := range allB {
			b.UserMessages++
		}

	// Claude Code transcript format
	case r.Type == "assistant" && r.Message != nil:
		s.Source = "claude"
		turnModel := firstNonEmpty(r.Message.Model, s.Model)
		var u *pricing.Usage
		if r.Message.Usage != nil {
			u = &pricing.Usage{
				InputTokens:      r.Message.Usage.InputTokens,
				OutputTokens:     r.Message.Usage.OutputTokens,
				CacheReadTokens:  r.Message.Usage.CacheReadInputTokens,
				CacheWriteTokens: r.Message.Usage.CacheCreationInputTokens,
			}
		}
		recordModelUsage(turnModel, r.Message.ID, u, s, recent, dayB, allB)
		var blocks []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if json.Unmarshal(r.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_use" && b.Name != "" {
					recordToolUse(b.Name, b.Input, s, files, ts, dayB, allB)
				}
			}
		}

	case r.Type == "user" && !r.IsMeta && r.Message != nil:
		if isRealClaudeUserContent(r.Message.Content) {
			s.Source = "claude"
			s.UserMessages++
			for _, b := range allB {
				b.UserMessages++
			}
		}
	}
}

func isRealClaudeUserContent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(s, "<local-command") || strings.HasPrefix(s, "<system-reminder") || strings.HasPrefix(s, "<command-") {
			return false
		}
		return strings.TrimSpace(s) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		hasText := false
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return false
			}
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				hasText = true
			}
		}
		return hasText
	}
	return false
}

// Live token + model cache per transcript path.
type tokCacheEntry struct {
	mtime  int64
	tokens state.Tokens
	model  string
}

var (
	tokMu    sync.Mutex
	tokCache = map[string]tokCacheEntry{}
)

func ReadSessionTokensAndModel(path string) (state.Tokens, string, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return state.Tokens{}, "", false
	}
	mtime := st.ModTime().UnixNano()
	tokMu.Lock()
	if c, ok := tokCache[path]; ok && c.mtime == mtime {
		tokMu.Unlock()
		return c.tokens, c.model, true
	}
	tokMu.Unlock()

	summary, err := ParseTranscript(path, nil)
	if err != nil {
		return state.Tokens{}, "", false
	}
	toks := state.Tokens{
		Input:      summary.InputTokens,
		Output:     summary.OutputTokens,
		CacheRead:  summary.CacheReadTokens,
		CacheWrite: summary.CacheWriteTokens,
	}
	// Latest model is most useful if the user switched models mid-session;
	// ParseTranscript stores first model in summary.Model; inspect last line's model via ModelsUsed fallback.
	model := summary.Model
	tokMu.Lock()
	if len(tokCache) > 256 {
		tokCache = map[string]tokCacheEntry{}
	}
	tokCache[path] = tokCacheEntry{mtime: mtime, tokens: toks, model: model}
	tokMu.Unlock()
	return toks, model, true
}

func DiscoverBrainDirs() []string {
	var out []string
	for _, d := range paths.AgyBrainDirs() {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

func DiscoverClaudeProjectDirs() []string {
	home := paths.HomeDir()
	cands := []string{
		paths.ClaudeProjectsDir(),
		filepath.Join(home, ".config", "claude", "projects"),
		filepath.Join(home, ".local", "share", "claude", "projects"),
		filepath.Join(home, "AppData", "Roaming", "claude", "projects"),
		filepath.Join(home, "AppData", "Local", "claude", "projects"),
		filepath.Join(home, "Library", "Application Support", "claude", "projects"),
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range cands {
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, p)
	}
	return out
}

type LiveOptions struct {
	AgyDirs            []string
	ClaudeDirs         []string
	ThresholdMs        int64
	IncludeAntigravity *bool
	IncludeClaude      *bool
}

func FindLiveSessions(opts LiveOptions) []state.LiveSession {
	if opts.ThresholdMs <= 0 {
		opts.ThresholdMs = 90_000
	}
	cfg := config.Load()
	wantAgy := cfg.Sources.WantAntigravity()
	if opts.IncludeAntigravity != nil {
		wantAgy = *opts.IncludeAntigravity
	}
	wantClaude := cfg.Sources.WantClaude()
	if opts.IncludeClaude != nil {
		wantClaude = *opts.IncludeClaude
	}
	now := time.Now().UnixMilli()
	cwds := state.ReadSessionCwds("")
	var live []state.LiveSession

	if wantAgy {
		dirs := opts.AgyDirs
		if len(dirs) == 0 {
			dirs = DiscoverBrainDirs()
		}
		for _, root := range dirs {
			entries, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				full := filepath.Join(root, e.Name(), ".system_generated", "logs", "transcript.jsonl")
				st, err := os.Stat(full)
				if err != nil {
					continue
				}
				mtime := st.ModTime().UnixMilli()
				age := now - mtime
				if age <= opts.ThresholdMs {
					cwd := cwds[e.Name()]
					proj := e.Name()
					if len(proj) > 8 {
						proj = proj[:8]
					}
					if cwd != "" {
						proj = CleanProjectName(filepath.Base(cwd))
					}
					live = append(live, state.LiveSession{
						Source: "antigravity", Path: full, SessionID: e.Name(),
						Project: proj, Cwd: cwd, Mtime: mtime, AgeSec: int(age / 1000),
					})
				}
			}
		}
	}

	if wantClaude {
		dirs := opts.ClaudeDirs
		if len(dirs) == 0 {
			dirs = DiscoverClaudeProjectDirs()
		}
		for _, root := range dirs {
			projs, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			for _, p := range projs {
				if !p.IsDir() {
					continue
				}
				projPath := filepath.Join(root, p.Name())
				files, err := os.ReadDir(projPath)
				if err != nil {
					continue
				}
				for _, f := range files {
					if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
						continue
					}
					full := filepath.Join(projPath, f.Name())
					st, err := os.Stat(full)
					if err != nil {
						continue
					}
					mtime := st.ModTime().UnixMilli()
					age := now - mtime
					if age <= opts.ThresholdMs {
						sid := strings.TrimSuffix(f.Name(), ".jsonl")
						cwd := cwds[sid]
						if cwd == "" {
							cwd = readHeadCwd(full)
						}
						proj := CleanProjectName(p.Name())
						if cwd != "" {
							proj = CleanProjectName(filepath.Base(cwd))
						}
						live = append(live, state.LiveSession{
							Source: "claude", Path: full, SessionID: sid,
							Project: proj, Cwd: cwd, Mtime: mtime, AgeSec: int(age / 1000),
						})
					}
				}
			}
		}
	}

	sort.Slice(live, func(i, j int) bool { return live[i].Mtime > live[j].Mtime })
	return live
}

func readHeadCwd(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	for i := 0; i < 20; i++ {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var stub struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(line, &stub) == nil && stub.Cwd != "" {
				return stub.Cwd
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

func listTranscripts(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if n == "node_modules" || n == ".git" || n == "scratch" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".jsonl") && d.Name() != "transcript_full.jsonl" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func readEventsByDay() (map[string]int, map[string]int) {
	notifs := map[string]int{}
	compacts := map[string]int{}
	for _, p := range []string{paths.EventsLogPath() + ".1", paths.EventsLogPath()} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if line == "" {
				continue
			}
			var e struct {
				Type string `json:"type"`
				Ts   int64  `json:"ts"`
			}
			if json.Unmarshal([]byte(line), &e) != nil || e.Ts <= 0 {
				continue
			}
			dk := DayKey(e.Ts)
			if e.Type == "notification" {
				notifs[dk]++
			} else if e.Type == "precompact" {
				compacts[dk]++
			}
		}
	}
	return notifs, compacts
}

func AggregateFrom(cache *ScanCache) *Aggregate {
	agg := &Aggregate{
		ToolBreakdown: map[string]int{},
		Projects:      map[string]*ProjectStats{},
		ByDay:         map[string]*DayBucket{},
		ByWeek:        map[string]*DayBucket{},
		ByHour:        map[string]*DayBucket{},
		ByWeekday:     map[string]*DayBucket{},
		FileEdits:     map[string]int{},
		FileEditTs:    map[string]int64{},
		ShipKinds:     map[string]int{},
		BashCommands:  map[string]int{},
		WebDomains:    map[string]int{},
		Subagents:     map[string]int{},
		Languages:     map[string]*LangStats{},
		CostByModel:   map[string]float64{},
		ModelsUsed:    map[string]int{},
		ByModel:       map[string]*ModelBucket{},
		BySource: map[string]*SourceStats{
			"antigravity": {},
			"claude":      {},
			"opencode":    {},
			"codex":       {},
		},
		CompactionsByDay: map[string]int{},
		SourceDays:       map[string]map[string]*SourceDay{"antigravity": {}, "claude": {}, "opencode": {}, "codex": {}},
		DualByDay:        map[string]int64{},
		GeneratedAt:      time.Now().UnixMilli(),
		Version:          CacheVersion,
	}

	fileSet := map[string]bool{}
	spansBySource := map[string][][2]int64{}
	for p, s := range cache.Files {
		if s == nil {
			continue
		}
		isSub := s.IsSubagent || strings.Contains(strings.ReplaceAll(p, "\\", "/"), "/subagents/")
		srcKey := SourceKey(s.Source)
		sb := agg.BySource[srcKey]
		sToks := s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheWriteTokens
		sb.Tokens += sToks
		sb.Cost += s.Cost
		if !isSub {
			sb.Sessions++
			sb.ActiveMs += s.ActiveMs
			sb.UserMessages += s.UserMessages
			spansBySource[srcKey] = append(spansBySource[srcKey], s.Spans...)
		}
		addSourceDays(agg.SourceDays[srcKey], s, isSub)

		agg.InputTokens += s.InputTokens
		agg.OutputTokens += s.OutputTokens
		agg.CacheReadTokens += s.CacheReadTokens
		agg.CacheWriteTokens += s.CacheWriteTokens
		agg.ToolCalls += s.ToolCalls
		for name, n := range s.ToolBreakdown {
			agg.ToolBreakdown[name] += n
		}
		for _, f := range s.Files {
			fileSet[f] = true
		}
		agg.LinesAdded += s.LinesAdded
		agg.LinesRemoved += s.LinesRemoved
		agg.Ships += s.Ships
		for k, n := range s.ShipKinds {
			agg.ShipKinds[k] += n
		}
		agg.EstimatedCost += s.Cost
		for m, v := range s.CostByModel {
			agg.CostByModel[m] += v
		}
		for m, v := range s.ModelsUsed {
			agg.ModelsUsed[m] += v
		}
		for m, v := range s.ByModel {
			t := agg.ByModel[m]
			if t == nil {
				t = &ModelBucket{}
				agg.ByModel[m] = t
			}
			t.Turns += v.Turns
			t.Tokens += v.Tokens
			t.Cost += v.Cost
		}
		for f, ts := range s.FileEditTs {
			if ts > agg.FileEditTs[f] {
				agg.FileEditTs[f] = ts
			}
		}
		for c, n := range s.BashCommands {
			agg.BashCommands[c] += n
		}
		for d, n := range s.WebDomains {
			agg.WebDomains[d] += n
		}
		for k, n := range s.Subagents {
			agg.Subagents[k] += n
		}
		for f, n := range s.FileEdits {
			agg.FileEdits[f] += n
		}

		if isSub {
			agg.SubagentRuns++
			agg.SubagentActiveMs += s.ActiveMs
			for k, src := range s.ByDay {
				t := ensureDay(agg.ByDay, k)
				t.InputTokens += src.InputTokens
				t.OutputTokens += src.OutputTokens
				t.CacheReadTokens += src.CacheReadTokens
				t.CacheWriteTokens += src.CacheWriteTokens
				t.ToolCalls += src.ToolCalls
				t.LinesAdded += src.LinesAdded
				t.LinesRemoved += src.LinesRemoved
				t.Cost += src.Cost
				t.Ships += src.Ships
			}
			continue
		}

		agg.Sessions++
		agg.UserMessages += s.UserMessages
		agg.ActiveMs += s.ActiveMs
		if s.FirstTs > 0 && s.LastTs > s.FirstTs {
			agg.WallMs += s.LastTs - s.FirstTs
		}
		if s.Project != "" {
			ps := agg.Projects[s.Project]
			if ps == nil {
				ps = &ProjectStats{}
				agg.Projects[s.Project] = ps
			}
			ps.Sessions++
			ps.ActiveMs += s.ActiveMs
			ps.InputTokens += s.InputTokens
			ps.OutputTokens += s.OutputTokens
			ps.UserMessages += s.UserMessages
			ps.ToolCalls += s.ToolCalls
			ps.LinesAdded += s.LinesAdded
			ps.LinesRemoved += s.LinesRemoved
			ps.Cost += s.Cost
		}
		if s.FirstTs > 0 && (agg.FirstTs == 0 || s.FirstTs < agg.FirstTs) {
			agg.FirstTs = s.FirstTs
		}
		if s.LastTs > agg.LastTs {
			agg.LastTs = s.LastTs
		}
		for k, d := range s.ByDay {
			t := ensureDay(agg.ByDay, k)
			mergeDay(t, d)
			if s.Project != "" {
				if t.Projects == nil {
					t.Projects = map[string]*ProjectDay{}
				}
				pd := t.Projects[s.Project]
				if pd == nil {
					pd = &ProjectDay{}
					t.Projects[s.Project] = pd
				}
				pd.ActiveMs += d.ActiveMs
				pd.Tokens += d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
			}
		}
		for k, w := range s.ByWeek {
			mergeDay(ensureDay(agg.ByWeek, k), w)
		}
		for k, h := range s.ByHour {
			mergeDay(ensureDay(agg.ByHour, k), h)
		}
		if s.FirstTs > 0 {
			ensureDay(agg.ByDay, DayKey(s.FirstTs)).Sessions++
			ensureDay(agg.ByWeek, WeekKey(s.FirstTs)).Sessions++
			ensureDay(agg.ByHour, HourKey(s.FirstTs)).Sessions++
		}
	}

	// "Dual wielding": time when two or more agents were active at once.
	agg.DualMs = multiAgentByDay(spansBySource, agg.DualByDay)
	agg.RecentSpans = map[string][][2]int64{}
	cutoff := agg.GeneratedAt - 24*3_600_000
	for src, spans := range spansBySource {
		for _, sp := range mergeSpans(spans) {
			if sp[1] >= cutoff {
				agg.RecentSpans[src] = append(agg.RecentSpans[src], sp)
			}
		}
	}
	agg.UniqueFiles = len(fileSet)
	agg.LinesNet = agg.LinesAdded - agg.LinesRemoved

	// Streaks, best day, days since first
	days := make([]string, 0, len(agg.ByDay))
	for k := range agg.ByDay {
		days = append(days, k)
	}
	sort.Strings(days)
	if len(days) > 0 {
		var best *BestDay
		for _, k := range days {
			d := agg.ByDay[k]
			if best == nil || d.ActiveMs > best.ActiveMs {
				best = &BestDay{Day: k, DayBucket: *d}
			}
		}
		agg.BestDay = best

		firstT, _ := time.ParseInLocation("2006-01-02", days[0], time.Local)
		now := time.Now()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		agg.DaysSinceFirst = int(today.Sub(firstT).Hours()/24) + 1

		offset := 0
		if agg.ByDay[DayKeyAt(today, 0)] == nil {
			offset = 1
		}
		for agg.ByDay[DayKeyAt(today, offset)] != nil {
			agg.Streak++
			offset++
		}

		longest, run := 0, 0
		var prevT time.Time
		for i, k := range days {
			curT, _ := time.ParseInLocation("2006-01-02", k, time.Local)
			if i > 0 && int(curT.Sub(prevT).Hours()/24) == 1 {
				run++
			} else {
				run = 1
			}
			if run > longest {
				longest = run
			}
			prevT = curT
		}
		agg.LongestStreak = longest
	}

	// Peak hour
	for hStr, d := range agg.ByHour {
		var h int
		_, _ = fmt.Sscanf(hStr, "%d", &h)
		if agg.PeakHour == nil || d.ActiveMs > agg.PeakHour.ActiveMs {
			agg.PeakHour = &PeakHour{Hour: h, DayBucket: *d}
		}
	}

	// Top edited files
	nowMs := time.Now().UnixMilli()
	hotspots := make([]HotspotFile, 0, len(agg.FileEdits))
	for fp, count := range agg.FileEdits {
		lastTs := agg.FileEditTs[fp]
		var ago *int
		if lastTs > 0 {
			v := int((nowMs - lastTs) / 86_400_000)
			ago = &v
		}
		hotspots = append(hotspots, HotspotFile{Path: fp, Count: count, LastEditedTs: lastTs, DaysSinceLastEdit: ago})
	}
	sort.Slice(hotspots, func(i, j int) bool { return hotspots[i].Count > hotspots[j].Count })
	if len(hotspots) > 25 {
		hotspots = hotspots[:25]
	}
	agg.TopEditedFiles = hotspots

	// Model split
	var splitCost, splitTok float64
	for _, v := range agg.ByModel {
		splitCost += v.Cost
		splitTok += float64(v.Tokens)
	}
	for m, v := range agg.ByModel {
		e := ModelSplitEntry{Model: m, Turns: v.Turns, Tokens: v.Tokens, Cost: v.Cost}
		if splitCost > 0 {
			e.CostPct = v.Cost / splitCost
		}
		if splitTok > 0 {
			e.TokenPct = float64(v.Tokens) / splitTok
		}
		agg.ModelSplit = append(agg.ModelSplit, e)
	}
	sort.Slice(agg.ModelSplit, func(i, j int) bool { return agg.ModelSplit[i].Cost > agg.ModelSplit[j].Cost })

	// Languages
	for fp, count := range agg.FileEdits {
		lang := languages.Of(fp)
		if lang == "" {
			continue
		}
		b := agg.Languages[lang]
		if b == nil {
			b = &LangStats{}
			agg.Languages[lang] = b
		}
		b.Files++
		b.Edits += count
	}

	// MCP vs built-in
	for name, count := range agg.ToolBreakdown {
		if strings.HasPrefix(name, "mcp__") || name == "call_mcp_tool" {
			agg.MCPToolCalls += count
		} else {
			agg.BuiltinToolCalls += count
		}
	}

	// Weekday fold
	for k, d := range agg.ByDay {
		t, err := time.ParseInLocation("2006-01-02", k, time.Local)
		if err != nil {
			continue
		}
		mergeDay(ensureDay(agg.ByWeekday, fmt.Sprintf("%d", int(t.Weekday()))), d)
	}

	// Events log (notifications & compactions)
	notifs, compacts := readEventsByDay()
	for k, n := range notifs {
		ensureDay(agg.ByDay, k).Notifications += n
		agg.Notifications += n
	}
	for k, n := range compacts {
		agg.CompactionsByDay[k] = n
		agg.Compactions += n
	}

	return agg
}

func readCache() *ScanCache {
	b, err := os.ReadFile(paths.ScanCachePath())
	if err != nil {
		return &ScanCache{Version: CacheVersion, Files: map[string]*TranscriptSummary{}}
	}
	var c ScanCache
	if err := json.Unmarshal(b, &c); err != nil || c.Files == nil {
		return &ScanCache{Version: CacheVersion, Files: map[string]*TranscriptSummary{}}
	}
	if c.Version != CacheVersion {
		kept := map[string]*TranscriptSummary{}
		for k, v := range c.Files {
			if v != nil && v.Gone {
				kept[k] = v
			}
		}
		return &ScanCache{Version: CacheVersion, Files: kept}
	}
	return &c
}

func writeCache(c *ScanCache) {
	c.Version = CacheVersion
	if b, err := json.Marshal(c); err == nil {
		_ = paths.WriteFileAtomic(paths.ScanCachePath(), b, 0o644)
	}
}

func writeAggregate(agg *Aggregate) {
	if b, err := json.MarshalIndent(agg, "", "  "); err == nil {
		_ = paths.WriteFileAtomic(paths.AggregatePath(), b, 0o644)
	}
}

func ReadAggregate() *Aggregate {
	b, err := os.ReadFile(paths.AggregatePath())
	if err != nil {
		return nil
	}
	var agg Aggregate
	if err := json.Unmarshal(b, &agg); err != nil {
		return nil
	}
	return &agg
}

type ScanOptions struct {
	Force     bool
	ExtraDirs []string
}

type ScanResult struct {
	Aggregate *Aggregate
	Scanned   int
	Skipped   int
	Removed   int
	Total     int
}

func Scan(opts ScanOptions) ScanResult {
	cfg := config.Load()
	var dirs []string
	if cfg.Sources.WantAntigravity() {
		dirs = append(dirs, DiscoverBrainDirs()...)
	}
	if cfg.Sources.WantClaude() {
		dirs = append(dirs, DiscoverClaudeProjectDirs()...)
	}
	if cfg.Sources.WantCodex() {
		for _, d := range CodexDirs() {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				dirs = append(dirs, d)
			}
		}
	}
	dirs = append(dirs, opts.ExtraDirs...)

	seen := map[string]bool{}
	var transcripts []string
	for _, d := range dirs {
		for _, fp := range listTranscripts(d) {
			if !seen[fp] {
				seen[fp] = true
				transcripts = append(transcripts, fp)
			}
		}
	}

	cache := readCache()
	cwds := state.ReadSessionCwds("")
	scanned, skipped, removed := 0, 0, 0
	total := len(transcripts)
	if cfg.Sources.WantOpenCode() {
		ocSeen, n, ok := scanOpenCode(cache, opts.Force)
		scanned += n
		total += len(ocSeen)
		for k := range ocSeen {
			seen[k] = true
		}
		if !ok {
			// OpenCode missing or its query failed: keep its history as-is.
			for k := range cache.Files {
				if strings.HasPrefix(k, openCodeKeyPrefix) {
					seen[k] = true
				}
			}
		}
	}

	for _, fp := range transcripts {
		st, err := os.Stat(fp)
		if err != nil {
			continue
		}
		sig := fmt.Sprintf("%d:%d", st.ModTime().UnixMilli(), st.Size())
		if !opts.Force && cache.Files[fp] != nil && cache.Files[fp].Sig == sig && !cache.Files[fp].Gone {
			sid := cache.Files[fp].SessionID
			if cache.Files[fp].Cwd == "" && sid != "" && cwds[sid] != "" {
				cache.Files[fp].Cwd = cwds[sid]
				cache.Files[fp].Project = CleanProjectName(filepath.Base(cwds[sid]))
				scanned++
			} else {
				skipped++
			}
			continue
		}
		var sum *TranscriptSummary
		if isCodexRollout(fp) {
			sum, err = ParseCodexRollout(fp)
		} else {
			sum, err = ParseTranscript(fp, cwds)
		}
		if err != nil {
			continue
		}
		sum.Sig = sig
		sum.IsSubagent = sum.IsSubagent || strings.Contains(strings.ReplaceAll(fp, "\\", "/"), "/subagents/")
		cache.Files[fp] = sum
		scanned++
	}

	for key, entry := range cache.Files {
		if seen[key] || entry == nil || entry.Gone {
			continue
		}
		entry.Gone = true
		removed++
	}

	if scanned > 0 || removed > 0 {
		writeCache(cache)
	} else {
		if existing := ReadAggregate(); existing != nil && existing.Version == CacheVersion && DayKey(existing.GeneratedAt) == DayKey(time.Now().UnixMilli()) {
			return ScanResult{Aggregate: existing, Scanned: scanned, Skipped: skipped, Removed: removed, Total: total}
		}
	}

	agg := AggregateFrom(cache)
	writeAggregate(agg)
	return ScanResult{Aggregate: agg, Scanned: scanned, Skipped: skipped, Removed: removed, Total: total}
}

func addSourceDays(days map[string]*SourceDay, s *TranscriptSummary, isSub bool) {
	get := func(k string) *SourceDay {
		d := days[k]
		if d == nil {
			d = &SourceDay{}
			days[k] = d
		}
		return d
	}
	for k, b := range s.ByDay {
		if b == nil {
			continue
		}
		d := get(k)
		d.Tokens += b.InputTokens + b.OutputTokens + b.CacheReadTokens + b.CacheWriteTokens
		d.Cost += b.Cost
		if !isSub {
			d.ActiveMs += b.ActiveMs
			d.Prompts += b.UserMessages
		}
	}
	if s.FirstTs <= 0 {
		return
	}
	d := get(DayKey(s.FirstTs))
	if !isSub {
		d.Sessions++
	}
	if len(s.ToolBreakdown) > 0 && d.Tools == nil {
		d.Tools = map[string]int{}
	}
	for name, n := range s.ToolBreakdown {
		d.Tools[name] += n
	}
}

// mergeSpans sorts and unions overlapping [start,end) intervals.
func mergeSpans(in [][2]int64) [][2]int64 {
	if len(in) == 0 {
		return nil
	}
	sp := make([][2]int64, len(in))
	copy(sp, in)
	sort.Slice(sp, func(i, j int) bool { return sp[i][0] < sp[j][0] })
	out := [][2]int64{sp[0]}
	for _, x := range sp[1:] {
		last := &out[len(out)-1]
		if x[0] <= last[1] {
			if x[1] > last[1] {
				last[1] = x[1]
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// overlapByDay returns the total intersection of two merged span lists and
// adds each overlapping stretch to byDay, split at local midnight.
func overlapByDay(a, b [][2]int64, byDay map[string]int64) int64 {
	var total int64
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		lo, hi := max(a[i][0], b[j][0]), min(a[i][1], b[j][1])
		if hi > lo {
			total += hi - lo
			for t := lo; t < hi; {
				d := time.UnixMilli(t).Local()
				next := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, time.Local).UnixMilli()
				end := min(next, hi)
				if byDay != nil {
					byDay[DayKey(t)] += end - t
				}
				t = end
			}
		}
		if a[i][1] < b[j][1] {
			i++
		} else {
			j++
		}
	}
	return total
}

// ActiveWithin sums the parts of spans that fall inside [from, to) (unix ms).
func ActiveWithin(spans [][2]int64, from, to int64) int64 {
	var total int64
	for _, sp := range spans {
		lo, hi := max(sp[0], from), min(sp[1], to)
		if hi > lo {
			total += hi - lo
		}
	}
	return total
}

// SourceKey normalizes a session's source to an aggregate bucket key.
func SourceKey(source string) string {
	switch source {
	case "claude", "opencode", "codex":
		return source
	}
	return "antigravity"
}

// multiAgentByDay returns the total time when at least two agents' active
// spans overlap, adding it to byDay split at local midnight.
func multiAgentByDay(spansBySource map[string][][2]int64, byDay map[string]int64) int64 {
	type edge struct {
		t     int64
		delta int
	}
	var edges []edge
	for _, spans := range spansBySource {
		for _, sp := range mergeSpans(spans) { // merge within an agent first
			edges = append(edges, edge{sp[0], 1}, edge{sp[1], -1})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].t != edges[j].t {
			return edges[i].t < edges[j].t
		}
		return edges[i].delta < edges[j].delta // close before open at the same instant
	})
	var total int64
	active := 0
	var from int64
	for _, e := range edges {
		if active >= 2 && e.t > from {
			total += addSplitByDay(from, e.t, byDay)
		}
		active += e.delta
		from = e.t
	}
	return total
}

// addSplitByDay adds [lo, hi) to byDay split at local midnight; returns hi-lo.
func addSplitByDay(lo, hi int64, byDay map[string]int64) int64 {
	for t := lo; t < hi; {
		d := time.UnixMilli(t).Local()
		next := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, time.Local).UnixMilli()
		end := min(next, hi)
		if byDay != nil {
			byDay[DayKey(t)] += end - t
		}
		t = end
	}
	return hi - lo
}
