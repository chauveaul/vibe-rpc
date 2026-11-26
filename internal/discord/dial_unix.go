//go:build !windows

package discord

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func tempBase() string {
	for _, k := range []string{"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"} {
		if v := os.Getenv(k); v != "" {
			if real, err := filepath.EvalSymlinks(v); err == nil {
				return real
			}
			return v
		}
	}
	return "/tmp"
}

// CandidatePaths returns existing Discord desktop IPC Unix socket paths.
func CandidatePaths() []string {
	base := tempBase()
	dirs := []string{base}
	if runtime.GOOS == "linux" {
		dirs = append(dirs,
			filepath.Join(base, "snap.discord"),
			filepath.Join(base, "app", "com.discordapp.Discord"),
		)
	}
	var out []string
	for _, dir := range dirs {
		for i := 0; i < 10; i++ {
			p := filepath.Join(dir, fmt.Sprintf("discord-ipc-%d", i))
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

func dialPath(p string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", p, timeout)
}
