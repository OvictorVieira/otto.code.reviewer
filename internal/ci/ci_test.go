package ci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeReader struct {
	rounds [][]Check // what each call to Checks returns; the last one repeats
	calls  int
	logs   map[int64]string
}

func (f *fakeReader) Checks(context.Context, string, string) ([]Check, error) {
	i := min(f.calls, len(f.rounds)-1)
	f.calls++
	return f.rounds[i], nil
}
func (f *fakeReader) JobLog(_ context.Context, _ string, id int64) (string, error) {
	if l, ok := f.logs[id]; ok {
		return l, nil
	}
	return "", errors.New("no log")
}

// clock is a fake clock that sleeping advances.
type clock struct{ t time.Time }

func (c *clock) opts(wait time.Duration) Options {
	return Options{Wait: wait, Poll: 30 * time.Second, Now: func() time.Time { return c.t },
		Sleep: func(_ context.Context, d time.Duration) bool { c.t = c.t.Add(d); return true }}
}

var running = Check{ID: 1, Name: "test", Status: "in_progress", Actions: true}
var passed = Check{ID: 1, Name: "test", Status: "completed", Conclusion: "success", Actions: true}
var failed = Check{ID: 1, Name: "test", Status: "completed", Conclusion: "failure", Actions: true}

func TestWaitsForRunningChecksAndUsesTheirResult(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	r := &fakeReader{rounds: [][]Check{{running}, {running}, {failed}}, logs: map[int64]string{1: "ok 1\n--- FAIL: TestReserve (0.01s)\n    oversold: got -1"}}

	rep, err := Collect(context.Background(), r, "o/r", "sha", c.opts(10*time.Minute))

	if err != nil || rep.Pending || r.calls != 3 {
		t.Fatalf("rep=%+v calls=%d err=%v", rep, r.calls, err)
	}
	if !strings.Contains(rep.Excerpts["test"], "--- FAIL: TestReserve") {
		t.Fatalf("the failing log must be excerpted: %q", rep.Excerpts["test"])
	}
}

func TestStopsWaitingAfterTheLimitAndSaysCIIsPending(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	r := &fakeReader{rounds: [][]Check{{running}}}

	rep, _ := Collect(context.Background(), r, "o/r", "sha", c.opts(10*time.Minute))

	if !rep.Pending {
		t.Fatal("checks still running after the wait must be reported as pending")
	}
	if waited := c.t.Sub(time.Unix(0, 0)); waited > 10*time.Minute {
		t.Fatalf("waited %s, more than the limit", waited)
	}
	if !strings.Contains(Render(rep), "still running") {
		t.Fatal("the prompt must say CI is still running")
	}
}

func TestDoesNotWaitWhenEverythingFinished(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	r := &fakeReader{rounds: [][]Check{{passed}}}

	rep, _ := Collect(context.Background(), r, "o/r", "sha", c.opts(10*time.Minute))

	if r.calls != 1 || rep.Pending || len(rep.Excerpts) != 0 {
		t.Fatalf("calls=%d rep=%+v", r.calls, rep)
	}
}

func TestNoChecksIsSaidPlainly(t *testing.T) {
	if got := Render(Report{}); got != "No CI checks ran on this commit." {
		t.Fatalf("got %q", got)
	}
}

func TestExcerptKeepsFailureLinesAndRedactsSecrets(t *testing.T) {
	log := strings.Repeat("2026-10-28T10:00:00.0000000Z setup step\n", 200) +
		"2026-10-28T10:00:01.0000000Z export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123\n" +
		"2026-10-28T10:00:02.0000000Z --- FAIL: TestReserve (0.01s)\n" +
		"    Authorization: Bearer abcdefghijklmnopqrstuvwxyz\n"
	ex := Excerpt(log, 60, 3000)
	if !strings.Contains(ex, "--- FAIL: TestReserve") {
		t.Fatalf("failure line missing: %q", ex)
	}
	if strings.Contains(ex, "ghp_abcdef") || strings.Contains(ex, "abcdefghijklmnopqrstuvwxyz\n") || strings.Contains(ex, "Bearer abcdefghij") {
		t.Fatalf("secret leaked: %q", ex)
	}
	if strings.Contains(ex, "2026-10-28T") {
		t.Fatal("timestamps are noise and must be dropped")
	}
	if strings.Count(ex, "setup step") > 3 {
		t.Fatal("unrelated log lines must be left out")
	}
}

func TestExcerptStripsColorCodes(t *testing.T) {
	ex := Excerpt("\x1b[31m--- FAIL: TestX\x1b[0m\n", 10, 1000)
	if strings.Contains(ex, "\x1b") || !strings.Contains(ex, "--- FAIL: TestX") {
		t.Fatalf("got %q", ex)
	}
}

func TestExcerptIgnoresSettingsThatMerelyContainTheWordFail(t *testing.T) {
	log := strings.Repeat("noise\n", 50) + "  fail-on-cache-miss: false\n" + strings.Repeat("noise\n", 50) + "##[error]Process completed with exit code 1.\n"
	ex := Excerpt(log, 60, 3000)
	if strings.Contains(ex, "fail-on-cache-miss") || !strings.Contains(ex, "exit code 1") {
		t.Fatalf("got %q", ex)
	}
}
