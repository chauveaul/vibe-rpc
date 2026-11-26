package notch

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chauveaul/vibe-rpc/internal/update"
)

func makeTestNotchZip(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		"VibeNotch.app/Contents/Info.plist":      {body: "<plist/>", mode: 0o644},
		"VibeNotch.app/Contents/MacOS/VibeNotch": {body: "#!/bin/sh\necho ok\n", mode: 0o755},
		"__MACOSX/VibeNotch.app/._Info.plist":    {body: "junk", mode: 0o644},
	}
	for k, v := range extra {
		files[k] = struct {
			body string
			mode os.FileMode
		}{body: v, mode: 0o644}
	}
	for name, spec := range files {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(spec.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(spec.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallFromZipReplacesAndSkipsMacOSX(t *testing.T) {
	dest := t.TempDir()
	// Pre-seed an old installation to verify replacement works cleanly.
	oldBin := filepath.Join(dest, "VibeNotch.app", "Contents", "MacOS", "VibeNotch")
	_ = os.MkdirAll(filepath.Dir(oldBin), 0o755)
	_ = os.WriteFile(oldBin, []byte("old"), 0o755)

	appPath, err := InstallFromZip(makeTestNotchZip(t, nil), dest)
	if err != nil {
		t.Fatalf("InstallFromZip: %v", err)
	}
	if appPath != filepath.Join(dest, "VibeNotch.app") {
		t.Fatalf("appPath = %q", appPath)
	}
	b, err := os.ReadFile(filepath.Join(appPath, "Contents", "MacOS", "VibeNotch"))
	if err != nil || !strings.Contains(string(b), "echo ok") {
		t.Fatalf("binary = %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "__MACOSX")); err == nil {
		t.Fatal("__MACOSX metadata must not be extracted")
	}
}

func TestInstallFromZipRejectsZipSlip(t *testing.T) {
	z := makeTestNotchZip(t, map[string]string{"../evil.txt": "nope"})
	if _, err := InstallFromZip(z, t.TempDir()); err == nil {
		t.Fatal("expected zip-slip error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInstallLatestWithAndWithoutChecksum(t *testing.T) {
	zipBytes := makeTestNotchZip(t, nil)
	sum := sha256.Sum256(zipBytes)
	hexSum := hex.EncodeToString(sum[:])

	var withSidecar bool
	var badSum bool
	baseURL := "https://mock.github.test"
	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp := func(status int, body []byte) *http.Response {
				return &http.Response{
					StatusCode: status,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(body)),
					Request:    r,
				}
			}
			switch r.URL.Path {
			case "/repos/" + update.Repo + "/releases/latest":
				assets := []update.Asset{
					{Name: "VibeNotch_2.3.0_darwin_all.zip", URL: baseURL + "/notch.zip"},
				}
				if withSidecar {
					assets = append(assets, update.Asset{Name: "VibeNotch_2.3.0_darwin_all.zip.sha256", URL: baseURL + "/notch.sha256"})
				}
				b, _ := json.Marshal(update.Release{Tag: "v2.3.0", Assets: assets})
				return resp(http.StatusOK, b), nil
			case "/notch.zip":
				return resp(http.StatusOK, zipBytes), nil
			case "/notch.sha256":
				if badSum {
					return resp(http.StatusOK, []byte(strings.Repeat("0", 64)+"  VibeNotch_2.3.0_darwin_all.zip\n")), nil
				}
				return resp(http.StatusOK, []byte(hexSum+"  VibeNotch_2.3.0_darwin_all.zip\n")), nil
			default:
				return resp(http.StatusNotFound, nil), nil
			}
		}),
	}

	client := update.Client{HTTP: httpClient, APIBase: baseURL}

	// 1. Without sidecar (e.g. v2.2.0): installs cleanly with Verified=false.
	res, err := InstallLatest(context.Background(), InstallOptions{Client: client, DestDir: t.TempDir()})
	if err != nil || res.Version != "2.3.0" || res.Verified {
		t.Fatalf("without sidecar: res=%+v err=%v", res, err)
	}

	// 2. With valid sidecar: installs with Verified=true.
	withSidecar = true
	res, err = InstallLatest(context.Background(), InstallOptions{Client: client, DestDir: t.TempDir()})
	if err != nil || !res.Verified {
		t.Fatalf("with valid sidecar: res=%+v err=%v", res, err)
	}

	// 3. With mismatched sidecar: refuses to install.
	badSum = true
	if _, err := InstallLatest(context.Background(), InstallOptions{Client: client, DestDir: t.TempDir()}); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}
