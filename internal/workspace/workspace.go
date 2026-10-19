// Package workspace prepares a disposable git worktree at a pull request head.
// Each repository is cloned once into a cache; every review gets its own
// worktree, which is removed afterwards.
package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree is a checkout of one pull request head.
type Worktree struct {
	path  string
	cache string
}

// Dir is the checkout directory.
func (w *Worktree) Dir() string { return w.path }

// Git prepares worktrees with the local git and gh CLIs.
type Git struct{}

// Prepare implements the checkout port used by the app.
func (Git) Prepare(ctx context.Context, repo string, number int, base string) (*Worktree, error) {
	return Prepare(ctx, repo, number, base)
}

// Sweep implements the checkout port used by the app.
func (Git) Sweep(olderThan time.Duration) { Sweep(olderThan) }

func run(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

// CacheDir is where repositories are cloned.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "otto"), nil
}

// Prepare clones repo if needed, fetches the pull request and the base
// branch, and checks the pull request head out in a new worktree.
func Prepare(ctx context.Context, repo string, number int, base string) (*Worktree, error) {
	root, err := CacheDir()
	if err != nil {
		return nil, err
	}
	cache := filepath.Join(root, "repos", filepath.FromSlash(repo))
	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
			return nil, err
		}
		if err := run(ctx, "", "gh", "repo", "clone", repo, cache, "--", "--filter=blob:none", "--no-checkout"); err != nil {
			return nil, err
		}
	}

	ref := fmt.Sprintf("refs/otto/pr-%d", number)
	if err := run(ctx, cache, "git", "fetch", "--force", "origin",
		fmt.Sprintf("+refs/pull/%d/head:%s", number, ref),
		fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", base, base)); err != nil {
		return nil, err
	}

	wtRoot := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(wtRoot, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(wtRoot, fmt.Sprintf("%s-pr%d-", strings.ReplaceAll(repo, "/", "_"), number))
	if err != nil {
		return nil, err
	}
	_ = os.Remove(dir) // git worktree add wants to create it
	if err := run(ctx, cache, "git", "worktree", "add", "--detach", dir, ref); err != nil {
		return nil, err
	}
	return &Worktree{path: dir, cache: cache}, nil
}

// Sweep removes worktrees left behind by an interrupted run (crash, reboot).
// Worktrees younger than olderThan may belong to a review still running in
// another otto process, so they are kept.
func Sweep(olderThan time.Duration) {
	root, err := CacheDir()
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(filepath.Join(root, "worktrees"))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < olderThan {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, "worktrees", e.Name()))
	}
	repos, _ := filepath.Glob(filepath.Join(root, "repos", "*", "*"))
	for _, r := range repos {
		_ = run(context.Background(), r, "git", "worktree", "prune")
	}
}

// Purge deletes every cached clone and worktree.
func Purge() error {
	root, err := CacheDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

// Remove deletes the worktree and everything the review created in it.
func (w *Worktree) Remove() {
	ctx := context.Background()
	_ = run(ctx, w.cache, "git", "worktree", "remove", "--force", w.path)
	_ = os.RemoveAll(w.path)
	_ = run(ctx, w.cache, "git", "worktree", "prune")
}
