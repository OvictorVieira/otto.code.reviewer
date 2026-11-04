package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/github"
	"github.com/OvictorVieira/otto.code.reviewer/internal/learn"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

const (
	sweepEvery     = 30 * time.Minute   // how often the watcher re-reads feedback per repository
	sweepPRs       = 15                 // most recently updated pull requests read per sweep
	firstSweep     = 7 * 24 * time.Hour // how far back the first sweep of a repository looks
	rateLimitPause = 15 * time.Minute   // how long learning waits after GitHub's rate limit when its reset is unknown
	distillMinimum = 3                  // new signals needed before the watcher updates the lessons
	distillBatch   = 40                 // most signals folded into one update
)

// Feedback reads how maintainers answered Otto's findings.
type Feedback interface {
	Threads(ctx context.Context, repo string, number int) ([]learn.Thread, error)
	// RecentPRs lists pull requests updated at or after since, newest first.
	RecentPRs(ctx context.Context, repo string, since time.Time, limit int) ([]int, error)
}

func (a *App) learningOn() bool {
	return a.Cfg.Learning && a.Feedback != nil && a.Learn.Dir != ""
}

// collect reads the review threads of one pull request, remembers the outcome of
// every finding Otto made there, and returns the repository's memory.
func (a *App) collect(ctx context.Context, repo string, pr int) (learn.Memory, error) {
	threads, terr := a.Feedback.Threads(ctx, repo, pr)
	a.learnMu.Lock()
	defer a.learnMu.Unlock()
	mem, err := a.Learn.Load(repo)
	if err != nil {
		return learn.Memory{Records: map[string]learn.Record{}}, err
	}
	if terr != nil {
		return mem, terr
	}
	if mem.Merge(learn.Extract(repo, pr, threads, time.Now(), a.trusted(ctx))) > 0 {
		err = a.Learn.Save(repo, mem)
	}
	return mem, err
}

// learningContext is what Otto brings into a review: the repository's lessons and
// the findings it already made on this pull request. Failures never block a review.
//
// Only a small, relevant slice is injected so the prompt does not grow as Otto learns:
// lessons are filtered by the changed files and ranked within a fixed budget, and earlier
// findings are reduced to the ones that still matter (see learn.SelectLessons/SelectPrior).
//
// seen is every earlier finding on this pull request, unbudgeted: the prompt only hints at them,
// so Review also uses seen to drop any repeat from the engine's answer.
func (a *App) learningContext(ctx context.Context, repo string, pr int, files []string, title string) (lessons []learn.Lesson, prior, seen []learn.Record) {
	if !a.learningOn() {
		return nil, nil, nil
	}
	mem, err := a.collect(ctx, repo, pr)
	if err != nil {
		a.logf("%s#%d: reading feedback failed, reviewing with what is remembered: %v", repo, pr, err)
	}
	lessons = learn.SelectLessons(mem.Lessons, files, title, time.Now())
	seen = mem.ForPR(pr)
	prior = learn.SelectPrior(seen)
	if len(lessons)+len(prior) > 0 {
		a.logf("%s#%d: bringing %d of %d lesson(s) and %d earlier finding(s) into the review", repo, pr, len(lessons), len(learn.Active(mem.Lessons)), len(prior))
	}
	return lessons, prior, seen
}

const (
	judgeEarlierMax = 30 // earlier findings shown to the judge
	judgeCodeRadius = 6  // lines of code shown around each finding
)

// judge asks an engine to decide, with the code and the author's replies in front of it, which of the
// review's findings are really new, which repeat something the author already settled, and which come
// back to something the author's answer or change did not settle. Word matching cannot tell a reworded
// repeat from a new problem next to an old one; reading the code can. It returns nil when there is
// nothing to judge or no engine could (reconcile then falls back to its stricter word matching).
func (a *App) judge(ctx context.Context, opt Options, dir, repo string, pr int, r review.Result, seen []learn.Record) []review.Decision {
	if len(seen) == 0 || len(r.Findings) == 0 {
		return nil
	}
	in := review.JudgeInput{}
	known := map[int64]bool{}
	for _, rec := range seen {
		if len(in.Earlier) == judgeEarlierMax {
			break
		}
		known[rec.ID] = true
		e := review.JudgeEarlier{
			Ref: rec.ID, Status: string(rec.Outcome), Title: rec.Title, Said: rec.Body,
			Location: fmt.Sprintf("%s:%d", rec.Path, rec.Line), Code: review.CodeAround(dir, rec.Path, rec.Line, judgeCodeRadius),
		}
		if rec.FollowsUp != 0 {
			e.Title += fmt.Sprintf(" (a follow-up Otto already made to ref %d)", rec.FollowsUp)
		}
		e.Replies = append(e.Replies, rec.Notes...)
		in.Earlier = append(in.Earlier, e)
	}
	for i, f := range r.Findings {
		in.Candidates = append(in.Candidates, review.JudgeCandidate{
			Index: i, Title: f.Title, Body: f.Body, Claims: f.FollowUp,
			Location: fmt.Sprintf("%s:%d-%d", f.Path, f.Start, f.End), Code: review.CodeAround(dir, f.Path, f.Start, judgeCodeRadius),
		})
	}
	prompt, err := review.BuildJudgePrompt(in)
	if err != nil {
		a.logf("%s#%d: checking findings against earlier ones failed: %v", repo, pr, err)
		return nil
	}
	pool, err := a.pool(opt.Engine, opt, engine.Heavy)
	if err != nil {
		a.logf("%s#%d: checking findings against earlier ones failed: %v", repo, pr, err)
		return nil
	}
	scratch, err := os.CreateTemp("", "otto-judge-*.txt")
	if err != nil {
		return nil
	}
	scratch.Close()
	defer os.Remove(scratch.Name())
	a.reviewStatus(opt, fmt.Sprintf("%s#%d: checking %d finding(s) against %d earlier one(s)", repo, pr, len(r.Findings), len(in.Earlier)),
		"Conferindo achados com as respostas do autor…")
	var decisions []review.Decision
	accept := func(out string) error {
		d, err := review.ParseJudge(out, len(r.Findings), known)
		decisions = d
		return err
	}
	if _, err := pool.Run(ctx, engine.Request{Prompt: prompt, Dir: dir, OutFile: scratch.Name()}, a.rotation(), accept); err != nil {
		a.logf("%s#%d: no engine could check findings against earlier ones, using word matching: %v", repo, pr, err)
		return nil
	}
	return decisions
}

// reconcile applies what Otto owes the author about the findings it already made on this pull
// request, before anything is posted. decisions are the judge's calls (nil when it did not run):
//
//   - A finding the judge calls new is posted.
//   - A finding the judge calls the same as an earlier one is posted only if the author's answer or
//     change did not settle it, and the earlier finding can be followed up (see learn.CanFollowUp).
//     The earlier thread is usually closed, so it is a new comment that names what it follows.
//   - Without a judge, a finding that restates an earlier one (by wording) is dropped, unless the
//     review's own re-check says that earlier one is unsatisfied and may be followed up.
//   - The re-checks are kept for the summary, so the author sees what was verified.
func (a *App) reconcile(repo string, pr int, r review.Result, seen []learn.Record, decisions []review.Decision) review.Result {
	if len(seen) == 0 {
		return r
	}
	byID := map[int64]learn.Record{}
	for _, rec := range seen {
		byID[rec.ID] = rec
	}
	verdict := map[int64]string{}
	reason := map[int64]string{}
	order := []int64{}
	note := func(ref int64, v, why string, override bool) {
		if _, ok := byID[ref]; !ok {
			return
		}
		prev, had := verdict[ref]
		if !had {
			order = append(order, ref)
		}
		if !had || override || (v == review.Unsatisfied && prev != review.Unsatisfied) {
			verdict[ref], reason[ref] = v, why
		}
	}
	for _, c := range r.Rechecks {
		note(c.Ref, c.Verdict, c.Reason, false)
	}
	for _, d := range decisions {
		if !d.New {
			note(d.Ref, d.Verdict, d.Reason, true)
		}
	}

	var kept []review.Finding
	for i, f := range r.Findings {
		ref, explicit, judged := f.FollowUp, f.FollowUp != 0, false
		if _, known := byID[ref]; !known {
			ref, explicit = 0, false
		}
		if i < len(decisions) && len(decisions) == len(r.Findings) {
			d := decisions[i]
			judged = true
			if d.New {
				f.FollowUp = 0
				kept = append(kept, f)
				continue
			}
			ref, explicit = d.Ref, false
			verdict[ref] = d.Verdict
		}
		if ref == 0 {
			if rec, repeat := learn.Match(f.Path, f.Start, f.Title, f.Body, seen); repeat {
				ref = rec.ID
			}
		}
		if ref == 0 {
			f.FollowUp = 0
			kept = append(kept, f) // about something new
			continue
		}
		rec := byID[ref]
		v, checked := verdict[ref]
		switch {
		case explicit && !checked && !judged:
			v = review.Unsatisfied // the follow-up itself is the verdict
		case v != review.Unsatisfied:
			a.logf("%s#%d: not repeating a finding the author already settled: %s (%s:%d)", repo, pr, f.Title, f.Path, f.Start)
			continue
		}
		if !learn.CanFollowUp(rec, seen) {
			a.logf("%s#%d: not repeating a finding already answered or followed up: %s (%s:%d)", repo, pr, f.Title, f.Path, f.Start)
			continue
		}
		f.FollowUp = ref
		kept = append(kept, f)
	}
	if len(kept) == 0 && len(r.Findings) > 0 {
		// The verdict and explanation were about findings the author already dealt with.
		r.Correct, r.Explanation = true, ""
	}
	var checks []review.Recheck
	for _, ref := range order {
		checks = append(checks, review.Recheck{Ref: ref, Verdict: verdict[ref], Reason: reason[ref], Title: byID[ref].Title})
	}
	r.Findings, r.Rechecks = kept, checks
	return r
}

// learnCycle runs in the background of the watcher: it reads recent feedback and
// updates the lessons of every repository that has enough new signals.
func (a *App) learnCycle(ctx context.Context, repos []string) {
	if !a.learningOn() || !atomic.CompareAndSwapInt32(&a.learnBusy, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&a.learnBusy, 0)
	if time.Now().Before(a.learnPausedUntil) {
		return
	}
	for _, repo := range repos {
		if ctx.Err() != nil {
			return
		}
		if _, err := a.Learn.Load(repo); err != nil {
			a.logf("%s: learning memory unreadable: %v", repo, err)
			continue
		}
		if _, err := a.LearnRepo(ctx, repo, false); err != nil {
			if github.RateLimited(err) {
				a.learnPausedUntil = time.Now().Add(rateLimitPause)
				if reset := a.limitLifts(ctx); !reset.IsZero() {
					a.learnPausedUntil = reset
				}
				a.logf("GitHub API rate limit reached; learning resumes at %s", a.learnPausedUntil.Format("15:04"))
				return
			}
			a.logf("%s: learning failed: %v", repo, err)
		}
	}
}

// LearnSummary reports one learning pass.
type LearnSummary struct {
	Signals  int  // findings with a maintainer signal not yet learned from
	Distill  bool // lessons were updated
	Lessons  int
	Throttle bool // skipped: swept recently and not forced
}

// LearnRepo reads recent feedback for repo and, when there is enough of it (or force is set),
// asks an engine to update the repository's lessons.
func (a *App) LearnRepo(ctx context.Context, repo string, force bool) (LearnSummary, error) {
	var sum LearnSummary
	if a.Feedback == nil || a.Learn.Dir == "" {
		return sum, errors.New("learning is not available")
	}
	a.learnMu.Lock()
	mem, err := a.Learn.Load(repo)
	a.learnMu.Unlock()
	if err != nil {
		return sum, err
	}
	if !force && time.Since(mem.LastSweep) < sweepEvery {
		sum.Throttle = true
		return sum, nil
	}
	// Only pull requests with activity since the last sweep can carry new feedback; reading the
	// threads of every recent pull request of every repository spends GitHub's hourly budget.
	started, since := time.Now(), mem.LastSweep
	if since.IsZero() || force {
		since = started.Add(-firstSweep)
	}
	prs, err := a.Feedback.RecentPRs(ctx, repo, since, sweepPRs)
	if err != nil {
		return sum, err
	}
	for _, n := range prs {
		if _, err := a.collect(ctx, repo, n); err != nil {
			if github.RateLimited(err) {
				return sum, err // the sweep is retried whole, so nothing it skipped is lost
			}
			a.logf("%s#%d: reading feedback failed: %v", repo, n, err)
		}
	}
	a.learnMu.Lock()
	mem, err = a.Learn.Load(repo)
	if err == nil {
		mem.LastSweep = started
		err = a.Learn.Save(repo, mem)
	}
	a.learnMu.Unlock()
	if err != nil {
		return sum, err
	}
	pending := mem.Unlearned()
	sum.Signals, sum.Lessons = len(pending), len(mem.Lessons)
	min := distillMinimum
	if force {
		min = 1
	}
	if len(pending) < min {
		return sum, nil
	}
	if len(pending) > distillBatch {
		pending = pending[:distillBatch]
	}
	lessons, err := a.distill(ctx, mem.Lessons, pending)
	if err != nil {
		return sum, err
	}
	a.learnMu.Lock()
	defer a.learnMu.Unlock()
	mem, err = a.Learn.Load(repo)
	if err != nil {
		return sum, err
	}
	mem.Lessons, mem.DistilledAt = lessons, time.Now()
	mem.MarkLearned(pending)
	if err := a.Learn.Save(repo, mem); err != nil {
		return sum, err
	}
	sum.Distill, sum.Lessons = true, len(lessons)
	a.logf("%s: learned from %d maintainer signal(s); %d lesson(s) now", repo, len(pending), len(lessons))
	return sum, nil
}

// distill asks the configured engines to fold new feedback into the lessons.
func (a *App) distill(ctx context.Context, current []learn.Lesson, recs []learn.Record) ([]learn.Lesson, error) {
	prompt, err := learn.DistillPrompt(current, recs)
	if err != nil {
		return nil, err
	}
	pool, err := a.pool("", Options{}, engine.Medium)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "otto-learn-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var lessons []learn.Lesson
	accept := func(out string) error {
		l, err := learn.ParseDistill(out, current, time.Now())
		lessons = l
		return err
	}
	if _, err := pool.Run(ctx, engine.Request{Prompt: prompt, Dir: dir, OutFile: dir + "/answer.txt"}, a.rotation(), accept); err != nil {
		return nil, fmt.Errorf("updating lessons: %w", err)
	}
	return lessons, nil
}

// Lessons returns what Otto has learned about repo.
func (a *App) Lessons(repo string) ([]learn.Lesson, error) {
	a.learnMu.Lock()
	defer a.learnMu.Unlock()
	mem, err := a.Learn.Load(repo)
	return mem.Lessons, err
}

// EditLesson changes one lesson by id: "pin", "unpin", "disable", "enable" or "forget".
func (a *App) EditLesson(repo, id, action string) error {
	a.learnMu.Lock()
	defer a.learnMu.Unlock()
	mem, err := a.Learn.Load(repo)
	if err != nil {
		return err
	}
	for i, l := range mem.Lessons {
		if !strings.HasPrefix(l.ID, id) {
			continue
		}
		switch action {
		case "pin":
			mem.Lessons[i].Pinned = true
		case "unpin":
			mem.Lessons[i].Pinned = false
		case "disable":
			mem.Lessons[i].Disabled = true
		case "enable":
			mem.Lessons[i].Disabled = false
		case "forget":
			mem.Lessons = append(mem.Lessons[:i], mem.Lessons[i+1:]...)
		default:
			return fmt.Errorf("unknown action %q (use pin, unpin, disable, enable or forget)", action)
		}
		return a.Learn.Save(repo, mem)
	}
	return fmt.Errorf("no lesson with id %q in %s", id, repo)
}

// trusted says whose replies and reactions Otto learns from: the people allowed to ask for reviews
// (or, when none are configured, the user Otto runs as).
func (a *App) trusted(ctx context.Context) func(string) bool {
	allowed := map[string]bool{}
	for _, l := range a.Cfg.AllowedAuthors {
		allowed[strings.ToLower(l)] = true
	}
	if len(allowed) == 0 {
		if login, err := a.Host.Login(ctx); err == nil {
			allowed[strings.ToLower(login)] = true
		}
	}
	return func(login string) bool { return allowed[strings.ToLower(login)] }
}
