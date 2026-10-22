package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNewerComparesSemverNumerically(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"v0.1.3", "v0.1.4", true},
		{"v0.1.9", "v0.1.10", true}, // not a string comparison
		{"v0.2.0", "v0.1.99", false},
		{"v0.1.3", "v0.1.3", false},
		{"dev", "v9.9.9", false}, // local builds never self-update
		{"v0.1.3", "garbage", false},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.cur, c.latest, got, c.want)
		}
	}
}

func TestCheckUsesCacheAndRespectsDecline(t *testing.T) {
	calls := 0
	u := &Updater{
		Current:   "v0.1.3",
		StatePath: filepath.Join(t.TempDir(), "update.json"),
		Latest:    func(context.Context) (string, error) { calls++; return "v0.1.4", nil },
		Now:       func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
	}

	tag, ok := u.Available(context.Background(), false)
	if !ok || tag != "v0.1.4" {
		t.Fatalf("Available = %q, %v", tag, ok)
	}
	u.Available(context.Background(), false)
	if calls != 1 {
		t.Errorf("latest fetched %d times, want 1 (cached)", calls)
	}

	u.Decline("v0.1.4")
	if _, ok := u.Available(context.Background(), false); ok {
		t.Error("a declined version must not be offered again right away")
	}
	if _, ok := u.Available(context.Background(), true); !ok {
		t.Error("forced check (auto-update) ignores a decline")
	}
	u.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	if _, ok := u.Available(context.Background(), false); !ok {
		t.Error("a decline expires after a day")
	}
}

func TestCheckFailureIsSilent(t *testing.T) {
	u := &Updater{Current: "v0.1.3", StatePath: filepath.Join(t.TempDir(), "u.json"),
		Latest: func(context.Context) (string, error) { return "", errors.New("offline") }}
	if _, ok := u.Available(context.Background(), true); ok {
		t.Error("no network must mean no update, not an error")
	}
}

func TestLatestVersionAlwaysChecksAndValidatesTheReleaseTag(t *testing.T) {
	calls := 0
	u := &Updater{
		StatePath: filepath.Join(t.TempDir(), "update.json"),
		Latest: func(context.Context) (string, error) {
			calls++
			return "v0.1.6", nil
		},
	}
	for i := 0; i < 2; i++ {
		tag, err := u.LatestVersion(context.Background())
		if err != nil || tag != "v0.1.6" {
			t.Fatalf("LatestVersion = %q, %v", tag, err)
		}
	}
	if calls != 2 {
		t.Errorf("latest endpoint called %d times, want a fresh check each time", calls)
	}

	u.Latest = func(context.Context) (string, error) { return "latest", nil }
	if _, err := u.LatestVersion(context.Background()); err == nil {
		t.Error("invalid release tag must fail")
	}
}

func release(t *testing.T, body string) (*httptest.Server, string) {
	t.Helper()
	var tgz []byte
	{
		var buf = &bytesBuf{}
		gz := gzip.NewWriter(buf)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: "otto", Mode: 0o755, Size: int64(len(body))})
		tw.Write([]byte(body))
		tw.Close()
		gz.Close()
		tgz = buf.b
	}
	sum := sha256.Sum256(tgz)
	asset := fmt.Sprintf("otto_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/v0.1.4/"+asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(tgz) })
	mux.HandleFunc("/dl/v0.1.4/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})
	return httptest.NewServer(mux), asset
}

type bytesBuf struct{ b []byte }

func (b *bytesBuf) Write(p []byte) (int, error) { b.b = append(b.b, p...); return len(p), nil }

func TestInstallReplacesBinaryAfterVerifyingChecksum(t *testing.T) {
	srv, _ := release(t, "NEW BINARY")
	defer srv.Close()
	exe := filepath.Join(t.TempDir(), "otto")
	os.WriteFile(exe, []byte("OLD BINARY"), 0o755)
	u := &Updater{Current: "v0.1.3", BaseURL: srv.URL + "/dl", Exe: exe}

	if err := u.Install(context.Background(), "v0.1.4"); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(exe)
	if string(got) != "NEW BINARY" {
		t.Errorf("binary = %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode()&0o111 == 0 {
		t.Error("installed binary must stay executable")
	}
}

func TestInstallRefusesTamperedDownload(t *testing.T) {
	srv, asset := release(t, "NEW BINARY")
	defer srv.Close()
	// Serve a checksum that does not match the archive.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "checksums.txt" {
			fmt.Fprintf(w, "%064d  %s\n", 0, asset)
			return
		}
		resp, err := http.Get(srv.URL + r.URL.Path)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 1<<20)
		n, _ := resp.Body.Read(buf)
		w.Write(buf[:n])
	}))
	defer bad.Close()
	exe := filepath.Join(t.TempDir(), "otto")
	os.WriteFile(exe, []byte("OLD BINARY"), 0o755)
	u := &Updater{Current: "v0.1.3", BaseURL: bad.URL + "/dl", Exe: exe}

	if err := u.Install(context.Background(), "v0.1.4"); err == nil {
		t.Fatal("a checksum mismatch must fail")
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD BINARY" {
		t.Errorf("the old binary must stay untouched, got %q", got)
	}
}

func TestFingerprintChangesWhenTheBinaryIsReplacedAndIsEmptyWhenUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otto")
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := FingerprintOf(path)
	if before == "" {
		t.Fatal("fingerprint of an existing binary must not be empty")
	}
	if FingerprintOf(path) != before {
		t.Error("fingerprint must be stable while the binary does not change")
	}

	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("a newer, longer binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if FingerprintOf(path) == before {
		t.Error("fingerprint must change after the binary is replaced")
	}

	if got := FingerprintOf(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("fingerprint of a missing file = %q, want empty", got)
	}
}
