package proc

import (
	"regexp"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

type ActiveAgents struct {
	Antigravity *bool
	Claude      *bool
	Any         *bool
}

var (
	flagArgRe = regexp.MustCompile(`(^|\s)--?[\w-]+=("[^"]*"|\S*)`)
	agyTokRe  = regexp.MustCompile(`(^|[/"'])(agy|antigravity)(\.exe)?["']?(\s|$)`)
	ccTokRe   = regexp.MustCompile(`(^|[/"'])claude(\.exe)?["']?(\s|$)`)
)

func LooksLikeAntigravity(name, cmdline string) bool {
	n := strings.ToLower(name)
	c := strings.ToLower(strings.ReplaceAll(cmdline, "\\", "/"))
	if strings.Contains(c, "vibe-rpc") || strings.Contains(c, "agy-rpc") || strings.Contains(c, "antigravity-rpc") ||
		strings.Contains(n, "vibe-rpc") || strings.Contains(n, "agy-rpc") {
		return false
	}
	if strings.Contains(c, "antigravity.app/") || strings.Contains(c, "/antigravity-cli/") || strings.Contains(c, "/antigravity-ide/") {
		return true
	}
	tokens := flagArgRe.ReplaceAllString(c, "$1")
	if agyTokRe.MatchString(tokens) {
		return true
	}
	if c == "" && (n == "agy" || n == "agy.exe" || n == "antigravity" || n == "antigravity.exe") {
		return true
	}
	return false
}

func LooksLikeClaudeCode(name, cmdline string) bool {
	n := strings.ToLower(name)
	c := strings.ToLower(strings.ReplaceAll(cmdline, "\\", "/"))
	if strings.Contains(c, "anthropicclaude") || strings.Contains(c, "claude.app/") || strings.Contains(c, "/claude-desktop") ||
		strings.Contains(c, "claude-rpc") || strings.Contains(c, "vibe-rpc") || strings.Contains(c, "agy-rpc") {
		return false
	}
	if strings.Contains(c, "@anthropic-ai/claude-code/") || strings.Contains(c, "/share/claude/versions/") {
		return true
	}
	tokens := flagArgRe.ReplaceAllString(c, "$1")
	if ccTokRe.MatchString(tokens) {
		return true
	}
	if c == "" && (n == "claude" || n == "claude.exe") {
		return true
	}
	return false
}

func boolPtr(v bool) *bool { return &v }

// Detect uses gopsutil to inspect running OS processes without spawning ps/powershell.
func Detect(includeAntigravity, includeClaude bool) ActiveAgents {
	procs, err := process.Processes()
	if err != nil {
		return ActiveAgents{}
	}
	agy := false
	cc := false
	for _, p := range procs {
		name, _ := p.Name()
		cmd, _ := p.Cmdline()
		exe, _ := p.Exe()
		if cmd == "" && exe != "" {
			cmd = exe
		}
		if includeAntigravity && !agy && LooksLikeAntigravity(name, cmd) {
			agy = true
		}
		if includeClaude && !cc && LooksLikeClaudeCode(name, cmd) {
			cc = true
		}
		if (!includeAntigravity || agy) && (!includeClaude || cc) {
			break
		}
	}
	any := (includeAntigravity && agy) || (includeClaude && cc)
	return ActiveAgents{
		Antigravity: boolPtr(includeAntigravity && agy),
		Claude:      boolPtr(includeClaude && cc),
		Any:         boolPtr(any),
	}
}
