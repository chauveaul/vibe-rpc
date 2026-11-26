package gitinfo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type info struct {
	ts     time.Time
	github string
	branch string
	repo   string
}

const ttl = 5 * time.Minute

var (
	mu          sync.Mutex
	cache       = map[string]info{}
	gitdirRe    = regexp.MustCompile(`(?m)^gitdir:\s*(.+?)\s*$`)
	remoteOriRe = regexp.MustCompile(`(?i)\[remote\s+"origin"\]`)
	urlLineRe   = regexp.MustCompile(`(?mi)^[ \t]*url\s*=\s*(.+)$`)
	sshGhRe     = regexp.MustCompile(`(?i)^git@github\.com:([^\s]+?)(?:\.git)?$`)
	headRefRe   = regexp.MustCompile(`^ref:\s+refs/heads/(.+)$`)
	commitPfxRe = regexp.MustCompile(`^commit(?:\s+\([^)]+\))?:\s*`)
)

func resolveGitDir(cwd string) string {
	dotGit := filepath.Join(cwd, ".git")
	st, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if st.IsDir() {
		return dotGit
	}
	b, err := os.ReadFile(dotGit)
	if err != nil {
		return ""
	}
	m := gitdirRe.FindStringSubmatch(string(b))
	if len(m) < 2 {
		return ""
	}
	if filepath.IsAbs(m[1]) {
		return m[1]
	}
	return filepath.Join(cwd, m[1])
}

func commonGitDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	rel := strings.TrimSpace(string(b))
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(gitDir, rel)
}

func readInfo(cwd string) info {
	out := info{ts: time.Now(), repo: filepath.Base(cwd)}
	if cwd == "" {
		out.repo = ""
		return out
	}
	gitDir := resolveGitDir(cwd)
	if gitDir == "" {
		return out
	}
	if cfgBytes, err := os.ReadFile(filepath.Join(commonGitDir(gitDir), "config")); err == nil {
		cfg := string(cfgBytes)
		if loc := remoteOriRe.FindStringIndex(cfg); loc != nil {
			rest := cfg[loc[0]:]
			section := rest
			if idx := strings.Index(rest[1:], "\n["); idx != -1 {
				section = rest[:idx+1]
			}
			if m := urlLineRe.FindStringSubmatch(section); len(m) >= 2 {
				raw := strings.TrimSpace(m[1])
				if sm := sshGhRe.FindStringSubmatch(raw); len(sm) >= 2 {
					out.github = "https://github.com/" + sm[1]
					out.repo = filepath.Base(sm[1])
				} else if strings.HasPrefix(strings.ToLower(raw), "https://github.com/") || strings.HasPrefix(strings.ToLower(raw), "http://github.com/") {
					out.github = strings.TrimSuffix(raw, ".git")
					out.repo = filepath.Base(out.github)
				}
			}
		}
	}
	if headBytes, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		if m := headRefRe.FindStringSubmatch(strings.TrimSpace(string(headBytes))); len(m) >= 2 {
			out.branch = strings.TrimSpace(m[1])
		}
	}
	return out
}

func lookup(cwd string) info {
	if cwd == "" {
		return info{}
	}
	mu.Lock()
	defer mu.Unlock()
	if c, ok := cache[cwd]; ok && time.Since(c.ts) < ttl {
		return c
	}
	v := readInfo(cwd)
	cache[cwd] = v
	return v
}

func GithubURL(cwd string) string { return lookup(cwd).github }
func Branch(cwd string) string    { return lookup(cwd).branch }
func Repo(cwd string) string      { return lookup(cwd).repo }

func LastCommitSubject(cwd string, maxLen int) string {
	if cwd == "" {
		return ""
	}
	if maxLen <= 0 {
		maxLen = 80
	}
	gitDir := resolveGitDir(cwd)
	if gitDir == "" {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(gitDir, "COMMIT_EDITMSG")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if len(t) > maxLen {
				return t[:maxLen]
			}
			return t
		}
	}
	if b, err := os.ReadFile(filepath.Join(gitDir, "logs", "HEAD")); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 0 {
			last := lines[len(lines)-1]
			if tab := strings.IndexByte(last, '\t'); tab >= 0 {
				msg := commitPfxRe.ReplaceAllString(strings.TrimSpace(last[tab+1:]), "")
				if len(msg) > maxLen {
					return msg[:maxLen]
				}
				return msg
			}
		}
	}
	return ""
}
