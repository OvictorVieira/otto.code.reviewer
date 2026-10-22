package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const codexLimit = "exit status 1: … number;\n+  candidateSignal?: CandidateSignal;\n }\n\nERROR: You’ve hit your usage limit. Upgrade to Plus to continue using Codex, or try again at Nov 18th, 2026 6:28 PM.\n"

func TestCodexUsageLimitMessageIsRecognizedAsAQuotaFailure(t *testing.T) {
	if !isQuotaMessage(codexLimit) {
		t.Fatal("the Codex usage limit message was not recognized as an exhausted quota")
	}
}

func TestFailuresExplainEachEngineWithoutRawOutput(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		fake{"codex", "", errors.New("codex: " + codexLimit), &runs},
		fake{"claude", "", errors.New("Not logged in · Please run /login"), &runs},
		fake{"cursor", "", errors.New(`exec: "cursor-agent": executable file not found in $PATH`), &runs},
		fake{"opencode", "", errors.New("engine output has no review JSON"), &runs},
		fake{"copilot", "", context.DeadlineExceeded, &runs},
		fake{"gemini", "", errors.New("dial tcp: lookup api.example.com: no such host"), &runs},
		fake{"other", "", errors.New("something nobody planned for"), &runs},
	}}
	_, err := p.Run(context.Background(), Request{}, 0, accept)

	got := Failures(err)
	want := []EngineFailure{
		{Engine: "codex", Kind: FailQuota, ResetAt: "Nov 18th, 2026 6:28 PM"},
		{Engine: "claude", Kind: FailAuth},
		{Engine: "cursor", Kind: FailMissing},
		{Engine: "opencode", Kind: FailInvalid},
		{Engine: "copilot", Kind: FailTimeout},
		{Engine: "gemini", Kind: FailNetwork},
		{Engine: "other", Kind: FailOther},
	}
	if len(got) != len(want) {
		t.Fatalf("failures = %+v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("failure %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFailuresKeepsAShortResetNoteFromAQuotaError(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		fake{"copilot", "", &QuotaError{Details: "quota resets in 2 hours"}, &runs},
	}}
	_, err := p.Run(context.Background(), Request{}, 0, accept)

	got := Failures(err)
	if len(got) != 1 || got[0].Kind != FailQuota || got[0].ResetAt != "quota resets in 2 hours" {
		t.Errorf("failures = %+v", got)
	}
}

func TestFailuresDropsALongQuotaDumpInsteadOfCopyingIt(t *testing.T) {
	var runs []string
	long := &QuotaError{Details: "+  candidateSignal?: CandidateSignal;\n }\n" + string(make([]byte, 400))}
	p := Pool{Strategy: Priority, Engines: []Engine{fake{"copilot", "", long, &runs}}}
	_, err := p.Run(context.Background(), Request{}, 0, accept)

	got := Failures(err)
	if len(got) != 1 || got[0].Kind != FailQuota || got[0].ResetAt != "" {
		t.Errorf("failures = %+v, want a quota failure with no copied details", got)
	}
}

func TestFailuresIsEmptyForAnErrorThatIsNotEngineFailures(t *testing.T) {
	if got := Failures(errors.New("fetching the diff failed")); len(got) != 0 {
		t.Errorf("failures = %+v, want none", got)
	}
	if got := Failures(nil); len(got) != 0 {
		t.Errorf("failures = %+v, want none", got)
	}
}

func TestFailuresRecognizeWhatEachCLIReportsWhenItStops(t *testing.T) {
	cases := []struct {
		name string
		text string
		kind FailureKind
	}{
		{"claude subscription limit", "claude: Claude AI usage limit reached|1792000000", FailQuota},
		{"claude credits", "claude: Credit balance is too low", FailQuota},
		{"claude login", "claude: Invalid API key · Please run /login", FailAuth},
		{"claude expired token", "claude: OAuth token has expired", FailAuth},
		{"claude overloaded", `claude: API Error: 529 {"type":"error","error":{"type":"overloaded_error"}}`, FailBusy},
		{"opencode free tier", `opencode: Error: {"name":"FreeUsageLimitError","data":{"message":"Rate limit exceeded."}}`, FailQuota},
		{"openai style quota", "codex: You exceeded your current quota, please check your plan", FailQuota},
		{"google style quota", "gemini: RESOURCE_EXHAUSTED", FailQuota},
		{"cursor limit", "cursor-agent: ActionRequiredError: You've hit your usage limit", FailQuota},
		{"repeated retries", "cursor-agent: stopped after repeated retries: Connection lost, reconnecting (attempt 3/10)", FailBusy},
		{"service unavailable", "copilot: 503 Service Unavailable", FailBusy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var runs []string
			p := Pool{Strategy: Priority, Engines: []Engine{fake{"e", "", errors.New(c.text), &runs}}}
			_, err := p.Run(context.Background(), Request{}, 0, accept)
			got := Failures(err)
			if len(got) != 1 || got[0].Kind != c.kind {
				t.Errorf("failures = %+v, want kind %s", got, c.kind)
			}
		})
	}
}

func TestClaudeSubscriptionLimitReportsTheResetTheProviderGave(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{fake{"claude", "", errors.New("Claude AI usage limit reached|1792000000"), &runs}}}
	_, err := p.Run(context.Background(), Request{}, 0, accept)

	got := Failures(err)
	if len(got) != 1 || got[0].Kind != FailQuota || !strings.Contains(got[0].ResetAt, "2026") {
		t.Errorf("failures = %+v, want a quota failure with the reset date", got)
	}
}
