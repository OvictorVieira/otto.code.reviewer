// Package slice splits a large pull request into parts several reviewers can hold in their head,
// and merges what they found back into one review.
package slice

import (
	"path"
	"sort"
	"strings"

	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

const (
	// A pull request is split only when it is larger than one reviewer handles well.
	SplitFiles = 40
	SplitLines = 1500
	// Each part stays around this size.
	PartFiles = 15
	PartLines = 600
)

// Plan returns the files of each part, or a single part when the pull request is small enough to
// review in one go. Files of the same area (top two directories, so code and its tests travel
// together) stay in the same part whenever the area fits.
func Plan(stats []diff.FileStat) [][]string {
	files, lines := 0, 0
	for _, f := range stats {
		files++
		lines += f.Added + f.Removed
	}
	if files <= SplitFiles && lines <= SplitLines {
		var all []string
		for _, f := range stats {
			all = append(all, f.Path)
		}
		return [][]string{all}
	}

	type group struct {
		key   string
		files []diff.FileStat
		lines int
	}
	byKey := map[string]*group{}
	var order []*group
	for _, f := range stats {
		k := area(f.Path)
		g, ok := byKey[k]
		if !ok {
			g = &group{key: k}
			byKey[k] = g
			order = append(order, g)
		}
		g.files = append(g.files, f)
		g.lines += f.Added + f.Removed
	}
	// Biggest areas first, so small ones fill the gaps (first-fit decreasing).
	sort.SliceStable(order, func(i, j int) bool { return order[i].lines > order[j].lines })

	type part struct {
		files []string
		lines int
	}
	var parts []*part
	place := func(f diff.FileStat) {
		n := f.Added + f.Removed
		for _, p := range parts {
			if len(p.files) < PartFiles && p.lines+n <= PartLines {
				p.files, p.lines = append(p.files, f.Path), p.lines+n
				return
			}
		}
		parts = append(parts, &part{files: []string{f.Path}, lines: n})
	}
	for _, g := range order {
		if len(g.files) <= PartFiles && g.lines <= PartLines {
			// Keep the area whole: put it in the first part with room for all of it.
			placed := false
			for _, p := range parts {
				if len(p.files)+len(g.files) <= PartFiles && p.lines+g.lines <= PartLines {
					for _, f := range g.files {
						p.files = append(p.files, f.Path)
					}
					p.lines += g.lines
					placed = true
					break
				}
			}
			if !placed {
				p := &part{lines: g.lines}
				for _, f := range g.files {
					p.files = append(p.files, f.Path)
				}
				parts = append(parts, p)
			}
			continue
		}
		for _, f := range g.files { // an area too big for one part is spread file by file
			place(f)
		}
	}
	out := make([][]string, len(parts))
	for i, p := range parts {
		out[i] = p.files
	}
	return out
}

func area(p string) string {
	parts := strings.Split(path.Dir(p), "/")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "/")
}

// SubDiff keeps only the sections of a unified diff that belong to the given files.
func SubDiff(raw string, files []string) string {
	want := map[string]bool{}
	for _, f := range files {
		want[f] = true
	}
	var b strings.Builder
	keep := false
	for _, line := range strings.SplitAfter(raw, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			keep = false
			if f := strings.Fields(line); len(f) >= 4 {
				keep = want[strings.TrimPrefix(f[3], "b/")]
			}
		}
		if keep {
			b.WriteString(line)
		}
	}
	return b.String()
}

// Merge joins the results of every part (and of the integration pass) into one review. Findings
// that point at the same lines with a similar title are kept once, at the higher priority. The
// verdict is "correct" only when every part said so.
func Merge(results []review.Result) review.Result {
	var out review.Result
	out.Correct = true
	out.Confidence = 1
	seenCheck, seenSpec := map[string]bool{}, map[string]bool{}
	var explanations []string
	for _, r := range results {
		for _, f := range r.Findings {
			if i := duplicateOf(out.Findings, f); i >= 0 {
				if f.Priority < out.Findings[i].Priority || (f.Priority == out.Findings[i].Priority && f.Confidence > out.Findings[i].Confidence) {
					out.Findings[i] = f
				}
				continue
			}
			out.Findings = append(out.Findings, f)
		}
		for _, c := range r.Verification {
			if k := c.Command + "|" + c.Result; !seenCheck[k] {
				seenCheck[k] = true
				out.Verification = append(out.Verification, c)
			}
		}
		for _, s := range r.Spec {
			if k := strings.ToLower(s.Requirement); !seenSpec[k] {
				seenSpec[k] = true
				out.Spec = append(out.Spec, s)
			}
		}
		out.Correct = out.Correct && r.Correct
		if r.Confidence < out.Confidence {
			out.Confidence = r.Confidence
		}
		if e := strings.TrimSpace(r.Explanation); e != "" {
			explanations = append(explanations, e)
		}
	}
	if len(results) == 0 {
		out.Confidence = 0
	}
	out.Explanation = strings.Join(explanations, " ")
	return out
}

func duplicateOf(fs []review.Finding, f review.Finding) int {
	for i, g := range fs {
		if g.Path != f.Path || g.End < f.Start || f.End < g.Start {
			continue
		}
		if similar(g.Title, f.Title) {
			return i
		}
	}
	return -1
}

func similar(a, b string) bool {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	return float64(shared)/float64(min(len(wa), len(wb))) >= 0.5
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}
