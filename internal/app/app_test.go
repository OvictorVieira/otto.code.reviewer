package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// identity records which GitHub identity ("" = the user's own gh login) made a write.
type write struct {
	what string
	as   string
}

type fakeHost struct {
	mu          sync.Mutex
	comments    []trigger.Comment
	posted      []format.Review
	rejectFirst bool // reject the first review that has inline comments
	plain       []string
	reactions   []reaction
	writes      []write
	diff        string // overrides the default one-file diff
}

func (h *fakeHost) Login(context.Context) (string, error) { return "OvictorVieira", nil }
func (h *fakeHost) GetPR(_ context.Context, _ string, n int) (github.PR, error) {
	return github.PR{Number: n, Title: "feat: reserve stock", Body: "Closes #7", URL: "https://github.com/" + repo + "/pull/1",
		BaseRefName: "main", HeadRefOid: "abc1234567890"}, nil
}
func (h *fakeHost) Diff(context.Context, string, int) (string, error) {
	if h.diff != "" {
		return h.diff, nil
	}
	return prDiff, nil
}
func (h *fakeHost) Repos(context.Context) ([]string, error)           { return []string{repo}, nil }
func (h *fakeHost) PostReview(ctx context.Context, _ string, _ int, payload any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, write{"review", github.TokenFrom(ctx)})
	r := payload.(format.Review)
	if h.rejectFirst && len(r.Comments) > 0 {
		h.rejectFirst = false
		return errors.New("HTTP 422: Line could not be resolved")
	}
	h.posted = append(h.posted, r)
	return nil
}
func (h *fakeHost) Comment(ctx context.Context, _ string, _ int, body string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, write{"comment", github.TokenFrom(ctx)})
	h.plain = append(h.plain, body)
	return nil
}
func (h *fakeHost) React(ctx context.Context, _ string, id int64, content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, write{"react", github.TokenFrom(ctx)})
	h.reactions = append(h.reactions, reaction{id, content})
	return nil
}
func (h *fakeHost) CommentsSince(context.Context, string, time.Time) ([]trigger.Comment, error) {
	return h.comments, nil
}

type fakeCheckout struct {
	dir     string
	removed *int
	mu      *sync.Mutex
}

func (c fakeCheckout) Dir() string { return c.dir }
func (c fakeCheckout) Remove()     { c.mu.Lock(); defer c.mu.Unlock(); *c.removed++ }

type fakeCheckouts struct {
	mu                sync.Mutex
	prepared, removed int
	swept             bool
}

func (f *fakeCheckouts) Prepare(context.Context, string, int, string) (Checkout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	return fakeCheckout{dir: "/tmp/otto-wt", removed: &f.removed, mu: &f.mu}, nil
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
	if got.Dir != "/tmp/otto-wt" || !strings.Contains(got.Prompt, "feat: reserve stock") {
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
	// claude's unusable answer is corrected twice (reformat, then run again) before codex takes over.
	if report.Engine != "codex" || strings.Join(calls, ",") != "claude,claude,claude,codex" {
		t.Errorf("engine = %s, calls = %v", report.Engine, calls)
	}
	if !strings.Contains(h.host.posted[0].Body, "`codex`") {
		t.Errorf("summary must name the engine that answered:\n%s", h.host.posted[0].Body)
	}
}

func TestManualReviewShowsLocalProgress(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "copilot", answer: noFindings})
	var statuses []string
	progressStopped := false

	if _, err := h.app.Review(context.Background(), repo, 1, Options{
		ShowProgress: true, Progress: func(status string) { statuses = append(statuses, status) },
		ProgressDone: func() { progressStopped = true },
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Carregando detalhes do pull request",
		"Analisando alterações do pull request",
		"Preparando worktree",
		"Analisando com copilot",
	} {
		if !strings.Contains(strings.Join(statuses, "\n"), want) {
			t.Errorf("progress updates missing %q:\n%v", want, statuses)
		}
	}
	if !progressStopped {
		t.Error("progress display must be stopped before returning")
	}
	if got := h.out.String(); !strings.Contains(got, "Otto Review: 0 finding(s)") ||
		strings.Contains(got, "loading pull request details") {
		t.Errorf("manual review should print only the final summary, got:\n%s", got)
	}
}

func TestManualQuotaExhaustionPostsResetDetailsToPR(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "copilot", err: &engine.QuotaError{Details: "quota resets in 2 hours"}})

	_, err := h.app.Review(context.Background(), repo, 1, Options{ShowProgress: true, PostQuotaFailure: true})
	if !errors.Is(err, engine.ErrQuotaExceeded) {
		t.Fatalf("err = %v, want quota exhaustion", err)
	}
	if len(h.host.plain) != 1 || !strings.Contains(h.host.plain[0], "Otto Review") ||
		!strings.Contains(h.host.plain[0], "quota resets in 2 hours") {
		t.Errorf("quota notice = %v", h.host.plain)
	}
	if len(h.host.posted) != 0 {
		t.Error("quota failure must not be reported as a completed code review")
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

// ---- Self-update ---------------------------------------------------------

func TestWatchUpdatesAndRestartsBeforeHandlingARequest(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	h.host.comments = []trigger.Comment{triggerComment(20, "OvictorVieira")}
	var order []string
	h.app.Upgrade = func(context.Context) (bool, error) { order = append(order, "upgrade"); return true, nil }
	h.app.Restart = func() error { order = append(order, "restart"); return nil }

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if strings.Join(order, ",") != "upgrade,restart" {
		t.Errorf("order = %v", order)
	}
	if len(h.host.posted) != 0 || h.app.State.Done(20) {
		t.Error("the request must be left for the new version to handle")
	}
}

func TestWatchCarriesOnWhenAlreadyUpToDateOrUpdateFails(t *testing.T) {
	for name, up := range map[string]func(context.Context) (bool, error){
		"up to date":    func(context.Context) (bool, error) { return false, nil },
		"update failed": func(context.Context) (bool, error) { return false, errors.New("offline") },
	} {
		h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
		h.host.comments = []trigger.Comment{triggerComment(21, "OvictorVieira")}
		h.app.Upgrade = up
		h.app.Restart = func() error { t.Errorf("%s: must not restart", name); return nil }

		if err := h.app.Watch(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if len(h.host.posted) != 1 {
			t.Errorf("%s: posted %d reviews, want 1", name, len(h.host.posted))
		}
	}
}

// ---- Who posts ------------------------------------------------------------

func (h *harness) withBot(token string, err error) {
	h.app.Bot = func(context.Context, string) (string, error) { return token, err }
}

func identities(h *harness) map[string]string {
	out := map[string]string{}
	for _, w := range h.host.writes {
		out[w.what] = w.as
	}
	return out
}

func TestTriggerFromGitHubIsAnsweredAsTheBot(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2})
	h.withBot("bot-token", nil)
	h.host.comments = []trigger.Comment{triggerComment(30, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if got := identities(h); got["review"] != "bot-token" || got["react"] != "bot-token" {
		t.Errorf("review and reactions must come from the bot, got %v", got)
	}
	if !strings.Contains(h.host.posted[0].Body, "**Requested by:** @OvictorVieira") {
		t.Errorf("a bot review must say who asked for it:\n%s", h.host.posted[0].Body)
	}
}

func TestWatchRefreshesBotTokenBeforePostingAfterLongReview(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2})
	var calls int
	h.app.Bot = func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "token-that-expired-during-review", nil
		}
		return "fresh-token", nil
	}
	h.host.comments = []trigger.Comment{triggerComment(34, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := identities(h); got["review"] != "fresh-token" || got["react"] != "token-that-expired-during-review" {
		t.Errorf("the review must use the refreshed token, got %v", got)
	}
	if calls < 2 {
		t.Fatalf("bot token was only fetched %d time(s)", calls)
	}
}

func TestBotFailureComplaintIsAlsoPostedAsTheBot(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", err: errors.New("usage limit")})
	h.withBot("bot-token", nil)
	h.host.comments = []trigger.Comment{triggerComment(31, "OvictorVieira")}

	_ = h.app.Watch(context.Background(), true)

	if got := identities(h); got["comment"] != "bot-token" {
		t.Errorf("failure comment identity = %q, want the bot", got["comment"])
	}
}

func TestWatchRefreshesBotTokenBeforeFailureComment(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", err: errors.New("usage limit")})
	var calls int
	h.app.Bot = func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "expired-token", nil
		}
		return "fresh-token", nil
	}
	h.host.comments = []trigger.Comment{triggerComment(35, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := identities(h); got["comment"] != "fresh-token" || got["react"] != "fresh-token" {
		t.Errorf("failure response must use the refreshed token, got %v", got)
	}
}

func TestCommandLineReviewIsPostedAsTheUserEvenWhenABotIsConfigured(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2})
	h.withBot("bot-token", nil)

	if _, err := h.app.Review(context.Background(), repo, 1, Options{}); err != nil {
		t.Fatal(err)
	}

	if got := identities(h); got["review"] != "" {
		t.Errorf("a command-line review is posted as the user, got token %q", got["review"])
	}
	if strings.Contains(h.host.posted[0].Body, "Requested by") {
		t.Error("the user's own review needs no 'requested by' line")
	}
}

func TestWithoutABotTheTriggerIsAnsweredAsTheUser(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2}) // no Bot configured
	h.host.comments = []trigger.Comment{triggerComment(32, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if got := identities(h); got["review"] != "" || len(h.host.posted) != 1 {
		t.Errorf("identities = %v, posted %d", got, len(h.host.posted))
	}
}

func TestBotUnavailableFallsBackToTheUserAndStillReviews(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: findingOnLine2})
	h.withBot("", errors.New("the Otto GitHub App is not installed on acme/api"))
	h.host.comments = []trigger.Comment{triggerComment(33, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if len(h.host.posted) != 1 || identities(h)["review"] != "" {
		t.Errorf("review should still be posted as the user: %v", identities(h))
	}
	if !strings.Contains(h.out.String(), "not installed") {
		t.Errorf("the reason must be logged:\n%s", h.out)
	}
}

func TestWatchFailureCommentExplainsTheCauseAndNeverPastesEngineOutput(t *testing.T) {
	raw := "exit status 1: + candidateSignal?: CandidateSignal;\nERROR: You’ve hit your usage limit. Upgrade to Plus, or try again at Nov 18th, 2026 6:28 PM.\n"
	h := newHarness(t, fakeEngine{name: "codex", err: errors.New(raw)})
	h.app.Cfg.Language = "Brazilian Portuguese"
	h.host.comments = []trigger.Comment{triggerComment(41, "OvictorVieira")}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if len(h.host.plain) != 1 {
		t.Fatalf("comments = %v", h.host.plain)
	}
	comment := h.host.plain[0]
	for _, want := range []string{"não conseguiu concluir este review", "**codex**", "limite de uso atingido", "Nov 18th, 2026 6:28 PM", "/otto review"} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment missing %q:\n%s", want, comment)
		}
	}
	for _, leaked := range []string{"candidateSignal", "exit status", "ERROR:", "```"} {
		if strings.Contains(comment, leaked) {
			t.Errorf("comment leaked engine output (%q):\n%s", leaked, comment)
		}
	}
}

func TestWatchRestartsIntoTheNewBinaryOnlyAfterTheReviewInFlightFinishes(t *testing.T) {
	var fingerprint atomic.Value
	fingerprint.Store("v1")
	var order []string
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings, onRun: func() {
		order = append(order, "reviewing")
		fingerprint.Store("v2") // the binary is replaced while the review is running
	}})
	h.app.Cfg.PollInterval = 5 * time.Millisecond
	h.app.Fingerprint = func() string { return fingerprint.Load().(string) }
	h.app.Restart = func() error { order = append(order, "restart"); return nil }
	h.host.comments = []trigger.Comment{triggerComment(50, "OvictorVieira")}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.app.Watch(ctx, false); err != nil {
		t.Fatal(err)
	}

	if strings.Join(order, ",") != "reviewing,restart" {
		t.Errorf("order = %v, want the review to finish before the restart", order)
	}
	if !h.app.State.Done(50) {
		t.Error("the review that was running must be finished and remembered, not cut short")
	}
}

func TestWatchDoesNotRestartWhileTheBinaryIsUnchangedOrUnreadable(t *testing.T) {
	for name, fingerprints := range map[string][]string{
		"unchanged":                 {"v1", "v1", "v1"},
		"unreadable while replaced": {"v1", "", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			restarts := 0
			h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
			h.app.Cfg.PollInterval = 5 * time.Millisecond
			h.app.Fingerprint = func() string {
				i := int(calls.Add(1)) - 1
				if i >= len(fingerprints) {
					i = len(fingerprints) - 1
				}
				return fingerprints[i]
			}
			h.app.Restart = func() error { restarts++; return nil }

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := h.app.Watch(ctx, false); err != nil {
				t.Fatal(err)
			}

			if restarts != 0 {
				t.Errorf("restarts = %d, want none", restarts)
			}
			if calls.Load() < 3 {
				t.Errorf("the binary was checked %d times, want it checked on every cycle", calls.Load())
			}
		})
	}
}

func TestWatchReloadsTheConfigOnlyAfterTheReviewInFlightFinishes(t *testing.T) {
	var stamp atomic.Value
	stamp.Store("config-v1")
	var order []string
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings, onRun: func() {
		order = append(order, "reviewing")
		stamp.Store("config-v2") // `otto setup` saves a new config while the review is running
	}})
	h.app.Cfg.PollInterval = 5 * time.Millisecond
	h.app.ConfigStamp = func() string { return stamp.Load().(string) }
	h.app.Restart = func() error { order = append(order, "restart"); return nil }
	h.host.comments = []trigger.Comment{triggerComment(60, "OvictorVieira")}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.app.Watch(ctx, false); err != nil {
		t.Fatal(err)
	}

	if strings.Join(order, ",") != "reviewing,restart" {
		t.Errorf("order = %v, want the review to finish before the config is reloaded", order)
	}
	if !h.app.State.Done(60) {
		t.Error("the review that was running must be finished and remembered")
	}
}

func TestWatchIgnoresAConfigThatIsMomentarilyUnreadable(t *testing.T) {
	var calls atomic.Int32
	stamps := []string{"config-v1", "", "", ""}
	restarts := 0
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	h.app.Cfg.PollInterval = 5 * time.Millisecond
	h.app.ConfigStamp = func() string {
		i := int(calls.Add(1)) - 1
		if i >= len(stamps) {
			i = len(stamps) - 1
		}
		return stamps[i]
	}
	h.app.Restart = func() error { restarts++; return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.app.Watch(ctx, false); err != nil {
		t.Fatal(err)
	}

	if restarts != 0 {
		t.Errorf("restarts = %d, want none while the config is being rewritten", restarts)
	}
}

func TestWatchSaysWhichEnginesAndModelsItLoaded(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "codex", answer: noFindings}, fakeEngine{name: "claude", answer: noFindings})
	h.app.Cfg.Engines = []engine.Spec{{Name: "codex", Model: "gpt-6-luna", Effort: "medium"}, {Name: "claude"}}

	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if out := h.out.String(); !strings.Contains(out, "engines: codex (gpt-6-luna, medium), claude (default)") {
		t.Errorf("log does not say which engines were loaded:\n%s", out)
	}
}

func TestWatchStartsIndependentReviewsConcurrently(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings, onRun: func() {
		started <- struct{}{}
		<-release
	}})
	h.host.comments = []trigger.Comment{triggerComment(101, "OvictorVieira"), triggerComment(102, "OvictorVieira")}
	done := make(chan struct{})
	go func() { defer close(done); _ = h.app.Watch(context.Background(), true) }()
	defer func() { close(release); <-done }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("only %d of 2 reviews started while another was running", i)
		}
	}
}

func TestWatchLimitsActiveReviewsAndAcknowledgesQueue(t *testing.T) {
	started := make(chan struct{}, 5)
	release := make(chan struct{})
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings, onRun: func() { started <- struct{}{}; <-release }})
	for i := int64(1); i <= 5; i++ {
		c := triggerComment(200+i, "OvictorVieira")
		c.HTMLURL = "https://github.com/" + repo + "/pull/" + fmt.Sprint(i) + "#issuecomment-1"
		h.host.comments = append(h.host.comments, c)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = h.app.Watch(context.Background(), true) }()
	defer func() { close(release); <-done }()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only %d reviews started", i)
		}
	}
	select {
	case <-started:
		t.Fatal("fourth review started before a worker became free")
	case <-time.After(100 * time.Millisecond):
	}
	h.host.mu.Lock()
	notices := append([]string(nil), h.host.plain...)
	h.host.mu.Unlock()
	if len(notices) != 2 || !strings.Contains(notices[0], "posição 4") || !strings.Contains(notices[1], "posição 5") {
		t.Fatalf("queue notices = %v", notices)
	}
}

func TestWatchResumesPersistedQueueWithoutAnotherGitHubComment(t *testing.T) {
	h := newHarness(t, fakeEngine{name: "claude", answer: noFindings})
	req := trigger.Request{Repo: repo, PR: 7, CommentID: 707, Author: "OvictorVieira"}
	h.app.State.Pending[req.CommentID] = req
	if err := h.app.State.Save(); err != nil {
		t.Fatal(err)
	}
	state, err := trigger.LoadState(h.app.State.Path())
	if err != nil {
		t.Fatal(err)
	}
	h.app.State = state
	if err := h.app.Watch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(h.host.posted) != 1 || !h.app.State.Done(707) || len(h.app.State.Pending) != 0 {
		t.Fatalf("review=%d processed=%v pending=%v", len(h.host.posted), h.app.State.Done(707), h.app.State.Pending)
	}
}
