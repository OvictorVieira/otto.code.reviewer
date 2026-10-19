package review

import (
	"strings"
	"testing"

	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
)

const engineJSON = `{
  "findings": [
    {
      "title": "[P1] Reserve stock atomically",
      "body": "Two concurrent orders can both read saldo=1 and both succeed.",
      "confidence_score": 0.8,
      "priority": 1,
      "code_location": {
        "absolute_file_path": "/tmp/otto/wt-42/src/stock.go",
        "line_range": {"start": 11, "end": 12}
      }
    }
  ],
  "overall_correctness": "patch is incorrect",
  "overall_explanation": "Concurrent reservations can oversell.",
  "overall_confidence_score": 0.7
}`

func TestParseAcceptsRawJSON(t *testing.T) {
	r, err := ParseResult(engineJSON, "/tmp/otto/wt-42")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(r.Findings))
	}
	f := r.Findings[0]
	if f.Title != "Reserve stock atomically" {
		t.Errorf("title = %q, want tag stripped", f.Title)
	}
	if f.Priority != 1 || f.Path != "src/stock.go" || f.Start != 11 || f.End != 12 {
		t.Errorf("finding = %+v", f)
	}
	if r.Correct {
		t.Error("verdict should be incorrect")
	}
	if r.Explanation != "Concurrent reservations can oversell." {
		t.Errorf("explanation = %q", r.Explanation)
	}
}

func TestParseFindsJSONInsideNoisyOutput(t *testing.T) {
	noisy := "I looked at the diff.\n```json\n" + engineJSON + "\n```\nDone."
	r, err := ParseResult(noisy, "/tmp/otto/wt-42")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(r.Findings))
	}
}

func TestParseUsesTitleTagWhenPriorityMissing(t *testing.T) {
	out := `{"findings":[{"title":"[P0] Leak secret","body":"b","confidence_score":0.9,
	"code_location":{"absolute_file_path":"a.go","line_range":{"start":3,"end":3}}}],
	"overall_correctness":"patch is incorrect","overall_explanation":"x","overall_confidence_score":0.9}`
	r, err := ParseResult(out, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if r.Findings[0].Priority != 0 {
		t.Errorf("priority = %d, want 0 from [P0] tag", r.Findings[0].Priority)
	}
}

func TestParseNoFindingsMeansCorrect(t *testing.T) {
	out := `{"findings":[],"overall_correctness":"patch is correct","overall_explanation":"ok","overall_confidence_score":0.9}`
	r, err := ParseResult(out, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Correct || len(r.Findings) != 0 {
		t.Errorf("result = %+v", r)
	}
}

func TestParseRejectsOutputWithoutResult(t *testing.T) {
	if _, err := ParseResult("I could not review this.", "/repo"); err == nil {
		t.Fatal("expected an error for output without a review JSON")
	}
}

func TestPlaceSplitsInlineAndOutsideDiff(t *testing.T) {
	d := diff.Parse(`diff --git a/src/stock.go b/src/stock.go
--- a/src/stock.go
+++ b/src/stock.go
@@ -10,2 +10,3 @@
 a
+b
+c
`)
	r := Result{Findings: []Finding{
		{Title: "inside range", Priority: 2, Confidence: 0.9, Path: "src/stock.go", Start: 10, End: 12},
		{Title: "start outside", Priority: 1, Confidence: 0.9, Path: "src/stock.go", Start: 5, End: 11},
		{Title: "outside", Priority: 3, Confidence: 0.9, Path: "src/other.go", Start: 1, End: 1},
		{Title: "low confidence", Priority: 0, Confidence: 0.1, Path: "src/stock.go", Start: 10, End: 10},
	}}

	p := Place(r, d, 0.3)

	if len(p.Inline) != 2 {
		t.Fatalf("inline = %+v", p.Inline)
	}
	if p.Inline[0].Title != "start outside" || p.Inline[0].Start != 11 || p.Inline[0].End != 11 {
		t.Errorf("first inline should be the P1 collapsed to line 11, got %+v", p.Inline[0])
	}
	if p.Inline[1].Title != "inside range" || p.Inline[1].Start != 10 || p.Inline[1].End != 12 {
		t.Errorf("second inline = %+v", p.Inline[1])
	}
	if len(p.Outside) != 1 || p.Outside[0].Title != "outside" {
		t.Errorf("outside = %+v", p.Outside)
	}
	if !strings.Contains(p.Dropped[0].Title, "low confidence") {
		t.Errorf("dropped = %+v", p.Dropped)
	}
}

func TestParseReadsVerificationAndSpecChecks(t *testing.T) {
	out := `{"findings":[],
	"verification":[{"command":"go test ./...","result":"passed","note":"42 tests"},{"command":"npm run e2e","result":"skipped","note":"needs secrets"}],
	"spec_checks":[{"requirement":"Reject duplicated cards","status":"partial","note":"only by number"}],
	"overall_correctness":"patch is correct","overall_explanation":"ok","overall_confidence_score":0.8}`
	r, err := ParseResult(out, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Verification) != 2 || r.Verification[0].Command != "go test ./..." || r.Verification[1].Result != "skipped" {
		t.Errorf("verification = %+v", r.Verification)
	}
	if len(r.Spec) != 1 || r.Spec[0].Status != "partial" {
		t.Errorf("spec = %+v", r.Spec)
	}
}
