// Package eval measures how well a model reviews: Otto's real review prompt, run on a small pull
// request with three planted bugs. It is what the recommended models are picked from. It calls
// real models, so it runs on demand (`otto eval models`), never in CI.
package eval

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

// The bar a model must clear to be recommended.
const (
	MinBugs = 3
	MaxTime = 3 * time.Minute
)

const baseFile = `package stock

import "errors"

var ErrNoStock = errors.New("not enough stock")

// Store keeps the available quantity per SKU.
type Store struct {
	qty map[string]int
}

func New() *Store { return &Store{qty: map[string]int{}} }

func (s *Store) Add(sku string, n int) { s.qty[sku] += n }

func (s *Store) Available(sku string) int { return s.qty[sku] }
`

// The change has three bugs: a check-then-act race in Reserve (Release locks, Reserve does not),
// errors ignored in Snapshot, and a division by zero in Percent.
const changeFile = `package stock

import (
	"encoding/json"
	"os"
	"sync"
)

var mu sync.Mutex

// Reserve takes n units of sku for an order. It is called by many HTTP handlers at once.
func (s *Store) Reserve(sku string, n int) error {
	if s.Available(sku) < n {
		return ErrNoStock
	}
	s.qty[sku] = s.qty[sku] - n
	return nil
}

// Release gives units back when an order is cancelled.
func (s *Store) Release(sku string, n int) {
	mu.Lock()
	defer mu.Unlock()
	s.qty[sku] += n
}

// Snapshot writes the current stock to disk so a restart can recover it.
func (s *Store) Snapshot(path string) {
	data, _ := json.Marshal(s.qty)
	os.WriteFile(path, data, 0o644)
}

// Percent returns how much of total is reserved, from 0 to 100.
func Percent(reserved, total int) int {
	return reserved * 100 / total
}
`

// Bugs are the planted problems and the words that show a finding is about each.
var Bugs = []struct {
	Name       string
	Start, End int // lines of the function that contains the planted bug
	Re         *regexp.Regexp
}{
	{"race", 12, 16, regexp.MustCompile(`(?i)race|concurren|mutex|\block|atomic|oversell|thread`)},
	{"ignored error", 28, 30, regexp.MustCompile(`(?i)ignor|unchecked|discard|swallow|silently|error.{0,20}(not|never) (checked|handled)`)},
	{"division by zero", 34, 35, regexp.MustCompile(`(?i)zero|divid|division|panic`)},
}

// Result is how one model did.
type Result struct {
	Engine, Model string
	Seconds       float64
	Valid         bool
	Findings      int
	Found         []string // planted bugs it found
	Err           string
}

// Passed reports whether the model clears the bar for a recommendation.
func (r Result) Passed() bool {
	return r.Valid && len(r.Found) >= MinBugs && r.Seconds <= MaxTime.Seconds()
}

// Score reads a review against the planted bugs.
func Score(res review.Result) []string {
	var found []string
	for _, b := range Bugs {
		for _, f := range res.Findings {
			if f.Path == "stock/reserve.go" && f.Start <= b.End && f.End >= b.Start &&
				b.Re.MatchString(f.Title+" "+f.Body) {
				found = append(found, b.Name)
				break
			}
		}
	}
	return found
}

// Fixture writes the pull request into dir as a git repository and returns its diff.
func Fixture(dir string) (string, error) {
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=otto", "GIT_AUTHOR_EMAIL=otto@example.com",
			"GIT_COMMITTER_NAME=otto", "GIT_COMMITTER_EMAIL=otto@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "stock"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "stock", "stock.go"), []byte(baseFile), 0o644); err != nil {
		return "", err
	}
	for _, step := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "base"}} {
		if err := run(step...); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stock", "reserve.go"), []byte(changeFile), 0o644); err != nil {
		return "", err
	}
	for _, step := range [][]string{{"checkout", "-q", "-b", "feature"}, {"add", "-A"}, {"commit", "-q", "-m", "feat: reserve stock"}} {
		if err := run(step...); err != nil {
			return "", err
		}
	}
	cmd := exec.Command("git", "diff", "main...feature")
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// Options tune a run.
type Options struct {
	Timeout  time.Duration // per model
	Parallel int
	// NewEngine builds the engine for a spec; nil uses the real CLI.
	NewEngine func(engine.Spec) engine.Engine
	Progress  io.Writer
}

// Run reviews the fixture with every model of an engine and scores each one, through the same
// path as a real review: Otto's prompt, the read-only lockdown, and the repair of unusable answers.
func Run(ctx context.Context, engineName string, models []string, opt Options) ([]Result, error) {
	if opt.Timeout <= 0 {
		opt.Timeout = 4 * time.Minute
	}
	if opt.Parallel <= 0 {
		opt.Parallel = 4
	}
	if opt.NewEngine == nil {
		opt.NewEngine = func(s engine.Spec) engine.Engine { return engine.CLI{Spec: s} }
	}
	dir, err := os.MkdirTemp("", "otto-eval-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	rawDiff, err := Fixture(dir)
	if err != nil {
		return nil, err
	}
	prompt, err := review.BuildPrompt(review.PromptInput{
		Repo: "acme/shop", URL: "https://github.com/acme/shop/pull/1", Title: "feat: reserve stock",
		Body: "Adds Reserve, Release and Snapshot for the stock store. Reserve is called by many HTTP handlers at once.",
		Base: "main", HeadSHA: "feature", Files: diff.Parse(rawDiff).Files(), Diff: rawDiff, Language: "English",
	})
	if err != nil {
		return nil, err
	}

	results := make([]Result, len(models))
	sem := make(chan struct{}, opt.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, model := range models {
		wg.Add(1)
		go func(i int, model string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			spec := engine.Spec{Name: engineName, Model: model} // each model at its own default effort
			p := engine.Pool{Strategy: engine.Priority, Engines: []engine.Engine{opt.NewEngine(spec)}, AttemptTimeout: opt.Timeout}
			out, err := os.CreateTemp("", "otto-eval-answer-*")
			if err != nil {
				results[i] = Result{Engine: engineName, Model: model, Err: firstLine(err.Error())}
				return
			}
			out.Close()
			defer os.Remove(out.Name())
			accept := func(s string) error { _, err := review.ParseResult(s, dir); return err }
			start := time.Now()
			oc, err := p.Run(ctx, engine.Request{Prompt: prompt, Dir: dir, OutFile: out.Name(), Contract: review.OutputContract()}, 0, accept)
			r := Result{Engine: engineName, Model: model, Seconds: time.Since(start).Seconds()}
			if err != nil {
				r.Err = firstLine(err.Error())
			} else if res, perr := review.ParseResult(oc.Output, dir); perr == nil {
				r.Valid, r.Findings, r.Found = true, len(res.Findings), Score(res)
			}
			results[i] = r
			if opt.Progress != nil {
				mu.Lock()
				fmt.Fprintf(opt.Progress, "  %s: %.0fs, %d/%d bugs\n", model, r.Seconds, len(r.Found), len(Bugs))
				mu.Unlock()
			}
		}(i, model)
	}
	wg.Wait()
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Passed() != b.Passed() {
			return a.Passed()
		}
		if len(a.Found) != len(b.Found) {
			return len(a.Found) > len(b.Found)
		}
		return a.Seconds < b.Seconds
	})
	return results, nil
}

// Table renders results for the terminal.
func Table(rs []Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-58s %6s  %-5s %4s  %-6s  %s\n", "model", "time", "json", "bugs", "passed", "found / error")
	for _, r := range rs {
		detail := strings.Join(r.Found, ", ")
		if r.Err != "" {
			detail = r.Err
		}
		fmt.Fprintf(&b, "%-58s %5.0fs  %-5v %2d/%d  %-6v  %s\n", r.Model, r.Seconds, r.Valid, len(r.Found), len(Bugs), r.Passed(), detail)
	}
	fmt.Fprintf(&b, "\npassed = valid JSON, at least %d of %d planted bugs found, under %s\n", MinBugs, len(Bugs), MaxTime)
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
