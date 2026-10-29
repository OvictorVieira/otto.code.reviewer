package format

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

func TestInlineBodyMatchesCodexLayout(t *testing.T) {
	f := review.Finding{
		Priority: 2,
		Title:    "Derive the default overlap from maxChars",
		Body:     "When a caller sets only `maxChars`, the default overlap fails.",
	}
	want := "**<sub><sub>![P2 Badge](https://img.shields.io/badge/P2-yellow?style=flat)</sub></sub>  Derive the default overlap from maxChars**\n" +
		"\n" +
		"When a caller sets only `maxChars`, the default overlap fails.\n" +
		"\n" +
		"Useful? React with 👍 / 👎.\n" +
		"\n" +
		"<!-- otto-review-finding:v1 -->"
	if got := InlineBody(f); got != want {
		t.Errorf("InlineBody mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestBadgeColorsBySeverity(t *testing.T) {
	cases := map[int]string{
		0: "https://img.shields.io/badge/P0-red?style=flat",
		1: "https://img.shields.io/badge/P1-orange?style=flat",
		2: "https://img.shields.io/badge/P2-yellow?style=flat",
		3: "https://img.shields.io/badge/P3-lightgrey?style=flat",
	}
	for p, url := range cases {
		if got := Badge(p); !strings.Contains(got, url) {
			t.Errorf("Badge(%d) = %q, want url %q", p, got, url)
		}
	}
}

func TestPayloadAttachesInlineCommentsToRightSide(t *testing.T) {
	placement := review.Placement{Inline: []review.Finding{
		{Priority: 1, Title: "single", Body: "b", Path: "a.go", Start: 7, End: 7},
		{Priority: 2, Title: "range", Body: "b", Path: "a.go", Start: 3, End: 5},
	}}
	p := Payload("abc1234567890", placement, review.Result{}, Meta{Engine: "claude"})

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Comments []map[string]any
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.CommitID != "abc1234567890" || got.Event != "COMMENT" {
		t.Errorf("payload header = %+v", got)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(got.Comments))
	}
	single := got.Comments[0]
	if single["line"] != float64(7) || single["side"] != "RIGHT" {
		t.Errorf("single-line comment = %v", single)
	}
	if _, has := single["start_line"]; has {
		t.Errorf("single-line comment must not send start_line: %v", single)
	}
	rng := got.Comments[1]
	if rng["start_line"] != float64(3) || rng["line"] != float64(5) || rng["start_side"] != "RIGHT" {
		t.Errorf("range comment = %v", rng)
	}
}

func TestSummaryListsVerdictCommitAndFindingsOutsideDiff(t *testing.T) {
	placement := review.Placement{Outside: []review.Finding{
		{Priority: 0, Title: "Token logged", Body: "The token is printed.", Path: "auth.go", Start: 40, End: 40},
	}}
	result := review.Result{Correct: false, Explanation: "A secret leaks to logs."}

	s := Summary("abc1234567890", placement, result, Meta{Engine: "codex", Model: "gpt-5.5", Effort: "high"})

	for _, want := range []string{
		"### 💡 Otto Review",
		"**Reviewed commit:** `abc1234567`",
		"`codex` · model `gpt-5.5` · effort `high`",
		"patch is incorrect",
		"A secret leaks to logs.",
		"Token logged",
		"`auth.go:40`",
		"/otto review",
		"<!-- otto-review-receipt:v1 -->",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q\n%s", want, s)
		}
	}
}

func TestSummaryWithoutFindings(t *testing.T) {
	s := Summary("abc1234567890", review.Placement{}, review.Result{Correct: true, Explanation: "ok"}, Meta{Engine: "claude"})
	if !strings.Contains(s, "Otto Review: Didn't find any major issues. Bravo.") ||
		!strings.Contains(s, "**Reviewed commit:** `abc1234567`") ||
		!strings.Contains(s, "<summary>ℹ️ About Otto in GitHub</summary>") {
		t.Errorf("summary = %s", s)
	}
}

func TestSummaryWithFindingsUsesCodexReviewLayout(t *testing.T) {
	s := Summary("abc1234567890", review.Placement{Inline: []review.Finding{{Title: "Finding"}}},
		review.Result{}, Meta{Engine: "copilot"})
	for _, want := range []string{
		"### 💡 Otto Review",
		"Here are some automated review suggestions for this pull request.",
		"**Reviewed commit:** `abc1234567`",
		"<summary>ℹ️ About Otto in GitHub</summary>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
}

func TestQuotaFailureUsesOnlyProviderReportedResetDetails(t *testing.T) {
	s := QuotaFailure(&engine.QuotaError{Details: "retry after 30 minutes"})
	if !strings.Contains(s, "retry after 30 minutes") || strings.Contains(s, "check your Copilot usage page") {
		t.Errorf("quota comment = %s", s)
	}
	s = QuotaFailure(engine.ErrQuotaExceeded)
	if !strings.Contains(s, "does not guess a reset time") || !strings.Contains(s, "Check your account's usage page") {
		t.Errorf("quota comment without provider details = %s", s)
	}
}

func TestSummaryShowsVerificationAndSpec(t *testing.T) {
	r := review.Result{
		Correct: true,
		Verification: []review.Check{
			{Command: "go test ./...", Result: "passed", Note: "42 tests"},
			{Command: "npm run e2e", Result: "failed", Note: "checkout spec fails"},
			{Command: "docker compose up", Result: "skipped", Note: "needs secrets"},
		},
		Spec: []review.SpecCheck{{Requirement: "Reject duplicated cards", Status: "partial", Note: "only by number"}},
	}
	s := Summary("abc1234567890", review.Placement{}, r, Meta{Engine: "claude"})
	for _, want := range []string{
		"#### Verification", "✅ `go test ./...`", "❌ `npm run e2e`", "⏭️ `docker compose up`",
		"#### Spec", "🟡 Reject duplicated cards",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q\n%s", want, s)
		}
	}
}

func TestFailureExplainsWhyInPortugueseWithoutRawOutput(t *testing.T) {
	s := Failure([]engine.EngineFailure{
		{Engine: "codex", Kind: engine.FailQuota, ResetAt: "Nov 18th, 2026 6:28 PM"},
		{Engine: "claude", Kind: engine.FailAuth},
	}, "@otto-assist review", "Brazilian Portuguese")

	for _, want := range []string{
		"Otto não conseguiu concluir este review",
		"**codex**", "limite de uso atingido", "Nov 18th, 2026 6:28 PM",
		"**claude**", "não está autenticado",
		"`@otto-assist review`", "otto setup",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("failure comment missing %q:\n%s", want, s)
		}
	}
	for _, raw := range []string{"exit status", "ERROR:", "```"} {
		if strings.Contains(s, raw) {
			t.Errorf("failure comment must not carry raw output (%q):\n%s", raw, s)
		}
	}
}

func TestFailureIsInEnglishByDefault(t *testing.T) {
	s := Failure([]engine.EngineFailure{{Engine: "codex", Kind: engine.FailQuota}}, "/otto review", "English")
	for _, want := range []string{"Otto could not finish this review", "**codex**", "usage limit reached", "`/otto review`"} {
		if !strings.Contains(s, want) {
			t.Errorf("failure comment missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "não") {
		t.Errorf("English comment contains Portuguese:\n%s", s)
	}
}

func TestFailureWithoutEngineDetailsStaysGeneric(t *testing.T) {
	s := Failure(nil, "/otto review", "English")
	if !strings.Contains(s, "Otto could not finish this review") || !strings.Contains(s, "`/otto review`") {
		t.Errorf("generic failure comment = %s", s)
	}
	if strings.Contains(s, "```") {
		t.Errorf("generic failure comment must not carry raw output:\n%s", s)
	}
}

func TestFailureNamesEveryKindOfProblem(t *testing.T) {
	kinds := map[engine.FailureKind]string{
		engine.FailQuota: "usage limit reached", engine.FailAuth: "not signed in", engine.FailMissing: "not installed", engine.FailBusy: "overloaded or the engine kept retrying", engine.FailPermission: "was not allowed to read a file it needed",
		engine.FailTimeout: "took too long", engine.FailNetwork: "connection problem",
		engine.FailInvalid: "answered in a format Otto could not use", engine.FailOther: "failed for an unexpected reason",
	}
	for kind, want := range kinds {
		s := Failure([]engine.EngineFailure{{Engine: "e", Kind: kind}}, "/otto review", "English")
		if !strings.Contains(s, want) {
			t.Errorf("%s: comment missing %q:\n%s", kind, want, s)
		}
	}
}

func TestFailureAdviceMatchesWhatWentWrong(t *testing.T) {
	cases := []struct {
		name     string
		failures []engine.EngineFailure
		language string
		want     []string
		wantNo   []string
	}{
		{"unusable format is not told to wait for a limit", []engine.EngineFailure{{Engine: "opencode", Kind: engine.FailInvalid}}, "Brazilian Portuguese",
			[]string{"formato pedido", "modelo mais forte", "`otto setup`"}, []string{"limite renovar", "faça login"}},
		{"usage limit says to wait or add an engine", []engine.EngineFailure{{Engine: "codex", Kind: engine.FailQuota}}, "Brazilian Portuguese",
			[]string{"limite renovar", "outro motor"}, []string{"formato pedido", "faça login"}},
		{"not signed in says to sign in", []engine.EngineFailure{{Engine: "claude", Kind: engine.FailAuth}}, "Brazilian Portuguese",
			[]string{"Entre na conta"}, []string{"limite renovar"}},
		{"temporary problems say to try again soon", []engine.EngineFailure{{Engine: "cursor", Kind: engine.FailBusy}, {Engine: "copilot", Kind: engine.FailTimeout}}, "Brazilian Portuguese",
			[]string{"tente de novo em alguns minutos"}, []string{"limite renovar", "formato pedido"}},
		{"several different problems get each hint once", []engine.EngineFailure{{Engine: "codex", Kind: engine.FailQuota}, {Engine: "claude", Kind: engine.FailAuth}, {Engine: "x", Kind: engine.FailQuota}}, "Brazilian Portuguese",
			[]string{"limite renovar", "Entre na conta"}, nil},
		{"english unusable format", []engine.EngineFailure{{Engine: "opencode", Kind: engine.FailInvalid}}, "English",
			[]string{"did not follow the requested format", "stronger model"}, []string{"limit to reset", "sign in"}},
		{"unknown problem points at the log", []engine.EngineFailure{{Engine: "e", Kind: engine.FailOther}}, "English",
			[]string{"Otto's log"}, []string{"limit to reset"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Failure(c.failures, "@otto-assist review", c.language)
			for _, want := range c.want {
				if !strings.Contains(s, want) {
					t.Errorf("advice missing %q:\n%s", want, s)
				}
			}
			for _, no := range c.wantNo {
				if strings.Contains(s, no) {
					t.Errorf("advice must not say %q for this problem:\n%s", no, s)
				}
			}
			if strings.Count(s, "limite renovar")+strings.Count(s, "limit to reset") > 1 {
				t.Errorf("the same hint must appear once:\n%s", s)
			}
		})
	}
}

func TestASecretAModelWasTrickedIntoQuotingIsNeverPosted(t *testing.T) {
	leak := "Key: -----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"
	r := review.Result{Explanation: leak}
	p := review.Placement{Inline: []review.Finding{{Path: "a.go", Start: 1, End: 1, Priority: 1, Title: "t", Body: "token ghp_abcdefghijklmnopqrstuvwxyz0123"}}}
	out := Payload("abc1234", p, r, Meta{Engine: "claude"})
	if strings.Contains(out.Body, "b3BlbnNzaC1rZXktdjEAAAAA") || strings.Contains(out.Comments[0].Body, "ghp_abcdef") {
		t.Fatalf("secret posted:\n%s\n%s", out.Body, out.Comments[0].Body)
	}
	if out.Event != "COMMENT" {
		t.Fatalf("event = %s, Otto never approves or blocks", out.Event)
	}
}
