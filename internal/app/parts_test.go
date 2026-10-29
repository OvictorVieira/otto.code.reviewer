package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
)

// bigDiff changes `areas` areas of `perArea` files with 30 lines each.
func bigDiff(areas, perArea int) string {
	var b strings.Builder
	for a := 0; a < areas; a++ {
		for f := 0; f < perArea; f++ {
			p := fmt.Sprintf("mod%d/pkg/f%d.go", a, f)
			fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -0,0 +1,30 @@\n", p, p, p, p)
			for l := 0; l < 30; l++ {
				fmt.Fprintf(&b, "+line %d\n", l)
			}
		}
	}
	return b.String()
}

// byPrompt answers each prompt with what answer returns; it is safe for parts running at once.
type byPrompt struct {
	name    string
	mu      *sync.Mutex
	prompts *[]string
	answer  func(prompt string) (string, error)
}

func (e byPrompt) Name() string { return e.name }
func (e byPrompt) Run(_ context.Context, req engine.Request) (string, error) {
	e.mu.Lock()
	*e.prompts = append(*e.prompts, req.Prompt)
	e.mu.Unlock()
	return e.answer(req.Prompt)
}

func finding(path string, line int, title string) string {
	return fmt.Sprintf(`{"findings":[{"title":"[P2] %s","body":"b","confidence_score":0.9,"priority":2,
"code_location":{"absolute_file_path":"%s","line_range":{"start":%d,"end":%d}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"x","overall_confidence_score":0.8}`, title, path, line, line)
}

func partsHarness(t *testing.T, answer func(string) (string, error)) (*harness, *[]string) {
	t.Helper()
	var prompts []string
	e := byPrompt{name: "claude", mu: &sync.Mutex{}, prompts: &prompts, answer: answer}
	h := newHarness(t)
	h.app.Cfg.Engines = []engine.Spec{{Name: "claude", Model: "sonnet"}}
	h.app.NewEngine = func(engine.Spec) engine.Engine { return e }
	h.host.diff = bigDiff(6, 9) // 54 files, 1620 lines: above the split limits
	return h, &prompts
}

func partNumber(prompt string) int {
	var i, n int
	if k := strings.Index(prompt, "You review part "); k >= 0 {
		fmt.Sscanf(prompt[k:], "You review part %d of %d", &i, &n)
	}
	return i
}

func TestALargePullRequestIsReviewedInPartsPlusAPassAcrossThem(t *testing.T) {
	h, prompts := partsHarness(t, func(p string) (string, error) {
		if strings.Contains(p, "## Integration pass") {
			return finding("mod0/pkg/f0.go", 1, "Contract changed in mod0 not followed in mod5"), nil
		}
		return finding(fmt.Sprintf("mod%d/pkg/f1.go", partNumber(p)-1), 2, fmt.Sprintf("Issue found by part %d", partNumber(p))), nil
	})

	report, err := h.app.Review(context.Background(), repo, 1, Options{})

	if err != nil {
		t.Fatal(err)
	}
	parts, integration := 0, 0
	for _, p := range *prompts {
		switch {
		case strings.Contains(p, "## Integration pass"):
			integration++
			if !strings.Contains(p, "Issue found by part 1") {
				t.Error("the pass across parts must be told what the parts found")
			}
		case strings.Contains(p, "You review part"):
			parts++
			if strings.Count(p, "diff --git") > 15 {
				t.Error("a part must only get its own files' diff")
			}
		}
	}
	if parts < 3 || integration != 1 {
		t.Fatalf("parts = %d, integration passes = %d", parts, integration)
	}
	if report.Findings != parts+1 {
		t.Fatalf("findings = %d, want one per part plus the integration one", report.Findings)
	}
	body := h.host.posted[0].Body
	if !strings.Contains(body, fmt.Sprintf("**Reviewed in:** %d parts", parts)) || strings.Contains(body, "Partial review") {
		t.Fatalf("summary:\n%s", body)
	}
}

func TestAPartNoEngineCouldReviewIsReportedInsteadOfFailingTheReview(t *testing.T) {
	h, _ := partsHarness(t, func(p string) (string, error) {
		if partNumber(p) == 2 {
			return "", errors.New("service unavailable")
		}
		return finding("mod0/pkg/f0.go", 3, "Something"), nil
	})

	report, err := h.app.Review(context.Background(), repo, 1, Options{})

	if err != nil || !report.Posted {
		t.Fatalf("a failed part must not lose the rest of the review: %v %+v", err, report)
	}
	body := h.host.posted[0].Body
	if !strings.Contains(body, "Partial review") || !strings.Contains(body, "mod") {
		t.Fatalf("the unreviewed files must be named:\n%s", body)
	}
}

func TestTheSameProblemFoundTwiceIsPostedOnce(t *testing.T) {
	h, _ := partsHarness(t, func(p string) (string, error) {
		return finding("mod0/pkg/f0.go", 5, "Reserve stock without a lock"), nil
	})

	report, err := h.app.Review(context.Background(), repo, 1, Options{})

	if err != nil || report.Findings != 1 {
		t.Fatalf("findings = %d (err %v), want the duplicate merged", report.Findings, err)
	}
}

func TestWhenEveryPartFailsTheReviewFails(t *testing.T) {
	h, _ := partsHarness(t, func(string) (string, error) { return "", errors.New("service unavailable") })

	if _, err := h.app.Review(context.Background(), repo, 1, Options{}); err == nil {
		t.Fatal("nothing reviewed must be a failure, so the failure comment is posted")
	}
}
