package learn

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Budgets keep what Otto remembers from inflating the review prompt. They are
// characters (about four per token), so the memory costs a few hundred tokens no
// matter how much Otto has learned.
const (
	LessonBudget = 1600
	LessonMax    = 12
	PriorBudget  = 1200
	PriorMax     = 8
	staleAfter   = 120 * 24 * time.Hour // unreinforced, unpinned lessons older than this stop being injected
)

var words = regexp.MustCompile(`[a-z0-9]{3,}`)

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range words.FindAllString(strings.ToLower(s), -1) {
		out[w] = true
	}
	return out
}

// globRE turns a scope glob ("apps/**/*.dart") into a regexp.
func globRE(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case c == '*' && strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 2
		case c == '*' && strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

func scopeHits(scope string, files []string) bool {
	re := globRE(scope)
	if re == nil {
		return false
	}
	for _, f := range files {
		if re.MatchString(f) {
			return true
		}
	}
	return false
}

// SelectLessons picks the few lessons that matter for this pull request, within the
// budget. A lesson scoped to paths the change does not touch is skipped; the rest are
// ranked by pin, scope match, word overlap with the changed files and title, then how many
// findings back it. Stale lessons nobody reinforced drop out of the prompt (they stay stored).
func SelectLessons(lessons []Lesson, files []string, title string, now time.Time) []Lesson {
	context := tokens(strings.Join(files, " ") + " " + title)
	type scored struct {
		l     Lesson
		score int
	}
	var cands []scored
	for _, l := range Active(lessons) {
		score := 0
		switch {
		case l.Scope != "" && !scopeHits(l.Scope, files):
			continue
		case l.Scope != "":
			score += 4
		}
		if !l.Pinned && l.Evidence <= 1 && !l.Updated.IsZero() && now.Sub(l.Updated) > staleAfter {
			continue
		}
		for w := range tokens(l.Rule + " " + l.Why) {
			if context[w] {
				score += 3
			}
		}
		if l.Pinned {
			score += 100
		}
		score += min(l.Evidence, 5)
		cands = append(cands, scored{l, score})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var out []Lesson
	used := 0
	for _, c := range cands {
		cost := len(c.l.Rule) + len(c.l.Why) + len(c.l.Scope) + 16
		if len(out) == LessonMax || (used+cost > LessonBudget && !c.l.Pinned) {
			continue
		}
		used += cost
		out = append(out, c.l)
	}
	return out
}

// SelectPrior keeps the earlier findings on this pull request that still change what
// Otto should say: rejected ones (with the maintainers' reason) and ones still open.
// Accepted or resolved findings are fixed already and cost nothing to leave out.
func SelectPrior(recs []Record) []Record {
	var keep []Record
	for _, r := range recs {
		if r.Outcome == Rejected || r.Outcome == Open {
			keep = append(keep, r)
		}
	}
	sort.SliceStable(keep, func(i, j int) bool {
		if (keep[i].Outcome == Rejected) != (keep[j].Outcome == Rejected) {
			return keep[i].Outcome == Rejected
		}
		return keep[i].Priority < keep[j].Priority
	})
	var out []Record
	used := 0
	for _, r := range keep {
		r.Title = clip(r.Title, 100)
		if len(r.Notes) > 1 {
			r.Notes = r.Notes[:1]
		}
		for i := range r.Notes {
			r.Notes[i] = clip(strings.Join(strings.Fields(r.Notes[i]), " "), 160)
		}
		cost := len(r.Title) + len(r.Path) + 24
		for _, n := range r.Notes {
			cost += len(n)
		}
		if len(out) == PriorMax || used+cost > PriorBudget {
			break
		}
		used += cost
		out = append(out, r)
	}
	return out
}
