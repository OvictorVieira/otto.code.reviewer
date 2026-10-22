package engine

import (
	"regexp"
	"strings"
)

// errorBodyLines is how many lines after an error line still belong to it, for errors that
// print a JSON body over several lines.
const errorBodyLines = 12

var errorStart = regexp.MustCompile(`(?i)^(?:[✗✘×!]\s*)?(?:[a-z]*error|fatal)\s*:`)

// errorTracker says, line by line, whether a line of an engine's output is part of an error the
// engine reported. Engines echo what they read and think to the same streams as their errors, so
// a line of code or prose that merely mentions "rate limit" or "429" must never count as one.
type errorTracker struct{ remaining int }

func (t *errorTracker) inError(line string) bool {
	if errorStart.MatchString(strings.TrimSpace(ansiColors.ReplaceAllString(line, ""))) {
		t.remaining = errorBodyLines
		return true
	}
	if t.remaining > 0 {
		t.remaining--
		return true
	}
	return false
}

// errorBlocks keeps only the error lines of an engine's output and the lines right after them.
func errorBlocks(text string) string {
	var tracker errorTracker
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if tracker.inError(line) {
			kept = append(kept, strings.TrimSpace(ansiColors.ReplaceAllString(line, "")))
		}
	}
	return strings.Join(kept, "\n")
}
