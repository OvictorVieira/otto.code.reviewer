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
// first line starts with the trigger phrase and that were not handled yet.
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

func matchTrigger(body, phrase string) (string, bool) {
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(body), "\n", 2)[0])
	if !strings.HasPrefix(strings.ToLower(first), phrase) {
		return "", false
	}
	rest := first[len(phrase):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false // "/otto reviewer" is not the trigger
	}
	return strings.TrimSpace(rest), true
}
