// Package detect finds which coding agents are installed on this machine, so
// Settings and `vibe-rpc setup` can offer only the ones you actually use.
package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/chauveaul/vibe-rpc/internal/config"
)

// Agent is one coding agent and where (if anywhere) it was found.
type Agent struct {
	Key       string // antigravity | claude | opencode
	Name      string
	Installed bool
	Where     string // what was found, for display
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p != "" && exists(p) {
			return p
		}
	}
	return ""
}

func onPath(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// OpenCodeBinary finds the opencode CLI, or "" if it isn't installed.
func OpenCodeBinary() string {
	if p := onPath("opencode"); p != "" {
		return p
	}
	h := home()
	return firstExisting(
		filepath.Join(h, ".opencode", "bin", "opencode"),
		"/opt/homebrew/bin/opencode",
		"/usr/local/bin/opencode",
		filepath.Join(h, ".local", "bin", "opencode"),
	)
}

func find(key string) string {
	h := home()
	switch key {
	case "claude":
		if p := onPath("claude"); p != "" {
			return p
		}
		return firstExisting(filepath.Join(h, ".claude", "local", "claude"), filepath.Join(h, ".local", "bin", "claude"), filepath.Join(h, ".claude"))
	case "opencode":
		return OpenCodeBinary()
	default: // antigravity
		app := ""
		if runtime.GOOS == "darwin" {
			app = firstExisting("/Applications/Antigravity.app", filepath.Join(h, "Applications", "Antigravity.app"))
		}
		if app != "" {
			return app
		}
		if p := onPath("antigravity", "agy"); p != "" {
			return p
		}
		return firstExisting(filepath.Join(h, ".gemini", "antigravity"), filepath.Join(h, ".gemini", "antigravity-cli"))
	}
}

// All reports every known agent, installed or not, in display order.
func All() []Agent {
	out := make([]Agent, 0, len(config.Agents))
	for _, k := range config.Agents {
		w := find(k)
		out = append(out, Agent{Key: k, Name: config.AgentName(k), Installed: w != "", Where: w})
	}
	return out
}
