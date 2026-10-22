// Package trigger finds "/otto review" requests in pull request comments and
// remembers which ones were already handled, across restarts.
package trigger

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Comment is a GitHub issue comment (pull request conversations are issues).
type Comment struct {
	ID        int64
	Repo      string // owner/name
	Author    string
	Body      string
	HTMLURL   string
	CreatedAt time.Time
}

// Request is one review asked for in a comment.
type Request struct {
	Repo         string
	PR           int
	CommentID    int64
	Author       string
	Instructions string // text after the trigger on the same line
}

var pullURL = regexp.MustCompile(`/pull/(\d+)`)

// Pending returns requests from allowed authors, on pull requests, whose
// have a line starting with the trigger phrase and that were not handled yet.
func Pending(comments []Comment, phrase string, allowed []string, done func(int64) bool) []Request {
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	allow := map[string]bool{}
	for _, a := range allowed {
		allow[strings.ToLower(a)] = true
	}

	var out []Request
	for _, c := range comments {
		if !allow[strings.ToLower(c.Author)] || done(c.ID) {
			continue
		}
		m := pullURL.FindStringSubmatch(c.HTMLURL)
		if m == nil {
			continue
		}
		instructions, ok := matchTrigger(c.Body, phrase)
		if !ok {
			continue
		}
		pr, _ := strconv.Atoi(m[1])
		out = append(out, Request{Repo: c.Repo, PR: pr, CommentID: c.ID, Author: c.Author, Instructions: instructions})
	}
	return out
}

// matchTrigger looks for a line that starts with the trigger phrase, anywhere in
// the comment, so a review can be asked for after a few words of context. Lines
// inside fenced code blocks never count, and neither do quoted lines or a phrase
// in the middle of a sentence.
func matchTrigger(body, phrase string) (string, bool) {
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(strings.ToLower(line), phrase) {
			continue
		}
		rest := line[len(phrase):]
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
			continue // "/otto reviewer" is not the trigger
		}
		return strings.TrimSpace(rest), true
	}
	return "", false
}
