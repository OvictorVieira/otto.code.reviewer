package review

import (
	_ "embed"
	"strings"
	"text/template"
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
