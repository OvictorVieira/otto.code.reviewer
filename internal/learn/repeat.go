package learn

import "strings"

// Thresholds for deciding that a new finding says what an earlier one already said.
// Overlap is |shared words| / |words of the shorter text|, so a reworded or shortened
// version of the same finding still matches.
const (
	repeatOverlap     = 0.5  // same file
	repeatOverlapNear = 0.35 // same file and the line is within repeatNear lines of the old one
	repeatNear        = 5
)

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true, "with": true, "not": true,
	"are": true, "was": true, "will": true, "can": true, "but": true, "from": true, "when": true,
	"which": true, "has": true, "have": true, "its": true, "also": true, "should": true, "would": true,
	"may": true, "any": true, "all": true, "into": true, "than": true, "then": true, "does": true,
}

func significant(s string) map[string]bool {
	out := map[string]bool{}
	for w := range tokens(s) {
		if !stopWords[w] {
			out[w] = true
		}
	}
	return out
}

// Match returns the earlier finding on this pull request that a new finding (path, line, title, body)
// restates, whatever happened to it since: fixed, rejected, answered, resolved or still open. Only
// findings in the same file can match.
func Match(path string, line int, title, body string, seen []Record) (Record, bool) {
	words := significant(title + " " + body)
	if len(words) == 0 {
		return Record{}, false
	}
	for _, r := range seen {
		if r.Path != path {
			continue
		}
		old := significant(r.Title + " " + r.Body)
		if len(old) == 0 {
			continue
		}
		shared := 0
		for w := range words {
			if old[w] {
				shared++
			}
		}
		overlap := float64(shared) / float64(min(len(words), len(old)))
		need := repeatOverlap
		if r.Line > 0 && line > 0 && abs(r.Line-line) <= repeatNear {
			need = repeatOverlapNear
		}
		sameTitle := strings.TrimSpace(title) != "" && strings.EqualFold(strings.TrimSpace(r.Title), strings.TrimSpace(title))
		if overlap >= need || sameTitle {
			return r, true
		}
	}
	return Record{}, false
}

// CanFollowUp says whether Otto may write a new comment because an earlier finding is still not
// resolved after the author's answer or change. The thread of the earlier finding is usually closed,
// so the follow-up is a new comment. Three cases stay quiet:
//   - the finding is still open: its comment is already on the pull request;
//   - Otto already followed it up and the author did not fix that either: asking again is a loop;
//   - the finding is itself a follow-up the author rejected or closed: the author has had two rounds.
func CanFollowUp(rec Record, seen []Record) bool {
	if rec.Outcome == Open {
		return false
	}
	if rec.FollowsUp != 0 && rec.Outcome != Accepted {
		return false
	}
	for _, s := range seen {
		if s.FollowsUp == rec.ID && s.Outcome != Accepted {
			return false
		}
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
