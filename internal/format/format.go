package format

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/gitinfo"
	"github.com/chauveaul/vibe-rpc/internal/languages"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

var (
	claudeModelRe = regexp.MustCompile(`(?i)(opus|sonnet|haiku|fable|mythos)[^\d]*(\d{1,2})(?:[-.](\d{1,2}))?`)
	geminiModelRe = regexp.MustCompile(`(?i)^gemini[-_ ]?(?:(\d{1,2})(?:[.-](\d{1,2}))?[-_ ]?)?(.*)$`)
	tplVarRe      = regexp.MustCompile(`\{(\w+)\}`)
	dateDigitsRe  = regexp.MustCompile(`^\d{4,}$`)
)

var agyToolNames = map[string]string{
	"read_file": "Read", "view_file": "Read", "view_file_outline": "Outline", "view_code_item": "Inspect",
	"edit_file": "Edit", "replace_file_content": "Edit", "multi_replace_file_content": "Edit",
	"write_file": "Write", "write_to_file": "Write",
	"run_command": "Shell", "command_status": "Shell", "send_command_input": "Shell",
	"search_web": "WebSearch", "read_url_content": "WebFetch",
	"grep_search": "Grep", "find_by_name": "Glob", "list_dir": "ListDir",
	"invoke_subagent": "Subagent", "define_subagent": "Subagent", "manage_subagents": "Subagents",
	"send_message": "Message", "ask_question": "AskUser", "run_workflow": "Workflow",
	"update_steps": "Plan", "schedule": "Schedule", "manage_task": "Task",
	"call_mcp_tool": "MCP", "list_resources": "MCP", "read_resource": "MCP", "swarm": "Swarm",
}

func FmtNum(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := ""
	v := float64(n)
	if v < 0 {
		neg = "-"
		v = -v
	}
	if v < 1000 {
		return fmt.Sprintf("%s%.0f", neg, math.Round(v))
	}
	tiers := []struct {
		suf  string
		div  float64
		prec int
	}{{"k", 1e3, 1}, {"M", 1e6, 2}, {"B", 1e9, 2}, {"T", 1e12, 2}}
	for _, t := range tiers {
		s := fmt.Sprintf("%.*f", t.prec, v/t.div)
		var parsed float64
		_, _ = fmt.Sscanf(s, "%f", &parsed)
		if parsed < 1000 {
			return neg + s + t.suf
		}
	}
	return fmt.Sprintf("%s%.2fT", neg, v/1e12)
}

func FmtHours(ms int64) string {
	if ms <= 0 {
		return "0h"
	}
	mins := math.Round(float64(ms) / 60_000.0)
	if mins < 60 {
		return fmt.Sprintf("%.0fm", mins)
	}
	hours := mins / 60.0
	if hours < 10 {
		return fmt.Sprintf("%.1fh", hours)
	}
	return fmt.Sprintf("%.0fh", math.Round(hours))
}

func FmtDuration(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	s := ms / 1000
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

func fmtToolElapsed(ms int64) string {
	if ms <= 0 {
		return ""
	}
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	mins := float64(sec) / 60.0
	if mins < 10 {
		return fmt.Sprintf("%.1fmin", mins)
	}
	if sec < 3600 {
		return fmt.Sprintf("%.0fmin", math.Round(mins))
	}
	return fmt.Sprintf("%dh %dm", sec/3600, (sec%3600)/60)
}

func fmtLinesNet(n int) string {
	if n == 0 {
		return "0"
	}
	if n > 0 {
		return "+" + FmtNum(int64(n))
	}
	return "−" + FmtNum(int64(-n))
}

func plural(n int, sing, plur string) string {
	w := plur
	if w == "" {
		w = sing + "s"
	}
	if n == 1 {
		w = sing
	}
	return fmt.Sprintf("%s %s", FmtNum(int64(n)), w)
}

func titleWord(w string) string {
	if w == "" {
		return ""
	}
	return strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
}

func HumanModel(id string) string {
	clean := strings.TrimSpace(id)
	if clean == "" || strings.EqualFold(clean, "auto") {
		return "Antigravity"
	}
	if m := claudeModelRe.FindStringSubmatch(clean); len(m) >= 3 {
		name := titleWord(m[1])
		if len(m) >= 4 && m[3] != "" {
			return fmt.Sprintf("%s %s.%s", name, m[2], m[3])
		}
		return fmt.Sprintf("%s %s", name, m[2])
	}
	low := strings.ToLower(clean)
	switch low {
	case "fable", "mythos", "opus", "sonnet", "haiku":
		return titleWord(low)
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "flash_lite", "flash-lite":
		return "Gemini Flash Lite"
	case "flash":
		return "Gemini Flash"
	case "pro":
		return "Gemini Pro"
	case "neon":
		return "Gemini Neon"
	}
	if m := geminiModelRe.FindStringSubmatch(clean); len(m) >= 4 {
		ver := m[1]
		if ver != "" && m[2] != "" {
			ver += "." + m[2]
		}
		rest := strings.FieldsFunc(m[3], func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
		var words []string
		effort := ""
		for _, p := range rest {
			lp := strings.ToLower(p)
			if dateDigitsRe.MatchString(lp) || lp == "preview" || lp == "exp" || lp == "latest" {
				continue
			}
			if lp == "high" || lp == "medium" || lp == "low" || lp == "minimal" {
				effort = titleWord(lp)
				continue
			}
			words = append(words, titleWord(lp))
		}
		parts := []string{"Gemini"}
		if ver != "" {
			parts = append(parts, ver)
		}
		parts = append(parts, words...)
		base := strings.Join(parts, " ")
		if effort != "" {
			return fmt.Sprintf("%s (%s)", base, effort)
		}
		return base
	}
	parts := strings.FieldsFunc(clean, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		parts[i] = titleWord(p)
	}
	return strings.Join(parts, " ")
}

func HumanTool(name string) string {
	if name == "" {
		return ""
	}
	if pretty, ok := agyToolNames[name]; ok {
		return pretty
	}
	if strings.HasPrefix(name, "mcp__") {
		var parts []string
		for _, p := range strings.Split(name, "__") {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) >= 3 {
			srv := strings.TrimPrefix(parts[len(parts)-2], "claude_ai_")
			return srv + ":" + parts[len(parts)-1]
		}
		if len(parts) > 1 {
			return strings.Join(parts[1:], ":")
		}
	}
	if strings.HasPrefix(name, "browser_") {
		return "Browser:" + name[len("browser_"):]
	}
	if strings.Contains(name, "_") {
		parts := strings.Split(name, "_")
		for i, p := range parts {
			parts[i] = titleWord(p)
		}
		return strings.Join(parts, "")
	}
	return name
}

func HumanProject(slugOrPath string) string {
	if slugOrPath == "" {
		return ""
	}
	if strings.ContainsAny(slugOrPath, `/\`) {
		return scanner.CleanProjectName(filepath.Base(slugOrPath))
	}
	if strings.HasPrefix(slugOrPath, "-Users-") || strings.HasPrefix(slugOrPath, "-home-") || strings.HasPrefix(slugOrPath, "-tmp-") {
		parts := strings.Split(slugOrPath, "-")
		if len(parts) > 0 {
			return scanner.CleanProjectName(parts[len(parts)-1])
		}
	}
	return scanner.CleanProjectName(slugOrPath)
}

func StatusVerbose(status, toolPretty string, idleMs int64) string {
	switch status {
	case "working":
		if toolPretty != "" {
			return "Using " + toolPretty
		}
		return "Working"
	case "thinking":
		return "Thinking"
	case "compacting":
		return "Compacting context"
	case "dual":
		return "Dual wielding"
	case "notification":
		return "Waiting on you"
	case "idle":
		if idleMs > 60_000 {
			mins := idleMs / 60_000
			if mins < 60 {
				return fmt.Sprintf("Idle · %dm", mins)
			}
			return fmt.Sprintf("Idle · %dh", mins/60)
		}
		return "Standing by"
	case "stale":
		return "Away"
	default:
		if status != "" {
			return status
		}
		return "Active"
	}
}

func looksLikeUsernameLeak(cwd string) bool {
	if cwd == "" {
		return false
	}
	norm := func(p string) string {
		return strings.ToLower(strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/"))
	}
	cwdN := norm(cwd)
	for _, k := range []string{"HOME", "USERPROFILE"} {
		if h := os.Getenv(k); h != "" && cwdN == norm(h) {
			return true
		}
	}
	parts := strings.Split(cwdN, "/")
	base := parts[len(parts)-1]
	for _, k := range []string{"USER", "USERNAME"} {
		if u := os.Getenv(k); u != "" && base == strings.ToLower(u) {
			return true
		}
	}
	return false
}

func prettyFilePath(p string) string {
	if p == "" {
		return ""
	}
	norm := strings.ReplaceAll(p, "\\", "/")
	var parts []string
	for _, seg := range strings.Split(norm, "/") {
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	if len(parts) <= 3 {
		return strings.Join(parts, "/")
	}
	return strings.Join(parts[len(parts)-3:], "/")
}

func BuildVars(st state.SessionState, cfg config.Config, agg *scanner.Aggregate) map[string]any {
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	now := time.Now()
	nowMs := now.UnixMilli()

	sessionReal := st.Tokens.Input + st.Tokens.Output
	sessionCache := st.Tokens.CacheRead + st.Tokens.CacheWrite
	sessionTokens := sessionReal + sessionCache
	var duration int64
	if st.SessionStart > 0 {
		duration = nowMs - st.SessionStart
	}

	appName := cfg.AppName
	if appName == "" {
		appName = "Antigravity"
	}
	safeProject := func(cwd string) string {
		if looksLikeUsernameLeak(cwd) {
			return appName
		}
		if hp := HumanProject(cwd); hp != "" {
			return hp
		}
		return appName
	}
	safeCwd := st.Cwd
	if looksLikeUsernameLeak(st.Cwd) {
		safeCwd = ""
	}
	projectPretty := safeProject(st.Cwd)
	toolPretty := HumanTool(st.CurrentTool)
	modelPretty := HumanModel(st.Model)

	allReal := agg.InputTokens + agg.OutputTokens
	allCache := agg.CacheReadTokens + agg.CacheWriteTokens
	allTotal := allReal + allCache

	todayB := &scanner.DayBucket{}
	if agg.ByDay != nil && agg.ByDay[scanner.DayKey(nowMs)] != nil {
		todayB = agg.ByDay[scanner.DayKey(nowMs)]
	}
	todayTokens := todayB.InputTokens + todayB.OutputTokens + todayB.CacheReadTokens + todayB.CacheWriteTokens

	weekB := &scanner.DayBucket{}
	if agg.ByWeek != nil && agg.ByWeek[scanner.WeekKey(nowMs)] != nil {
		weekB = agg.ByWeek[scanner.WeekKey(nowMs)]
	}
	weekTokens := weekB.InputTokens + weekB.OutputTokens + weekB.CacheReadTokens + weekB.CacheWriteTokens

	// Top language
	type kvLang struct {
		name  string
		edits int
	}
	var langList []kvLang
	for k, v := range agg.Languages {
		if v != nil {
			langList = append(langList, kvLang{k, v.Edits})
		}
	}
	sort.Slice(langList, func(i, j int) bool { return langList[i].edits > langList[j].edits })
	topLang, topLangEdits := "", 0
	if len(langList) > 0 {
		topLang = langList[0].name
		topLangEdits = langList[0].edits
	}

	// Model split label
	var msParts []string
	for i, m := range agg.ModelSplit {
		if i >= 3 || m.CostPct <= 0 {
			break
		}
		msParts = append(msParts, fmt.Sprintf("%s %.0f%%", HumanModel(m.Model), math.Round(m.CostPct*100)))
	}
	modelSplitLabel := strings.Join(msParts, " · ")

	// Hotspot
	topFile, topFileCount, topFileAgeLabel := "", 0, ""
	if len(agg.TopEditedFiles) > 0 {
		hs := agg.TopEditedFiles[0]
		topFile = filepath.Base(hs.Path)
		topFileCount = hs.Count
		if hs.DaysSinceLastEdit != nil {
			switch *hs.DaysSinceLastEdit {
			case 0:
				topFileAgeLabel = "edited today"
			case 1:
				topFileAgeLabel = "edited yesterday"
			default:
				topFileAgeLabel = fmt.Sprintf("%dd since last edit", *hs.DaysSinceLastEdit)
			}
		}
	}

	// Streak milestone
	streakMilestone := 0
	if agg.Streak > 0 && (agg.Streak%7 == 0 || agg.Streak == 30 || agg.Streak == 60 || agg.Streak == 100 || agg.Streak == 365) {
		streakMilestone = 1
	}

	// Session duration milestones (1h, 2h, 3h, 5h, 8h, 12h)
	sessionMilestoneHit := 0
	sessionMilestoneLabel := ""
	if st.SessionStart > 0 && st.Status != "stale" && duration > 0 {
		for _, h := range []int64{1, 2, 3, 5, 8, 12} {
			t := h * 3_600_000
			if duration >= t && duration-t < 5*60_000 {
				sessionMilestoneHit = 1
				sessionMilestoneLabel = fmt.Sprintf("%d-hour session", h)
			}
		}
	}

	// Idle & tool elapsed
	var idleMs int64
	if st.Status == "idle" && st.LastActivity > 0 && nowMs > st.LastActivity {
		idleMs = nowMs - st.LastActivity
	}
	var toolMs int64
	toolElapsed := ""
	if st.Status == "working" && st.ToolStartedAt > 0 && nowMs > st.ToolStartedAt {
		toolMs = nowMs - st.ToolStartedAt
		if toolMs >= 5_000 {
			toolElapsed = fmtToolElapsed(toolMs)
		}
	}

	// Goals & budget
	goalLabel, goalHit := "", 0
	if cfg.Goals.DailyHours > 0 {
		pct := int(math.Min(999, math.Round((float64(todayB.ActiveMs)/3_600_000.0/cfg.Goals.DailyHours)*100)))
		goalLabel = fmt.Sprintf("%s / %.4gh · %d%%", FmtHours(todayB.ActiveMs), cfg.Goals.DailyHours, pct)
		if pct >= 100 {
			goalHit = 1
		}
	} else if cfg.Goals.DailyPrompts > 0 {
		pct := int(math.Min(999, math.Round((float64(todayB.UserMessages)/float64(cfg.Goals.DailyPrompts))*100)))
		goalLabel = fmt.Sprintf("%d / %d prompts · %d%%", todayB.UserMessages, cfg.Goals.DailyPrompts, pct)
		if pct >= 100 {
			goalHit = 1
		}
	} else if cfg.Goals.WeeklyHours > 0 {
		pct := int(math.Min(999, math.Round((float64(weekB.ActiveMs)/3_600_000.0/cfg.Goals.WeeklyHours)*100)))
		goalLabel = fmt.Sprintf("%s / %.4gh this week · %d%%", FmtHours(weekB.ActiveMs), cfg.Goals.WeeklyHours, pct)
		if pct >= 100 {
			goalHit = 1
		}
	}

	budgetLabel := ""
	if cfg.Budget.Monthly > 0 {
		monthPfx := scanner.DayKey(nowMs)[:7]
		var mtd float64
		for k, d := range agg.ByDay {
			if strings.HasPrefix(k, monthPfx) && d != nil {
				mtd += d.Cost
			}
		}
		pct := int(math.Round((mtd / cfg.Budget.Monthly) * 100))
		budgetLabel = fmt.Sprintf("%s / %s · %d%%", pricing.FmtCost(mtd), pricing.FmtCost(cfg.Budget.Monthly), pct)
	}

	// Just-shipped label
	justShippedLabel := ""
	switch st.JustShippedKind {
	case "push":
		if st.JustShippedBranch != "" {
			justShippedLabel = "Pushed to " + st.JustShippedBranch
		} else {
			justShippedLabel = "Pushed"
		}
	case "commit":
		if st.JustShippedBranch != "" {
			justShippedLabel = "Committed on " + st.JustShippedBranch
		} else {
			justShippedLabel = "Committed"
		}
	case "pr":
		justShippedLabel = "Opened a pull request"
	case "issue":
		justShippedLabel = "Opened an issue"
	case "tag":
		if st.JustShippedBranch != "" {
			justShippedLabel = "Tagged " + st.JustShippedBranch
		} else {
			justShippedLabel = "Tagged a release"
		}
	}

	// Source breakdown
	source := "antigravity"
	sourcePretty := "Antigravity"
	switch st.Source {
	case "claude":
		source = "claude"
		sourcePretty = "Claude Code"
	case "codex":
		source = "codex"
		sourcePretty = "Codex"
	case "opencode":
		source = "opencode"
		sourcePretty = "OpenCode"
	}
	liveSrcs := map[string]bool{}
	if st.AntigravityActive {
		liveSrcs["antigravity"] = true
	}
	if st.ClaudeActive {
		liveSrcs["claude"] = true
	}
	for _, ls := range st.LiveSessions {
		switch ls.Source {
		case "claude", "opencode", "codex":
			liveSrcs[ls.Source] = true
		default:
			liveSrcs["antigravity"] = true
		}
	}
	sourcesActiveLabel := sourcePretty
	switch {
	case len(liveSrcs) == 2 && liveSrcs["antigravity"] && liveSrcs["claude"]:
		sourcesActiveLabel = "Antigravity & Claude"
	case len(liveSrcs) > 1:
		var names []string
		for _, a := range config.Agents {
			if liveSrcs[a] {
				names = append(names, config.AgentName(a))
			}
		}
		sourcesActiveLabel = strings.Join(names, " & ")
	case len(liveSrcs) == 1:
		for a := range liveSrcs {
			sourcesActiveLabel = config.AgentName(a)
		}
	}

	agySrc := agg.BySource["antigravity"]
	if agySrc == nil {
		agySrc = &scanner.SourceStats{}
	}
	ccSrc := agg.BySource["claude"]
	if ccSrc == nil {
		ccSrc = &scanner.SourceStats{}
	}

	allCachePct := 0
	allCachePctLabel := ""
	if allTotal > 0 {
		allCachePct = int(math.Round(float64(allCache) / float64(allTotal) * 100))
		allCachePctLabel = fmt.Sprintf("%d%% from cache", allCachePct)
	}

	tokensLabel := ""
	if sessionTokens > 0 {
		tokensLabel = FmtNum(sessionTokens) + " tokens"
	}

	fileLang := languages.Of(st.CurrentFile)
	statusIcon := cfg.StatusIcons[st.Status]
	if statusIcon == "" {
		statusIcon = st.Status
	}

	streakLabel := "no streak"
	if agg.Streak > 0 {
		streakLabel = fmt.Sprintf("%d-day streak", agg.Streak)
	}
	daysSinceFirstLabel := ""
	if agg.DaysSinceFirst > 0 {
		daysSinceFirstLabel = fmt.Sprintf("Day %d", agg.DaysSinceFirst)
	}

	compactTriggerLabel := "context squeeze"
	if st.CompactTrigger == "manual" {
		compactTriggerLabel = "manual compaction"
	} else if st.CompactTrigger == "auto" {
		compactTriggerLabel = "auto-compaction"
	}

	// Dual-wielding per-agent variables (Antigravity + Claude Code)
	agyProj, agyModel, agyFile, agyTool := "", "", "", ""
	ccProj, ccModel, ccFile, ccTool := "", "", "", ""
	if st.AgySession != nil {
		if st.AgySession.Cwd != "" {
			agyProj = safeProject(st.AgySession.Cwd)
		}
		if st.AgySession.Model != "" {
			agyModel = HumanModel(st.AgySession.Model)
		}
		agyFile = prettyFilePath(st.AgySession.CurrentFile)
		agyTool = HumanTool(st.AgySession.CurrentTool)
	} else if st.Source == "antigravity" || st.Source == "" {
		agyProj = projectPretty
		agyModel = modelPretty
		agyFile = prettyFilePath(st.CurrentFile)
		agyTool = toolPretty
	}
	if st.ClaudeSession != nil {
		if st.ClaudeSession.Cwd != "" {
			ccProj = safeProject(st.ClaudeSession.Cwd)
		}
		if st.ClaudeSession.Model != "" {
			ccModel = HumanModel(st.ClaudeSession.Model)
		}
		ccFile = prettyFilePath(st.ClaudeSession.CurrentFile)
		ccTool = HumanTool(st.ClaudeSession.CurrentTool)
	} else if st.Source == "claude" {
		ccProj = projectPretty
		ccModel = modelPretty
		ccFile = prettyFilePath(st.CurrentFile)
		ccTool = toolPretty
	}
	if agyModel == "" {
		agyModel = "Antigravity"
	}
	if ccModel == "" {
		ccModel = "Claude"
	}
	dualModels := agyModel + " + " + ccModel

	joinPair := func(a, b string) string {
		if a != "" && b != "" {
			if strings.EqualFold(a, b) {
				return a
			}
			return a + " & " + b
		}
		if a != "" {
			return a
		}
		return b
	}
	dualProjects := joinPair(agyProj, ccProj)
	if dualProjects == "" {
		dualProjects = projectPretty
	}
	dualFiles := joinPair(agyFile, ccFile)
	// Smart combined target: prefers two files, falls back to file + project, or two projects
	tA := agyFile
	if tA == "" {
		tA = agyProj
	}
	tB := ccFile
	if tB == "" {
		tB = ccProj
	}
	dualTargets := joinPair(tA, tB)
	if dualTargets == "" {
		dualTargets = projectPretty
	}

	return map[string]any{
		"source":                source,
		"sourcePretty":          sourcePretty,
		"sourcesActiveLabel":    sourcesActiveLabel,
		"agyProject":            agyProj,
		"claudeProject":         ccProj,
		"dualProjects":          dualProjects,
		"agyFile":               agyFile,
		"claudeFile":            ccFile,
		"dualFiles":             dualFiles,
		"dualTargets":           dualTargets,
		"agyModel":              agyModel,
		"claudeModel":           ccModel,
		"dualModels":            dualModels,
		"agyTool":               agyTool,
		"claudeTool":            ccTool,
		"agySessions":           agySrc.Sessions,
		"claudeSessions":        ccSrc.Sessions,
		"agyHours":              FmtHours(agySrc.ActiveMs),
		"claudeHours":           FmtHours(ccSrc.ActiveMs),
		"status":                st.Status,
		"statusVerbose":         StatusVerbose(st.Status, toolPretty, idleMs),
		"statusIcon":            statusIcon,
		"project":               projectPretty,
		"projectPretty":         projectPretty,
		"cwd":                   safeCwd,
		"model":                 st.Model,
		"modelPretty":           modelPretty,
		"appName":               appName,
		"messages":              st.Messages,
		"tools":                 st.Tools,
		"filesOpened":           len(st.FilesOpened),
		"filesEdited":           len(st.FilesEdited),
		"filesRead":             len(st.FilesRead),
		"tokens":                sessionTokens,
		"tokensFmt":             FmtNum(sessionTokens),
		"tokensLabel":           tokensLabel,
		"messagesLabel":         plural(st.Messages, "prompt", "prompts"),
		"toolsLabel":            plural(st.Tools, "tool call", "tool calls"),
		"filesEditedLabel":      plural(len(st.FilesEdited), "edit", "edits"),
		"filesReadLabel":        plural(len(st.FilesRead), "file read", "file reads"),
		"duration":              FmtDuration(duration),
		"durationHours":         FmtHours(duration),
		"currentTool":           st.CurrentTool,
		"currentToolPretty":     toolPretty,
		"currentFile":           st.CurrentFile,
		"currentFilePretty":     prettyFilePath(st.CurrentFile),
		"fileName":              filepath.Base(st.CurrentFile),
		"fileExt":               strings.ToLower(filepath.Ext(st.CurrentFile)),
		"fileLang":              fileLang,
		"fileLangUpper":         strings.ToUpper(fileLang),
		"gitBranch":             gitinfo.Branch(st.Cwd),
		"gitRepo":               gitinfo.Repo(st.Cwd),
		"toolMs":                toolMs,
		"toolElapsed":           toolElapsed,
		"justShippedKind":       st.JustShippedKind,
		"justShippedLabel":      justShippedLabel,
		"lastCommit":            st.JustShippedSubject,
		"compactTriggerLabel":   compactTriggerLabel,
		"concurrent":            len(st.LiveSessions),
		"allTokens":             allTotal,
		"allTokensFmt":          FmtNum(allTotal),
		"allFreshTokens":        allReal,
		"allFreshTokensFmt":     FmtNum(allReal),
		"allCachePct":           allCachePct,
		"allCachePctLabel":      allCachePctLabel,
		"modelSplitLabel":       modelSplitLabel,
		"allHours":              FmtHours(agg.ActiveMs),
		"allWallHours":          FmtHours(agg.WallMs),
		"allMessages":           agg.UserMessages,
		"allMessagesFmt":        FmtNum(int64(agg.UserMessages)),
		"allTools":              agg.ToolCalls,
		"allToolsFmt":           FmtNum(int64(agg.ToolCalls)),
		"allSessions":           agg.Sessions,
		"allSessionsLabel":      plural(agg.Sessions, "session", "sessions"),
		"allFiles":              agg.UniqueFiles,
		"allFilesFmt":           FmtNum(int64(agg.UniqueFiles)),
		"todayActiveMs":         todayB.ActiveMs,
		"todayHours":            FmtHours(todayB.ActiveMs),
		"todayPrompts":          todayB.UserMessages,
		"todayPromptsLabel":     plural(todayB.UserMessages, "prompt", "prompts"),
		"todayTokens":           todayTokens,
		"todayTokensFmt":        FmtNum(todayTokens),
		"todayCost":             todayB.Cost,
		"todayCostFmt":          pricing.FmtCost(todayB.Cost),
		"weekActiveMs":          weekB.ActiveMs,
		"weekHours":             FmtHours(weekB.ActiveMs),
		"weekPrompts":           weekB.UserMessages,
		"weekPromptsLabel":      plural(weekB.UserMessages, "prompt", "prompts"),
		"weekTokens":            weekTokens,
		"weekTokensFmt":         FmtNum(weekTokens),
		"weekCost":              weekB.Cost,
		"weekCostFmt":           pricing.FmtCost(weekB.Cost),
		"allCost":               agg.EstimatedCost,
		"allCostFmt":            pricing.FmtCost(agg.EstimatedCost),
		"streak":                agg.Streak,
		"streakLabel":           streakLabel,
		"longestStreak":         agg.LongestStreak,
		"daysSinceFirst":        agg.DaysSinceFirst,
		"daysSinceFirstLabel":   daysSinceFirstLabel,
		"streakIsMilestone":     streakMilestone,
		"sessionMilestoneHit":   sessionMilestoneHit,
		"sessionMilestoneLabel": sessionMilestoneLabel,
		"topEditedFile":         topFile,
		"topEditedCount":        topFileCount,
		"topEditedCountLabel":   plural(topFileCount, "edit", "edits"),
		"topEditedAgeLabel":     topFileAgeLabel,
		"linesAdded":            agg.LinesAdded,
		"linesAddedFmt":         FmtNum(int64(agg.LinesAdded)),
		"linesRemoved":          agg.LinesRemoved,
		"linesRemovedFmt":       FmtNum(int64(agg.LinesRemoved)),
		"linesNet":              agg.LinesNet,
		"linesNetFmt":           fmtLinesNet(agg.LinesNet),
		"topLanguage":           topLang,
		"topLanguageEdits":      topLangEdits,
		"goalLabel":             goalLabel,
		"goalHit":               goalHit,
		"budgetLabel":           budgetLabel,
		"weekdayLabel":          now.Weekday().String(),
		"empty":                 " ",
	}
}

func FillTemplate(tpl string, vars map[string]any) string {
	if tpl == "" {
		return ""
	}
	filled := tplVarRe.ReplaceAllStringFunc(tpl, func(match string) string {
		key := match[1 : len(match)-1]
		if v, ok := vars[key]; ok {
			if v == nil {
				return ""
			}
			return fmt.Sprint(v)
		}
		return match
	})
	if !strings.Contains(filled, "·") {
		return strings.TrimSpace(filled)
	}
	parts := strings.Split(filled, "·")
	var kept []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, " · ")
}

func FramePasses(frame config.Frame, vars map[string]any) bool {
	if len(frame.Requires) == 0 {
		return true
	}
	for _, req := range frame.Requires {
		v, ok := vars[req]
		if !ok || v == nil {
			return false
		}
		switch t := v.(type) {
		case string:
			if t == "" {
				return false
			}
		case int:
			if t == 0 {
				return false
			}
		case int64:
			if t == 0 {
				return false
			}
		case float64:
			if t == 0 {
				return false
			}
		case bool:
			if !t {
				return false
			}
		}
	}
	return true
}

func staleWipe(st state.SessionState) state.SessionState {
	st.Status = "stale"
	st.CurrentTool = ""
	st.CurrentFile = ""
	st.SessionStart = 0
	st.Cwd = ""
	st.Messages = 0
	st.Tools = 0
	st.FilesOpened = nil
	st.FilesEdited = nil
	st.FilesRead = nil
	st.Tokens = state.Tokens{}
	return st
}

func borrowLive(st state.SessionState, live state.LiveSession, now int64) state.SessionState {
	st.Source = live.Source
	if st.Source == "" {
		st.Source = "antigravity"
	}
	st.Status = "working"
	st.Borrowed = true
	if live.Cwd != "" {
		st.Cwd = live.Cwd
	}
	st.TranscriptPath = live.Path
	if live.Mtime > 0 {
		st.SessionStart = live.Mtime
		st.LastActivity = live.Mtime
	} else {
		st.SessionStart = now
		st.LastActivity = now
	}
	st.CurrentTool = ""
	st.CurrentFile = ""
	st.Messages = 0
	st.Tools = 0
	st.Tokens = state.Tokens{}
	return st
}

func ApplyIdle(st state.SessionState, cfg config.Config) state.SessionState {
	now := time.Now().UnixMilli()
	ageMs := now - st.LastActivity
	idleMs := int64(cfg.IdleThresholdSec) * 1000
	if idleMs <= 0 {
		idleMs = 60_000
	}
	staleMs := int64(cfg.StaleSessionMin) * 60_000
	if staleMs < 60_000 {
		staleMs = 5 * 60_000
	}
	notifMs := int64(cfg.NotificationWindowSec) * 1000
	if notifMs <= 0 {
		notifMs = 8_000
	}
	procAlive := st.AgentProcessAlive != nil && *st.AgentProcessAlive

	var mostRecentLive int64
	for _, ls := range st.LiveSessions {
		if ls.Mtime > mostRecentLive {
			mostRecentLive = ls.Mtime
		}
	}
	liveAgeMs := int64(math.MaxInt64)
	if mostRecentLive > 0 {
		liveAgeMs = now - mostRecentLive
	}

	if st.AgentClosed || st.ClaudeClosed {
		for _, ls := range st.LiveSessions {
			if ls.Cwd != "" && ls.Cwd != st.Cwd && now-ls.Mtime <= staleMs {
				return borrowLive(st, ls, now)
			}
		}
		return staleWipe(st)
	}

	if st.Status == "notification" {
		if now-st.LastNotification <= notifMs {
			return st
		}
		st.Status = "idle"
		st.CurrentTool = ""
		st.CurrentFile = ""
	}

	if ageMs > staleMs && liveAgeMs > staleMs {
		if procAlive {
			st.Status = "idle"
			st.CurrentTool = ""
			st.CurrentFile = ""
			return st
		}
		return staleWipe(st)
	}
	if ageMs > staleMs && liveAgeMs <= staleMs && len(st.LiveSessions) > 0 {
		return borrowLive(st, st.LiveSessions[0], now)
	}
	if st.Status == "idle" {
		if len(st.LiveSessions) == 0 && !cfg.IdleWhenOpen && !procAlive {
			return staleWipe(st)
		}
		return st
	}
	if ageMs > idleMs {
		if liveAgeMs <= idleMs {
			return st
		}
		if len(st.LiveSessions) == 0 && !cfg.IdleWhenOpen && !procAlive {
			return staleWipe(st)
		}
		st.Status = "idle"
		st.CurrentTool = ""
		st.CurrentFile = ""
	}
	return st
}

func ApplyShipped(st state.SessionState, cfg config.Config) state.SessionState {
	if st.Status == "stale" || st.JustShipped == 0 || cfg.ShippedFrameSec <= 0 {
		return st
	}
	if time.Now().UnixMilli()-st.JustShipped <= int64(cfg.ShippedFrameSec)*1000 {
		st.Status = "shipped"
	}
	return st
}

func ApplyTrigger(st state.SessionState, cfg config.Config) state.SessionState {
	if st.Status == "stale" || st.Status == "shipped" || len(cfg.Triggers) == 0 || st.LastBashCommand == "" || st.LastBashAt == 0 {
		return st
	}
	winSec := cfg.TriggerFrameSec
	if winSec <= 0 {
		winSec = 20
	}
	if time.Now().UnixMilli()-st.LastBashAt > int64(winSec)*1000 {
		return st
	}
	for _, t := range cfg.Triggers {
		if t.Match == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + t.Match)
		if err == nil && re.MatchString(st.LastBashCommand) {
			st.Status = "trigger"
			st.TriggerFrame = &state.TriggerFrame{Details: t.Details, State: t.State, LargeImageText: t.LargeImageText}
			return st
		}
	}
	return st
}

// ActiveAgents returns the agents with recorded sessions in agg, in display
// order.
func ActiveAgents(agg *scanner.Aggregate) []string {
	if agg == nil {
		return nil
	}
	var out []string
	for _, a := range config.Agents {
		if s := agg.BySource[a]; s != nil && (s.Sessions > 0 || s.ActiveMs > 0) {
			out = append(out, a)
		}
	}
	return out
}
