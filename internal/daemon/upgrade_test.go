//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/paths"
)

func fakeBinary(t *testing.T, path, version string) {
	t.Helper()
	script := "#!/bin/sh\necho \"vibe-rpc version " + version + "\"\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestBinaryWatchDetectsNewVersion(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "vibe-rpc")
	fakeBinary(t, bin, paths.Version)
	w := &binaryWatch{path: bin}
	w.real, w.modTime = resolveBinary(bin)

	if v := w.newVersion(); v != "" {
		t.Fatalf("unchanged binary reported %q", v)
	}
	// Reinstalling the same version is not an upgrade.
	fakeBinary(t, bin, paths.Version)
	_ = os.Chtimes(bin, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	if v := w.newVersion(); v != "" {
		t.Fatalf("same version reported %q", v)
	}
	fakeBinary(t, bin, "99.0.0")
	_ = os.Chtimes(bin, time.Now().Add(2*time.Minute), time.Now().Add(2*time.Minute))
	if v := w.newVersion(); v != "99.0.0" {
		t.Fatalf("newVersion = %q, want 99.0.0", v)
	}
	// Reported once per change on disk.
	if v := w.newVersion(); v != "" {
		t.Fatalf("second check reported %q again", v)
	}
}

func TestBinaryVersionRejectsOtherOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho hello world\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if v := binaryVersion(bin); v != "" {
		t.Fatalf("binaryVersion = %q", v)
	}
}
