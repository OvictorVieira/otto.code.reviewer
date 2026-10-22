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
			err = accept(text)
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
