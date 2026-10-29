//go:build !windows

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCLI installs an executable named bin on PATH that runs the given shell script.
func fakeCLI(t *testing.T, bin, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, bin), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runStuck runs the engine and fails the test if it is still going after a few seconds,
// which is how a CLI that reports an error but keeps retrying would hang a review.
func runStuck(t *testing.T, name string) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now()
	_, err := CLI{Spec: Spec{Name: name}}.Run(ctx, Request{Prompt: "review", Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")})
	elapsed := time.Since(started)
	if elapsed > 6*time.Second {
		t.Fatalf("%s kept running for %s after reporting an error", name, elapsed)
	}
	return elapsed, err
}

func TestOpencodeStopsAtTheFirstErrorInsteadOfWaitingForItsRetries(t *testing.T) {
	fakeCLI(t, "opencode", `printf '\033[91m\033[1mError: \033[0m{\n  "name": "UnknownError",\n  "data": { "message": "Unexpected server error." }\n}\n' >&2; sleep 30`)

	_, err := runStuck(t, "opencode")

	if err == nil || !strings.Contains(err.Error(), "Unexpected server error") {
		t.Fatalf("err = %v, want the opencode error", err)
	}
	if errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("a generic opencode error must not be reported as a quota failure: %v", err)
	}
}

func TestOpencodeUsageLimitIsReportedAsQuotaAndStopsImmediately(t *testing.T) {
	fakeCLI(t, "opencode", `printf 'Error: {"name":"FreeUsageLimitError","data":{"message":"Rate limit exceeded. Please try again later."}}\n' >&2; sleep 30`)

	_, err := runStuck(t, "opencode")

	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want a quota failure", err)
	}
}

func TestAnEngineThatKeepsRetryingIsStoppedAndReportedAsUnstable(t *testing.T) {
	fakeCLI(t, "cursor-agent", `i=1; while [ $i -le 4 ]; do echo "Connection lost, reconnecting (attempt $i/10)" >&2; i=$((i+1)); done; sleep 30`)

	_, err := runStuck(t, "cursor")

	if err == nil || !strings.Contains(err.Error(), "repeated retries") {
		t.Fatalf("err = %v, want a repeated retries failure", err)
	}
}

func TestOneRetryNoticeDoesNotStopAnEngineThatThenSucceeds(t *testing.T) {
	fakeCLI(t, "opencode", `echo "Retrying in 2s (attempt 1/10)" >&2; printf 'review done'`)

	out, err := CLI{Spec: Spec{Name: "opencode"}}.Run(context.Background(), Request{Prompt: "review", Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")})

	if err != nil || out != "review done" {
		t.Fatalf("out = %q, err = %v, want the answer", out, err)
	}
}

func TestOpencodeAnswerThatMentionsAnErrorIsNotTreatedAsAFailure(t *testing.T) {
	fakeCLI(t, "opencode", `printf 'Error: handling is missing in the retry path\n'; printf 'also fine\n' >&2`)

	out, err := CLI{Spec: Spec{Name: "opencode"}}.Run(context.Background(), Request{Prompt: "review", Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")})

	if err != nil || !strings.Contains(out, "handling is missing") {
		t.Fatalf("out = %q, err = %v, want the review text from stdout", out, err)
	}
}

func TestOpencodeGetsALargePromptOnStdinNotAsAnAttachedFile(t *testing.T) {
	// Regression: a large review prompt attached with -f made opencode stall before it ever
	// reached the model (and, outside the project, trigger a rejected external_directory read).
	// The same prompt on stdin is answered in seconds.
	fakeCLI(t, "opencode", `for arg in "$@"; do
  if [ "$arg" = "-f" ]; then echo "Error: a large attached file hangs opencode" >&2; sleep 30; fi
done
input=$(cat)
case "$input" in *"FINAL INSTRUCTION"*) printf 'review done';; *) echo "Error: the prompt did not arrive on stdin" >&2;; esac`)
	prompt := strings.Repeat("filler line of a long review prompt\n", 3000) + "FINAL INSTRUCTION: answer with JSON\n"
	checkout := t.TempDir()

	out, err := CLI{Spec: Spec{Name: "opencode"}}.Run(context.Background(), Request{Prompt: prompt, Dir: checkout, OutFile: filepath.Join(t.TempDir(), "out")})

	if err != nil || out != "review done" {
		t.Fatalf("out = %q, err = %v, want the prompt delivered on stdin", out, err)
	}
	if entries, _ := os.ReadDir(checkout); len(entries) != 0 {
		t.Errorf("the checkout must be left untouched, found %d entries", len(entries))
	}
}

func TestCodeFromTheReviewedPullRequestIsNeverMistakenForAUsageLimit(t *testing.T) {
	// Regression: opencode echoes what it reads to stderr. A pull request that itself contains
	// rate limiting code ("Too many requests", 429) made Otto stop the review as "quota exceeded".
	fakeCLI(t, "opencode", `printf '→ Read apps/api/src/http/rateLimit.ts\n' >&2
printf '    return c.json({ error: "Too many requests", code: "rate_limited" }, 429);\n' >&2
printf '    // retry after the rate limit exceeded window, see status code 429 handling\n' >&2
printf '  if (res.status === 429) throw new Error("rate limit exceeded");\n' >&2
printf 'review done'`)

	out, err := CLI{Spec: Spec{Name: "opencode"}}.Run(context.Background(), Request{Prompt: "review", Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")})

	if err != nil || out != "review done" {
		t.Fatalf("out = %q, err = %v: code that merely mentions rate limits must not stop the review", out, err)
	}
}

func TestAnEngineErrorThatSpansSeveralLinesIsStillRecognizedAsAUsageLimit(t *testing.T) {
	fakeCLI(t, "opencode", `printf '→ Read a.go\n' >&2
printf 'Error: {\n  "name": "APIError",\n  "data": {\n    "message": "Rate limit exceeded. Please try again later."\n  }\n}\n' >&2
sleep 30`)

	_, err := runStuck(t, "opencode")

	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want a quota failure from the error block", err)
	}
}

func TestCodexStyleUsageLimitLineStopsTheEngine(t *testing.T) {
	fakeCLI(t, "codex", `printf 'ERROR: You’ve hit your usage limit. Upgrade to Plus, or try again at Nov 18th, 2026 6:28 PM.\n' >&2; sleep 30`)

	_, err := runStuck(t, "codex")

	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want a quota failure", err)
	}
}

func TestErrorBlocksKeepOnlyErrorLinesAndWhatFollowsThem(t *testing.T) {
	text := "→ Read a.ts\n" +
		"    return c.json({ error: \"Too many requests\" }, 429);\n" +
		"\x1b[91mError: \x1b[0m{\n  \"message\": \"Rate limit exceeded\"\n}\n" +
		"ActionRequiredError: You've hit your usage limit\n"
	got := errorBlocks(text)
	for _, want := range []string{"Error: {", "Rate limit exceeded", "ActionRequiredError: You've hit your usage limit"} {
		if !strings.Contains(got, want) {
			t.Errorf("error blocks missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Too many requests", "Read a.ts"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("error blocks must not include %q:\n%s", unwanted, got)
		}
	}
}

func TestSilentOpencodeProcessIsKilledAtReviewDeadline(t *testing.T) {
	fakeCLI(t, "opencode", `sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := CLI{Spec: Spec{Name: "opencode"}}.Run(ctx, Request{Prompt: "review", Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")})
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("silent process was not stopped by deadline: %v", err)
	}
}

const ocQuota = `timestamp=2026-10-29T13:12:44.649Z level=ERROR run=54b580aa message="stream error" providerID=opencode modelID=big-pickle session.id=ses_x small=false agent=plan mode=primary error.error="AI_APICallError: Free usage limit reached. Free models are limited."`
const ocTitleError = `timestamp=2026-10-29T13:15:59.152Z level=ERROR run=c131211d message="stream error" providerID=openrouter modelID=google/gemini-nano-banana-2.1 session.id=ses_x small=true agent=title mode=primary error.error="AI_APICallError: Rate limit exceeded: free-models-per-day"`
const ocStep = `timestamp=2026-10-29T13:22:13.812Z level=INFO run=b90c02a0 message="touching file" file=/tmp/a.go`

func TestOpencodeQuotaInItsLogStopsAtOnceButTheTitleHelperIsIgnored(t *testing.T) {
	fakeCLI(t, "opencode", `echo '`+ocQuota+`' >&2; sleep 30`)
	if _, err := runStuck(t, "opencode"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want a quota failure from the primary agent's log", err)
	}

	fakeCLI(t, "opencode", `echo '`+ocTitleError+`' >&2; echo '`+ocStep+`' >&2; printf 'review done'`)
	out, err := CLI{Spec: Spec{Name: "opencode"}}.Run(context.Background(), Request{Prompt: "review", Dir: t.TempDir(), OutFile: filepath.Join(t.TempDir(), "out")})
	if err != nil || out != "review done" {
		t.Fatalf("a failed session title must not stop the review: out=%q err=%v", out, err)
	}
}

func TestOpencodeLogLinesReportActivity(t *testing.T) {
	fakeCLI(t, "opencode", `echo '`+ocStep+`' >&2; echo 'timestamp=2026-10-29T13:22:14Z level=INFO run=b message=loop session.id=s step=2' >&2; echo 'timestamp=2026-10-29T13:22:14Z level=INFO run=b message="event connected"' >&2; printf 'ok'`)
	beats := 0
	_, err := CLI{Spec: Spec{Name: "opencode"}}.Run(context.Background(), Request{Prompt: "review", Dir: t.TempDir(),
		OutFile: filepath.Join(t.TempDir(), "out"), Alive: func() { beats++ }})
	if err != nil || beats != 2 {
		t.Fatalf("beats = %d err = %v, want one per model step or file read", beats, err)
	}
}
