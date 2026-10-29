package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
	"github.com/OvictorVieira/otto.code.reviewer/internal/slice"
)

// maxParallelParts is how many parts of a large pull request are reviewed at the same time: fast,
// without hitting the usage limits of the plans behind the engines.
const maxParallelParts = 3

// integrationShare is the part of the review's time kept for the pass across parts.
const integrationShare = 0.25

// runReview runs one prompt through the pool and keeps only an answer Otto can read.
func (a *App) runReview(ctx context.Context, p pool, in review.PromptInput, dir string, cursor int, progress func(string)) (engine.Outcome, error) {
	prompt, err := review.BuildPrompt(in)
	if err != nil {
		return engine.Outcome{}, err
	}
	scratch, err := os.CreateTemp("", "otto-answer-*.txt")
	if err != nil {
		return engine.Outcome{}, err
	}
	scratch.Close()
	defer os.Remove(scratch.Name())
	accept := func(out string) error { _, err := review.ParseResult(out, dir); return err }
	return p.Run(ctx, engine.Request{
		Prompt: prompt, Dir: dir, OutFile: scratch.Name(), Progress: progress, Contract: review.OutputContract(),
	}, cursor, accept)
}

type partsResult struct {
	result             review.Result
	engines            string   // which engines answered
	unreviewed         []string // files of parts no engine could review
	integrationSkipped bool
}

// reviewInParts reviews a large pull request with several reviewers at once, each on one part
// (code and its tests together), then runs one pass across the parts for problems between them.
// A part every engine failed on is reported as unreviewed instead of failing the whole review.
func (a *App) reviewInParts(ctx context.Context, repo string, number int, dir string, in review.PromptInput,
	parts [][]string, tier engine.Tier, opt Options) (partsResult, error) {
	a.logf("%s#%d: large pull request, reviewing %d parts (up to %d at a time)", repo, number, len(parts), maxParallelParts)
	partsCtx, cancel := context.WithCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		left := time.Until(deadline)
		partsCtx, cancel = context.WithTimeout(ctx, left-time.Duration(float64(left)*integrationShare))
	}
	defer cancel()

	type outcome struct {
		result review.Result
		engine string
		err    error
	}
	outcomes := make([]outcome, len(parts))
	sem := make(chan struct{}, maxParallelParts)
	var wg sync.WaitGroup
	start := a.rotation()
	for i, files := range parts {
		wg.Add(1)
		go func(i int, files []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pin := in
			pin.Files, pin.Diff = files, slice.SubDiff(in.Diff, files)
			pin.Part = fmt.Sprintf("You review part %d of %d: %s. The whole pull request changes: %s.",
				i+1, len(parts), strings.Join(files, ", "), strings.Join(in.Files, ", "))
			p, err := a.pool(opt.Engine, opt, tier)
			if err != nil {
				outcomes[i].err = err
				return
			}
			// Each part starts on a different engine so the work is spread across plans.
			out, err := a.runReview(partsCtx, p, pin, dir, start+i, nil)
			if err != nil {
				a.logf("%s#%d: part %d of %d failed on every engine: %v", repo, number, i+1, len(parts), err)
				outcomes[i].err = err
				return
			}
			outcomes[i].result, _ = review.ParseResult(out.Output, dir)
			outcomes[i].engine = p.spec(out.Index).Name
			a.logf("%s#%d: part %d of %d reviewed by %s (%d findings)", repo, number, i+1, len(parts),
				outcomes[i].engine, len(outcomes[i].result.Findings))
		}(i, files)
	}
	wg.Wait()
	if opt.Engine == "" {
		a.setRotation(start + len(parts))
	}

	var res partsResult
	var results []review.Result
	engines := map[string]bool{}
	var lastErr error
	for i, o := range outcomes {
		if o.err != nil {
			res.unreviewed = append(res.unreviewed, parts[i]...)
			lastErr = o.err
			continue
		}
		results = append(results, o.result)
		engines[o.engine] = true
	}
	if len(results) == 0 {
		return res, lastErr // nothing was reviewed: fail like a single review would
	}

	// The pass across parts: what the parts found, and the whole diff, to find what lies between them.
	var found strings.Builder
	for _, r := range results {
		for _, f := range r.Findings {
			fmt.Fprintf(&found, "- [P%d] %s (%s:%d)\n", f.Priority, f.Title, f.Path, f.Start)
		}
	}
	if found.Len() == 0 {
		found.WriteString("(the parts found nothing)\n")
	}
	iin := in
	iin.Integration = found.String()
	if p, err := a.pool(opt.Engine, opt, engine.Heavy); err == nil {
		if out, err := a.runReview(ctx, p, iin, dir, start, nil); err == nil {
			r, _ := review.ParseResult(out.Output, dir)
			results = append(results, r)
			engines[p.spec(out.Index).Name] = true
			a.logf("%s#%d: pass across parts by %s (%d findings)", repo, number, p.spec(out.Index).Name, len(r.Findings))
		} else {
			res.integrationSkipped = true
			a.logf("%s#%d: the pass across parts failed: %v", repo, number, err)
		}
	}
	res.result = slice.Merge(results)
	var names []string
	for n := range engines {
		names = append(names, n)
	}
	sort.Strings(names)
	res.engines = strings.Join(names, ", ")
	return res, nil
}
