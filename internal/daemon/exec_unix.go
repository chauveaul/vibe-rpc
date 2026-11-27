//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// canExecSelf reports whether the daemon can replace itself in place, which
// keeps its pid (and so launchd/systemd supervision) across the restart.
const canExecSelf = true

func execSelf(path string) error {
	return syscall.Exec(path, []string{path, "daemon"}, os.Environ())
}
