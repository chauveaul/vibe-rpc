package proc

import (
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// HostApp walks up the process tree from pid to the GUI app hosting it: the
// terminal (Terminal, iTerm, Ghostty, Warp…) or editor (VS Code, Cursor,
// Antigravity…) an agent runs in. It returns the .app bundle path and that
// app's pid, or ("", 0) when there's none (e.g. inside tmux or over SSH).
//
// Helper processes live in nested bundles such as
// "Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/…";
// those are skipped so the result is the app that can be frontmost.
func HostApp(pid int32) (string, int32) {
	return hostApp(pid, exeOf, parentOf)
}

func exeOf(pid int32) string {
	p, err := process.NewProcess(pid)
	if err != nil {
		return ""
	}
	exe, _ := p.Exe()
	return exe
}

func parentOf(pid int32) int32 {
	p, err := process.NewProcess(pid)
	if err != nil {
		return 0
	}
	pp, _ := p.Ppid()
	return pp
}

func hostApp(pid int32, exe func(int32) string, parent func(int32) int32) (string, int32) {
	for i := 0; i < 24 && pid > 1; i++ {
		if b := topLevelBundle(exe(pid)); b != "" {
			return b, pid
		}
		pid = parent(pid)
	}
	return "", 0
}

// topLevelBundle returns "/…/X.app" when exe is X.app's main executable
// (X.app/Contents/MacOS/…) and X.app isn't nested inside another bundle.
func topLevelBundle(exe string) string {
	exe = filepath.ToSlash(exe)
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 || strings.Count(exe[:i+4], ".app/") > 0 {
		return ""
	}
	return exe[:i+4]
}

// Alive reports whether a process with this pid exists.
func Alive(pid int32) bool {
	ok, err := process.PidExists(pid)
	return err == nil && ok
}
