package engine

import (
	"context"
	"fmt"
	"strings"
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
		text, err := e.Run(ctx, req)
		if err == nil {
			if err = accept(text); err != nil {
				text, err = askForTheAnswer(ctx, e, req, err, accept)
			}
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
