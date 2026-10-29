package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Strategy decides which engine goes first.
type Strategy string

const (
	// RoundRobin starts each review with the next engine, spreading usage
	// across subscriptions so no single plan hits its limit.
	RoundRobin Strategy = "round-robin"
	// Priority always starts with the first engine and only falls back on failure.
	Priority Strategy = "priority"
)

// Pool tries engines in order until one returns an accepted answer.
type Pool struct {
	Strategy Strategy
	Engines  []Engine

	// AttemptTimeout caps one engine's attempt. Zero means each attempt only gets its fair
	// share of the time left (see attemptContext), so a hung engine can never use up the
	// whole review and leave the others no turn.
	AttemptTimeout time.Duration
	// MinAttempt is the smallest slice a non-final engine is given. Zero means DefaultMinAttempt.
	MinAttempt time.Duration
	// IdleLimit stops an engine that reports activity (see Reporter) after this long without
	// any. Such an engine is not cut off by its time slice while it keeps working: it may use
	// the review's time, keeping MinAttempt for each engine after it. Zero means DefaultIdleLimit.
	IdleLimit time.Duration

	// Labels name each engine for the log ("opencode · opencode/big-pickle · medium"), and Log
	// receives one line when an attempt starts and one when it ends, so the log shows which
	// model actually ran and why it stopped. Both are optional.
	Labels []string
	Log    func(format string, args ...any)
}

func (p Pool) label(i int) string {
	if i < len(p.Labels) && p.Labels[i] != "" {
		return p.Labels[i]
	}
	return p.Engines[i].Name()
}

func (p Pool) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

// DefaultMinAttempt is the shortest turn a non-final engine gets, so a slow but healthy
// engine is not cut off before it can answer a normal review.
const DefaultMinAttempt = 5 * time.Minute

// DefaultIdleLimit is how long an engine that reports activity may go without any before
// Otto concludes it is stuck. Free models are slow, but a working one still moves every few minutes.
const DefaultIdleLimit = 5 * time.Minute

// errStalled is the cause of an attempt stopped for lack of activity.
var errStalled = errors.New("stalled")

// workingContext bounds the turn of an engine that reports activity: it runs while it keeps
// working, until the review's deadline less a minimum turn for each engine after it, and is
// stopped after IdleLimit without activity. alive must be called on every sign of work.
func (p Pool) workingContext(ctx context.Context, enginesLeft int) (context.Context, context.CancelFunc, func(), time.Duration) {
	idle := p.IdleLimit
	if idle <= 0 {
		idle = DefaultIdleLimit
	}
	min := p.MinAttempt
	if min <= 0 {
		min = DefaultMinAttempt
	}
	c, cancelCause := context.WithCancelCause(ctx)
	var limit time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		limit = time.Until(deadline) - time.Duration(enginesLeft-1)*min
		if limit < min {
			limit = min
		}
		var stop context.CancelFunc
		c, stop = context.WithTimeout(c, limit)
		prev := cancelCause
		cancelCause = func(cause error) { prev(cause); stop() } // parent first, so the cause reaches c
	}
	var mu sync.Mutex
	last := time.Now()
	alive := func() { mu.Lock(); last = time.Now(); mu.Unlock() }
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(idle / 10)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-c.Done():
				return
			case <-t.C:
				mu.Lock()
				quiet := time.Since(last)
				mu.Unlock()
				if quiet >= idle {
					cancelCause(fmt.Errorf("%w: no activity for %s", errStalled, quiet.Round(time.Second)))
					return
				}
			}
		}
	}()
	var once sync.Once
	return c, func() { once.Do(func() { close(done); cancelCause(context.Canceled) }) }, alive, limit
}

// attemptContext bounds one engine's turn. The time left on the review is split between the
// engines still waiting (this one included), never below MinAttempt; the last engine gets
// everything that is left. An engine that hangs therefore fails over to the next one instead of
// holding the review until the overall timeout, when nobody would be left to answer.
func (p Pool) attemptContext(ctx context.Context, enginesLeft int) (context.Context, context.CancelFunc, time.Duration) {
	var limit time.Duration
	if deadline, ok := ctx.Deadline(); ok && enginesLeft > 1 {
		min := p.MinAttempt
		if min <= 0 {
			min = DefaultMinAttempt
		}
		remaining := time.Until(deadline)
		limit = max(remaining/time.Duration(enginesLeft), min)
		if limit >= remaining {
			limit = remaining / time.Duration(enginesLeft) // preserve a turn for each fallback
		}
	}
	if p.AttemptTimeout > 0 && (limit == 0 || p.AttemptTimeout < limit) {
		limit = p.AttemptTimeout
	}
	if limit <= 0 {
		c, cancel := context.WithCancel(ctx)
		return c, cancel, 0
	}
	c, cancel := context.WithTimeout(ctx, limit)
	return c, cancel, limit
}

// Outcome reports which engine answered and where the next review starts.
type Outcome struct {
	Output   string
	Engine   string
	Index    int
	Next     int
	Failures []string
}

type allFailedError struct {
	failures []error
	messages []string
}

func (e allFailedError) Error() string {
	return fmt.Sprintf("%s: %s", ErrAllFailed, strings.Join(e.messages, "; "))
}

func (e allFailedError) Unwrap() []error {
	return append([]error{ErrAllFailed}, e.failures...)
}

// Run starts at cursor (round-robin) or at 0 (priority). accept validates the
// answer; a rejected answer counts as a failure and triggers the fallback.
func (p Pool) Run(ctx context.Context, req Request, cursor int, accept func(string) error) (Outcome, error) {
	n := len(p.Engines)
	if n == 0 {
		return Outcome{}, fmt.Errorf("no engines configured")
	}
	start := 0
	if p.Strategy != Priority {
		start = ((cursor % n) + n) % n
	}

	var out Outcome
	var failures []error
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		e := p.Engines[idx]
		if req.Progress != nil {
			req.Progress(fmt.Sprintf("Analisando com %s…", e.Name()))
		}
		var attempt context.Context
		var cancel context.CancelFunc
		var limit time.Duration
		ereq := req
		if r, ok := e.(Reporter); ok && r.ReportsActivity() {
			var alive func()
			attempt, cancel, alive, limit = p.workingContext(ctx, n-i)
			ereq.Alive = alive
			idle := p.IdleLimit
			if idle <= 0 {
				idle = DefaultIdleLimit
			}
			p.logf("trying %s (while it keeps working, up to %s; stopped after %s without activity)",
				p.label(idx), limit.Round(time.Second), idle)
		} else {
			attempt, cancel, limit = p.attemptContext(ctx, n-i)
			if limit > 0 {
				p.logf("trying %s (up to %s)", p.label(idx), limit.Round(time.Second))
			} else {
				p.logf("trying %s", p.label(idx))
			}
		}
		started := time.Now()
		text, err := e.Run(attempt, ereq)
		if err == nil {
			if err = accept(text); err != nil {
				text, err = repairAnswer(attempt, e, req, text, err, accept, p.logf, p.label(idx))
			}
		}
		// This engine ran out of its turn while the review as a whole still has time: say so,
		// and go on to the next engine.
		if err != nil && ctx.Err() == nil && errors.Is(context.Cause(attempt), errStalled) {
			err = fmt.Errorf("%w; stopped", context.Cause(attempt))
		} else if err != nil && ctx.Err() == nil && errors.Is(attempt.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %s without an answer: %w", limit.Round(time.Second), context.DeadlineExceeded)
		}
		cancel()
		if err != nil {
			p.logf("%s failed after %s: %s", p.label(idx), time.Since(started).Round(time.Second), firstLine(err.Error()))
		} else {
			p.logf("%s answered in %s", p.label(idx), time.Since(started).Round(time.Second))
		}
		if err != nil {
			failure := &EngineError{Name: e.Name(), Err: err}
			out.Failures = append(out.Failures, failure.Error())
			failures = append(failures, failure)
			if ctx.Err() != nil {
				break
			}
			if req.Progress != nil && i+1 < n {
				req.Progress(fmt.Sprintf("%s indisponível; tentando o próximo engine…", e.Name()))
			}
			continue
		}
		out.Output, out.Engine, out.Index, out.Next = text, e.Name(), idx, (idx+1)%n
		return out, nil
	}
	return out, allFailedError{failures: failures, messages: out.Failures}
}

// firstLine keeps log lines short: engine errors can carry a long tail of output.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
