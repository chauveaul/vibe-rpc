package privacy

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/config"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/state"
)

const cacheTTL = 5 * time.Minute

type Resolution struct {
	Visibility  string `json:"visibility"`
	ProjectName string `json:"projectName,omitempty"`
	Reason      string `json:"reason"`
}

type privateListFile struct {
	Paths      []string          `json:"paths"`
	Visibility map[string]string `json:"visibility,omitempty"`
}

type projectFileCfg struct {
	Private     bool   `json:"private"`
	Visibility  string `json:"visibility"`
	ProjectName string `json:"projectName"`
}

type ghCacheEntry struct {
	ts  time.Time
	val *bool
}

var (
	ghMu       sync.Mutex
	ghCache    = map[string]ghCacheEntry{}
	ghInFlight = map[string]bool{}
)

func readList(file string) privateListFile {
	if file == "" {
		file = paths.PrivateListPath()
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return privateListFile{Paths: []string{}, Visibility: map[string]string{}}
	}
	var out privateListFile
	if err := json.Unmarshal(b, &out); err != nil {
		return privateListFile{Paths: []string{}, Visibility: map[string]string{}}
	}
	if out.Paths == nil {
		out.Paths = []string{}
	}
	if out.Visibility == nil {
		out.Visibility = map[string]string{}
	}
	return out
}

func writeList(file string, list privateListFile) error {
	if file == "" {
		file = paths.PrivateListPath()
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(file, append(b, '\n'), 0o644)
}

func normLevel(v string) string {
	switch v {
	case "public", "name-only", "hidden":
		return v
	default:
		return ""
	}
}

func ListVisibility() map[string]string {
	return readList("").Visibility
}

func ListPrivateCwds() []string {
	return readList("").Paths
}

func SetCwdVisibility(cwd, level string) (map[string]string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	list := readList("")
	if l := normLevel(level); l != "" {
		list.Visibility[abs] = l
	} else {
		delete(list.Visibility, abs)
	}
	filtered := make([]string, 0, len(list.Paths))
	for _, p := range list.Paths {
		if p != abs {
			filtered = append(filtered, p)
		}
	}
	list.Paths = filtered
	if err := writeList("", list); err != nil {
		return nil, err
	}
	return list.Visibility, nil
}

func AddPrivateCwd(cwd string) error {
	abs, _ := filepath.Abs(cwd)
	if _, err := SetCwdVisibility(abs, "hidden"); err != nil {
		return err
	}
	return nil
}

func RemovePrivateCwd(cwd string) error {
	abs, _ := filepath.Abs(cwd)
	if _, err := SetCwdVisibility(abs, "public"); err != nil {
		return err
	}
	return nil
}

func findProjectFile(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir := cwd
	for {
		for _, name := range []string{".vibe-rpc.json", ".agy-rpc.json", ".claude-rpc.json"} {
			cand := filepath.Join(dir, name)
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func readProjectFile(cwd string) *projectFileCfg {
	p := findProjectFile(cwd)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var cfg projectFileCfg
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil
	}
	if cfg.Private && cfg.Visibility == "" {
		cfg.Visibility = "hidden"
	}
	cfg.Visibility = normLevel(cfg.Visibility)
	return &cfg
}

func matchesPattern(name, pattern string) bool {
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(name, pattern)
	}
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re, err := regexp.Compile("(?i)^" + strings.Join(parts, ".*") + "$")
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

func detectGithubPrivate(cwd string) *bool {
	if cwd == "" {
		return nil
	}
	ghMu.Lock()
	if entry, ok := ghCache[cwd]; ok && time.Since(entry.ts) < cacheTTL {
		ghMu.Unlock()
		return entry.val
	}
	if !ghInFlight[cwd] {
		ghInFlight[cwd] = true
		go probeGithubPrivate(cwd)
	}
	entry := ghCache[cwd]
	ghMu.Unlock()
	return entry.val
}

func probeGithubPrivate(cwd string) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "repo", "view", "--json", "isPrivate", "-q", ".isPrivate")
	cmd.Dir = cwd
	out, err := cmd.Output()
	var val *bool
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s == "true" {
			t := true
			val = &t
		} else if s == "false" {
			f := false
			val = &f
		}
	}
	ghMu.Lock()
	ghCache[cwd] = ghCacheEntry{ts: time.Now(), val: val}
	delete(ghInFlight, cwd)
	ghMu.Unlock()
}

func Resolve(cwd string, cfg config.Config) Resolution {
	proj := readProjectFile(cwd)
	alias := ""
	if proj != nil {
		alias = proj.ProjectName
		if proj.Visibility != "" {
			return Resolution{Visibility: proj.Visibility, ProjectName: alias, Reason: ".vibe-rpc.json"}
		}
	}
	if cwd != "" {
		abs, _ := filepath.Abs(cwd)
		list := readList("")
		if v := normLevel(list.Visibility[abs]); v != "" {
			return Resolution{Visibility: v, ProjectName: alias, Reason: "private-list (visibility)"}
		}
		for p, v := range list.Visibility {
			if abs == p || strings.HasPrefix(abs, p+string(filepath.Separator)) {
				if nl := normLevel(v); nl != "" {
					return Resolution{Visibility: nl, ProjectName: alias, Reason: "private-list (visibility)"}
				}
			}
		}
		for _, p := range list.Paths {
			if abs == p || strings.HasPrefix(abs, p+string(filepath.Separator)) {
				return Resolution{Visibility: "hidden", ProjectName: alias, Reason: "private-list"}
			}
		}
		leaf := filepath.Base(abs)
		for _, pat := range cfg.Privacy.Patterns {
			if matchesPattern(leaf, pat) {
				mode := normLevel(cfg.Privacy.Mode)
				if mode == "" {
					mode = "hidden"
				}
				return Resolution{Visibility: mode, ProjectName: alias, Reason: "config pattern '" + pat + "'"}
			}
		}
		if cfg.Privacy.AutoDetectGithubPrivate == nil || *cfg.Privacy.AutoDetectGithubPrivate {
			if priv := detectGithubPrivate(abs); priv != nil && *priv {
				mode := normLevel(cfg.Privacy.GithubPrivateMode)
				if mode == "" {
					mode = "hidden"
				}
				return Resolution{Visibility: mode, ProjectName: alias, Reason: "github private repo"}
			}
		}
	}
	return Resolution{Visibility: "public", ProjectName: alias, Reason: "default"}
}

func joinCwdAlias(cwd, alias string) string {
	if cwd == "" {
		return alias
	}
	dir := filepath.Dir(cwd)
	if dir == "." || dir == "" {
		return alias
	}
	return filepath.Join(dir, alias)
}

func Apply(st state.SessionState, cfg config.Config) state.SessionState {
	if st.Status == "stale" {
		return st
	}
	res := Resolve(st.Cwd, cfg)
	switch res.Visibility {
	case "public":
		if res.ProjectName != "" {
			st.Cwd = joinCwdAlias(st.Cwd, res.ProjectName)
			st.Privacy = &state.PrivacyVerdict{Visibility: "public", Alias: true}
		}
		return st
	case "name-only":
		if res.ProjectName != "" {
			st.Cwd = joinCwdAlias(st.Cwd, res.ProjectName)
		}
		st.CurrentTool = ""
		st.CurrentFile = ""
		st.FilesEdited = nil
		st.FilesRead = nil
		st.FilesOpened = nil
		st.Privacy = &state.PrivacyVerdict{Visibility: "name-only", Alias: res.ProjectName != ""}
		return st
	default: // hidden
		st.Cwd = ""
		st.CurrentTool = ""
		st.CurrentFile = ""
		st.FilesEdited = nil
		st.FilesRead = nil
		st.FilesOpened = nil
		st.Privacy = &state.PrivacyVerdict{Visibility: "hidden", Alias: false}
		return st
	}
}
