package engine

import (
	"context"
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

func TestTheFollowUpIsTriedOnlyOnceAndThenTheNextEngineTakesOver(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		continuing{"opencode", "no json", "still no json", &runs},
		fake{"claude", "ok", nil, &runs},
	}}

	out, err := p.Run(context.Background(), Request{Prompt: "review"}, 0, accept)

	if err != nil || out.Engine != "claude" {
		t.Fatalf("out = %+v, err = %v, want claude after the follow-up also failed", out, err)
	}
	if !slices.Equal(runs, []string{"opencode", "opencode+followup", "claude"}) {
		t.Errorf("runs = %v, want exactly one follow-up", runs)
	}
}

func TestAnEngineThatCannotContinueIsNotAskedAgain(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{fake{"claude", "garbage", nil, &runs}}}

	_, err := p.Run(context.Background(), Request{Prompt: "review"}, 0, accept)

	if err == nil || !slices.Equal(runs, []string{"claude"}) {
		t.Errorf("err = %v, runs = %v, want a single run and a failure", err, runs)
	}
}

func TestOpencodeFollowUpContinuesTheSessionWithoutResendingThePrompt(t *testing.T) {
	c, err := BuildCommand(Spec{Name: "opencode", Model: "m", Effort: "high"}, Request{Prompt: "the big prompt", Dir: "/wt", Followup: "emit the JSON now"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.Args, "--continue") {
		t.Errorf("args %v must continue the last session", c.Args)
	}
	if c.Stdin != "" {
		t.Errorf("the follow-up must not resend the prompt, stdin = %d bytes", len(c.Stdin))
	}
	if last := c.Args[len(c.Args)-1]; last != "emit the JSON now" {
		t.Errorf("last arg = %q, want the follow-up message", last)
	}
	if !containsSeq(c.Args, []string{"--dir", "/wt"}) || !containsSeq(c.Args, []string{"-m", "m"}) {
		t.Errorf("args %v must keep the directory and model", c.Args)
	}
}

func TestOpencodeThroughTheRealCommandAnswersTheFollowUp(t *testing.T) {
	fakeCLI(t, "opencode", `for arg in "$@"; do
  if [ "$arg" = "--continue" ]; then printf 'ok'; exit 0; fi
done
cat >/dev/null
printf 'I have the plan. Confirm and I will emit the JSON.'`)
	engine := CLI{Spec: Spec{Name: "opencode"}}
	p := Pool{Strategy: Priority, Engines: []Engine{engine}}

	out, err := p.Run(context.Background(), Request{Prompt: strings.Repeat("x", 1000), Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")}, 0, accept)

	if err != nil || out.Output != "ok" {
		t.Fatalf("out = %+v, err = %v, want the answer given after the follow-up", out, err)
	}
}
