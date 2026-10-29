package review

import (
	_ "embed"
	"strings"
	"text/template"

	"github.com/OvictorVieira/otto.code.reviewer/internal/learn"
)

//go:embed prompt.md
var promptTemplate string

var prompt = template.Must(template.New("review").Parse(promptTemplate))

// DefaultMaxDiffBytes keeps the prompt within every engine's context window.
const DefaultMaxDiffBytes = 200_000

// PromptInput is everything the engine needs to review one pull request.
type PromptInput struct {
	Repo, URL, Title, Body, Base, HeadSHA string
	Files                                 []string
	Diff                                  string
	Language                              string
	Instructions                          string
	Verify                                bool
	MaxDiffBytes                          int

	DiffTruncated bool

	// Lessons are what Otto learned from maintainer feedback on earlier reviews of this
	// repository. Prior are the findings already made on this pull request and their outcome.
	Lessons []learn.Lesson
	Prior   []learn.Record
}

// BuildPrompt renders the review harness for one pull request.
func BuildPrompt(in PromptInput) (string, error) {
	limit := in.MaxDiffBytes
	if limit <= 0 {
		limit = DefaultMaxDiffBytes
	}
	if len(in.Diff) > limit {
		in.Diff, in.DiffTruncated = in.Diff[:limit], true
	}
	var b strings.Builder
	err := prompt.Execute(&b, in)
	return b.String(), err
}

// OutputContract is the part of the prompt that says what the answer must look like. Otto sends it
// back to an engine whose answer was not valid JSON, so the engine can fix its own output.
func OutputContract() string {
	if i := strings.Index(promptTemplate, "# Output"); i >= 0 {
		return strings.TrimSpace(promptTemplate[i:])
	}
	return ""
}
