package notch

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chauveaul/vibe-rpc/internal/update"
)

const (
	appBundleName = "VibeNotch.app"
	maxExtract    = 200 << 20
)

// ZipName returns the expected release archive name for VibeNotch at version.
func ZipName(version string) string {
	return fmt.Sprintf("VibeNotch_%s_darwin_all.zip", strings.TrimPrefix(version, "v"))
}

// InstallDir returns the default user Applications directory (~/Applications).
func InstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications"), nil
}

// InstallOptions controls where and how VibeNotch.app is fetched and installed.
type InstallOptions struct {
	Client  update.Client
	DestDir string          // default ~/Applications
	Release *update.Release // optional pre-fetched release (tests)
}

// InstallResult summarizes a completed VibeNotch installation.
type InstallResult struct {
	Version  string
	AppPath  string
	Verified bool
}

// InstallLatest downloads VibeNotch.app from the latest GitHub release and
// installs it into DestDir (default ~/Applications) without requiring Xcode.
func InstallLatest(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	var rel update.Release
	if opts.Release != nil {
		rel = *opts.Release
	} else {
		var err error
		rel, err = opts.Client.Latest(ctx)
		if err != nil {
			return InstallResult{}, err
		}
	}

	asset, ok := pickNotchAsset(rel)
	if !ok {
		return InstallResult{}, fmt.Errorf("release %s does not include %s", rel.Tag, ZipName(rel.Version()))
	}

	zipBytes, err := opts.Client.Download(ctx, asset)
	if err != nil {
		return InstallResult{}, err
	}

	verified, err := verifyOptionalChecksum(ctx, opts.Client, rel, asset.Name, zipBytes)
	if err != nil {
		return InstallResult{}, err
	}

	destDir := opts.DestDir
	if destDir == "" {
		destDir, err = InstallDir()
		if err != nil {
			return InstallResult{}, err
		}
	}
	appPath, err := InstallFromZip(zipBytes, destDir)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{
		Version:  rel.Version(),
		AppPath:  appPath,
		Verified: verified,
	}, nil
}

func pickNotchAsset(r update.Release) (update.Asset, bool) {
	for _, name := range []string{ZipName(r.Version()), "VibeNotch.zip"} {
		if a, ok := update.FindAsset(r, name); ok {
			return a, true
		}
	}
	return update.Asset{}, false
}

// verifyOptionalChecksum checks a SHA-256 sidecar (<zip>.sha256) or an entry in
// the release's checksums file when present. Returns (true, nil) if verified,
// (false, nil) if the release didn't publish a checksum for the notch zip, or
// an error if a published checksum didn't match.
func verifyOptionalChecksum(ctx context.Context, client update.Client, r update.Release, assetName string, data []byte) (bool, error) {
	var want string
	if sidecar, ok := update.FindAsset(r, assetName+".sha256"); ok {
		b, err := client.Download(ctx, sidecar)
		if err != nil {
			return false, err
		}
		fields := strings.Fields(string(b))
		if len(fields) == 0 {
			return false, fmt.Errorf("empty checksum file %s", sidecar.Name)
		}
		want = strings.ToLower(fields[0])
	} else if sumsAsset, ok := update.FindAsset(r, update.ChecksumsName(r.Version())); ok {
		if b, err := client.Download(ctx, sumsAsset); err == nil {
			if s, err := update.ExpectedSum(b, assetName); err == nil {
				want = s
			}
		}
	}
	if want == "" {
		return false, nil
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return false, fmt.Errorf("checksum mismatch for %s; download may be corrupted or tampered with", assetName)
	}
	return true, nil
}

// InstallFromZip safely extracts VibeNotch.app from zipBytes and atomically
// swaps it into destDir/VibeNotch.app.
func InstallFromZip(zipBytes []byte, destDir string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	stageDir, err := os.MkdirTemp(destDir, ".VibeNotch-install-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stageDir)

	var total int64
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if name == "" || strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") {
			continue
		}
		clean := filepath.Clean(name)
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return "", fmt.Errorf("invalid archive path %q", f.Name)
		}
		// Only extract entries inside VibeNotch.app.
		if clean != appBundleName && !strings.HasPrefix(clean, appBundleName+string(filepath.Separator)) {
			continue
		}
		target := filepath.Join(stageDir, clean)
		if rel, err := filepath.Rel(stageDir, target); err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("archive path escapes destination: %q", f.Name)
		}
		mode := f.Mode()
		switch {
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			linkBytes, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			if err := os.Symlink(string(linkBytes), target); err != nil {
				return "", err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			perm := mode.Perm()
			if perm == 0 {
				perm = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				rc.Close()
				return "", err
			}
			n, copyErr := io.Copy(out, io.LimitReader(rc, maxExtract-total+1))
			rc.Close()
			closeErr := out.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			total += n
			if total > maxExtract {
				return "", errors.New("extracted archive exceeds size limit")
			}
		}
	}

	stagedApp := filepath.Join(stageDir, appBundleName)
	binPath := filepath.Join(stagedApp, "Contents", "MacOS", "VibeNotch")
	plistPath := filepath.Join(stagedApp, "Contents", "Info.plist")
	if fi, err := os.Stat(binPath); err != nil || fi.IsDir() {
		return "", errors.New("archive does not contain a valid VibeNotch.app bundle")
	}
	if fi, err := os.Stat(plistPath); err != nil || fi.IsDir() {
		return "", errors.New("archive is missing VibeNotch.app/Contents/Info.plist")
	}
	_ = os.Chmod(binPath, 0o755)

	finalApp := filepath.Join(destDir, appBundleName)
	backupApp := ""
	if _, err := os.Stat(finalApp); err == nil {
		backupApp = filepath.Join(stageDir, "backup-"+appBundleName)
		if err := os.Rename(finalApp, backupApp); err != nil {
			return "", fmt.Errorf("replace existing %s: %w", finalApp, err)
		}
	}
	if err := os.Rename(stagedApp, finalApp); err != nil {
		if backupApp != "" {
			_ = os.Rename(backupApp, finalApp)
		}
		return "", fmt.Errorf("install %s: %w", finalApp, err)
	}
	return finalApp, nil
}
