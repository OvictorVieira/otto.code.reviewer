package review

import (
	"strings"
	"testing"

	"github.com/OvictorVieira/otto.code.reviewer/internal/learn"
)

func TestPromptIncludesSpecDiffAndVerificationSteps(t *testing.T) {
	p, err := BuildPrompt(PromptInput{
		Repo: "OvictorVieira/elevra.ai", URL: "https://github.com/OvictorVieira/elevra.ai/pull/165",
		Title: "feat: add evidence contracts", Body: "Closes #160", Base: "main", HeadSHA: "abc123",
		Files: []string{"src/a.ts"}, Diff: "+const a = 1", Language: "English",
		Instructions: "focus on security",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Closes #160", "git diff origin/main...HEAD", "- src/a.ts",
		"+const a = 1", "focus on security", "Never modify tracked files", "Write titles and bodies in English",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestPromptNeverAsksTheEngineToRunTheProject(t *testing.T) {
	p, err := BuildPrompt(PromptInput{Repo: "o/r", Base: "main", Diff: "+x", Language: "English"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "Verify by running it") || !strings.Contains(p, "Do not install dependencies, build, run tests") {
		t.Error("the review reads the code: it must forbid running the project")
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

func TestPromptCarriesLessonsAndPriorFindings(t *testing.T) {
	out, err := BuildPrompt(PromptInput{
		Repo: "o/r", Language: "English",
		Lessons: []learn.Lesson{{Kind: "avoid", Rule: "Do not flag test helpers in lib.", Why: "only used by tests"}},
		Prior: []learn.Record{{Outcome: learn.Rejected, Priority: 3, Title: "Move helper", Path: "a.dart",
			Notes: []string{"Won't fix. tree-shaken"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"What you have learned", "[avoid] Do not flag test helpers in lib.", "Why: only used by tests",
		"Findings you already made", "[rejected] P3 Move helper (a.dart)", "Reply: Won't fix. tree-shaken"} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestPromptOmitsLearningSectionsWhenThereIsNothingToSay(t *testing.T) {
	out, err := BuildPrompt(PromptInput{Repo: "o/r", Language: "English"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "What you have learned") || strings.Contains(out, "already made") {
		t.Fatal("empty learning sections must not appear")
	}
}
