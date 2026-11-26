package ships

import (
	"regexp"
	"strings"
)

var (
	precedence = []string{"push", "commit", "pr", "issue", "tag"}
	singleQRe  = regexp.MustCompile(`'[^']*'`)
	doubleQRe  = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
	envLeadRe  = regexp.MustCompile(`^\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S+\s+)+`)
	sepRe      = regexp.MustCompile(`[;&|\n]+`)
)

func tokenizeSegment(seg string) []string {
	stripped := strings.TrimSpace(envLeadRe.ReplaceAllString(seg, ""))
	if stripped == "" {
		return nil
	}
	toks := strings.Fields(stripped)
	for len(toks) > 0 && (toks[0] == "sudo" || toks[0] == "time") {
		toks = toks[1:]
	}
	if len(toks) > 0 {
		if idx := strings.LastIndex(toks[0], "/"); idx != -1 {
			toks[0] = toks[0][idx+1:]
		}
		toks[0] = strings.ToLower(toks[0])
	}
	return toks
}

func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-C" || a == "-c" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return strings.ToLower(a)
	}
	return ""
}

func ghSubcommand(args []string) (string, string) {
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-R" || a == "--repo" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		pos = append(pos, strings.ToLower(a))
		if len(pos) == 2 {
			break
		}
	}
	if len(pos) < 2 {
		return "", ""
	}
	return pos[0], pos[1]
}

func shipKindForSegment(seg string) string {
	toks := tokenizeSegment(seg)
	if len(toks) == 0 {
		return ""
	}
	switch toks[0] {
	case "git":
		sub := gitSubcommand(toks[1:])
		if sub == "push" || sub == "commit" {
			return sub
		}
	case "gh":
		noun, verb := ghSubcommand(toks[1:])
		if verb == "create" {
			switch noun {
			case "pr":
				return "pr"
			case "issue":
				return "issue"
			case "release":
				return "tag"
			}
		}
	}
	return ""
}

// Classify returns "push", "commit", "pr", "issue", "tag", or "".
func Classify(cmd string) string {
	if cmd == "" {
		return ""
	}
	blanked := singleQRe.ReplaceAllString(cmd, " ")
	blanked = doubleQRe.ReplaceAllString(blanked, " ")
	found := map[string]bool{}
	for _, seg := range sepRe.Split(blanked, -1) {
		if k := shipKindForSegment(seg); k != "" {
			found[k] = true
		}
	}
	for _, kind := range precedence {
		if found[kind] {
			return kind
		}
	}
	return ""
}
