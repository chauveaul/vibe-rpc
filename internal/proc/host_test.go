package proc

import "testing"

func TestHostAppSkipsHelpersAndShells(t *testing.T) {
	exes := map[int32]string{
		100: "/Users/me/.local/bin/claude",
		90:  "/bin/zsh",
		80:  "/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin)",
		70:  "/Applications/Visual Studio Code.app/Contents/MacOS/Electron",
		60:  "/sbin/launchd",
	}
	parents := map[int32]int32{100: 90, 90: 80, 80: 70, 70: 1}
	exe := func(p int32) string { return exes[p] }
	parent := func(p int32) int32 { return parents[p] }

	if b, pid := hostApp(100, exe, parent); b != "/Applications/Visual Studio Code.app" || pid != 70 {
		t.Fatalf("got %q %d", b, pid)
	}
	exes[70] = "/usr/bin/tmux" // detached multiplexer: no GUI host
	if b, pid := hostApp(100, exe, parent); b != "" || pid != 0 {
		t.Fatalf("expected no host, got %q %d", b, pid)
	}
	if b := topLevelBundle("/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal"); b != "/System/Applications/Utilities/Terminal.app" {
		t.Fatalf("terminal bundle = %q", b)
	}
}
