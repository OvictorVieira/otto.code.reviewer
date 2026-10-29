package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/learn"
)

const ottoFinding = "**<sub><sub>![P3 Badge](https://img.shields.io/badge/P3-lightgrey?style=flat)</sub></sub>  Move helper out of lib**\n\n" +
	"The helper imports flutter_test.\n\nUseful? React with 👍 / 👎.\n\n<!-- otto-review-finding:v1 -->"

type fakeFeedback struct {
	threads map[int][]learn.Thread
	prs     []int
	err     error
	reads   int
}

func (f *fakeFeedback) Threads(_ context.Context, _ string, n int) ([]learn.Thread, error) {
	f.reads++
	return f.threads[n], f.err
}
func (f *fakeFeedback) RecentPRs(context.Context, string, int) ([]int, error) { return f.prs, nil }

func rejectedThread() learn.Thread {
	return learn.Thread{Path: "pkg/a.dart", Line: 4, Resolved: true, Comments: []learn.ThreadComment{
		{ID: 10, Body: ottoFinding, Down: 1},
		{ID: 11, Author: "dev", Body: "Won't fix. only imported from test/"},
	}}
}

func withLearning(t *testing.T, h *harness, fb *fakeFeedback) {
	t.Helper()
	h.app.Feedback = fb
	h.app.Learn = learn.Store{Dir: t.TempDir()}
}

func TestReviewBringsPriorFindingsAndLessonsIntoThePrompt(t *testing.T) {
	var got engine.Request
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings, got: &got})
	fb := &fakeFeedback{threads: map[int][]learn.Thread{1: {rejectedThread()}}}
	withLearning(t, h, fb)
	mem, _ := h.app.Learn.Load(repo)
	mem.Lessons = []learn.Lesson{
		{ID: "a", Kind: "avoid", Rule: "Do not flag test helpers in lib."},
		{ID: "b", Kind: "check", Rule: "Disabled rule.", Disabled: true},
	}
	if err := h.app.Learn.Save(repo, mem); err != nil {
		t.Fatal(err)
	}

	if _, err := h.app.Review(context.Background(), repo, 1, Options{}); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"[avoid] Do not flag test helpers in lib.", "[rejected] P3 Move helper out of lib", "Won't fix. only imported from test/"} {
		if !strings.Contains(got.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(got.Prompt, "Disabled rule.") {
		t.Error("a disabled lesson must not reach the prompt")
	}
}

func TestReviewStillRunsWhenFeedbackCannotBeRead(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	withLearning(t, h, &fakeFeedback{err: errors.New("graphql down")})

	report, err := h.app.Review(context.Background(), repo, 1, Options{})

	if err != nil || !report.Posted {
		t.Fatalf("review must not depend on learning: %v %+v", err, report)
	}
}

func TestReviewIgnoresLearningWhenDisabledInConfig(t *testing.T) {
	var got engine.Request
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings, got: &got})
	fb := &fakeFeedback{threads: map[int][]learn.Thread{1: {rejectedThread()}}}
	withLearning(t, h, fb)
	h.app.Cfg.Learning = false

	if _, err := h.app.Review(context.Background(), repo, 1, Options{}); err != nil {
		t.Fatal(err)
	}

	if fb.reads != 0 || strings.Contains(got.Prompt, "already made") {
		t.Fatalf("learning is off: reads=%d", fb.reads)
	}
}

const distillAnswer = `{"lessons":[{"id":"","kind":"avoid","rule":"Do not flag test-only helpers that live in lib.","why":"Maintainers rejected it: tree-shaken out of releases.","evidence":1}]}`

func TestLearnRepoTurnsRejectionIntoALessonOnce(t *testing.T) {
	var got engine.Request
	h := newHarness(t, fakeEngine{name: "claude", answer: distillAnswer, got: &got})
	fb := &fakeFeedback{prs: []int{1}, threads: map[int][]learn.Thread{1: {rejectedThread()}}}
	withLearning(t, h, fb)

	sum, err := h.app.LearnRepo(context.Background(), repo, true)

	if err != nil || !sum.Distill || sum.Signals != 1 || sum.Lessons != 1 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
	if !strings.Contains(got.Prompt, "[rejected]") || !strings.Contains(got.Prompt, "only imported from test/") {
		t.Fatal("distill prompt must carry the outcome and the maintainers' reason")
	}
	lessons, _ := h.app.Lessons(repo)
	if len(lessons) != 1 || lessons[0].ID == "" || lessons[0].Kind != "avoid" {
		t.Fatalf("lessons = %+v", lessons)
	}

	// Nothing new: the same feedback is not learned twice, and a recent sweep is throttled.
	again, err := h.app.LearnRepo(context.Background(), repo, false)
	if err != nil || !again.Throttle || again.Distill {
		t.Fatalf("second pass = %+v err=%v", again, err)
	}
	forced, err := h.app.LearnRepo(context.Background(), repo, true)
	if err != nil || forced.Distill || forced.Signals != 0 {
		t.Fatalf("forced pass over already-learned feedback = %+v err=%v", forced, err)
	}
}

func TestLearnRepoKeepsOldLessonsWhenTheEngineAnswersGarbage(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: "sorry, no JSON"})
	fb := &fakeFeedback{prs: []int{1}, threads: map[int][]learn.Thread{1: {rejectedThread()}}}
	withLearning(t, h, fb)
	mem, _ := h.app.Learn.Load(repo)
	mem.Lessons = []learn.Lesson{{ID: "keep", Kind: "style", Rule: "Existing lesson."}}
	_ = h.app.Learn.Save(repo, mem)

	if _, err := h.app.LearnRepo(context.Background(), repo, true); err == nil {
		t.Fatal("expected an error")
	}

	lessons, _ := h.app.Lessons(repo)
	if len(lessons) != 1 || lessons[0].ID != "keep" {
		t.Fatalf("lessons must survive a failed update: %+v", lessons)
	}
	if m, _ := h.app.Learn.Load(repo); len(m.Unlearned()) != 1 {
		t.Fatal("feedback must stay unlearned so the next pass retries")
	}
}

func TestEditLessonPinsDisablesAndForgets(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	withLearning(t, h, &fakeFeedback{})
	mem, _ := h.app.Learn.Load(repo)
	mem.Lessons = []learn.Lesson{{ID: "abcd1234", Kind: "avoid", Rule: "r"}}
	_ = h.app.Learn.Save(repo, mem)

	for _, a := range []string{"pin", "disable"} {
		if err := h.app.EditLesson(repo, "abcd", a); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := h.app.Lessons(repo)
	if !l[0].Pinned || !l[0].Disabled {
		t.Fatalf("flags not applied: %+v", l[0])
	}
	if err := h.app.EditLesson(repo, "abcd", "forget"); err != nil {
		t.Fatal(err)
	}
	if l, _ := h.app.Lessons(repo); len(l) != 0 {
		t.Fatal("lesson not forgotten")
	}
	if err := h.app.EditLesson(repo, "zzzz", "pin"); err == nil {
		t.Fatal("unknown id must error")
	}
}

func TestPromptGrowthIsBoundedNoMatterHowMuchWasLearned(t *testing.T) {
	var base engine.Request
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings, got: &base})
	withLearning(t, h, &fakeFeedback{})
	if _, err := h.app.Review(context.Background(), repo, 1, Options{}); err != nil {
		t.Fatal(err)
	}

	var many engine.Request
	h2 := newHarness(t, fakeEngine{name: "claude", answer: noFindings, got: &many})
	withLearning(t, h2, &fakeFeedback{})
	mem, _ := h2.app.Learn.Load(repo)
	for i := 0; i < learn.MaxLessons; i++ {
		mem.Lessons = append(mem.Lessons, learn.Lesson{ID: strings.Repeat("x", i+1), Kind: "check", Evidence: 3,
			Rule: strings.Repeat("a long lesson that would cost tokens ", 5), Why: strings.Repeat("because ", 20)})
	}
	_ = h2.app.Learn.Save(repo, mem)
	if _, err := h2.app.Review(context.Background(), repo, 1, Options{}); err != nil {
		t.Fatal(err)
	}

	grew := len(many.Prompt) - len(base.Prompt)
	if grew <= 0 {
		t.Fatal("lessons should reach the prompt")
	}
	if grew > learn.LessonBudget+600 { // budget + section heading and formatting
		t.Fatalf("prompt grew by %d chars, budget is %d", grew, learn.LessonBudget)
	}
}

func TestReviewLogsTheModelEachEngineRunsWithAndFlagsTheCLIDefault(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	h.app.Cfg.Engines = []engine.Spec{{Name: "claude", Model: "sonnet", Effort: "high"}}
	if _, err := h.app.Review(context.Background(), repo, 1, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "trying claude · sonnet · high") {
		t.Fatalf("the log must say which model ran:\n%s", h.out.String())
	}

	if got := specLabel(engine.Spec{Name: "opencode", Effort: "medium"}); got != "opencode · CLI default model · medium" {
		t.Fatalf("an engine with no model must be labelled as the CLI default, got %q", got)
	}
}
