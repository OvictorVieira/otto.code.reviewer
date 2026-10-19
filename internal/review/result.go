// Package review turns an engine's answer into findings and decides where
// each finding can be posted.
package review

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
)

// Finding is one issue reported by the engine.
type Finding struct {
	Title      string
	Body       string
	Confidence float64
	Priority   int // 0 (P0) .. 3 (P3)
	Path       string
	Start      int
	End        int
}

// Check is one command the engine ran to verify the change.
type Check struct {
	Command string `json:"command"`
	Result  string `json:"result"` // passed | failed | skipped
	Note    string `json:"note"`
}

// SpecCheck is one requirement of the pull request spec and how it was met.
type SpecCheck struct {
	Requirement string `json:"requirement"`
	Status      string `json:"status"` // met | partial | missing | wrong
	Note        string `json:"note"`
}

// Result is the full verdict of one review.
type Result struct {
	Findings     []Finding
	Verification []Check
	Spec         []SpecCheck
	Correct      bool
	Explanation  string
	Confidence   float64
}

type wireResult struct {
	Findings []struct {
		Title        string  `json:"title"`
		Body         string  `json:"body"`
		Confidence   float64 `json:"confidence_score"`
		Priority     *int    `json:"priority"`
		CodeLocation struct {
			Path      string `json:"absolute_file_path"`
			LineRange struct {
				Start int `json:"start"`
				End   int `json:"end"`
			} `json:"line_range"`
		} `json:"code_location"`
	} `json:"findings"`
	Verification       []Check     `json:"verification"`
	SpecChecks         []SpecCheck `json:"spec_checks"`
	OverallCorrectness string      `json:"overall_correctness"`
	OverallExplanation string      `json:"overall_explanation"`
	OverallConfidence  float64     `json:"overall_confidence_score"`
}

var titleTag = regexp.MustCompile(`^\s*\[P([0-3])\]\s*`)

// ErrNoResult means the engine answered without a review JSON.
var ErrNoResult = errors.New("engine output has no review JSON")

// ParseResult extracts the last review JSON object found in output.
// repoRoot is stripped from absolute file paths.
func ParseResult(output, repoRoot string) (Result, error) {
	wire, ok := lastReviewObject(output)
	if !ok {
		return Result{}, ErrNoResult
	}

	r := Result{
		Verification: wire.Verification,
		Spec:         wire.SpecChecks,
		Correct:      strings.TrimSpace(strings.ToLower(wire.OverallCorrectness)) == "patch is correct",
		Explanation:  strings.TrimSpace(wire.OverallExplanation),
		Confidence:   wire.OverallConfidence,
	}
	for _, w := range wire.Findings {
		f := Finding{
			Body:       strings.TrimSpace(w.Body),
			Confidence: w.Confidence,
			Priority:   2,
			Path:       relativePath(w.CodeLocation.Path, repoRoot),
			Start:      w.CodeLocation.LineRange.Start,
			End:        w.CodeLocation.LineRange.End,
		}
		title := w.Title
		if m := titleTag.FindStringSubmatch(title); m != nil {
			f.Priority = int(m[1][0] - '0')
			title = title[len(m[0]):]
		}
		if w.Priority != nil && *w.Priority >= 0 && *w.Priority <= 3 {
			f.Priority = *w.Priority
		}
		f.Title = strings.TrimSpace(title)
		if f.End < f.Start {
			f.End = f.Start
		}
		r.Findings = append(r.Findings, f)
	}
	return r, nil
}

// lastReviewObject scans every '{' and keeps the last object that decodes
// with a "findings" key, so prose or fences around the JSON do not matter.
func lastReviewObject(output string) (wireResult, bool) {
	var found wireResult
	ok := false
	for i := 0; i < len(output); i++ {
		if output[i] != '{' {
			continue
		}
		var probe map[string]json.RawMessage
		dec := json.NewDecoder(strings.NewReader(output[i:]))
		if dec.Decode(&probe) != nil {
			continue
		}
		if _, has := probe["findings"]; !has {
			continue
		}
		var w wireResult
		if json.NewDecoder(strings.NewReader(output[i:])).Decode(&w) == nil {
			found, ok = w, true
			i += int(dec.InputOffset()) - 1
		}
	}
	return found, ok
}

func relativePath(path, repoRoot string) string {
	p := filepath.ToSlash(path)
	root := strings.TrimSuffix(filepath.ToSlash(repoRoot), "/")
	if root != "" && strings.HasPrefix(p, root+"/") {
		p = strings.TrimPrefix(p, root+"/")
	}
	return strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/")
}

// Placement says where each finding goes.
type Placement struct {
	Inline         []Finding // attached to a line in the diff
	Outside        []Finding // valid but outside the diff: listed in the summary
	Dropped        []Finding // below the confidence threshold
	InlineFallback bool      // inline comments were rejected by GitHub and moved to the summary
}

// Place keeps findings at or above minConfidence, attaches them to diff lines
// when possible, and sorts each group by priority (P0 first).
func Place(r Result, d diff.Diff, minConfidence float64) Placement {
	var p Placement
	for _, f := range r.Findings {
		if f.Confidence < minConfidence {
			p.Dropped = append(p.Dropped, f)
			continue
		}
		switch {
		case d.SameHunk(f.Path, f.Start, f.End):
			p.Inline = append(p.Inline, f)
		case d.Commentable(f.Path, f.End):
			f.Start = f.End
			p.Inline = append(p.Inline, f)
		case d.Commentable(f.Path, f.Start):
			f.End = f.Start
			p.Inline = append(p.Inline, f)
		default:
			p.Outside = append(p.Outside, f)
		}
	}
	byPriority := func(fs []Finding) {
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].Priority < fs[j].Priority })
	}
	byPriority(p.Inline)
	byPriority(p.Outside)
	return p
}
