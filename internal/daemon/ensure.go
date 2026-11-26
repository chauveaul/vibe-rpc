package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/meze/agy-rpc/internal/paths"
)

const (
	BeatEvery = 30 * time.Second
	BeatStale = 5 * time.Minute
)

type Info struct {
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
	BeatAt    int64  `json:"beatAt"`
}

func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

func ReadPID(file string) int {
	if file == "" {
		file = paths.PidPath()
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func ReadInfo(file string) *Info {
	if file == "" {
		file = paths.DaemonInfoPath()
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var info Info
	if err := json.Unmarshal(b, &info); err != nil || info.PID <= 0 {
		return nil
	}
	return &info
}

func WriteInfo(info Info) {
	b, err := json.Marshal(info)
	if err == nil {
		_ = paths.WriteFileAtomic(paths.DaemonInfoPath(), b, 0o644)
	}
}

func AlivePID() int {
	pid := ReadPID("")
	if !IsAlive(pid) {
		return 0
	}
	if info := ReadInfo(""); info != nil && info.PID == pid && info.BeatAt > 0 {
		if time.Since(time.UnixMilli(info.BeatAt)) > BeatStale {
			return 0
		}
	}
	return pid
}

func IsStopped() bool {
	_, err := os.Stat(paths.StoppedPath())
	return err == nil
}

func MarkStopped() {
	_ = paths.WriteFileAtomic(paths.StoppedPath(), []byte(strconv.FormatInt(time.Now().UnixMilli(), 10)), 0o644)
}

func ClearStopped() {
	_ = os.Remove(paths.StoppedPath())
}

func StopAndWait() bool {
	pid := AlivePID()
	if pid == 0 {
		_ = os.Remove(paths.PidPath())
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	_ = p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !IsAlive(pid) {
			_ = os.Remove(paths.PidPath())
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = p.Kill()
	deadline = time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !IsAlive(pid) {
			_ = os.Remove(paths.PidPath())
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func SpawnDetached() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	detachCmd(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func EnsureRunning(autostart bool) {
	if !autostart || IsStopped() || AlivePID() != 0 {
		return
	}
	marker := paths.SpawnMarker()
	if st, err := os.Stat(marker); err == nil && time.Since(st.ModTime()) < 15*time.Second {
		return
	}
	_ = os.MkdirAll(filepath.Dir(marker), 0o755)
	_ = os.WriteFile(marker, []byte(strconv.FormatInt(time.Now().UnixMilli(), 10)), 0o644)
	_, _ = SpawnDetached()
}

func claimSingleInstance() int {
	pidPath := paths.PidPath()
	_ = os.MkdirAll(filepath.Dir(pidPath), 0o755)
	myPID := os.Getpid()
	for attempt := 0; attempt < 4; attempt++ {
		f, err := os.OpenFile(pidPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(myPID))
			_ = f.Close()
			return 0
		}
		owner := ReadPID(pidPath)
		if owner > 0 && owner != myPID && AlivePID() == owner {
			return owner
		}
		_ = os.Remove(pidPath)
	}
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(myPID)), 0o644)
	return 0
}
