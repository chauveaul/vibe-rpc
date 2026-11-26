// Package notch finds, launches and quits VibeNotch.app, the macOS notch
// companion app (source in macos/VibeNotch).
package notch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrNotInstalled means VibeNotch.app couldn't be found.
var ErrNotInstalled = errors.New("VibeNotch.app not found in ~/Applications or /Applications; install it with: vibe-rpc notch install")

// Supported reports whether VibeNotch can run on this OS.
func Supported() bool { return runtime.GOOS == "darwin" }

// Find locates VibeNotch.app: the usual install locations, then a dev build
// next to the repo checkout the vibe-rpc binary lives in.
func Find(exe string) string {
	home, _ := os.UserHomeDir()
	cands := []string{
		filepath.Join(home, "Applications", "VibeNotch.app"),
		"/Applications/VibeNotch.app",
	}
	if exe != "" {
		cands = append(cands, filepath.Join(filepath.Dir(exe), "macos", "VibeNotch", "build", "VibeNotch.app"))
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return ""
}

// Running reports whether a VibeNotch process exists.
func Running() bool {
	if !Supported() {
		return false
	}
	return exec.Command("pgrep", "-x", "VibeNotch").Run() == nil
}

// Launch starts VibeNotch, telling it which vibe-rpc binary to stream from.
// It returns the app path it launched.
func Launch(exe string) (string, error) {
	if !Supported() {
		return "", errors.New("VibeNotch is macOS-only")
	}
	app := Find(exe)
	if app == "" {
		return "", ErrNotInstalled
	}
	return app, exec.Command("open", "-a", app, "--args", "--vibe-rpc", exe).Run()
}

// Quit stops any running VibeNotch.
func Quit() {
	if Supported() {
		_ = exec.Command("pkill", "-x", "VibeNotch").Run()
	}
}
