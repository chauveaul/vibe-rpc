package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const Version = "2.0.0"

func HomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func UserConfigDir() string {
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "agy-rpc")
		}
		return filepath.Join(HomeDir(), "AppData", "Roaming", "agy-rpc")
	case "darwin":
		return filepath.Join(HomeDir(), "Library", "Application Support", "agy-rpc")
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "agy-rpc")
		}
		return filepath.Join(HomeDir(), ".config", "agy-rpc")
	}
}

func ConfigPath() string {
	if p := os.Getenv("AGY_RPC_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(UserConfigDir(), "config.json")
}

func StateDir() string {
	if d := os.Getenv("AGY_RPC_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "agy-rpc")
}

func StatePath() string      { return filepath.Join(StateDir(), "state.json") }
func PidPath() string        { return filepath.Join(StateDir(), "daemon.pid") }
func LogPath() string        { return filepath.Join(StateDir(), "daemon.log") }
func DaemonInfoPath() string { return filepath.Join(StateDir(), "daemon.json") }
func PausePath() string      { return filepath.Join(StateDir(), "pause.json") }
func SpawnMarker() string    { return filepath.Join(StateDir(), "daemon-spawn.ts") }

func DataDir() string {
	if d := os.Getenv("AGY_RPC_DATA_DIR"); d != "" {
		return d
	}
	return filepath.Join(HomeDir(), ".agy-rpc")
}

func AggregatePath() string   { return filepath.Join(DataDir(), "aggregate.json") }
func ScanCachePath() string   { return filepath.Join(DataDir(), "scan-cache.json") }
func EventsLogPath() string   { return filepath.Join(DataDir(), "events.jsonl") }
func SessionCwdsPath() string { return filepath.Join(DataDir(), "session-cwds.json") }
func PrivateListPath() string {
	if p := os.Getenv("AGY_RPC_PRIVATE_LIST"); p != "" {
		return p
	}
	return filepath.Join(DataDir(), "private-list.json")
}
func VersionStampPath() string { return filepath.Join(DataDir(), "version") }
func StoppedPath() string      { return filepath.Join(DataDir(), "stopped") }

// Antigravity directories (CLI, Antigravity 2.0 App, Antigravity IDE).
func GeminiHome() string       { return filepath.Join(HomeDir(), ".gemini") }
func AgyConfigDir() string     { return filepath.Join(GeminiHome(), "config") }
func AgyHooksPath() string     { return filepath.Join(AgyConfigDir(), "hooks.json") }
func AgyMcpConfigPath() string { return filepath.Join(AgyConfigDir(), "mcp_config.json") }

func AgyBrainDirs() []string {
	g := GeminiHome()
	return []string{
		filepath.Join(g, "antigravity-cli", "brain"),
		filepath.Join(g, "antigravity", "brain"),
		filepath.Join(g, "antigravity-ide", "brain"),
	}
}

// Claude Code directories for combined activity tracking.
func ClaudeHome() string        { return filepath.Join(HomeDir(), ".claude") }
func ClaudeProjectsDir() string { return filepath.Join(ClaudeHome(), "projects") }
func ClaudeSettingsPath() string {
	return filepath.Join(ClaudeHome(), "settings.json")
}
func ClaudeRPCStateDir() string {
	if d := os.Getenv("CLAUDE_RPC_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "claude-rpc")
}
func ClaudeRPCPidPath() string { return filepath.Join(ClaudeRPCStateDir(), "daemon.pid") }

func CanonicalExeName() string {
	if runtime.GOOS == "windows" {
		return "agy-rpc.exe"
	}
	return "agy-rpc"
}

func CanonicalInstallDir() string { return filepath.Join(UserConfigDir(), "bin") }
func CanonicalExePath() string    { return filepath.Join(CanonicalInstallDir(), CanonicalExeName()) }

// WriteFileAtomic writes data via a sibling temp file + atomic rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
