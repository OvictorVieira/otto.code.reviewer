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
	"github.com/OvictorVieira/otto.code.reviewer/internal/learn"
)

const (
	sweepEvery     = 30 * time.Minute // how often the watcher re-reads feedback per repository
	sweepPRs       = 15               // most recently updated pull requests read per sweep
	distillMinimum = 3                // new signals needed before the watcher updates the lessons
	distillBatch   = 40               // most signals folded into one update
)

// Feedback reads how maintainers answered Otto's findings.
type Feedback interface {
	Threads(ctx context.Context, repo string, number int) ([]learn.Thread, error)
	RecentPRs(ctx context.Context, repo string, limit int) ([]int, error)
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
func (a *App) learningContext(ctx context.Context, repo string, pr int, files []string, title string) ([]learn.Lesson, []learn.Record) {
	if !a.learningOn() {
		return nil, nil
	}
	mem, err := a.collect(ctx, repo, pr)
	if err != nil {
		a.logf("%s#%d: reading feedback failed, reviewing with what is remembered: %v", repo, pr, err)
	}
	lessons := learn.SelectLessons(mem.Lessons, files, title, time.Now())
	prior := learn.SelectPrior(mem.ForPR(pr))
	if len(lessons)+len(prior) > 0 {
		a.logf("%s#%d: bringing %d of %d lesson(s) and %d earlier finding(s) into the review", repo, pr, len(lessons), len(learn.Active(mem.Lessons)), len(prior))
	}
	return lessons, prior
}

// learnCycle runs in the background of the watcher: it reads recent feedback and
// updates the lessons of every repository that has enough new signals.
func (a *App) learnCycle(ctx context.Context, repos []string) {
	if !a.learningOn() || !atomic.CompareAndSwapInt32(&a.learnBusy, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&a.learnBusy, 0)
	for _, repo := range repos {
		if ctx.Err() != nil {
			return
		}
		if _, err := a.Learn.Load(repo); err != nil {
			a.logf("%s: learning memory unreadable: %v", repo, err)
			continue
		}
		if _, err := a.LearnRepo(ctx, repo, false); err != nil {
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
	prs, err := a.Feedback.RecentPRs(ctx, repo, sweepPRs)
	if err != nil {
		return sum, err
	}
	for _, n := range prs {
		if _, err := a.collect(ctx, repo, n); err != nil {
			a.logf("%s#%d: reading feedback failed: %v", repo, n, err)
		}
	}
	a.learnMu.Lock()
	mem, err = a.Learn.Load(repo)
	if err == nil {
		mem.LastSweep = time.Now()
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
