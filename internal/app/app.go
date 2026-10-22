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
	"sync"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/format"
	"github.com/OvictorVieira/otto.code.reviewer/internal/ghapp"
	"github.com/OvictorVieira/otto.code.reviewer/internal/github"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
	"github.com/OvictorVieira/otto.code.reviewer/internal/trigger"
	"github.com/OvictorVieira/otto.code.reviewer/internal/update"
	"github.com/OvictorVieira/otto.code.reviewer/internal/workspace"
)

// Host is everything Otto needs from GitHub.
type Host interface {
	Login(ctx context.Context) (string, error)
	GetPR(ctx context.Context, repo string, number int) (github.PR, error)
	Diff(ctx context.Context, repo string, number int) (string, error)
	PostReview(ctx context.Context, repo string, number int, payload any) error
	Comment(ctx context.Context, repo string, number int, body string) error
	React(ctx context.Context, repo string, commentID int64, content string) error
	CommentsSince(ctx context.Context, repo string, since time.Time) ([]trigger.Comment, error)
	Repos(ctx context.Context) ([]string, error)
}

// Checkout is a disposable working copy of a pull request head.
type Checkout interface {
	Dir() string
	Remove()
}

// Checkouts creates checkouts and cleans up the ones a crash left behind.
type Checkouts interface {
	Prepare(ctx context.Context, repo string, number int, base string) (Checkout, error)
	Sweep(olderThan time.Duration)
}

type gitCheckouts struct{ workspace.Git }

func (g gitCheckouts) Prepare(ctx context.Context, repo string, number int, base string) (Checkout, error) {
	wt, err := g.Git.Prepare(ctx, repo, number, base)
	if err != nil {
		return nil, err
	}
	return wt, nil
}

// App holds the loaded configuration, state and the ports to the outside world.
type App struct {
	Cfg       config.Config
	State     *trigger.State
	Out       io.Writer
	Host      Host
	Checkouts Checkouts
	NewEngine func(engine.Spec) engine.Engine
	Installed func(name string) bool

	// Upgrade installs a newer release when there is one and reports whether
	// it did. Restart then replaces the process with the new binary.
	Upgrade func(ctx context.Context) (bool, error)
	Restart func() error

	// Fingerprint identifies the running binary on disk. When it changes while the watcher
	// runs (an update replaced it), the watcher restarts into the new binary at the next safe
	// point: between cycles, after any review in progress has finished. Nil disables the check.
	Fingerprint func() string

	// ConfigStamp identifies the config file's content. A change while the watcher runs (setup or
	// a hand edit) makes it reload the config the same safe way. Nil disables the check.
	ConfigStamp func() string

	// Bot returns a token that makes GitHub writes come from Otto's own
	// identity for repo. Nil when no GitHub App is configured.
	Bot func(ctx context.Context, repo string) (string, error)
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
	var bot func(context.Context, string) (string, error)
	if cfg.App.ID != 0 {
		bot = (&ghapp.Client{AppID: cfg.App.ID, KeyPath: cfg.App.KeyPath}).TokenForRepo
	}
	return &App{
		Cfg: cfg, State: st, Out: out, Bot: bot,
		Restart:     update.Restart,
		Fingerprint: update.Fingerprint,
		ConfigStamp: config.Stamp,
		Host:        github.CLI{},
		Checkouts:   gitCheckouts{},
		NewEngine:   func(s engine.Spec) engine.Engine { return engine.CLI{Spec: s} },
		Installed:   engine.Installed,
	}, nil
}

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(a.Out, "%s otto: %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// Options tune one review.
type Options struct {
	Engine           string       // run only this engine (no rotation)
	DryRun           bool         // print instead of posting
	Instructions     string       // extra focus from the requester
	PostToken        string       // post as this identity instead of the user's gh login
	Requester        string       // who asked, shown when posting as the bot
	ShowProgress     bool         // use a transient manual-review display instead of progress log lines
	PostQuotaFailure bool         // comment on a manual review when an engine reports an exhausted quota
	Progress         func(string) // update the transient manual-review display
	ProgressDone     func()       // clear the transient display before the result is written
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
	var progressOnce sync.Once
	finishProgress := func() {
		if opt.ProgressDone != nil {
			progressOnce.Do(opt.ProgressDone)
		}
	}
	defer finishProgress()

	a.reviewStatus(opt, fmt.Sprintf("%s#%d: loading pull request details", repo, number), "Carregando detalhes do pull request…")
	pr, err := a.Host.GetPR(ctx, repo, number)
	if err != nil {
		return Report{}, err
	}
	a.reviewStatus(opt, fmt.Sprintf("%s#%d: fetching the pull request diff", repo, number), "Analisando alterações do pull request…")
	rawDiff, err := a.Host.Diff(ctx, repo, number)
	if err != nil {
		return Report{}, err
	}
	d := diff.Parse(rawDiff)

	a.reviewStatus(opt, fmt.Sprintf("%s#%d: preparing worktree at %s", repo, number, short(pr.HeadRefOid)),
		fmt.Sprintf("Preparando worktree · %s", short(pr.HeadRefOid)))
	wt, err := a.Checkouts.Prepare(ctx, repo, number, pr.BaseRefName)
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
	a.reviewStatus(opt, fmt.Sprintf("%s#%d: preparing review instructions for %d changed files", repo, number, len(d.Files())),
		fmt.Sprintf("Preparando análise de %d arquivos…", len(d.Files())))

	pool, err := a.pool(opt.Engine, opt)
	if err != nil {
		return Report{}, err
	}
	scratch, err := os.CreateTemp("", "otto-answer-*.txt")
	if err != nil {
		return Report{}, err
	}
	scratch.Close()
	defer os.Remove(scratch.Name())

	a.reviewStatus(opt, fmt.Sprintf("%s#%d: reviewing (strategy %s, verify %v)", repo, number, pool.Strategy, a.Cfg.Verify),
		"Preparando revisão…")
	accept := func(out string) error { _, err := review.ParseResult(out, wt.Dir()); return err }
	var progress func(string)
	if opt.ShowProgress && opt.Progress != nil {
		progress = opt.Progress
	}
	outcome, err := pool.Run(ctx, engine.Request{
		Prompt: prompt, Dir: wt.Dir(), OutFile: scratch.Name(), Verify: a.Cfg.Verify, Progress: progress,
	}, a.State.Rotation, accept)
	if !opt.ShowProgress {
		for _, f := range outcome.Failures {
			a.logf("%s#%d: engine failed, falling back: %s", repo, number, f)
		}
	}
	if err != nil {
		if opt.PostQuotaFailure && !opt.DryRun && errors.Is(err, engine.ErrQuotaExceeded) {
			a.reviewStatus(opt, "", "Limite de uso atingido; publicando aviso no pull request…")
			post := github.WithToken(ctx, opt.PostToken)
			if postErr := a.Host.Comment(post, repo, number, format.QuotaFailure(err)); postErr != nil {
				return Report{}, fmt.Errorf("%w (also failed to post quota notice: %v)", err, postErr)
			}
		}
		return Report{}, err
	}
	if opt.Engine == "" {
		a.State.Rotation = outcome.Next
		_ = a.State.Save()
	}

	result, _ := review.ParseResult(outcome.Output, wt.Dir())
	placement := review.Place(result, d, a.Cfg.MinConfidence)
	spec := pool.spec(outcome.Index)
	meta := format.Meta{Engine: spec.Name, Model: spec.Model, Effort: spec.Effort, Trigger: a.Cfg.Trigger}
	if opt.PostToken != "" {
		meta.Requester = opt.Requester
	}
	post := github.WithToken(ctx, opt.PostToken)
	payload := format.Payload(pr.HeadRefOid, placement, result, meta)
	report := Report{Engine: spec.Name, Findings: len(placement.Inline) + len(placement.Outside)}

	if opt.DryRun {
		finishProgress()
		fmt.Fprintln(a.Out, payload.Body)
		for _, c := range payload.Comments {
			fmt.Fprintf(a.Out, "\n--- %s:%d\n%s\n", c.Path, c.Line, c.Body)
		}
		return report, nil
	}

	a.reviewStatus(opt, fmt.Sprintf("%s#%d: posting review to GitHub", repo, number), "Publicando revisão no GitHub…")
	if err := a.Host.PostReview(post, repo, number, payload); err != nil {
		if len(payload.Comments) == 0 {
			return report, err
		}
		// GitHub rejects the whole review when one inline position is invalid.
		// Retry with every finding listed in the summary instead.
		a.reviewStatus(opt, fmt.Sprintf("%s#%d: inline comments rejected (%v); posting them in the summary", repo, number, err),
			"Reformatando comentários para o GitHub…")
		placement.Outside = append(placement.Inline, placement.Outside...)
		placement.Inline = nil
		if err := a.Host.PostReview(post, repo, number, format.Payload(pr.HeadRefOid, placement, result, meta)); err != nil {
			return report, err
		}
	}
	report.Posted = true
	finishProgress()
	if opt.ShowProgress {
		fmt.Fprintf(a.Out, "Otto Review: %d finding(s) · %s · posted\n", report.Findings, spec.Name)
	} else {
		a.logf("%s#%d: posted review with %d findings (engine %s)", repo, number, report.Findings, spec.Name)
	}
	return report, nil
}

func (a *App) reviewStatus(opt Options, logMessage, progressMessage string) {
	if opt.ShowProgress {
		if opt.Progress != nil && progressMessage != "" {
			opt.Progress(progressMessage)
		}
		return
	}
	if logMessage != "" {
		a.logf("%s", logMessage)
	}
}

type pool struct {
	engine.Pool
	specs []engine.Spec
}

func (p pool) spec(i int) engine.Spec { return p.specs[i] }

func (a *App) pool(only string, opt Options) (pool, error) {
	var p pool
	p.Strategy = a.Cfg.Strategy
	for _, s := range a.Cfg.Engines {
		if only != "" && s.Name != only {
			continue
		}
		if !a.Installed(s.Name) {
			a.reviewStatus(opt, fmt.Sprintf("engine %s is configured but not installed; skipping", s.Name),
				fmt.Sprintf("Ignorando %s: não está instalado…", s.Name))
			continue
		}
		p.Engines = append(p.Engines, a.NewEngine(s))
		p.specs = append(p.specs, s)
	}
	if only != "" && len(p.Engines) == 0 {
		if !a.Installed(only) {
			return p, fmt.Errorf("engine %q is not installed", only)
		}
		s := engine.Spec{Name: only}
		p.Engines, p.specs = []engine.Engine{a.NewEngine(s)}, []engine.Spec{s}
	}
	if len(p.Engines) == 0 {
		return p, errors.New("no installed engine is configured: run `otto setup`")
	}
	return p, nil
}

// Watch polls the configured repositories and answers trigger comments.
func (a *App) Watch(ctx context.Context, once bool) error {
	a.Checkouts.Sweep(2 * a.Cfg.Timeout)

	login, err := a.Host.Login(ctx)
	if err != nil {
		return fmt.Errorf("gh is not logged in (run `gh auth login`): %w", err)
	}
	allowed := a.Cfg.AllowedAuthors
	if len(allowed) == 0 {
		allowed = []string{login}
	}
	a.logf("watching as %s, trigger %q, every %s", login, a.Cfg.Trigger, a.Cfg.PollInterval)
	a.logf("engines: %s", a.engineSummary())
	startedAs, configStartedAs := a.fingerprint(), a.configStamp()

	var repos []string
	var reposAt time.Time
	for {
		if a.selfUpdate(ctx) || a.restartForChanges(startedAs, configStartedAs) {
			return nil // only reached when Restart returns (tests); normally the process is replaced
		}
		if a.Cfg.Repos.All && time.Since(reposAt) > 10*time.Minute {
			if r, err := a.Host.Repos(ctx); err == nil {
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

func (a *App) fingerprint() string {
	if a.Fingerprint == nil {
		return ""
	}
	return a.Fingerprint()
}

func (a *App) configStamp() string {
	if a.ConfigStamp == nil {
		return ""
	}
	return a.ConfigStamp()
}

// engineSummary lists the engines and models this process loaded, so the log shows what it uses.
func (a *App) engineSummary() string {
	var parts []string
	for _, e := range a.Cfg.Engines {
		model, effort := e.Model, e.Effort
		if model == "" {
			model = "default"
		}
		detail := model
		if effort != "" {
			detail += ", " + effort
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", e.Name, detail))
	}
	return strings.Join(parts, ", ")
}

// restartForChanges restarts into the binary and config on disk when an update replaced the
// binary or `otto setup` rewrote the config since this process started. It runs between cycles,
// so a review in progress always finishes first, and the process is replaced in place, so no old
// copy keeps running next to the new one. A binary or config that is unreadable for a moment
// while being rewritten is ignored until it is readable again.
func (a *App) restartForChanges(binaryAtStart, configAtStart string) bool {
	changed := func(atStart, now string) bool { return atStart != "" && now != "" && now != atStart }
	binaryChanged := changed(binaryAtStart, a.fingerprint())
	configChanged := changed(configAtStart, a.configStamp())
	if (!binaryChanged && !configChanged) || a.Restart == nil {
		return false
	}
	if configChanged {
		a.logf("the Otto config was changed; reloading it")
	} else {
		a.logf("the Otto binary was updated; restarting into the new version")
	}
	if err := a.Restart(); err != nil {
		a.logf("restart failed, still running the old version: %v", err)
		return false
	}
	return true
}

// selfUpdate installs a newer release and restarts into it. Requests triggered
// from GitHub run on the newest version: the pending comment is not marked
// done, so the restarted process picks it up.
func (a *App) selfUpdate(ctx context.Context) bool {
	if a.Upgrade == nil {
		return false
	}
	updated, err := a.Upgrade(ctx)
	if err != nil {
		a.logf("update check failed: %v", err)
		return false
	}
	if !updated {
		return false
	}
	a.logf("updated to a newer version, restarting")
	if a.Restart == nil {
		return false
	}
	if err := a.Restart(); err != nil {
		a.logf("restart failed: %v", err)
		return false
	}
	return true
}

func (a *App) poll(ctx context.Context, repo string, allowed []string) {
	started := time.Now()
	since := a.State.Cursor(repo, started.Add(-a.Cfg.Lookback)).Add(-2 * time.Minute)
	comments, err := a.Host.CommentsSince(ctx, repo, since)
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

	// A request made on GitHub is answered by Otto itself when a GitHub App is
	// configured; a review run from the command line is always the user's own.
	token := ""
	if a.Bot != nil {
		t, err := a.Bot(ctx, req.Repo)
		if err != nil {
			a.logf("%s#%d: posting as the Otto app is not possible, using your GitHub login: %v", req.Repo, req.PR, err)
		}
		token = t
	}
	write := github.WithToken(ctx, token)
	_ = a.Host.React(write, req.Repo, req.CommentID, "eyes")

	report, err := a.Review(ctx, req.Repo, req.PR, Options{Instructions: req.Instructions, PostToken: token, Requester: req.Author})
	if ctx.Err() != nil {
		return // shutting down: leave it unmarked so it runs again on restart
	}
	a.State.MarkDone(req.CommentID, time.Now())
	_ = a.State.Save()

	if err != nil {
		a.logf("%s#%d: review failed: %v", req.Repo, req.PR, err)
		_ = a.Host.React(write, req.Repo, req.CommentID, "confused")
		_ = a.Host.Comment(write, req.Repo, req.PR, format.Failure(engine.Failures(err), a.Cfg.Trigger, a.Cfg.Language))
		return
	}
	if report.Findings == 0 {
		_ = a.Host.React(write, req.Repo, req.CommentID, "+1")
	}
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
