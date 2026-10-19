// Package format renders a review in the same layout Codex uses on GitHub:
// colored P0-P3 badges, inline comments and a summary review body.
package format

import (
	"errors"
	"fmt"
	"strings"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

var badgeColor = map[int]string{0: "red", 1: "orange", 2: "yellow", 3: "lightgrey"}

var checkIcon = map[string]string{"passed": "✅", "failed": "❌", "skipped": "⏭️"}

var specIcon = map[string]string{"met": "🟢", "partial": "🟡", "missing": "🔴", "wrong": "🔴"}

func note(n string) string {
	if n == "" {
		return ""
	}
	return " — " + n
}

// Meta describes which engine produced the review.
type Meta struct {
	Engine  string
	Model   string
	Effort  string
	Trigger string // defaults to "/otto review"
}

// Badge returns the small colored severity badge.
func Badge(priority int) string {
	color, ok := badgeColor[priority]
	if !ok {
		priority, color = 2, badgeColor[2]
	}
	return fmt.Sprintf("<sub><sub>![P%d Badge](https://img.shields.io/badge/P%d-%s?style=flat)</sub></sub>", priority, priority, color)
}

// InlineBody is the markdown of one inline comment.
func InlineBody(f review.Finding) string {
	return fmt.Sprintf("**%s  %s**\n\n%s\n\n%s\n\nUseful? React with 👍 / 👎.\n\n<!-- otto-review-finding:v1 -->",
		Badge(f.Priority), f.Title, f.Body, agentPromptBlock(f))
}

func agentPromptBlock(f review.Finding) string {
	prompt := fmt.Sprintf("In `%s` at %s, fix this review finding: %s. Context: %s "+
		"Inspect the surrounding code and repository instructions, make the smallest correct change, add or update a regression test, and run the relevant checks. Preserve unrelated behavior.",
		f.Path, lineLabel(f), f.Title, strings.TrimSpace(f.Body))
	lines := strings.Split(prompt, "\n")
	for i := range lines {
		lines[i] = "> " + lines[i]
	}
	return "**Suggested prompt for a coding agent**\n\n" + strings.Join(lines, "\n")
}

func lineLabel(f review.Finding) string {
	if f.Start == f.End {
		return fmt.Sprintf("line %d", f.Start)
	}
	return fmt.Sprintf("lines %d-%d", f.Start, f.End)
}

// Comment is one inline comment in the GitHub "create review" payload.
type Comment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// Review is the GitHub "create review" payload.
type Review struct {
	CommitID string    `json:"commit_id"`
	Body     string    `json:"body"`
	Event    string    `json:"event"`
	Comments []Comment `json:"comments"`
}

// Payload builds the review posted to GitHub. The event is always COMMENT so
// it works on the user's own pull requests too.
func Payload(commitSHA string, p review.Placement, r review.Result, m Meta) Review {
	out := Review{
		CommitID: commitSHA,
		Body:     Summary(commitSHA, p, r, m),
		Event:    "COMMENT",
		Comments: []Comment{},
	}
	for _, f := range p.Inline {
		c := Comment{Path: f.Path, Line: f.End, Side: "RIGHT", Body: InlineBody(f)}
		if f.Start < f.End {
			c.StartLine, c.StartSide = f.Start, "RIGHT"
		}
		out.Comments = append(out.Comments, c)
	}
	return out
}

// Summary is the top-level review body.
func Summary(commitSHA string, p review.Placement, r review.Result, m Meta) string {
	trigger := m.Trigger
	if trigger == "" {
		trigger = "/otto review"
	}
	var b strings.Builder
	b.WriteString("<!-- otto-review-receipt:v1 -->\n\n")

	if len(p.Inline)+len(p.Outside) == 0 {
		b.WriteString("Otto Review: Didn't find any major issues. Bravo.\n\n")
	} else {
		b.WriteString("### 💡 Otto Review\n\nHere are some automated review suggestions for this pull request.\n\n")
	}

	fmt.Fprintf(&b, "**Reviewed commit:** `%s`\n", short(commitSHA))
	b.WriteString("\n<details> <summary>ℹ️ About Otto in GitHub</summary>\n<br/>\n\n")
	fmt.Fprintf(&b, "**Engine:** %s\n", engineLabel(m))
	verdict := "patch is correct"
	if !r.Correct {
		verdict = "patch is incorrect"
	}
	fmt.Fprintf(&b, "**Verdict:** %s", verdict)
	if r.Explanation != "" {
		fmt.Fprintf(&b, ". %s", r.Explanation)
	}
	b.WriteString("\n")

	if len(r.Verification) > 0 {
		b.WriteString("\n#### Verification\n\n")
		for _, c := range r.Verification {
			fmt.Fprintf(&b, "- %s `%s`%s\n", checkIcon[c.Result], c.Command, note(c.Note))
		}
	}

	if len(r.Spec) > 0 {
		b.WriteString("\n#### Spec\n\n")
		for _, c := range r.Spec {
			fmt.Fprintf(&b, "- %s %s (%s)%s\n", specIcon[c.Status], c.Requirement, c.Status, note(c.Note))
		}
	}

	if len(p.Outside) > 0 {
		heading := "Findings outside the diff"
		if p.InlineFallback {
			heading = "Findings not attached inline"
			b.WriteString("\n#### " + heading + "\n\n")
			b.WriteString("GitHub could not attach these findings to changed lines; the affected file and line are shown below.\n\n")
		} else {
			b.WriteString("\n#### " + heading + "\n\n")
		}
		for _, f := range p.Outside {
			fmt.Fprintf(&b, "- %s **%s** (`%s:%d`)\n  %s\n", Badge(f.Priority), f.Title, f.Path, f.Start, f.Body)
			fmt.Fprintf(&b, "\n%s\n\n", agentPromptBlock(f))
		}
	}

	fmt.Fprintf(&b, "\nOtto is an open-source reviewer that runs on the author's own machine, using the AI access on their account. "+
		"Comment `%s` on a pull request to request a review.\n\n</details>\n", trigger)
	return b.String()
}

// QuotaFailure renders a provider quota failure for a manual review request.
func QuotaFailure(err error) string {
	var quota *engine.QuotaError
	_ = errors.As(err, &quota)
	details := "The provider did not include a reset time. Check your account's usage page for when requests become available again."
	if quota != nil && quota.Details != "" {
		details = strings.TrimSpace(quota.Details)
	}
	return fmt.Sprintf("### 💡 Otto Review\n\n"+
		"Otto stopped the review because an AI usage limit was reached. Any reset information reported by the provider is below; Otto does not guess a reset time.\n\n"+
		"<details> <summary>Engine status</summary>\n<br/>\n\n```\n%s\n```\n\n</details>\n", details)
}

func engineLabel(m Meta) string {
	label := fmt.Sprintf("`%s`", m.Engine)
	if m.Model != "" {
		label += fmt.Sprintf(" · model `%s`", m.Model)
	}
	if m.Effort != "" {
		label += fmt.Sprintf(" · effort `%s`", m.Effort)
	}
	return label
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
