package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"2.1.0", "2.0.0", true},
		{"v2.0.1", "2.0.0", true},
		{"2.0.0", "2.0.0", false},
		{"1.9.9", "2.0.0", false},
		{"2.10.0", "2.9.0", true},
		{"2.0.0", "2.0.0-rc.1", true},
		{"2.0.0-rc.2", "2.0.0", false},
		{"2.0.0", "dev", true},
		{"garbage", "2.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.cand, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}

func TestPickAssetAndNames(t *testing.T) {
	r := Release{Tag: "v2.1.0", Assets: []Asset{
		{Name: "vibe-rpc_2.1.0_darwin_all.tar.gz"},
		{Name: "vibe-rpc_2.1.0_linux_amd64.tar.gz"},
		{Name: "vibe-rpc_2.1.0_windows_amd64.zip"},
	}}
	for _, c := range []struct{ os, arch, want string }{
		{"linux", "amd64", "vibe-rpc_2.1.0_linux_amd64.tar.gz"},
		{"darwin", "arm64", "vibe-rpc_2.1.0_darwin_all.tar.gz"},
		{"windows", "amd64", "vibe-rpc_2.1.0_windows_amd64.zip"},
	} {
		a, err := PickAsset(r, c.os, c.arch)
		if err != nil || a.Name != c.want {
			t.Errorf("%s/%s: got %q, %v", c.os, c.arch, a.Name, err)
		}
	}
	if _, err := PickAsset(r, "linux", "arm64"); err == nil {
		t.Error("expected an error for a missing platform")
	}
	if ChecksumsName("v2.1.0") != "vibe-rpc_2.1.0_checksums.txt" {
		t.Error("checksums name")
	}
}

func TestManagedBy(t *testing.T) {
	if ManagedBy("/opt/homebrew/Cellar/vibe-rpc/2.0.0/bin/vibe-rpc") == "" {
		t.Error("Homebrew path not detected")
	}
	if ManagedBy("/usr/local/bin/vibe-rpc") != "" {
		t.Error("plain path flagged as managed")
	}
}

// fakeBinary is a shell script that answers --version like the real CLI.
func fakeBinary(version string) []byte {
	return []byte("#!/bin/sh\necho \"vibe-rpc version " + version + "\"\n")
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("hi")}, {name, body}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(f.body)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeGitHub serves a release for this platform. Mutators let tests corrupt
// the checksum or drop the checksums file.
func fakeGitHub(t *testing.T, version string, bin []byte, mutate func(sums *string, assets *[]Asset)) *httptest.Server {
	t.Helper()
	archiveName := ArchiveName(version, runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, "vibe-rpc_"+version+"/vibe-rpc", bin)
	sum := sha256.Sum256(archive)
	sums := fmt.Sprintf("%s  %s\nffff  other.tar.gz\n", hex.EncodeToString(sum[:]), archiveName)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assets := []Asset{
			{Name: archiveName, URL: srv.URL + "/dl/archive"},
			{Name: ChecksumsName(version), URL: srv.URL + "/dl/sums"},
		}
		s := sums
		if mutate != nil {
			mutate(&s, &assets)
		}
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			if r.Header.Get("User-Agent") == "" {
				http.Error(w, "UA required", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(Release{Tag: "v" + version, HTMLURL: "https://example.test/r", Assets: assets})
		case "/dl/archive":
			_, _ = w.Write(archive)
		case "/dl/sums":
			_, _ = w.Write([]byte(s))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchAndInstallEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script stand-in binary")
	}
	srv := fakeGitHub(t, "2.1.0", fakeBinary("2.1.0"), nil)
	c := Client{APIBase: srv.URL}
	ctx := context.Background()

	rel, err := c.Latest(ctx)
	if err != nil || rel.Version() != "2.1.0" {
		t.Fatalf("Latest = %+v, %v", rel, err)
	}
	bin, err := c.Fetch(ctx, rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(t.TempDir(), "vibe-rpc")
	if err := os.WriteFile(exe, fakeBinary("2.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(bin, exe, rel.Version()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if !strings.Contains(string(got), "2.1.0") {
		t.Fatalf("binary not replaced: %q", got)
	}
	if fi, _ := os.Stat(exe); fi.Mode()&0o111 == 0 {
		t.Fatal("installed binary is not executable")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".vibe-rpc-upgrade-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestFetchRejectsBadChecksumAndMissingSums(t *testing.T) {
	ctx := context.Background()
	bad := fakeGitHub(t, "2.1.0", fakeBinary("2.1.0"), func(s *string, _ *[]Asset) {
		*s = strings.Replace(*s, (*s)[:8], "00000000", 1)
	})
	rel, _ := Client{APIBase: bad.URL}.Latest(ctx)
	if _, err := (Client{APIBase: bad.URL}).Fetch(ctx, rel, runtime.GOOS, runtime.GOARCH); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}

	noSums := fakeGitHub(t, "2.1.0", fakeBinary("2.1.0"), func(_ *string, a *[]Asset) { *a = (*a)[:1] })
	rel, _ = Client{APIBase: noSums.URL}.Latest(ctx)
	if _, err := (Client{APIBase: noSums.URL}).Fetch(ctx, rel, runtime.GOOS, runtime.GOARCH); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("expected refusal without checksums, got %v", err)
	}
}

func TestInstallRefusesBinaryWithWrongVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script stand-in binary")
	}
	exe := filepath.Join(t.TempDir(), "vibe-rpc")
	orig := fakeBinary("2.0.0")
	_ = os.WriteFile(exe, orig, 0o755)
	if err := Install(fakeBinary("1.0.0"), exe, "2.1.0"); err == nil {
		t.Fatal("expected refusal for a binary reporting the wrong version")
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, orig) {
		t.Fatal("original binary was modified after a failed install")
	}
}

func TestLatestNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := (Client{APIBase: srv.URL}).Latest(context.Background()); err != ErrNoRelease {
		t.Fatalf("expected ErrNoRelease, got %v", err)
	}
}
