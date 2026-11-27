package notch

import (
	"os"
	"path/filepath"
	"testing"
)

func writeApp(t *testing.T, app, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>CFBundleShortVersionString</key><string>` + version + `</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
}

// brewLayout builds <root>/Cellar/vibe-rpc/<ver>/{bin/vibe-rpc,VibeNotch.app}
// with Homebrew's opt/ and bin/ symlinks, and returns the bin/ link.
func brewLayout(t *testing.T, root, version string) string {
	t.Helper()
	keg := filepath.Join(root, "Cellar", "vibe-rpc", version)
	if err := os.MkdirAll(filepath.Join(keg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keg, "bin", "vibe-rpc"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeApp(t, filepath.Join(keg, "VibeNotch.app"), version)
	for link, target := range map[string]string{
		filepath.Join(root, "opt", "vibe-rpc"): keg,
		filepath.Join(root, "bin", "vibe-rpc"): filepath.Join(keg, "bin", "vibe-rpc"),
	} {
		_ = os.MkdirAll(filepath.Dir(link), 0o755)
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlinks unsupported:", err)
		}
	}
	return filepath.Join(root, "bin", "vibe-rpc")
}

// tempRoot resolves symlinks (macOS's /var -> /private/var) so expected
// paths match the resolved ones Bundled returns.
func tempRoot(t *testing.T) string {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBundledUsesHomebrewOptPath(t *testing.T) {
	root := tempRoot(t)
	exe := brewLayout(t, root, "2.4.2")
	want := filepath.Join(root, "opt", "vibe-rpc", "VibeNotch.app")
	if got := Bundled(exe); got != want {
		t.Fatalf("Bundled = %q, want %q", got, want)
	}
	if got := AppVersion(Bundled(exe)); got != "2.4.2" {
		t.Fatalf("AppVersion = %q", got)
	}
	// A plain binary with no app beside it has nothing bundled.
	plain := filepath.Join(t.TempDir(), "bin", "vibe-rpc")
	_ = os.MkdirAll(filepath.Dir(plain), 0o755)
	_ = os.WriteFile(plain, []byte("x"), 0o755)
	if got := Bundled(plain); got != "" {
		t.Fatalf("plain install Bundled = %q", got)
	}
}

func TestStaleAfterUpgrade(t *testing.T) {
	t.Setenv("VIBE_RPC_STATE_DIR", t.TempDir())
	root := tempRoot(t)
	exe := brewLayout(t, root, "2.4.2")

	// Started before launches were recorded: stale once.
	if !Stale(exe) {
		t.Fatal("unrecorded launch should be stale")
	}
	recordLaunch(Find(exe))
	if Stale(exe) {
		t.Fatal("just-recorded launch should not be stale")
	}

	// brew upgrade: new keg, opt/ now points at it.
	brewLayout(t, root, "2.4.3")
	if !Stale(exe) {
		t.Fatal("running 2.4.2 after upgrading to 2.4.3 should be stale")
	}
	recordLaunch(Find(exe))
	if Stale(exe) {
		t.Fatal("relaunched app should match")
	}
}
