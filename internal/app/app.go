// Package app wires everything together: one review from start to finish,
// and the watcher loop that answers "/otto review" comments.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/format"
	"github.com/OvictorVieira/otto.code.reviewer/internal/github"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
	"github.com/OvictorVieira/otto.code.reviewer/internal/trigger"
	"github.com/OvictorVieira/otto.code.reviewer/internal/workspace"
)

// App holds the loaded configuration and state.
type App struct {
	Cfg   config.Config
	State *trigger.State
	Out   io.Writer
}

// New loads config and state.
func New(out io.Writer) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	st, err := trigger.LoadState(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	return &App{Cfg: cfg, State: st, Out: out}, nil
}

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(a.Out, "%s otto: %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// Options tune one review.
type Options struct {
	Engine       string // run only this engine (no rotation)
	DryRun       bool   // print instead of posting
	Instructions string // extra focus from the requester
}

// Report summarizes one finished review.
type Report struct {
	Engine   string
	Findings int
	Posted   bool
}

var prRef = regexp.MustCompile(`^(?:https?://github\.com/)?([\w.-]+/[\w.-]+?)(?:/pull/|#)(\d+)/?$`)

// ParseRef accepts "https://github.com/o/r/pull/12", "o/r/pull/12" or "o/r#12".
func ParseRef(ref string) (string, int, error) {
	m := prRef.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return "", 0, fmt.Errorf("not a pull request reference: %q (use https://github.com/owner/repo/pull/123 or owner/repo#123)", ref)
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], n, nil
}

// Review runs one full review: checkout, engine, parse, post, clean up.
func (a *App) Review(ctx context.Context, repo string, number int, opt Options) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Cfg.Timeout)
	defer cancel()

	pr, err := github.GetPR(ctx, repo, number)
	if err != nil {
		return Report{}, err
	}
	rawDiff, err := github.Diff(ctx, repo, number)
	if err != nil {
		return Report{}, err
	}
	d := diff.Parse(rawDiff)

	a.logf("%s#%d: preparing worktree at %s", repo, number, short(pr.HeadRefOid))
	wt, err := workspace.Prepare(ctx, repo, number, pr.BaseRefName)
	if err != nil {
		return Report{}, err
	}
	defer wt.Remove()

	prompt, err := review.BuildPrompt(review.PromptInput{
		Repo: repo, URL: pr.URL, Title: pr.Title, Body: pr.Body, Base: pr.BaseRefName, HeadSHA: pr.HeadRefOid,
		Files: d.Files(), Diff: rawDiff, Language: a.Cfg.Language, Instructions: opt.Instructions, Verify: a.Cfg.Verify,
	})
	if err != nil {
		return Report{}, err
	}

	pool, err := a.pool(opt.Engine)
	if err != nil {
		return Report{}, err
	}
	scratch, err := os.CreateTemp("", "otto-answer-*.txt")
	if err != nil {
		return Report{}, err
	}
	scratch.Close()
	defer os.Remove(scratch.Name())

	a.logf("%s#%d: reviewing (strategy %s, verify %v)", repo, number, pool.Strategy, a.Cfg.Verify)
	accept := func(out string) error { _, err := review.ParseResult(out, wt.Dir); return err }
	outcome, err := pool.Run(ctx, engine.Request{
		Prompt: prompt, Dir: wt.Dir, OutFile: scratch.Name(), Verify: a.Cfg.Verify,
	}, a.State.Rotation, accept)
	for _, f := range outcome.Failures {
		a.logf("%s#%d: engine failed, falling back: %s", repo, number, f)
	}
	if err != nil {
		return Report{}, err
	}
	if opt.Engine == "" {
		a.State.Rotation = outcome.Next
		_ = a.State.Save()
	}

	result, _ := review.ParseResult(outcome.Output, wt.Dir)
	placement := review.Place(result, d, a.Cfg.MinConfidence)
	spec := pool.spec(outcome.Index)
	meta := format.Meta{Engine: spec.Name, Model: spec.Model, Effort: spec.Effort, Trigger: a.Cfg.Trigger}
	payload := format.Payload(pr.HeadRefOid, placement, result, meta)
	report := Report{Engine: spec.Name, Findings: len(placement.Inline) + len(placement.Outside)}

	if opt.DryRun {
		fmt.Fprintln(a.Out, payload.Body)
		for _, c := range payload.Comments {
			fmt.Fprintf(a.Out, "\n--- %s:%d\n%s\n", c.Path, c.Line, c.Body)
		}
		return report, nil
	}

	if err := github.PostReview(ctx, repo, number, payload); err != nil {
		if len(payload.Comments) == 0 {
			return report, err
		}
		// GitHub rejects the whole review when one inline position is invalid.
		// Retry with every finding listed in the summary instead.
		a.logf("%s#%d: inline comments rejected (%v); posting them in the summary", repo, number, err)
		placement.Outside = append(placement.Inline, placement.Outside...)
		placement.Inline = nil
		if err := github.PostReview(ctx, repo, number, format.Payload(pr.HeadRefOid, placement, result, meta)); err != nil {
			return report, err
		}
	}
	report.Posted = true
	a.logf("%s#%d: posted review with %d findings (engine %s)", repo, number, report.Findings, spec.Name)
	return report, nil
}

type pool struct {
	engine.Pool
	specs []engine.Spec
}

func (p pool) spec(i int) engine.Spec { return p.specs[i] }

func (a *App) pool(only string) (pool, error) {
	var p pool
	p.Strategy = a.Cfg.Strategy
	for _, s := range a.Cfg.Engines {
		if only != "" && s.Name != only {
			continue
		}
		if !engine.Installed(s.Name) {
			a.logf("engine %s is configured but not installed; skipping", s.Name)
			continue
		}
		p.Engines = append(p.Engines, engine.CLI{Spec: s})
		p.specs = append(p.specs, s)
	}
	if only != "" && len(p.Engines) == 0 {
		if !engine.Installed(only) {
			return p, fmt.Errorf("engine %q is not installed", only)
		}
		s := engine.Spec{Name: only}
		p.Engines, p.specs = []engine.Engine{engine.CLI{Spec: s}}, []engine.Spec{s}
	}
	if len(p.Engines) == 0 {
		return p, errors.New("no installed engine is configured: run `otto setup`")
	}
	return p, nil
}

// Watch polls the configured repositories and answers trigger comments.
func (a *App) Watch(ctx context.Context, once bool) error {
	workspace.Sweep(2 * a.Cfg.Timeout)

	login, err := github.Login(ctx)
	if err != nil {
		return fmt.Errorf("gh is not logged in (run `gh auth login`): %w", err)
	}
	allowed := a.Cfg.AllowedAuthors
	if len(allowed) == 0 {
		allowed = []string{login}
	}
	a.logf("watching as %s, trigger %q, every %s", login, a.Cfg.Trigger, a.Cfg.PollInterval)

	var repos []string
	var reposAt time.Time
	for {
		if a.Cfg.Repos.All && time.Since(reposAt) > 10*time.Minute {
			if r, err := github.Repos(ctx); err == nil {
				repos, reposAt = r, time.Now()
			} else {
				a.logf("listing repositories failed: %v", err)
			}
		} else if !a.Cfg.Repos.All {
			repos = a.Cfg.Repos.Selected
		}

		for _, repo := range repos {
			if ctx.Err() != nil {
				return nil
			}
			a.poll(ctx, repo, allowed)
		}
		a.State.Prune(time.Now(), 30*24*time.Hour)
		_ = a.State.Save()

		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.Cfg.PollInterval):
		}
	}
}

func (a *App) poll(ctx context.Context, repo string, allowed []string) {
	started := time.Now()
	since := a.State.Cursor(repo, started.Add(-a.Cfg.Lookback)).Add(-2 * time.Minute)
	comments, err := github.CommentsSince(ctx, repo, since)
	if err != nil {
		a.logf("%s: reading comments failed: %v", repo, err)
		return
	}
	for _, req := range trigger.Pending(comments, a.Cfg.Trigger, allowed, a.State.Done) {
		a.handle(ctx, req)
	}
	a.State.SetCursor(repo, started)
	_ = a.State.Save()
}

func (a *App) handle(ctx context.Context, req trigger.Request) {
	a.logf("%s#%d: review requested by %s", req.Repo, req.PR, req.Author)
	_ = github.React(ctx, req.Repo, req.CommentID, "eyes")

	report, err := a.Review(ctx, req.Repo, req.PR, Options{Instructions: req.Instructions})
	if ctx.Err() != nil {
		return // shutting down: leave it unmarked so it runs again on restart
	}
	a.State.MarkDone(req.CommentID, time.Now())
	_ = a.State.Save()

	if err != nil {
		a.logf("%s#%d: review failed: %v", req.Repo, req.PR, err)
		_ = github.React(ctx, req.Repo, req.CommentID, "confused")
		_ = github.Comment(ctx, req.Repo, req.PR, fmt.Sprintf(
			"Otto could not finish this review.\n\n```\n%s\n```\n\nComment `%s` to try again.", trim(err.Error(), 1500), a.Cfg.Trigger))
		return
	}
	if report.Findings == 0 {
		_ = github.React(ctx, req.Repo, req.CommentID, "+1")
	}
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
