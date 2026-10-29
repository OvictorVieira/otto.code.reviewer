// Package learn turns how maintainers react to Otto's findings (reactions,
// replies, resolved threads) into memory: per-pull-request context, and a
// small set of lessons per repository that Otto reads before every review.
package learn

import (
	"regexp"
	"strings"
	"time"
)

// Outcome is what happened to one finding.
type Outcome string

const (
	Accepted Outcome = "accepted" // thumbs up, or a reply saying it was fixed
	Rejected Outcome = "rejected" // thumbs down, or a reply saying it will not be fixed
	Resolved Outcome = "resolved" // thread closed with no explicit signal: weakly accepted
	Open     Outcome = "open"     // no reaction yet
)

// Settled reports whether the maintainers gave a signal Otto can learn from.
func (o Outcome) Settled() bool { return o == Accepted || o == Rejected || o == Resolved }

// ThreadComment is one comment of a review thread.
type ThreadComment struct {
	ID     int64
	Author string
	Body   string
	Up     int // thumbs up reactions
	Down   int // thumbs down reactions
}

// Thread is a pull request review thread as returned by GitHub.
type Thread struct {
	Path     string
	Line     int
	Resolved bool
	Comments []ThreadComment
}

// Record is Otto's finding together with how the maintainers answered it.
type Record struct {
	Repo     string    `json:"repo"`
	PR       int       `json:"pr"`
	ID       int64     `json:"id"` // GitHub id of Otto's comment
	Path     string    `json:"path"`
	Line     int       `json:"line,omitempty"`
	Priority int       `json:"priority"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Outcome  Outcome   `json:"outcome"`
	Notes    []string  `json:"notes,omitempty"` // human replies, the reasoning Otto learns from
	Seen     time.Time `json:"seen"`
	Learned  bool      `json:"learned,omitempty"` // already folded into the lessons
}

var (
	ottoComment = regexp.MustCompile(`img\.shields\.io/badge/P([0-3])-`)
	badgeHTML   = regexp.MustCompile(`<sub><sub>.*?</sub></sub>`)
	fixedReply  = regexp.MustCompile(`(?i)^\s*(fixed|done|addressed|corrigid|resolvid)`)
	rejectReply = regexp.MustCompile(`(?i)(won'?t fix|wontfix|will not fix|already rejected|not applicable|n[aã]o vou corrigir|rejeitad)`)
)

// Extract keeps the threads Otto started and infers the outcome of each.
func Extract(repo string, pr int, threads []Thread, now time.Time) []Record {
	var out []Record
	for _, t := range threads {
		if len(t.Comments) == 0 {
			continue
		}
		first := t.Comments[0]
		m := ottoComment.FindStringSubmatch(first.Body)
		if m == nil || !strings.Contains(first.Body, "otto-review-finding:") {
			continue
		}
		title, body := splitFinding(first.Body)
		rec := Record{
			Repo: repo, PR: pr, ID: first.ID, Path: t.Path, Line: t.Line,
			Priority: int(m[1][0] - '0'), Title: title, Body: body, Seen: now,
		}
		var replies []string
		for _, c := range t.Comments[1:] {
			if txt := strings.TrimSpace(c.Body); txt != "" {
				replies = append(replies, txt)
			}
		}
		rec.Outcome = infer(first, replies, t.Resolved)
		for _, r := range replies {
			if len(rec.Notes) == 3 {
				break
			}
			rec.Notes = append(rec.Notes, clip(r, 400))
		}
		out = append(out, rec)
	}
	return out
}

// infer decides the outcome. A written reply is stronger than a reaction, and a
// reaction is stronger than merely resolving the thread.
func infer(first ThreadComment, replies []string, resolved bool) Outcome {
	for _, r := range replies {
		switch {
		case rejectReply.MatchString(r):
			return Rejected
		case fixedReply.MatchString(r):
			return Accepted
		}
	}
	switch {
	case first.Down > 0 && first.Up == 0:
		return Rejected
	case first.Up > 0 && first.Down == 0:
		return Accepted
	}
	if resolved {
		return Resolved
	}
	return Open
}

// splitFinding returns the title (first line, without badge or bold markers)
// and the paragraph under it, without the reaction prompt and hidden marker.
func splitFinding(body string) (title, text string) {
	first, rest, _ := strings.Cut(strings.TrimSpace(body), "\n")
	first = badgeHTML.ReplaceAllString(first, "")
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(first), "*"))
	for _, cut := range []string{"Useful? React", "<details>", "<!-- otto-review-finding"} {
		if i := strings.Index(rest, cut); i >= 0 {
			rest = rest[:i]
		}
	}
	return title, strings.TrimSpace(rest)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
