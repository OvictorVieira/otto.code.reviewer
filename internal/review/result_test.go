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

// modelJSON is the review a smaller model (DeepSeek flash) really produced: valid JSON, but with
// text where Otto expects numbers and objects, and its own names for the spec checks.
const modelJSON = `Some summary text before the answer.

` + "```json" + `
{
  "findings": [
    {
      "title": "[P2] Starting the conversation while the question plays does not pause the microphone",
      "body": "startConversation starts recognition without pausing the question audio.",
      "confidence_score": 0.75,
      "priority": "P2",
      "code_location": {
        "absolute_file_path": "/tmp/otto/wt-7/apps/web/src/components/InterviewPanel.tsx",
        "line_range": "245-269"
      }
    }
  ],
  "verification": [{"command": "npm test", "result": "skipped", "note": "node_modules absent"}],
  "spec_checks": [
    {"spec_item": "End of turn after 2s of silence", "status": "pass", "explanation": "lastTextAt is rearmed on any text."},
    {"spec_item": "Mic paused while the interviewer speaks", "status": "partial", "explanation": "Missing on start."},
    {"spec_item": "Filler never triggers", "status": "fail", "explanation": "Not handled."}
  ],
  "overall_correctness": "patch is correct",
  "overall_explanation": "Two behavior defects found.",
  "overall_confidence_score": 0.63
}
` + "```" + `
`

func TestParseAcceptsTheFormatVariationsOfSmallerModels(t *testing.T) {
	r, err := ParseResult(modelJSON, "/tmp/otto/wt-7")
	if err != nil {
		t.Fatalf("a valid review with text priority, a range string and its own spec check names was thrown away: %v", err)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %d, want the finding kept", len(r.Findings))
	}
	f := r.Findings[0]
	if f.Priority != 2 || f.Path != "apps/web/src/components/InterviewPanel.tsx" || f.Start != 245 || f.End != 269 || f.Confidence != 0.75 {
		t.Errorf("finding = %+v, want priority 2, the relative path and lines 245-269", f)
	}
	if f.Title != "Starting the conversation while the question plays does not pause the microphone" {
		t.Errorf("title = %q, want the [P2] tag stripped", f.Title)
	}
	want := []SpecCheck{
		{Requirement: "End of turn after 2s of silence", Status: "met", Note: "lastTextAt is rearmed on any text."},
		{Requirement: "Mic paused while the interviewer speaks", Status: "partial", Note: "Missing on start."},
		{Requirement: "Filler never triggers", Status: "wrong", Note: "Not handled."},
	}
	if len(r.Spec) != len(want) {
		t.Fatalf("spec checks = %+v", r.Spec)
	}
	for i := range want {
		if r.Spec[i] != want[i] {
			t.Errorf("spec check %d = %+v, want %+v", i, r.Spec[i], want[i])
		}
	}
	if len(r.Verification) != 1 || r.Verification[0].Result != "skipped" {
		t.Errorf("verification = %+v", r.Verification)
	}
}

func TestParseToleratesOtherCommonShapesOfNumbersAndRanges(t *testing.T) {
	cases := map[string]struct {
		priority, rng, confidence string
		wantPriority, start, end  int
	}{
		"priority as numeric text":    {`"1"`, `{"start": 5, "end": 7}`, `0.9`, 1, 5, 7},
		"lowercase tag":               {`"p0"`, `{"start": 5}`, `0.9`, 0, 5, 5},
		"single line as a number":     {`2`, `12`, `0.9`, 2, 12, 12},
		"single line as text":         {`2`, `"12"`, `0.9`, 2, 12, 12},
		"range with an en dash":       {`2`, `"3–9"`, `0.9`, 2, 3, 9},
		"range with L prefixes":       {`2`, `"L3-L9"`, `0.9`, 2, 3, 9},
		"confidence as text":          {`2`, `{"start": 1, "end": 1}`, `"0.8"`, 2, 1, 1},
		"unknown priority keeps tag":  {`"high"`, `{"start": 1, "end": 1}`, `0.9`, 2, 1, 1},
		"missing range keeps finding": {`2`, `null`, `0.9`, 2, 0, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			json := `{"findings": [{"title": "[P2] Something", "body": "b", "confidence_score": ` + c.confidence + `, "priority": ` + c.priority + `, "code_location": {"absolute_file_path": "/r/a.go", "line_range": ` + c.rng + `}}], "overall_correctness": "patch is incorrect", "overall_explanation": "e", "overall_confidence_score": 0.5}`
			r, err := ParseResult(json, "/r")
			if err != nil || len(r.Findings) != 1 {
				t.Fatalf("err = %v, findings = %v, want the finding kept", err, r.Findings)
			}
			f := r.Findings[0]
			if f.Start != c.start || f.End != c.end {
				t.Errorf("lines = %d-%d, want %d-%d", f.Start, f.End, c.start, c.end)
			}
			if f.Priority != c.wantPriority && name != "unknown priority keeps tag" {
				t.Errorf("priority = %d, want %d", f.Priority, c.wantPriority)
			}
			if f.Confidence == 0 {
				t.Errorf("confidence = 0, want it read from %s", c.confidence)
			}
		})
	}
}

func TestParseStillRejectsAnObjectThatIsNotAReview(t *testing.T) {
	for _, out := range []string{`{"findings": "none"}`, `{"findings": 3}`, `{"other": []}`} {
		if _, err := ParseResult(out, "/r"); err == nil {
			t.Errorf("ParseResult(%q) accepted something that is not a review", out)
		}
	}
}
