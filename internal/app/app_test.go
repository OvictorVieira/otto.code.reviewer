package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/format"
	"github.com/OvictorVieira/otto.code.reviewer/internal/github"
	"github.com/OvictorVieira/otto.code.reviewer/internal/trigger"
)

const repo = "OvictorVieira/shop"

const prDiff = `diff --git a/stock.go b/stock.go
--- a/stock.go
+++ b/stock.go
@@ -1,2 +1,3 @@
 package stock
+func Reserve(qty int) { saldo -= qty }
 // end
`

const findingOnLine2 = `{"findings":[{"title":"[P1] Reserve stock atomically","body":"Two orders can oversell.",
"confidence_score":0.9,"priority":1,"code_location":{"absolute_file_path":"stock.go","line_range":{"start":2,"end":2}}}],
"verification":[{"command":"go test ./...","result":"passed","note":"3 tests"}],
"overall_correctness":"patch is incorrect","overall_explanation":"Oversell under concurrency.","overall_confidence_score":0.8}`

const noFindings = `{"findings":[],"overall_correctness":"patch is correct","overall_explanation":"ok","overall_confidence_score":0.9}`

// ---- fakes ---------------------------------------------------------------

type reaction struct {
	commentID int64
	content   string
}

type fakeHost struct {
	mu          sync.Mutex
	comments    []trigger.Comment
	posted      []format.Review
	rejectFirst bool // reject the first review that has inline comments
	plain       []string
	reactions   []reaction
}

func (h *fakeHost) Login(context.Context) (string, error) { return "OvictorVieira", nil }
func (h *fakeHost) GetPR(_ context.Context, _ string, n int) (github.PR, error) {
	return github.PR{Number: n, Title: "feat: reserve stock", Body: "Closes #7", URL: "https://github.com/" + repo + "/pull/1",
		BaseRefName: "main", HeadRefOid: "abc1234567890"}, nil
}
func (h *fakeHost) Diff(context.Context, string, int) (string, error) { return prDiff, nil }
func (h *fakeHost) Repos(context.Context) ([]string, error)           { return []string{repo}, nil }
func (h *fakeHost) PostReview(_ context.Context, _ string, _ int, payload any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := payload.(format.Review)
	if h.rejectFirst && len(r.Comments) > 0 {
		h.rejectFirst = false
		return errors.New("HTTP 422: Line could not be resolved")
	}
	h.posted = append(h.posted, r)
	return nil
}
func (h *fakeHost) Comment(_ context.Context, _ string, _ int, body string) error {
	h.plain = append(h.plain, body)
	return nil
}
func (h *fakeHost) React(_ context.Context, _ string, id int64, content string) error {
	h.reactions = append(h.reactions, reaction{id, content})
	return nil
}
func (h *fakeHost) CommentsSince(context.Context, string, time.Time) ([]trigger.Comment, error) {
	return h.comments, nil
}

type fakeCheckout struct {
	dir     string
	removed *int
}

func (c fakeCheckout) Dir() string { return c.dir }
func (c fakeCheckout) Remove()     { *c.removed++ }

type fakeCheckouts struct {
	prepared, removed int
	swept             bool
}

func (f *fakeCheckouts) Prepare(context.Context, string, int, string) (Checkout, error) {
	f.prepared++
	return fakeCheckout{dir: "/tmp/otto-wt", removed: &f.removed}, nil
}
func (f *fakeCheckouts) Sweep(time.Duration) { f.swept = true }

type fakeEngine struct {
	name    string
	answer  string
	err     error
	onRun   func()
	got     *engine.Request
	callLog *[]string
}

func (e fakeEngine) Name() string { return e.name }
func (e fakeEngine) Run(_ context.Context, req engine.Request) (string, error) {
	if e.got != nil {
		*e.got = req
	}
	if e.callLog != nil {
		*e.callLog = append(*e.callLog, e.name)
	}
	if e.onRun != nil {
		e.onRun()
	}
	return e.answer, e.err
}

type harness struct {
	app       *App
	host      *fakeHost
	checkouts *fakeCheckouts
	out       *bytes.Buffer
	engines   map[string]fakeEngine
}

func newHarness(t *testing.T, engines ...fakeEngine) *harness {
	t.Helper()
	st, err := trigger.LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Repos.Selected = []string{repo}
	h := &harness{host: &fakeHost{}, checkouts: &fakeCheckouts{}, out: &bytes.Buffer{}, engines: map[string]fakeEngine{}}
	for _, e := range engines {
		cfg.Engines = append(cfg.Engines, engine.Spec{Name: e.name, Model: "m-" + e.name})
		h.engines[e.name] = e
	}
	h.app = &App{
		Cfg: cfg, State: st, Out: h.out, Host: h.host, Checkouts: h.checkouts,
		NewEngine: func(s engine.Spec) engine.Engine { return h.engines[s.Name] },
		Installed: func(string) bool { return true },
	}
	return h
}

func triggerComment(id int64, author string) trigger.Comment {
	return trigger.Comment{ID: id, Repo: repo, Author: author, Body: "/otto review",
		HTMLURL: "https://github.com/" + repo + "/pull/1#issuecomment-1"}
}

// ---- Review --------------------------------------------------------------

func TestReviewPostsInlineFindingWithEngineLabelAndRemovesCheckout(t *testing.T) {
	// Arrange
	var got engine.Request
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2, got: &got})

	// Act
	report, err := h.app.Review(context.Background(), repo, 1, Options{})

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if !report.Posted || report.Findings != 1 || report.Engine != "claude" {
		t.Errorf("report = %+v", report)
	}
	if len(h.host.posted) != 1 {
		t.Fatalf("posted %d reviews, want 1", len(h.host.posted))
	}
	r := h.host.posted[0]
	if r.CommitID != "abc1234567890" || len(r.Comments) != 1 || r.Comments[0].Path != "stock.go" || r.Comments[0].Line != 2 {
		t.Errorf("review = %+v", r)
	}
	if !strings.Contains(r.Comments[0].Body, "P1-orange") || !strings.Contains(r.Body, "`claude` · model `m-claude`") {
		t.Errorf("review body or badge wrong:\n%s\n%s", r.Body, r.Comments[0].Body)
	}
	if got.Dir != "/tmp/otto-wt" || !got.Verify || !strings.Contains(got.Prompt, "feat: reserve stock") {
		t.Errorf("engine request = %+v", got)
	}
	if h.checkouts.prepared != 1 || h.checkouts.removed != 1 {
		t.Errorf("checkout prepared %d, removed %d; want 1 and 1", h.checkouts.prepared, h.checkouts.removed)
	}
}

func TestReviewMovesFindingsToSummaryWhenGitHubRejectsInlineComments(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2})
	h.host.rejectFirst = true

	if _, err := h.app.Review(context.Background(), repo, 1, Options{}); err != nil {
		t.Fatal(err)
	}

	if len(h.host.posted) != 1 {
		t.Fatalf("posted %d reviews, want 1 retry", len(h.host.posted))
	}
	r := h.host.posted[0]
	if len(r.Comments) != 0 || !strings.Contains(r.Body, "Findings outside the diff") || !strings.Contains(r.Body, "Reserve stock atomically") {
		t.Errorf("retry should list the finding in the summary:\n%s", r.Body)
	}
}

func TestReviewFallsBackToNextEngineAndLabelsTheOneThatAnswered(t *testing.T) {
	var calls []string
	h := newHarness(t,
		fakeEngine{name: "claude", answer: "sorry, I hit my usage limit", callLog: &calls},
		fakeEngine{name: "codex", answer: findingOnLine2, callLog: &calls},
	)

	report, err := h.app.Review(context.Background(), repo, 1, Options{})

	if err != nil {
		t.Fatal(err)
	}
	if report.Engine != "codex" || strings.Join(calls, ",") != "claude,codex" {
		t.Errorf("engine = %s, calls = %v", report.Engine, calls)
	}
	if !strings.Contains(h.host.posted[0].Body, "`codex`") {
		t.Errorf("summary must name the engine that answered:\n%s", h.host.posted[0].Body)
	}
}

func TestRotationAdvancesBetweenReviewsButNotForExplicitEngine(t *testing.T) {
	var calls []string
	h := newHarness(t,
		fakeEngine{name: "claude", answer: noFindings, callLog: &calls},
		fakeEngine{name: "codex", answer: noFindings, callLog: &calls},
	)
	ctx := context.Background()

	_, _ = h.app.Review(ctx, repo, 1, Options{})
	_, _ = h.app.Review(ctx, repo, 1, Options{})
	_, _ = h.app.Review(ctx, repo, 1, Options{Engine: "claude"})

	if strings.Join(calls, ",") != "claude,codex,claude" {
		t.Errorf("calls = %v", calls)
	}
	if h.app.State.Rotation != 0 {
		t.Errorf("rotation = %d, want 0 (explicit engine must not move it)", h.app.State.Rotation)
	}
}

func TestDryRunPrintsInsteadOfPosting(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2})

	report, err := h.app.Review(context.Background(), repo, 1, Options{DryRun: true})

	if err != nil {
		t.Fatal(err)
	}
	if report.Posted || len(h.host.posted) != 0 {
		t.Error("dry run must not post")
	}
	if !strings.Contains(h.out.String(), "Reserve stock atomically") {
		t.Errorf("dry run must print the review:\n%s", h.out)
	}
}

func TestCheckoutIsRemovedWhenEveryEngineFails(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", err: errors.New("network down")})

	_, err := h.app.Review(context.Background(), repo, 1, Options{})

	if !errors.Is(err, engine.ErrAllFailed) {
		t.Fatalf("err = %v, want ErrAllFailed", err)
	}
	if h.checkouts.removed != 1 {
		t.Errorf("checkout must be removed after a failure, removed = %d", h.checkouts.removed)
	}
	if len(h.host.posted) != 0 {
		t.Error("nothing should be posted when every engine fails")
	}
}

// ---- Watch ---------------------------------------------------------------

func TestWatchAnswersTriggerOnceAndRemembersItAcrossRestarts(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2})
	h.host.comments = []trigger.Comment{triggerComment(10, "OvictorVieira")}
	ctx := context.Background()

	if err := h.app.Watch(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Simulate a restart: reload state from disk and poll again.
	reloaded, err := trigger.LoadState(h.app.State.Path())
	if err != nil {
		t.Fatal(err)
	}
	h.app.State = reloaded
	if err := h.app.Watch(ctx, true); err != nil {
		t.Fatal(err)
	}

	if len(h.host.posted) != 1 {
		t.Errorf("posted %d reviews, want exactly 1", len(h.host.posted))
	}
	if len(h.host.reactions) == 0 || h.host.reactions[0] != (reaction{10, "eyes"}) {
		t.Errorf("reactions = %v, want 👀 first", h.host.reactions)
	}
	if !h.checkouts.swept {
		t.Error("watch must sweep leftover worktrees on start")
	}
}

func TestWatchIgnoresTriggerFromOtherUsers(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	h.host.comments = []trigger.Comment{triggerComment(11, "stranger")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if len(h.host.posted)+len(h.host.reactions) != 0 {
		t.Errorf("a stranger must not trigger anything: posted %v, reactions %v", h.host.posted, h.host.reactions)
	}
}

func TestWatchReportsFailureWithConfusedReactionAndComment(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", err: errors.New("usage limit reached")})
	h.host.comments = []trigger.Comment{triggerComment(12, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if len(h.host.plain) != 1 || !strings.Contains(h.host.plain[0], "usage limit reached") || !strings.Contains(h.host.plain[0], "/otto review") {
		t.Errorf("failure comment = %v", h.host.plain)
	}
	if last := h.host.reactions[len(h.host.reactions)-1]; last.content != "confused" {
		t.Errorf("reactions = %v, want 😕 last", h.host.reactions)
	}
	if !h.app.State.Done(12) {
		t.Error("a failed request is still marked done; the user re-triggers it")
	}
}

func TestWatchGivesThumbsUpWhenThereAreNoFindings(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	h.host.comments = []trigger.Comment{triggerComment(13, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if last := h.host.reactions[len(h.host.reactions)-1]; last != (reaction{13, "+1"}) {
		t.Errorf("reactions = %v, want 👍 last", h.host.reactions)
	}
}

func TestShutdownDuringReviewLeavesRequestPendingForRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newHarness(t, fakeEngine{name: "claude", err: context.Canceled, onRun: cancel})
	h.host.comments = []trigger.Comment{triggerComment(14, "OvictorVieira")}

	if err := h.app.Watch(ctx, true); err != nil {
		t.Fatal(err)
	}

	if h.app.State.Done(14) {
		t.Error("an interrupted review must stay pending so it runs again after restart")
	}
	if len(h.host.plain) != 0 {
		t.Errorf("shutdown is not a failure to report: %v", h.host.plain)
	}
}

// ---- ParseRef ------------------------------------------------------------

func TestParseRefAcceptsCommonPullRequestReferences(t *testing.T) {
	for _, ref := range []string{
		"https://github.com/OvictorVieira/otto.code.reviewer/pull/12",
		"https://github.com/OvictorVieira/otto.code.reviewer/pull/12/",
		"OvictorVieira/otto.code.reviewer/pull/12",
		"OvictorVieira/otto.code.reviewer#12",
	} {
		r, n, err := ParseRef(ref)
		if err != nil || r != "OvictorVieira/otto.code.reviewer" || n != 12 {
			t.Errorf("ParseRef(%q) = %q, %d, %v", ref, r, n, err)
		}
	}
	for _, bad := range []string{"", "12", "https://github.com/o/r/issues/12", "o/r"} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) should fail", bad)
		}
	}
}
