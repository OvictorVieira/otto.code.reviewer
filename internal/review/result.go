// Package review turns an engine's answer into findings and decides where
// each finding can be posted.
package review

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// The engine's JSON is read leniently: smaller models keep the structure of the requested review
// but write text where a number or an object was asked for ("P2", "245-269"), or name a field
// their own way. A field Otto cannot read is left empty; it never throws the whole review away.

// flexNumber is a number written as a JSON number or as text.
type flexNumber float64

func (n *flexNumber) UnmarshalJSON(b []byte) error {
	var f float64
	if json.Unmarshal(b, &f) == nil {
		*n = flexNumber(f)
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			*n = flexNumber(f)
		}
	}
	return nil
}

// flexPriority is a priority 0..3 written as 2, "2", "P2" or "p2".
type flexPriority struct {
	value int
	ok    bool
}

var priorityText = regexp.MustCompile(`^\s*[Pp]?\s*([0-3])\s*$`)

func (p *flexPriority) UnmarshalJSON(b []byte) error {
	var n int
	if json.Unmarshal(b, &n) == nil {
		p.value, p.ok = n, n >= 0 && n <= 3
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		if m := priorityText.FindStringSubmatch(s); m != nil {
			p.value, p.ok = int(m[1][0]-'0'), true
		}
	}
	return nil
}

// flexRange is a line range written as {"start": 5, "end": 7}, 5, "5", "5-7", "5–7" or "L5-L7".
type flexRange struct{ start, end int }

var (
	rangeText   = regexp.MustCompile(`(\d+)\D+(\d+)`)
	firstNumber = regexp.MustCompile(`\d+`)
)

func (r *flexRange) UnmarshalJSON(b []byte) error {
	var obj struct {
		Start flexNumber `json:"start"`
		End   flexNumber `json:"end"`
	}
	if json.Unmarshal(b, &obj) == nil && obj.Start > 0 {
		r.start, r.end = int(obj.Start), int(obj.End)
		return nil
	}
	var text string
	if json.Unmarshal(b, &text) != nil {
		var n float64
		if json.Unmarshal(b, &n) != nil {
			return nil
		}
		text = strconv.Itoa(int(n))
	}
	if m := rangeText.FindStringSubmatch(text); m != nil {
		r.start, _ = strconv.Atoi(m[1])
		r.end, _ = strconv.Atoi(m[2])
	} else if m := firstNumber.FindString(text); m != "" {
		r.start, _ = strconv.Atoi(m)
	}
	return nil
}

type wireFinding struct {
	Title        string       `json:"title"`
	Body         string       `json:"body"`
	Confidence   flexNumber   `json:"confidence_score"`
	Priority     flexPriority `json:"priority"`
	CodeLocation struct {
		Path      string    `json:"absolute_file_path"`
		AltPath   string    `json:"path"`
		LineRange flexRange `json:"line_range"`
	} `json:"code_location"`
}

type wireSpec struct {
	Requirement string `json:"requirement"`
	SpecItem    string `json:"spec_item"`
	Item        string `json:"item"`
	Status      string `json:"status"`
	Note        string `json:"note"`
	Explanation string `json:"explanation"`
}

func (w wireSpec) check() SpecCheck {
	return SpecCheck{Requirement: firstNonEmpty(w.Requirement, w.SpecItem, w.Item), Status: specStatus(w.Status), Note: firstNonEmpty(w.Note, w.Explanation)}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// specStatus maps the words models use for a spec check onto Otto's four statuses.
func specStatus(s string) string {
	switch status := strings.ToLower(strings.TrimSpace(s)); status {
	case "met", "pass", "passed", "ok", "yes", "done":
		return "met"
	case "partial", "partially", "partly":
		return "partial"
	case "missing", "absent", "not implemented":
		return "missing"
	case "wrong", "fail", "failed", "unmet", "no", "incorrect":
		return "wrong"
	default:
		return status
	}
}

type wireResult struct {
	Findings           []wireFinding
	Verification       []Check
	SpecChecks         []wireSpec
	OverallCorrectness string
	OverallExplanation string
	OverallConfidence  flexNumber
}

// decodeWire reads a review object part by part. Only "findings" must be a list; any other part
// that cannot be read is skipped, and so is a single finding that cannot be read.
func decodeWire(probe map[string]json.RawMessage) (wireResult, bool) {
	var rawFindings []json.RawMessage
	if json.Unmarshal(probe["findings"], &rawFindings) != nil {
		return wireResult{}, false
	}
	var w wireResult
	for _, raw := range rawFindings {
		var f wireFinding
		if json.Unmarshal(raw, &f) == nil {
			w.Findings = append(w.Findings, f)
		}
	}
	_ = json.Unmarshal(probe["verification"], &w.Verification)
	_ = json.Unmarshal(probe["spec_checks"], &w.SpecChecks)
	_ = json.Unmarshal(probe["overall_correctness"], &w.OverallCorrectness)
	_ = json.Unmarshal(probe["overall_explanation"], &w.OverallExplanation)
	_ = json.Unmarshal(probe["overall_confidence_score"], &w.OverallConfidence)
	return w, true
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
		Correct:      strings.TrimSpace(strings.ToLower(wire.OverallCorrectness)) == "patch is correct",
		Explanation:  strings.TrimSpace(wire.OverallExplanation),
		Confidence:   float64(wire.OverallConfidence),
	}
	for _, spec := range wire.SpecChecks {
		r.Spec = append(r.Spec, spec.check())
	}
	for _, w := range wire.Findings {
		f := Finding{
			Body:       strings.TrimSpace(w.Body),
			Confidence: float64(w.Confidence),
			Priority:   2,
			Path:       relativePath(firstNonEmpty(w.CodeLocation.Path, w.CodeLocation.AltPath), repoRoot),
			Start:      w.CodeLocation.LineRange.start,
			End:        w.CodeLocation.LineRange.end,
		}
		title := w.Title
		if m := titleTag.FindStringSubmatch(title); m != nil {
			f.Priority = int(m[1][0] - '0')
			title = title[len(m[0]):]
		}
		if w.Priority.ok {
			f.Priority = w.Priority.value
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
		if w, valid := decodeWire(probe); valid {
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
	Inline  []Finding // attached to a line in the diff
	Outside []Finding // valid but outside the diff: listed in the summary
	Dropped []Finding // below the confidence threshold
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
