package learn

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"
)

// MaxLessons keeps the memory small enough to read before every review.
const MaxLessons = 25

// Lesson is one piece of guidance Otto learned from maintainer feedback.
type Lesson struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"` // avoid | check | style
	Rule     string    `json:"rule"` // imperative, one sentence
	Why      string    `json:"why,omitempty"`
	Scope    string    `json:"scope,omitempty"` // optional path glob such as "apps/**/*.dart"
	Evidence int       `json:"evidence"`        // how many findings back it
	Pinned   bool      `json:"pinned,omitempty"`
	Disabled bool      `json:"disabled,omitempty"`
	Updated  time.Time `json:"updated"`
}

func lessonID(rule string) string {
	h := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(rule))))
	return hex.EncodeToString(h[:])[:8]
}

var distillTemplate = template.Must(template.New("distill").Parse(`You maintain the memory of a code reviewer named Otto.
Otto reviews pull requests for one repository. After each review, the maintainers react to every finding
(thumbs up or down) and often explain why in a reply. Your job is to turn that feedback into a short list of
lessons so Otto's next reviews improve, the way a developer learns from code review comments over time.

# Current lessons
{{ if .Lessons }}{{ range .Lessons }}- id={{ .ID }} [{{ .Kind }}]{{ if .Pinned }} (pinned){{ end }}{{ if .Scope }} scope={{ .Scope }}{{ end }}: {{ .Rule }}{{ if .Why }} (why: {{ .Why }}){{ end }}
{{ end }}{{ else }}(none yet)
{{ end }}
# New feedback since the lessons were last updated
Each item is a finding Otto posted, then the maintainers' outcome and their words.
{{ range .Records }}
- [{{ .Outcome }}] P{{ .Priority }} {{ .Title }} ({{ .Path }})
  Otto said: {{ .Body }}
{{- range .Notes }}
  Maintainer reply: {{ . }}
{{- end }}
{{ end }}
# How to update the lessons

- accepted: the finding was useful. Reinforce the pattern worth always checking.
- rejected: the finding was wrong, noisy or over-engineered for this repository. Write what to stop flagging, and when.
- resolved: weak positive signal only; never create a lesson from it alone.
- A lesson must be general and reusable across pull requests: no PR numbers, no file-specific trivia, no names of people.
  Scope it with a path glob only when it truly applies to part of the repository.
- Prefer the maintainers' own stated reasons. If there is no reason and only one data point, do not invent a rule.
- Merge duplicates, rewrite lessons the new feedback contradicts, and drop lessons that no longer hold.
- Never drop or weaken a pinned lesson. Keep at most {{ .Max }} lessons; keep the ones backed by the most evidence.
- Each rule is one imperative sentence of at most 200 characters. "why" is at most 200 characters.
- kind: "avoid" (stop flagging), "check" (always verify), or "style" (repository convention).
- Never include secrets, tokens or personal data.

# Output

Answer with exactly this JSON object, without markdown fences. Include every lesson that should remain,
reusing the id of an existing lesson when you keep or edit it:

{"lessons":[{"id":"<existing id or empty>","kind":"avoid","rule":"<imperative sentence>","why":"<short reason>","scope":"","evidence":1}]}
`))

// DistillPrompt asks an engine to fold new feedback into the lessons.
func DistillPrompt(current []Lesson, recs []Record) (string, error) {
	recs = append([]Record(nil), recs...)
	for i := range recs {
		recs[i].Body = clip(strings.Join(strings.Fields(recs[i].Body), " "), 300)
	}
	var b strings.Builder
	err := distillTemplate.Execute(&b, struct {
		Lessons []Lesson
		Records []Record
		Max     int
	}{current, recs, MaxLessons})
	return b.String(), err
}

// ParseDistill validates the engine's answer and merges it with the current
// lessons: pinned and disabled lessons are never lost, ids stay stable.
func ParseDistill(out string, current []Lesson, now time.Time) ([]Lesson, error) {
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return nil, errors.New("no JSON object in the answer")
	}
	var parsed struct {
		Lessons []Lesson `json:"lessons"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &parsed); err != nil {
		return nil, fmt.Errorf("answer is not valid JSON: %w", err)
	}
	byID := map[string]Lesson{}
	for _, l := range current {
		byID[l.ID] = l
	}
	var next []Lesson
	seen := map[string]bool{}
	for _, l := range parsed.Lessons {
		l.Rule = strings.TrimSpace(l.Rule)
		if l.Rule == "" || len([]rune(l.Rule)) > 300 {
			continue
		}
		switch l.Kind {
		case "avoid", "check", "style":
		default:
			l.Kind = "check"
		}
		if old, ok := byID[l.ID]; ok {
			l.Pinned, l.Disabled = old.Pinned, old.Disabled
			if old.Rule == l.Rule {
				l.Updated = old.Updated
			} else {
				l.Updated = now
			}
		} else {
			l.ID, l.Updated = lessonID(l.Rule), now
		}
		if seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		next = append(next, l)
	}
	// Pinned lessons survive even when the engine forgot them.
	for _, l := range current {
		if (l.Pinned || l.Disabled) && !seen[l.ID] {
			next = append(next, l)
		}
	}
	sort.SliceStable(next, func(i, j int) bool {
		if next[i].Pinned != next[j].Pinned {
			return next[i].Pinned
		}
		return next[i].Evidence > next[j].Evidence
	})
	if len(next) > MaxLessons {
		next = next[:MaxLessons]
	}
	return next, nil
}

// Active returns the lessons Otto should apply (not disabled).
func Active(ls []Lesson) []Lesson {
	var out []Lesson
	for _, l := range ls {
		if !l.Disabled {
			out = append(out, l)
		}
	}
	return out
}
