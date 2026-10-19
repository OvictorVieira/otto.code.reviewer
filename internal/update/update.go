// Package update checks for a newer Otto release and replaces the running
// binary with it, after verifying the release checksum.
package update

import (
	"archive/tar"
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
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const repo = "OvictorVieira/otto.code.reviewer"

// Updater knows the running version and where releases live.
type Updater struct {
	Current   string
	StatePath string                                // remembers the last check and declines
	BaseURL   string                                // release download base, default GitHub
	Exe       string                                // binary to replace, default the running one
	Latest    func(context.Context) (string, error) // newest release tag, default GitHub API
	Now       func() time.Time
}

type state struct {
	CheckedAt  time.Time `json:"checked_at"`
	Latest     string    `json:"latest"`
	Declined   string    `json:"declined"`
	DeclinedAt time.Time `json:"declined_at"`
}

const (
	checkEvery  = time.Hour
	declineFor  = 24 * time.Hour
	httpTimeout = 20 * time.Second
)

var semver = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func parse(v string) ([3]int, bool) {
	m := semver.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// Newer reports whether latest is a higher release than current.
// Development builds ("dev") never update.
func Newer(current, latest string) bool {
	c, okC := parse(current)
	l, okL := parse(latest)
	if !okC || !okL {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) load() state {
	var s state
	if b, err := os.ReadFile(u.StatePath); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (u *Updater) save(s state) {
	if u.StatePath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(u.StatePath), 0o700)
	if b, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(u.StatePath, b, 0o600)
	}
}

// Available returns the newest release tag when it is newer than the running
// version. The answer is cached for an hour. A version the user declined is
// not offered again for a day unless force is set. Network failures are
// silent: no network means no update.
func (u *Updater) Available(ctx context.Context, force bool) (string, bool) {
	if _, ok := parse(u.Current); !ok {
		return "", false
	}
	s := u.load()
	if s.Latest == "" || u.now().Sub(s.CheckedAt) > checkEvery {
		latest := u.Latest
		if latest == nil {
			latest = githubLatest
		}
		tag, err := latest(ctx)
		if err != nil {
			return "", false
		}
		s.Latest, s.CheckedAt = tag, u.now()
		u.save(s)
	}
	if !Newer(u.Current, s.Latest) {
		return "", false
	}
	if !force && s.Declined == s.Latest && u.now().Sub(s.DeclinedAt) < declineFor {
		return "", false
	}
	return s.Latest, true
}

// Decline remembers that the user said no to this version.
func (u *Updater) Decline(tag string) {
	s := u.load()
	s.Declined, s.DeclinedAt = tag, u.now()
	u.save(s)
}

func githubLatest(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.Tag, nil
}

func (u *Updater) get(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout*3)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// Install downloads release tag, verifies its checksum and atomically
// replaces the binary. The old binary stays untouched on any failure.
func (u *Updater) Install(ctx context.Context, tag string) error {
	base := u.BaseURL
	if base == "" {
		base = "https://github.com/" + repo + "/releases/download"
	}
	exe := u.Exe
	if exe == "" {
		p, err := os.Executable()
		if err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(p); err != nil {
			return err
		}
	}

	asset := fmt.Sprintf("otto_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive, err := u.get(ctx, fmt.Sprintf("%s/%s/%s", base, tag, asset))
	if err != nil {
		return err
	}
	sums, err := u.get(ctx, fmt.Sprintf("%s/%s/checksums.txt", base, tag))
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	got := sha256.Sum256(archive)
	if want == "" || hex.EncodeToString(got[:]) != want {
		return errors.New("checksum mismatch: the download is corrupted or tampered with")
	}

	bin, err := extract(archive)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".otto-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}

func extract(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("release archive has no otto binary")
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == "otto" {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}
