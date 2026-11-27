package notch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chauveaul/vibe-rpc/internal/paths"
)

// Bundled returns the VibeNotch.app a package manager installed alongside the
// vibe-rpc binary, or "". Homebrew puts it in the formula's prefix
// (<prefix>/bin/vibe-rpc next to <prefix>/VibeNotch.app); the returned path
// uses Homebrew's version-independent opt/ link so it survives upgrades.
func Bundled(exe string) string {
	if exe == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		real = exe
	}
	prefix := filepath.Dir(filepath.Dir(real))
	app := filepath.Join(prefix, appBundleName)
	if !isDir(app) {
		return ""
	}
	slash := filepath.ToSlash(prefix)
	if i := strings.Index(slash, "/Cellar/"); i >= 0 {
		name := strings.SplitN(slash[i+len("/Cellar/"):], "/", 2)[0]
		opt := filepath.Join(filepath.FromSlash(slash[:i]), "opt", name, appBundleName)
		if isDir(opt) {
			return opt
		}
	}
	return app
}

var shortVersionRe = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)

// AppVersion reads CFBundleShortVersionString from an app bundle's Info.plist,
// or "" when it can't.
func AppVersion(app string) string {
	if app == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	if m := shortVersionRe.FindSubmatch(b); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// launchRecord is what Launch writes, so the daemon can tell whether the
// VibeNotch that's running is the version installed now.
type launchRecord struct {
	Version string `json:"version"`
	App     string `json:"app"`
}

func recordPath() string { return filepath.Join(paths.StateDir(), "notch.json") }

func recordLaunch(app string) {
	b, err := json.Marshal(launchRecord{Version: AppVersion(app), App: app})
	if err != nil {
		return
	}
	_ = os.MkdirAll(paths.StateDir(), 0o755)
	_ = paths.WriteFileAtomic(recordPath(), b, 0o644)
}

// launchedVersion is the app version recorded by the last Launch, or "".
func launchedVersion() string {
	b, err := os.ReadFile(recordPath())
	if err != nil {
		return ""
	}
	var r launchRecord
	if json.Unmarshal(b, &r) != nil {
		return ""
	}
	return r.Version
}

// Stale reports whether the running VibeNotch was launched from a different
// version than the one installed now (for example after `brew upgrade`), so
// it should be relaunched. A VibeNotch started before launches were recorded
// counts as stale once, then the record matches.
func Stale(exe string) bool {
	want := AppVersion(Find(exe))
	return want != "" && launchedVersion() != want
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
