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
