package engine

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// continuing is an engine that can continue its session: the first run answers with `first`, and
// a run that carries a follow-up answers with `second`.
type continuing struct {
	name          string
	first, second string
	runs          *[]string
}

func (c continuing) Name() string      { return c.name }
func (c continuing) CanContinue() bool { return true }
func (c continuing) Run(_ context.Context, req Request) (string, error) {
	if req.Followup != "" {
		*c.runs = append(*c.runs, c.name+"+followup")
		return c.second, nil
	}
	*c.runs = append(*c.runs, c.name)
	return c.first, nil
}

func TestAnEngineThatEndedWithoutTheAnswerIsAskedForItInTheSameSession(t *testing.T) {
	// Regression: the model finished its analysis but wrote "confirm and I will emit the JSON" and
	// stopped. opencode runs one shot, nobody answers, and the whole review was lost.
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{continuing{"opencode", "I have the plan. Confirm and I will emit the JSON.", "ok", &runs}}}

	out, err := p.Run(context.Background(), Request{Prompt: "review"}, 0, accept)

	if err != nil || out.Output != "ok" || out.Engine != "opencode" {
		t.Fatalf("out = %+v, err = %v, want the answer from the follow-up", out, err)
	}
	if !slices.Equal(runs, []string{"opencode", "opencode+followup"}) {
		t.Errorf("runs = %v, want one run and one follow-up", runs)
	}
}

func TestAFollowUpThatFailsIsRetriedOnceMoreBeforeTheNextEngineTakesOver(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		continuing{"opencode", "no json", "still no json", &runs},
		fake{"claude", "ok", nil, &runs},
	}}

	out, err := p.Run(context.Background(), Request{Prompt: "review"}, 0, accept)

	if err != nil || out.Engine != "claude" {
		t.Fatalf("out = %+v, err = %v, want claude after the corrections also failed", out, err)
	}
	if !slices.Equal(runs, []string{"opencode", "opencode+followup", "opencode+followup", "claude"}) {
		t.Errorf("runs = %v, want the original run, %d corrections, then claude", runs, maxRepairs)
	}
}

// scripted answers each run with the next of its outputs and records the requests it received.
type scripted struct {
	name string
	outs []string
	reqs *[]Request
}

func (s scripted) Name() string { return s.name }
func (s scripted) Run(_ context.Context, req Request) (string, error) {
	n := len(*s.reqs)
	*s.reqs = append(*s.reqs, req)
	if n >= len(s.outs) {
		return "", nil
	}
	return s.outs[n], nil
}

func TestBrokenJSONFromAnyEngineIsFixedByAskingItToReformatItsOwnAnswer(t *testing.T) {
	// The model did the review but wrote broken JSON. Otto must not discard it or jump to another engine:
	// it sends the rejected answer back with the required shape and gets the usable one.
	var reqs []Request
	e := scripted{"claude", []string{`{"findings": [ {"title": "[P1] race"  oops`, "ok"}, &reqs}
	p := Pool{Strategy: Priority, Engines: []Engine{e, fake{"codex", "ok", nil, new([]string)}}}

	out, err := p.Run(context.Background(), Request{Prompt: "REVIEW REQUEST", Contract: "SHAPE: {findings:[]}"}, 0, accept)

	if err != nil || out.Engine != "claude" || out.Output != "ok" {
		t.Fatalf("out = %+v, err = %v, want claude's corrected answer", out, err)
	}
	if len(reqs) != 2 {
		t.Fatalf("claude ran %d times, want the review and one correction", len(reqs))
	}
	fix := reqs[1].Prompt
	for _, want := range []string{"could not be used", "SHAPE: {findings:[]}", `"title": "[P1] race"  oops`} {
		if !strings.Contains(fix, want) {
			t.Errorf("the correction request is missing %q:\n%s", want, fix)
		}
	}
	if strings.Contains(fix, "REVIEW REQUEST") {
		t.Error("reformatting must not resend the whole review request")
	}
}

func TestWhenReformattingIsNotEnoughTheOriginalRequestIsRunAgainWithAReminder(t *testing.T) {
	// A plan with no review in it cannot be reformatted: the second correction runs the request again.
	var reqs []Request
	e := scripted{"claude", []string{"Here is my plan. Shall I proceed?", "still a plan", "ok"}, &reqs}
	p := Pool{Strategy: Priority, Engines: []Engine{e}}

	out, err := p.Run(context.Background(), Request{Prompt: "REVIEW REQUEST", Contract: "SHAPE"}, 0, accept)

	if err != nil || out.Output != "ok" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if len(reqs) != 3 || !strings.Contains(reqs[2].Prompt, "REVIEW REQUEST") || !strings.Contains(reqs[2].Prompt, "ONLY the final review") {
		t.Fatalf("third run must repeat the request with a reminder: %d runs, %q", len(reqs), reqs[len(reqs)-1].Prompt)
	}
}

func TestCorrectionsAreBoundedAndTheFailureIsReportedWhenNothingWorks(t *testing.T) {
	var reqs []Request
	e := scripted{"claude", []string{"bad", "bad", "bad", "bad", "bad"}, &reqs}
	p := Pool{Strategy: Priority, Engines: []Engine{e}}

	_, err := p.Run(context.Background(), Request{Prompt: "r", Contract: "S"}, 0, func(string) error { return errors.New("no review JSON found in the output") })

	if err == nil {
		t.Fatal("a review nobody could produce must still be reported as failed")
	}
	if len(reqs) != 3 {
		t.Fatalf("claude ran %d times, want the review plus at most %d corrections", len(reqs), maxRepairs)
	}
	if f := Failures(err); len(f) != 1 || f[0].Kind != FailInvalid {
		t.Fatalf("failures = %+v, want one invalid-output failure", f)
	}
}

func TestACorrectionThatTimesOutDoesNotCountAsAFreshReview(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		fake{"claude", "garbage", nil, &runs},
		fake{"codex", "ok", nil, &runs},
	}}

	out, err := p.Run(context.Background(), Request{Prompt: "review"}, 0, accept)

	if err != nil || out.Engine != "codex" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestOpencodeDoesNotContinueAnUnpinnedSession(t *testing.T) {
	e := CLI{Spec: Spec{Name: "opencode"}}
	if e.CanContinue() {
		t.Fatal("the latest session may belong to another parallel review")
	}
	if _, err := BuildCommand(e.Spec, Request{Dir: "/wt", Followup: "emit the JSON now"}); err == nil {
		t.Fatal("a follow-up without a pinned session ID must be rejected")
	}
}

func TestOpencodeRepairsWithAFreshRequest(t *testing.T) {
	fakeCLI(t, "opencode", `for arg in "$@"; do
  if [ "$arg" = "--continue" ]; then echo 'unsafe session continuation' >&2; exit 1; fi
done
input=$(cat)
case "$input" in *"Your previous answer"*) printf 'ok';; *) printf 'I have the plan. Confirm and I will emit the JSON.';; esac`)
	engine := CLI{Spec: Spec{Name: "opencode"}}
	p := Pool{Strategy: Priority, Engines: []Engine{engine}}

	out, err := p.Run(context.Background(), Request{Prompt: strings.Repeat("x", 1000), Contract: "{findings:[]}", Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")}, 0, accept)

	if err != nil || out.Output != "ok" {
		t.Fatalf("out = %+v, err = %v, want the answer from a fresh repair request", out, err)
	}
}

func TestAnEngineThatSaysItIsOutOfQuotaIsNotAskedToCorrectItsAnswer(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		fake{"claude", "usage limit reached|1760000000", nil, &runs},
		fake{"codex", "ok", nil, &runs},
	}}

	out, err := p.Run(context.Background(), Request{Prompt: "review", Contract: "S"}, 0, accept)

	if err != nil || out.Engine != "codex" || !slices.Equal(runs, []string{"claude", "codex"}) {
		t.Fatalf("out = %+v err = %v runs = %v, want an immediate fallback", out, err, runs)
	}
}
