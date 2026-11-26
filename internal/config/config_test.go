package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveDiffOnlyWritesOverrides(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.json")

	base := Default()
	edited := Default()
	edited.AppName = "My Agy"
	edited.IdleThresholdSec = 90

	loaded, err := SaveDiff(cfgPath, base, edited)
	if err != nil {
		t.Fatalf("SaveDiff failed: %v", err)
	}
	if loaded.AppName != "My Agy" || loaded.IdleThresholdSec != 90 {
		t.Fatalf("unexpected reloaded config: appName=%q idle=%d", loaded.AppName, loaded.IdleThresholdSec)
	}
	raw, _ := os.ReadFile(cfgPath)
	if len(raw) > 200 {
		t.Fatalf("expected sparse diff file, got %d bytes: %s", len(raw), string(raw))
	}

	// Reject malformed client ID
	bad := edited
	bad.ClientID = "not-a-snowflake"
	if _, err := SaveDiff(cfgPath, loaded, bad); err == nil {
		t.Fatalf("expected validation error for bad clientId")
	}
}
