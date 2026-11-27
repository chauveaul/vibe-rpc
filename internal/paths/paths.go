package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Version is overridden at release build time via
// -ldflags "-X github.com/chauveaul/vibe-rpc/internal/paths.Version=<tag>".
var Version = "2.0.0"

func HomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func UserConfigDir() string {
	dir := userConfigDirFor("vibe-rpc")
	migrateOnce(userConfigDirFor("agy-rpc"), dir)
	return dir
}

// migrateOnce moves a pre-rename agy-rpc directory to its vibe-rpc location
// exactly once per process, only when the new location does not exist yet.
var migrated sync.Map

func migrateOnce(oldDir, newDir string) {
	if _, loaded := migrated.LoadOrStore(newDir, true); loaded {
		return
	}
	if _, err := os.Stat(newDir); err == nil {
		return
	}
	if fi, err := os.Stat(oldDir); err == nil && fi.IsDir() {
		_ = os.MkdirAll(filepath.Dir(newDir), 0o755)
		_ = os.Rename(oldDir, newDir)
	}
}

func userConfigDirFor(name string) string {
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, name)
		}
		return filepath.Join(HomeDir(), "AppData", "Roaming", name)
	case "darwin":
		return filepath.Join(HomeDir(), "Library", "Application Support", name)
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, name)
		}
		return filepath.Join(HomeDir(), ".config", name)
	}
}

func ConfigPath() string {
	if p := os.Getenv("VIBE_RPC_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(UserConfigDir(), "config.json")
}

func StateDir() string {
	if d := os.Getenv("VIBE_RPC_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "vibe-rpc")
}

func StatePath() string      { return filepath.Join(StateDir(), "state.json") }
func PidPath() string        { return filepath.Join(StateDir(), "daemon.pid") }
func LogPath() string        { return filepath.Join(StateDir(), "daemon.log") }
func DaemonInfoPath() string { return filepath.Join(StateDir(), "daemon.json") }
func PausePath() string      { return filepath.Join(StateDir(), "pause.json") }
func SpawnMarker() string    { return filepath.Join(StateDir(), "daemon-spawn.ts") }

func DataDir() string {
	if d := os.Getenv("VIBE_RPC_DATA_DIR"); d != "" {
		return d
	}
	dir := filepath.Join(HomeDir(), ".vibe-rpc")
	migrateOnce(filepath.Join(HomeDir(), ".agy-rpc"), dir)
	return dir
}

func AggregatePath() string   { return filepath.Join(DataDir(), "aggregate.json") }
func ScanCachePath() string   { return filepath.Join(DataDir(), "scan-cache.json") }
func EventsLogPath() string   { return filepath.Join(DataDir(), "events.jsonl") }
func SessionCwdsPath() string { return filepath.Join(DataDir(), "session-cwds.json") }
func PrivateListPath() string {
	if p := os.Getenv("VIBE_RPC_PRIVATE_LIST"); p != "" {
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
func AgyCLISettingsPath() string {
	return filepath.Join(GeminiHome(), "antigravity-cli", "settings.json")
}

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
func ClaudeConfigPath() string {
	if p := os.Getenv("VIBE_RPC_CLAUDE_CONFIG"); p != "" {
		return p
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(HomeDir(), ".claude.json")
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
		return "vibe-rpc.exe"
	}
	return "vibe-rpc"
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
