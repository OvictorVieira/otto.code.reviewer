package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/review"
)

type scripted map[string]string // model -> answer

type fake struct {
	model  string
	answer string
	delay  time.Duration
}

func (f fake) Name() string { return "opencode" }
func (f fake) Run(ctx context.Context, req engine.Request) (string, error) {
	if !strings.Contains(req.Prompt, "Percent") || !strings.Contains(req.Prompt, "UNTRUSTED") {
		return "", errors.New("the eval must use Otto's real review prompt with the fixture")
	}
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if f.answer == "" {
		return "", errors.New("service unavailable")
	}
	return f.answer, nil
}

const good = `{"findings":[
{"title":"[P1] Reserve is not atomic","body":"Check-then-act race: two handlers can oversell; Release takes the mutex but Reserve does not.","confidence_score":0.9,"priority":1,"code_location":{"absolute_file_path":"stock/reserve.go","line_range":{"start":12,"end":16}}},
{"title":"[P2] Snapshot ignores errors","body":"json.Marshal and os.WriteFile errors are discarded.","confidence_score":0.9,"priority":2,"code_location":{"absolute_file_path":"stock/reserve.go","line_range":{"start":29,"end":30}}},
{"title":"[P2] Percent divides by zero","body":"total == 0 panics.","confidence_score":0.9,"priority":2,"code_location":{"absolute_file_path":"stock/reserve.go","line_range":{"start":35,"end":35}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"x","overall_confidence_score":0.9}`

const oneBug = `{"findings":[{"title":"[P2] Percent divides by zero","body":"total == 0 panics.","confidence_score":0.9,"priority":2,"code_location":{"absolute_file_path":"stock/reserve.go","line_range":{"start":35,"end":35}}}],
"overall_correctness":"patch is incorrect","overall_explanation":"x","overall_confidence_score":0.9}`

func TestEvalScoresModelsOnThePlantedBugsAndRanksThePassingOnesFirst(t *testing.T) {
	answers := map[string]fake{
		"m/good":   {answer: good},
		"m/weak":   {answer: oneBug},
		"m/broken": {answer: "I will now write the JSON. Shall I proceed?"},
		"m/down":   {},
	}
	rs, err := Run(context.Background(), "opencode", []string{"m/weak", "m/broken", "m/good", "m/down"}, Options{
		Timeout:   5 * time.Second,
		NewEngine: func(s engine.Spec) engine.Engine { f := answers[s.Model]; f.model = s.Model; return f },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Model != "m/good" || !rs[0].Passed() || len(rs[0].Found) != 3 {
		t.Fatalf("first = %+v", rs[0])
	}
	for _, r := range rs[1:] {
		if r.Passed() {
			t.Errorf("%s must not pass: %+v", r.Model, r)
		}
	}
	table := Table(rs)
	if !strings.Contains(table, "m/good") || !strings.Contains(table, "3/3") {
		t.Fatalf("table:\n%s", table)
	}
}

func TestAModelThatIsTooSlowDoesNotPass(t *testing.T) {
	r := Result{Valid: true, Found: []string{"race", "division by zero"}, Seconds: (MaxTime + time.Second).Seconds()}
	if r.Passed() {
		t.Fatal("a model slower than the limit must not be recommended")
	}
}

func TestRecommendationRequiresEveryPlantedBug(t *testing.T) {
	r := Result{Valid: true, Found: []string{"race", "division by zero"}, Seconds: 1}
	if r.Passed() {
		t.Fatal("a model that misses one planted bug must not be recommended")
	}
}

func TestScoreMatchesEachPlantedBugOnce(t *testing.T) {
	res, err := review.ParseResult(good, "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := Score(res); len(got) != 3 {
		t.Fatalf("found = %v", got)
	}
}

func TestScoreDoesNotCreditKeywordsAtTheWrongLocation(t *testing.T) {
	res := review.Result{Findings: []review.Finding{
		{Path: "stock/reserve.go", Start: 12, End: 12, Title: "Race, ignored error, division by zero"},
		{Path: "other.go", Start: 34, End: 34, Title: "Percent divides by zero"},
	}}
	got := Score(res)
	if len(got) != 1 || got[0] != "race" {
		t.Fatalf("wrongly credited bugs at unrelated locations: %v", got)
	}
}
