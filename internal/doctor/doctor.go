package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/meze/agy-rpc/internal/config"
	"github.com/meze/agy-rpc/internal/daemon"
	"github.com/meze/agy-rpc/internal/discord"
	"github.com/meze/agy-rpc/internal/install"
	"github.com/meze/agy-rpc/internal/paths"
	"github.com/meze/agy-rpc/internal/privacy"
	"github.com/meze/agy-rpc/internal/scanner"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	Info Status = "info"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func IPCStateFromLog(logText string) (state, detail string) {
	lines := strings.Split(strings.TrimSpace(logText), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if strings.Contains(line, "connected to Discord IPC") {
			return "connected", line
		}
		if strings.Contains(line, "IPC disconnected") {
			return "disconnected", line
		}
		if strings.Contains(line, "Discord not running") {
			return "absent", line
		}
		if strings.Contains(line, "Discord IPC connect failed") {
			return "error", line
		}
	}
	return "unknown", ""
}

func BridgeFromLog(logText string) bool {
	lines := strings.Split(strings.TrimSpace(logText), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "connected to Discord IPC") {
			return strings.Contains(lines[i], "arRPC bridge")
		}
	}
	return false
}

var pidRe = regexp.MustCompile(`^\d+$`)

func checkClaudeRPCConflict() *Check {
	b, err := os.ReadFile(paths.ClaudeRPCPidPath())
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(b))
	if !pidRe.MatchString(s) {
		return nil
	}
	pid, _ := strconv.Atoi(s)
	if daemon.IsAlive(pid) {
		return &Check{
			Name:   "claude-rpc daemon",
			Status: Warn,
			Detail: fmt.Sprintf("claude-rpc daemon is also running (pid %d) — both daemons will compete for Discord Rich Presence", pid),
			Fix:    "Stop claude-rpc (`claude-rpc stop`) so agy-rpc manages combined Antigravity + Claude Code presence cleanly",
		}
	}
	return nil
}

func Run() []Check {
	var checks []Check

	// 1. Native Go binary
	exe, _ := os.Executable()
	checks = append(checks, Check{
		Name:   "Binary",
		Status: OK,
		Detail: fmt.Sprintf("agy-rpc v%s (%s, %s/%s) at %s", paths.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, exe),
	})

	// 2. Config & Client ID
	cfg := config.Load()
	cidClass := config.ClassifyClientID(cfg.ClientID)
	switch cidClass {
	case "ok":
		checks = append(checks, Check{
			Name:   "Config",
			Status: OK,
			Detail: fmt.Sprintf("%s (clientId=%s, appName=%q, sources=agy:%v,claude:%v)",
				paths.ConfigPath(), cfg.ClientID, cfg.AppName, cfg.Sources.WantAntigravity(), cfg.Sources.WantClaude()),
		})
	default:
		checks = append(checks, Check{
			Name:   "Config",
			Status: Fail,
			Detail: fmt.Sprintf("clientId is %s in %s", cidClass, paths.ConfigPath()),
			Fix:    "Run `agy-rpc settings` or edit config.json to set a valid 17–21 digit Discord Application ID",
		})
	}

	// 3. Antigravity hooks
	if b, err := os.ReadFile(paths.AgyHooksPath()); err != nil {
		checks = append(checks, Check{
			Name:   "Antigravity hooks",
			Status: Fail,
			Detail: fmt.Sprintf("%s not found", paths.AgyHooksPath()),
			Fix:    "Run `agy-rpc setup` or `agy-rpc doctor --fix`",
		})
	} else {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m[install.AgyHookGroupKey] != nil {
			checks = append(checks, Check{
				Name:   "Antigravity hooks",
				Status: OK,
				Detail: fmt.Sprintf("wired in %s", paths.AgyHooksPath()),
			})
		} else {
			checks = append(checks, Check{
				Name:   "Antigravity hooks",
				Status: Fail,
				Detail: fmt.Sprintf("%s exists but missing %q group", paths.AgyHooksPath(), install.AgyHookGroupKey),
				Fix:    "Run `agy-rpc setup` or `agy-rpc doctor --fix`",
			})
		}
	}

	// 4. Claude Code hooks (if enabled)
	if cfg.Sources.WantClaude() {
		if b, err := os.ReadFile(paths.ClaudeSettingsPath()); err != nil {
			checks = append(checks, Check{
				Name:   "Claude Code hooks",
				Status: Warn,
				Detail: fmt.Sprintf("%s not found (passive transcript & claude-rpc state tracking still active)", paths.ClaudeSettingsPath()),
				Fix:    "Run `agy-rpc setup` to wire live Claude Code hooks",
			})
		} else if strings.Contains(string(b), `"_agyRpc"`) || strings.Contains(string(b), "agy-rpc hook") {
			checks = append(checks, Check{
				Name:   "Claude Code hooks",
				Status: OK,
				Detail: fmt.Sprintf("wired in %s", paths.ClaudeSettingsPath()),
			})
		} else {
			checks = append(checks, Check{
				Name:   "Claude Code hooks",
				Status: Warn,
				Detail: fmt.Sprintf("%s exists without agy-rpc hooks", paths.ClaudeSettingsPath()),
				Fix:    "Run `agy-rpc setup` or `agy-rpc doctor --fix`",
			})
		}
	}

	// 5. Daemon status & heartbeat
	pid := daemon.AlivePID()
	if pid > 0 {
		info := daemon.ReadInfo("")
		extra := ""
		if info != nil && info.BeatAt > 0 {
			age := time.Since(time.UnixMilli(info.BeatAt)).Round(time.Second)
			extra = fmt.Sprintf(", heartbeat %s ago", age)
		}
		checks = append(checks, Check{
			Name:   "Daemon",
			Status: OK,
			Detail: fmt.Sprintf("running (pid %d%s)", pid, extra),
		})
	} else {
		checks = append(checks, Check{
			Name:   "Daemon",
			Status: Warn,
			Detail: "not running",
			Fix:    "Run `agy-rpc start` or `agy-rpc doctor --fix`",
		})
	}

	// 6. Discord IPC socket & log
	logBytes, _ := os.ReadFile(paths.LogPath())
	ipcState, _ := IPCStateFromLog(string(logBytes))
	bridge := BridgeFromLog(string(logBytes))
	sockFound := ""
	for _, p := range discord.CandidatePaths() {
		if _, err := os.Stat(p); err == nil {
			sockFound = p
			break
		}
	}
	switch {
	case ipcState == "connected":
		via := "desktop client"
		if bridge {
			via = "arRPC / Vesktop bridge"
		}
		checks = append(checks, Check{
			Name:   "Discord IPC",
			Status: OK,
			Detail: fmt.Sprintf("connected (%s)", via),
		})
	case sockFound != "":
		checks = append(checks, Check{
			Name:   "Discord IPC",
			Status: OK,
			Detail: fmt.Sprintf("socket present at %s (log state: %s)", sockFound, ipcState),
		})
	default:
		checks = append(checks, Check{
			Name:   "Discord IPC",
			Status: Warn,
			Detail: "no Discord IPC socket found (is Discord open?)",
		})
	}

	// 7. Concurrent claude-rpc daemon check
	if c := checkClaudeRPCConflict(); c != nil {
		checks = append(checks, *c)
	}

	// 8. Transcript directories & aggregate
	agyDirs := scanner.DiscoverBrainDirs()
	ccDirs := scanner.DiscoverClaudeProjectDirs()
	if len(agyDirs) == 0 && len(ccDirs) == 0 {
		checks = append(checks, Check{
			Name:   "Transcripts",
			Status: Warn,
			Detail: "no Antigravity brain or Claude Code project directories found yet",
		})
	} else {
		checks = append(checks, Check{
			Name:   "Transcripts",
			Status: OK,
			Detail: fmt.Sprintf("%d Antigravity dir(s), %d Claude Code dir(s)", len(agyDirs), len(ccDirs)),
		})
	}

	if agg := scanner.ReadAggregate(); agg != nil {
		checks = append(checks, Check{
			Name:   "Aggregate cache",
			Status: OK,
			Detail: fmt.Sprintf("%d sessions (%d agy, %d claude) in %s",
				agg.Sessions,
				agg.BySource["antigravity"].Sessions,
				agg.BySource["claude"].Sessions,
				paths.AggregatePath()),
		})
	} else {
		checks = append(checks, Check{
			Name:   "Aggregate cache",
			Status: Warn,
			Detail: "not built yet",
			Fix:    "Run `agy-rpc scan`",
		})
	}

	// 9. Current directory privacy
	if cwd, err := os.Getwd(); err == nil {
		res := privacy.Resolve(cwd, cfg)
		checks = append(checks, Check{
			Name:   "CWD privacy",
			Status: Info,
			Detail: fmt.Sprintf("%s -> %s (%s)", filepath.Base(cwd), res.Visibility, res.Reason),
		})
	}

	return checks
}

// Fix repairs hooks, config seed, aggregate cache, and daemon liveness.
func Fix() []string {
	var actions []string
	if created, err := install.SeedConfig(""); err == nil && created {
		actions = append(actions, "seeded "+paths.ConfigPath())
	}
	if changed, err := install.InstallAntigravityHooks("", ""); err == nil && changed {
		actions = append(actions, "wired Antigravity hooks in "+paths.AgyHooksPath())
	}
	cfg := config.Load()
	if cfg.Sources.WantClaude() {
		if changed, err := install.InstallClaudeHooks("", ""); err == nil && changed {
			actions = append(actions, "wired Claude Code hooks in "+paths.ClaudeSettingsPath())
		}
	}
	if scanner.ReadAggregate() == nil {
		res := scanner.Scan(scanner.ScanOptions{})
		actions = append(actions, fmt.Sprintf("scanned %d transcripts", res.Total))
	}
	if daemon.AlivePID() == 0 {
		daemon.ClearStopped()
		if pid, err := daemon.SpawnDetached(); err == nil {
			actions = append(actions, fmt.Sprintf("started daemon (pid %d)", pid))
		}
	}
	return actions
}
