package review

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
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
	MaxDiffBytes                          int

	DiffTruncated bool

	// Lessons are what Otto learned from maintainer feedback on earlier reviews of this
	// repository. Prior are the findings already made on this pull request and their outcome.
	Lessons []learn.Lesson
	Prior   []learn.Record

	// CI describes the pull request's CI checks on the reviewed commit, with failing log excerpts.
	CI string

	// Part, when set, says this reviewer covers only some files of a large pull request (the diff
	// then holds only those files). Integration, when set, makes this the final pass that looks
	// across all parts; it lists what the parts already found.
	Part        string
	Integration string

	// Issues holds the title and body of issues the description links to.
	Issues string
	// Fence marks where untrusted text starts and ends. It is random per prompt, so text in a pull
	// request cannot close the fence and pose as Otto's own instructions. Empty picks one.
	Fence string
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
	if in.Fence == "" {
		in.Fence = newFence()
	}
	// A pull request that contains the fence (it cannot know it in advance) gets it neutralized.
	for _, f := range []*string{&in.Body, &in.Diff, &in.CI, &in.Integration, &in.Issues, &in.Title} {
		*f = strings.ReplaceAll(*f, in.Fence, "[fence]")
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

func newFence() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
