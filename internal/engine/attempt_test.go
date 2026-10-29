package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// hang blocks until its turn is cancelled, like an engine stuck waiting on a model that never answers.
type hang struct{ name string }

func (h hang) Name() string { return h.name }
func (h hang) Run(ctx context.Context, _ Request) (string, error) {
	<-ctx.Done()
	return "", errors.New(h.name + ": " + ctx.Err().Error())
}

type answers struct{ name string }

func (a answers) Name() string                                 { return a.name }
func (a answers) Run(context.Context, Request) (string, error) { return "ok", nil }

func TestAHungEngineFailsOverInsteadOfConsumingTheWholeReview(t *testing.T) {
	// Regression: every engine used to share the one review deadline, so a hung engine held
	// it until the end and nobody else got a turn ("opencode took too long, nobody reviewed").
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	p := Pool{Strategy: Priority, MinAttempt: 50 * time.Millisecond,
		Engines: []Engine{hang{"opencode"}, answers{"claude"}}}

	start := time.Now()
	out, err := p.Run(ctx, Request{}, 0, accept)

	if err != nil || out.Engine != "claude" || out.Output != "ok" {
		t.Fatalf("the next engine must take over: out=%+v err=%v", out, err)
	}
	if time.Since(start) > 800*time.Millisecond {
		t.Fatalf("the hung engine was not cut off in time: %s", time.Since(start))
	}
	if len(out.Failures) != 1 || !containsAny(out.Failures[0], "timed out after") {
		t.Fatalf("the timeout must be recorded: %v", out.Failures)
	}
}

func TestEveryEngineGetsATurnEvenWhenTheFirstTwoHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	p := Pool{Strategy: Priority, MinAttempt: 50 * time.Millisecond,
		Engines: []Engine{hang{"opencode"}, hang{"codex"}, answers{"claude"}}}

	out, err := p.Run(ctx, Request{}, 0, accept)

	if err != nil || out.Engine != "claude" || len(out.Failures) != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestWhenEveryEngineHangsTheFailureNamesEachOneAsATimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	p := Pool{Strategy: Priority, MinAttempt: 50 * time.Millisecond,
		Engines: []Engine{hang{"opencode"}, hang{"claude"}}}

	_, err := p.Run(ctx, Request{}, 0, accept)

	failures := Failures(err)
	if len(failures) != 2 {
		t.Fatalf("failures = %+v (err=%v)", failures, err)
	}
	for _, f := range failures {
		if f.Kind != FailTimeout {
			t.Fatalf("%s: kind = %s, want timeout", f.Engine, f.Kind)
		}
	}
}

func TestAQuotaFailureStillFallsBackToTheNextEngine(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		fake{"claude", "", &QuotaError{Details: "usage limit reached"}, &runs},
		answers{"codex"},
	}}

	out, err := p.Run(context.Background(), Request{}, 0, accept)

	if err != nil || out.Engine != "codex" {
		t.Fatalf("quota on one engine must not stop the review: out=%+v err=%v", out, err)
	}
}

func TestAttemptTimeoutConfigCapsAnEngineEvenWithoutAnOverallDeadline(t *testing.T) {
	p := Pool{Strategy: Priority, AttemptTimeout: 80 * time.Millisecond,
		Engines: []Engine{hang{"opencode"}, answers{"claude"}}}

	out, err := p.Run(context.Background(), Request{}, 0, accept)

	if err != nil || out.Engine != "claude" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestLastEngineKeepsAllTheTimeThatIsLeft(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := Pool{MinAttempt: time.Second}

	attempt, stop, limit := p.attemptContext(ctx, 1)
	defer stop()

	if limit != 0 {
		t.Fatalf("the last engine must not be capped below the review deadline, got %s", limit)
	}
	if d, _ := attempt.Deadline(); time.Until(d) < 4*time.Second {
		t.Fatal("the last engine lost review time")
	}
}

func TestOpencodeFreeTierLimitIsAQuotaFailure(t *testing.T) {
	// opencode's default model is a free one; when its free tier runs out the CLI reports this text.
	msg := "AI_APICallError: Free usage limit reached. Free models work in OpenCode; subscribe to keep going"
	if !isQuotaMessage(msg) {
		t.Fatal("a free-tier limit must be classified as quota so the next engine takes over")
	}
	got := classify(&EngineError{Name: "opencode", Err: errors.New(msg)})
	if got.Kind != FailQuota {
		t.Fatalf("kind = %s, want quota", got.Kind)
	}
}

func TestPoolLogsWhichModelRanAndWhyItStopped(t *testing.T) {
	var lines []string
	p := Pool{Strategy: Priority, AttemptTimeout: 60 * time.Millisecond,
		Labels:  []string{"opencode · CLI default model", "claude · sonnet · high"},
		Log:     func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
		Engines: []Engine{hang{"opencode"}, answers{"claude"}}}

	if _, err := p.Run(context.Background(), Request{}, 0, accept); err != nil {
		t.Fatal(err)
	}

	log := strings.Join(lines, "\n")
	for _, want := range []string{"trying opencode · CLI default model (up to", "opencode · CLI default model failed after", "timed out after", "trying claude · sonnet · high", "claude · sonnet · high answered in"} {
		if !strings.Contains(log, want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
}
