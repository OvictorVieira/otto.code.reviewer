package review

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed judge.md
var judgeTemplate string

var judgePrompt = template.Must(template.New("judge").Parse(judgeTemplate))

// JudgeEarlier is a finding Otto made before, with how the author answered it and the code now.
type JudgeEarlier struct {
	Ref                                 int64
	Status, Title, Location, Said, Code string
	Replies                             []string
}

// JudgeCandidate is a finding Otto is about to post.
type JudgeCandidate struct {
	Index                       int
	Title, Location, Body, Code string
	Claims                      int64 // the earlier finding the reviewer said it follows up, if any
}

// JudgeInput is what the judge needs to decide which candidates are worth posting.
type JudgeInput struct {
	Earlier    []JudgeEarlier
	Candidates []JudgeCandidate
	Fence      string
}

// BuildJudgePrompt renders the judge's instructions.
func BuildJudgePrompt(in JudgeInput) (string, error) {
	if in.Fence == "" {
		in.Fence = newFence()
	}
	clean := func(s string) string { return strings.ReplaceAll(s, in.Fence, "[fence]") }
	for i := range in.Earlier {
		e := &in.Earlier[i]
		e.Title, e.Said, e.Code = clean(e.Title), clean(e.Said), clean(e.Code)
		for j := range e.Replies {
			e.Replies[j] = clean(e.Replies[j])
		}
	}
	for i := range in.Candidates {
		c := &in.Candidates[i]
		c.Title, c.Body, c.Code = clean(c.Title), clean(c.Body), clean(c.Code)
	}
	var b strings.Builder
	err := judgePrompt.Execute(&b, in)
	return b.String(), err
}

// Decision is the judge's call on one candidate finding.
type Decision struct {
	Candidate int
	New       bool   // not the same problem as any earlier finding
	Ref       int64  // the earlier finding it repeats
	Verdict   string // Satisfied or Unsatisfied, for a repeat
	Reason    string
}

// ParseJudge reads the judge's answer. It must decide every candidate; an answer that leaves one out
// or contradicts itself is rejected so the engine is asked again instead of guessing.
func ParseJudge(out string, candidates int, knownRefs map[int64]bool) ([]Decision, error) {
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return nil, errors.New("no JSON object in the answer")
	}
	var parsed struct {
		Decisions []struct {
			Candidate flexRef `json:"candidate"`
			Relation  string  `json:"relation"`
			Ref       flexRef `json:"ref"`
			Verdict   string  `json:"verdict"`
			Reason    string  `json:"reason"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &parsed); err != nil {
		return nil, fmt.Errorf("answer is not valid JSON: %w", err)
	}
	byIndex := map[int]Decision{}
	for _, d := range parsed.Decisions {
		i := int(d.Candidate)
		if i < 0 || i >= candidates {
			continue
		}
		dec := Decision{Candidate: i, Reason: strings.TrimSpace(d.Reason)}
		switch strings.ToLower(strings.TrimSpace(d.Relation)) {
		case "new", "different", "unrelated":
			dec.New = true
		case "same", "repeat", "related":
			dec.Ref = int64(d.Ref)
			if !knownRefs[dec.Ref] {
				return nil, fmt.Errorf("candidate %d repeats ref %d, which is not an earlier finding", i, dec.Ref)
			}
			dec.Verdict = recheckVerdict(d.Verdict)
			if dec.Verdict == Unclear {
				return nil, fmt.Errorf("candidate %d repeats ref %d but has no satisfied/unsatisfied verdict", i, dec.Ref)
			}
		default:
			return nil, fmt.Errorf("candidate %d has no relation (new or same)", i)
		}
		byIndex[i] = dec
	}
	out2 := make([]Decision, 0, candidates)
	for i := 0; i < candidates; i++ {
		d, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("no decision for candidate %d", i)
		}
		out2 = append(out2, d)
	}
	return out2, nil
}

// CodeAround returns the lines around line of path in the checkout at dir, numbered, for the judge to
// read. It returns "" when the file cannot be read or lies outside the checkout.
func CodeAround(dir, path string, line, radius int) string {
	if dir == "" || path == "" || line <= 0 {
		return ""
	}
	full := filepath.Join(dir, filepath.FromSlash(path))
	if rel, err := filepath.Rel(dir, full); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	b, err := os.ReadFile(full)
	if err != nil || len(b) > 1<<20 {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	from, to := max(1, line-radius), min(len(lines), line+radius)
	var out strings.Builder
	for n := from; n <= to; n++ {
		fmt.Fprintf(&out, "    %d| %s\n", n, strings.TrimRight(lines[n-1], "\r"))
	}
	return strings.TrimRight(out.String(), "\n")
}
