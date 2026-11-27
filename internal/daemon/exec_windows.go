//go:build windows

package daemon

import "errors"

const canExecSelf = false

func execSelf(string) error { return errors.New("in-place restart is not supported on Windows") }
