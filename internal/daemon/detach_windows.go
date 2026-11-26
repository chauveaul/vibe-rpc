//go:build windows

package daemon

import "os/exec"

func detachCmd(cmd *exec.Cmd) {
	_ = cmd
}
