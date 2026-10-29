package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
}

// DefaultMinAttempt is the shortest turn a non-final engine gets, so a slow but healthy
// engine is not cut off before it can answer a normal review.
const DefaultMinAttempt = 5 * time.Minute

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
			limit = 0 // not enough time to split; let it use what is left
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

// followup asks an engine that finished its analysis without giving the answer to give it now.
const followup = "Reply now with ONLY the final review as the single JSON object described in the instructions. " +
	"Do not ask for confirmation, do not add a plan or any other text, and do not use tools."

// askForTheAnswer gives an engine that can continue its session one chance to answer after its
// output was rejected, for example when a model ends with "confirm and I will write the JSON".
// Otherwise, or when the follow-up fails too, the original rejection stands.
func askForTheAnswer(ctx context.Context, e Engine, req Request, rejected error, accept func(string) error) (string, error) {
	c, ok := e.(Continuer)
	if !ok || !c.CanContinue() || ctx.Err() != nil {
		return "", rejected
	}
	req.Followup = followup
	text, err := e.Run(ctx, req)
	if err != nil {
		return "", rejected
	}
	if err := accept(text); err != nil {
		return "", rejected
	}
	return text, nil
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
		attempt, cancel, limit := p.attemptContext(ctx, n-i)
		text, err := e.Run(attempt, req)
		if err == nil {
			if err = accept(text); err != nil {
				text, err = askForTheAnswer(attempt, e, req, err, accept)
			}
		}
		// This engine ran out of its turn while the review as a whole still has time: say so,
		// and go on to the next engine.
		if err != nil && ctx.Err() == nil && errors.Is(attempt.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %s without an answer: %w", limit.Round(time.Second), context.DeadlineExceeded)
		}
		cancel()
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
