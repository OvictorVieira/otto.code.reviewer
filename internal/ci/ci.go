// Package ci gathers the evidence Otto uses instead of running the project: the results of the
// pull request's CI checks on the reviewed commit, with an excerpt of each failing log.
package ci

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Check is one CI check run on a commit.
type Check struct {
	ID         int64
	Name       string
	Status     string // queued | in_progress | completed
	Conclusion string // success | failure | cancelled | timed_out | skipped | neutral | action_required
	URL        string
	Actions    bool // run by GitHub Actions, so its log can be read
}

func (c Check) pending() bool { return c.Status != "completed" }
func (c Check) failed() bool {
	switch c.Conclusion {
	case "failure", "timed_out", "cancelled", "action_required":
		return true
	}
	return false
}

// Reader reads checks and job logs from GitHub.
type Reader interface {
	Checks(ctx context.Context, repo, sha string) ([]Check, error)
	JobLog(ctx context.Context, repo string, jobID int64) (string, error)
}

// Report is what the reviewer is told about CI.
type Report struct {
	Checks   []Check
	Pending  bool              // some checks had not finished when Otto stopped waiting
	Excerpts map[string]string // failing check name -> log excerpt
}

// Options tune how long Otto waits for running checks.
type Options struct {
	Wait  time.Duration // how long to wait for checks still running
	Poll  time.Duration // how often to look again while waiting
	Sleep func(context.Context, time.Duration) bool
	Now   func() time.Time
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Collect reads the checks of sha, waiting up to opt.Wait for running ones to finish, then reads
// an excerpt of each failing GitHub Actions log.
func Collect(ctx context.Context, r Reader, repo, sha string, opt Options) (Report, error) {
	if opt.Poll <= 0 {
		opt.Poll = 30 * time.Second
	}
	if opt.Sleep == nil {
		opt.Sleep = sleep
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	deadline := opt.Now().Add(opt.Wait)
	var checks []Check
	for {
		var err error
		if checks, err = r.Checks(ctx, repo, sha); err != nil {
			return Report{}, err
		}
		left := deadline.Sub(opt.Now())
		if !anyPending(checks) || left <= 0 {
			break
		}
		if !opt.Sleep(ctx, min(opt.Poll, left)) {
			break
		}
	}
	rep := Report{Checks: checks, Pending: anyPending(checks), Excerpts: map[string]string{}}
	for _, c := range checks {
		if !c.failed() || !c.Actions {
			continue
		}
		if log, err := r.JobLog(ctx, repo, c.ID); err == nil {
			rep.Excerpts[c.Name] = Excerpt(log, 60, 3000)
		}
	}
	return rep, nil
}

func anyPending(cs []Check) bool {
	for _, c := range cs {
		if c.pending() {
			return true
		}
	}
	return false
}

var (
	failLine  = regexp.MustCompile(`--- FAIL|\bFAIL\b|(?i:\bfailed\b|\berror\b|panic:|exception|assertion|\bexpected\b|✗|✕|##\[error\])`)
	timestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[\d:.]+Z\s?`)
	ansi      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	secrets   = []*regexp.Regexp{
		regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`(?i)(bearer|token|password|secret|api[_-]?key)(["'\s:=]+)[^\s"']{8,}`),
		regexp.MustCompile(`\bnapi_[A-Za-z0-9]{20,}`),
	}
)

// Excerpt keeps the lines of a CI log that explain a failure (with a little context), at most
// maxLines and maxBytes, and redacts anything that looks like a secret.
func Excerpt(log string, maxLines, maxBytes int) string {
	lines := strings.Split(ansi.ReplaceAllString(strings.ReplaceAll(log, "\r", ""), ""), "\n")
	keep := map[int]bool{}
	for i, l := range lines {
		if failLine.MatchString(l) {
			for j := max(0, i-2); j <= min(len(lines)-1, i+2); j++ {
				keep[j] = true
			}
		}
	}
	var out []string
	if len(keep) == 0 { // nothing recognizable: the end of the log is where it stopped
		for i := max(0, len(lines)-maxLines); i < len(lines); i++ {
			keep[i] = true
		}
	}
	for i, l := range lines {
		if keep[i] {
			out = append(out, strings.TrimRight(timestamp.ReplaceAllString(l, ""), " "))
		}
	}
	if len(out) > maxLines {
		out = out[len(out)-maxLines:]
	}
	text := Redact(strings.Join(out, "\n"))
	if len(text) > maxBytes {
		text = "…" + text[len(text)-maxBytes:]
	}
	return text
}

// Redact hides tokens and credentials before a log reaches a prompt or a pull request comment.
func Redact(s string) string {
	for _, re := range secrets {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if sub := re.FindStringSubmatch(m); len(sub) == 3 {
				return sub[1] + sub[2] + "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return s
}

// Render describes the report for the review prompt.
func Render(r Report) string {
	if len(r.Checks) == 0 {
		return "No CI checks ran on this commit."
	}
	var b strings.Builder
	for _, c := range r.Checks {
		state := c.Conclusion
		if c.pending() {
			state = "still running"
		}
		fmt.Fprintf(&b, "- %s: %s\n", c.Name, state)
	}
	if r.Pending {
		b.WriteString("\nSome checks had not finished when the review started.\n")
	}
	for name, ex := range r.Excerpts {
		fmt.Fprintf(&b, "\nLog excerpt of the failing check %q:\n```\n%s\n```\n", name, ex)
	}
	return b.String()
}
