// Package update implements `vibe-rpc upgrade`: it checks GitHub Releases for
// a newer version, downloads the archive for this OS/arch, verifies it against
// the release's SHA-256 checksums file, sanity-checks the new binary, and
// atomically swaps it in for the running executable.
//
// Release assets are expected to follow GoReleaser's default naming:
//
//	vibe-rpc_<version>_<os>_<arch>.tar.gz   (.zip on Windows; arch "all" for
//	                                         macOS universal binaries)
//	vibe-rpc_<version>_checksums.txt
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	Repo        = "chauveaul/vibe-rpc"
	ProjectName = "vibe-rpc"
	// maxDownload caps archive size so a bad release can't fill the disk.
	maxDownload = 200 << 20
)

// ErrNoRelease means the repository has no published (non-draft) release.
var ErrNoRelease = errors.New("no published release found")

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is the subset of the GitHub release payload we use.
type Release struct {
	Tag     string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Version returns the tag without a leading "v".
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Client talks to GitHub. APIBase is overridable for tests.
type Client struct {
	HTTP    *http.Client
	APIBase string // default https://api.github.com
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", ProjectName+"-updater")
	return c.http().Do(req)
}

// Latest fetches the newest published release.
func (c Client) Latest(ctx context.Context) (Release, error) {
	base := c.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	resp, err := c.get(ctx, strings.TrimRight(base, "/")+"/repos/"+Repo+"/releases/latest")
	if err != nil {
		return Release{}, fmt.Errorf("couldn't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, ErrNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return Release{}, fmt.Errorf("GitHub rate limit hit (HTTP %d); try again later", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Release{}, fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return Release{}, fmt.Errorf("couldn't read release info: %w", err)
	}
	if r.Tag == "" {
		return Release{}, ErrNoRelease
	}
	return r, nil
}

// ── Versions ─────────────────────────────────────────────────────────────────

// parseVersion reads "v1.2.3", "1.2.3-rc.1" or "1.2" into major/minor/patch
// plus a prerelease flag. ok is false for non-numeric versions like "dev".
func parseVersion(v string) (nums [3]int, pre bool, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "+"); i >= 0 {
		v = v[:i]
	}
	if i := strings.Index(v, "-"); i >= 0 {
		pre = true
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return nums, pre, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, pre, false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// Newer reports whether candidate is a higher version than current. A
// non-numeric current version (e.g. a "dev" build) is never auto-upgraded
// unless candidate parses and current doesn't — then any release is newer.
func Newer(candidate, current string) bool {
	c, cPre, cOK := parseVersion(candidate)
	if !cOK {
		return false
	}
	n, nPre, nOK := parseVersion(current)
	if !nOK {
		return true
	}
	for i := 0; i < 3; i++ {
		if c[i] != n[i] {
			return c[i] > n[i]
		}
	}
	// Same numbers: a final release beats a prerelease of it.
	return nPre && !cPre
}

// ── Assets ───────────────────────────────────────────────────────────────────

// ArchiveName is the expected archive for goos/goarch at version.
func ArchiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", ProjectName, strings.TrimPrefix(version, "v"), goos, goarch, ext)
}

// ChecksumsName is GoReleaser's default checksums file name.
func ChecksumsName(version string) string {
	return fmt.Sprintf("%s_%s_checksums.txt", ProjectName, strings.TrimPrefix(version, "v"))
}

// PickAsset finds this platform's archive, falling back to a macOS universal
// ("all") build.
func PickAsset(r Release, goos, goarch string) (Asset, error) {
	want := []string{ArchiveName(r.Version(), goos, goarch)}
	if goos == "darwin" {
		want = append(want, ArchiveName(r.Version(), goos, "all"))
	}
	for _, w := range want {
		for _, a := range r.Assets {
			if a.Name == w {
				return a, nil
			}
		}
	}
	return Asset{}, fmt.Errorf("release %s has no build for %s/%s (looked for %s)", r.Tag, goos, goarch, strings.Join(want, ", "))
}

// FindAsset returns the named asset from r if present.
func FindAsset(r Release, name string) (Asset, bool) { return findAsset(r, name) }

// Download fetches an asset's bytes with the client's size limit and timeouts.
func (c Client) Download(ctx context.Context, a Asset) ([]byte, error) { return c.download(ctx, a) }

// ExpectedSum looks up name's SHA-256 hex digest in a sha256sum-style file.
func ExpectedSum(checksums []byte, name string) (string, error) {
	return expectedSum(checksums, name)
}

func findAsset(r Release, name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

func (c Client) download(ctx context.Context, a Asset) ([]byte, error) {
	resp, err := c.get(ctx, a.URL)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", a.Name, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	if len(b) > maxDownload {
		return nil, fmt.Errorf("download %s: larger than %d MB", a.Name, maxDownload>>20)
	}
	return b, nil
}

// expectedSum finds name's SHA-256 in a `sha256sum`-style checksums file.
func expectedSum(checksums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums file has no entry for %s", name)
}

// extractBinary pulls the vibe-rpc executable out of a .tar.gz or .zip.
func extractBinary(archive []byte, archiveName, goos string) ([]byte, error) {
	bin := ProjectName
	if goos == "windows" {
		bin += ".exe"
	}
	if strings.HasSuffix(archiveName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == bin && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, fmt.Errorf("%s not found in %s", bin, archiveName)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == bin {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
	return nil, fmt.Errorf("%s not found in %s", bin, archiveName)
}

// Fetch downloads, verifies and extracts the new binary for this platform.
func (c Client) Fetch(ctx context.Context, r Release, goos, goarch string) ([]byte, error) {
	a, err := PickAsset(r, goos, goarch)
	if err != nil {
		return nil, err
	}
	sumsAsset, ok := findAsset(r, ChecksumsName(r.Version()))
	if !ok {
		return nil, fmt.Errorf("release %s has no %s; refusing to install an unverified binary", r.Tag, ChecksumsName(r.Version()))
	}
	sums, err := c.download(ctx, sumsAsset)
	if err != nil {
		return nil, err
	}
	want, err := expectedSum(sums, a.Name)
	if err != nil {
		return nil, err
	}
	archive, err := c.download(ctx, a)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s; download may be corrupted or tampered with", a.Name)
	}
	return extractBinary(archive, a.Name, goos)
}

// ── Install ──────────────────────────────────────────────────────────────────

// ManagedBy names a package manager that owns exe ("" when self-managed), so
// we don't fight it by overwriting its files.
func ManagedBy(exe string) string {
	p := filepath.ToSlash(exe)
	switch {
	case strings.Contains(p, "/Cellar/") || strings.Contains(p, "/homebrew/") || strings.Contains(p, "/linuxbrew/"):
		return "Homebrew (run: brew upgrade vibe-rpc)"
	case strings.Contains(p, "/nix/store/"):
		return "Nix (run: nix profile upgrade vibe-rpc, or update your flake inputs)"
	case strings.HasPrefix(p, "/usr/bin/"):
		return "the system package manager (on Arch, update vibe-rpc-bin with your AUR helper)"
	case strings.Contains(strings.ToLower(p), "/scoop/apps/"):
		return "Scoop (run: scoop update vibe-rpc)"
	}
	return ""
}

// Executable returns the real path of the running binary.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// Install writes bin next to exe, checks it runs and reports wantVersion,
// then atomically replaces exe. On Windows the running file is moved aside
// to exe+".old" first, since it can't be overwritten while running.
func Install(bin []byte, exe, wantVersion string) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".vibe-rpc-upgrade-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("no permission to write to %s; re-run with sudo or reinstall somewhere you own", dir)
		}
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return err
	}
	if err := verifyRuns(tmpPath, wantVersion); err != nil {
		cleanup()
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			cleanup()
			return err
		}
		if err := os.Rename(tmpPath, exe); err != nil {
			_ = os.Rename(old, exe)
			cleanup()
			return err
		}
		return nil
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		cleanup()
		return err
	}
	return nil
}

// verifyRuns executes `bin --version` and checks it reports wantVersion.
func verifyRuns(bin, wantVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("downloaded binary failed to run (%v); not installing", err)
	}
	if wantVersion != "" && !strings.Contains(string(out), strings.TrimPrefix(wantVersion, "v")) {
		return fmt.Errorf("downloaded binary reports %q, expected %s; not installing", strings.TrimSpace(string(out)), wantVersion)
	}
	return nil
}
