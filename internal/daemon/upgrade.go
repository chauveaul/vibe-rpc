package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/install"
	"github.com/chauveaul/vibe-rpc/internal/paths"
)

// binaryWatch notices when the vibe-rpc binary the daemon was started from is
// replaced on disk by a different version.
type binaryWatch struct {
	path    string // stable path the daemon restarts through, e.g. /opt/homebrew/bin/vibe-rpc
	real    string // what path resolved to when last checked
	modTime time.Time
}

func newBinaryWatch() *binaryWatch {
	w := &binaryWatch{path: install.ResolveBinary()}
	w.real, w.modTime = resolveBinary(w.path)
	return w
}

func resolveBinary(p string) (string, time.Time) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", time.Time{}
	}
	fi, err := os.Stat(real)
	if err != nil {
		return real, time.Time{}
	}
	return real, fi.ModTime()
}

// newVersion returns the version now installed at w.path when the file has
// changed and reports a version other than the running one; otherwise "".
// It only asks the binary for its version once per change on disk.
func (w *binaryWatch) newVersion() string {
	real, mt := resolveBinary(w.path)
	if real == "" || (real == w.real && mt.Equal(w.modTime)) {
		return ""
	}
	w.real, w.modTime = real, mt
	v := binaryVersion(w.path)
	if v == "" || v == paths.Version {
		return ""
	}
	return v
}

// binaryVersion runs `<path> --version` ("vibe-rpc version 2.4.1").
func binaryVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) < 3 || f[0] != "vibe-rpc" || f[1] != "version" {
		return ""
	}
	return f[2]
}
