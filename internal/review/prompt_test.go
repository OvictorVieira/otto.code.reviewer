package review

import (
	"strings"
	"testing"
)

func TestPromptIncludesSpecDiffAndVerificationSteps(t *testing.T) {
	p, err := BuildPrompt(PromptInput{
		Repo: "OvictorVieira/elevra.ai", URL: "https://github.com/OvictorVieira/elevra.ai/pull/165",
		Title: "feat: add evidence contracts", Body: "Closes #160", Base: "main", HeadSHA: "abc123",
		Files: []string{"src/a.ts"}, Diff: "+const a = 1", Language: "English", Verify: true,
		Instructions: "focus on security",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Closes #160", "git diff origin/main...HEAD", "Verify by running it", "- src/a.ts",
		"+const a = 1", "focus on security", "Never modify tracked files", "Write titles and bodies in English",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestPromptWithoutVerificationSkipsRunningSteps(t *testing.T) {
	p, err := BuildPrompt(PromptInput{Repo: "o/r", Base: "main", Diff: "+x", Language: "English"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "Verify by running it") {
		t.Error("read-only review must not ask the engine to run the project")
	}
}

func TestPromptTruncatesLargeDiffs(t *testing.T) {
	big := strings.Repeat("+line\n", 100000)
	p, err := BuildPrompt(PromptInput{Repo: "o/r", Base: "main", Diff: big, Language: "English", MaxDiffBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "was truncated") || len(p) > 20000 {
		t.Errorf("diff not truncated (len %d)", len(p))
	}
}
