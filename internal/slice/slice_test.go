package slice

import (
	"fmt"
	"strings"
	"testing"

	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

func area_(dir string, n, lines int) []diff.FileStat {
	var out []diff.FileStat
	for i := 0; i < n; i++ {
		out = append(out, diff.FileStat{Path: fmt.Sprintf("%s/f%d.go", dir, i), Added: lines})
	}
	return out
}

func TestASmallPullRequestIsOnePart(t *testing.T) {
	if parts := Plan(area_("internal/stock", 10, 20)); len(parts) != 1 || len(parts[0]) != 10 {
		t.Fatalf("parts = %v", parts)
	}
}

func TestALargePullRequestIsSplitIntoPartsWithinTheLimits(t *testing.T) {
	var stats []diff.FileStat
	for i := 0; i < 6; i++ {
		stats = append(stats, area_(fmt.Sprintf("mod%d/pkg", i), 9, 30)...) // 54 files, 1620 lines
	}
	parts := Plan(stats)
	if len(parts) < 3 {
		t.Fatalf("got %d parts", len(parts))
	}
	seen := map[string]int{}
	for _, p := range parts {
		if len(p) > PartFiles {
			t.Fatalf("a part has %d files", len(p))
		}
		lines := 0
		for range p {
			lines += 30
		}
		if lines > PartLines {
			t.Fatalf("a part has %d lines", lines)
		}
		for _, f := range p {
			seen[f]++
		}
	}
	if len(seen) != len(stats) {
		t.Fatalf("every file must be reviewed exactly once: %d of %d", len(seen), len(stats))
	}
	for f, n := range seen {
		if n != 1 {
			t.Fatalf("%s reviewed %d times", f, n)
		}
	}
}

func TestCodeAndItsTestsStayInTheSamePart(t *testing.T) {
	var stats []diff.FileStat
	stats = append(stats, area_("apps/web", 30, 40)...)
	stats = append(stats,
		diff.FileStat{Path: "apps/live/lib/mixer.dart", Added: 50},
		diff.FileStat{Path: "apps/live/test/mixer_test.dart", Added: 50},
	)
	for _, p := range Plan(stats) {
		joined := strings.Join(p, " ")
		if strings.Contains(joined, "apps/live/lib/mixer.dart") != strings.Contains(joined, "apps/live/test/mixer_test.dart") {
			t.Fatalf("mixer and its test were split: %v", p)
		}
	}
}

func TestSubDiffKeepsOnlyThePartsFiles(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+a\ndiff --git a/b.go b/b.go\n+++ b/b.go\n@@ -1 +1 @@\n+b\n"
	got := SubDiff(raw, []string{"b.go"})
	if strings.Contains(got, "a.go") || !strings.Contains(got, "+b") {
		t.Fatalf("got %q", got)
	}
}

func TestMergeKeepsOneCopyOfTheSameFindingAtTheHigherPriority(t *testing.T) {
	a := review.Result{Correct: true, Confidence: 0.9, Findings: []review.Finding{
		{Path: "x.go", Start: 10, End: 12, Priority: 2, Title: "Reserve stock without a lock"},
	}}
	b := review.Result{Correct: false, Confidence: 0.7, Explanation: "Race.", Findings: []review.Finding{
		{Path: "x.go", Start: 11, End: 11, Priority: 1, Title: "Reserve stock atomically: no lock"},
		{Path: "y.go", Start: 3, End: 3, Priority: 2, Title: "Ignore write errors"},
	}}
	m := Merge([]review.Result{a, b})
	if len(m.Findings) != 2 {
		t.Fatalf("findings = %+v", m.Findings)
	}
	if m.Findings[0].Priority != 1 {
		t.Fatalf("the duplicate must keep the higher priority: %+v", m.Findings[0])
	}
	if m.Correct || m.Confidence != 0.7 || m.Explanation != "Race." {
		t.Fatalf("verdict = %+v", m)
	}
}

func TestDifferentProblemsOnTheSameLinesAreBothKept(t *testing.T) {
	r := review.Result{Findings: []review.Finding{
		{Path: "x.go", Start: 10, End: 10, Title: "Division by zero when total is 0"},
		{Path: "x.go", Start: 10, End: 10, Title: "Ignored error from WriteFile"},
	}}
	if m := Merge([]review.Result{r}); len(m.Findings) != 2 {
		t.Fatalf("findings = %+v", m.Findings)
	}
}
