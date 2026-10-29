package learn

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

const finding = "**<sub><sub>![P3 Badge](https://img.shields.io/badge/P3-lightgrey?style=flat)</sub></sub>  Move helper out of lib**\n\n" +
	"The helper imports flutter_test.\n\nUseful? React with 👍 / 👎.\n\n<!-- otto-review-finding:v1 -->"

func thread(replies []string, up, down int, resolved bool) Thread {
	cs := []ThreadComment{{ID: 10, Author: "otto[bot]", Body: finding, Up: up, Down: down}}
	for i, r := range replies {
		cs = append(cs, ThreadComment{ID: int64(11 + i), Author: "dev", Body: r})
	}
	return Thread{Path: "pkg/a.dart", Line: 4, Resolved: resolved, Comments: cs}
}

func TestExtractParsesOttoFinding(t *testing.T) {
	recs := Extract("o/r", 7, []Thread{thread(nil, 0, 0, false)}, now)
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.Title != "Move helper out of lib" || r.Priority != 3 || r.Path != "pkg/a.dart" || r.PR != 7 {
		t.Fatalf("bad record: %+v", r)
	}
	if r.Body != "The helper imports flutter_test." {
		t.Fatalf("body kept reaction prompt or marker: %q", r.Body)
	}
}

func TestExtractIgnoresThreadsOttoDidNotStart(t *testing.T) {
	human := Thread{Comments: []ThreadComment{{ID: 1, Body: "please rename this"}}}
	if recs := Extract("o/r", 1, []Thread{human}, now); len(recs) != 0 {
		t.Fatalf("learned from a human thread: %+v", recs)
	}
}

func TestOutcomeInference(t *testing.T) {
	cases := []struct {
		name    string
		replies []string
		up, dn  int
		resolv  bool
		want    Outcome
	}{
		{"thumbs up", nil, 1, 0, false, Accepted},
		{"thumbs down", nil, 0, 1, false, Rejected},
		{"mixed reactions are no signal", nil, 1, 1, false, Open},
		{"fixed reply", []string{"Fixed in 9fb8e9f. seeded"}, 0, 0, false, Accepted},
		{"wont fix reply", []string{"Won't fix. tree-shaken"}, 0, 0, true, Rejected},
		{"reply beats reaction", []string{"Won't fix. no"}, 1, 0, false, Rejected},
		{"resolved only", nil, 0, 0, true, Resolved},
		{"nothing", nil, 0, 0, false, Open},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Extract("o/r", 1, []Thread{thread(c.replies, c.up, c.dn, c.resolv)}, now)[0].Outcome
			if got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestRejectionKeepsTheMaintainersReason(t *testing.T) {
	rec := Extract("o/r", 1, []Thread{thread([]string{"Won't fix. only imported from test/"}, 0, 1, true)}, now)[0]
	if len(rec.Notes) != 1 || !strings.Contains(rec.Notes[0], "only imported from test/") {
		t.Fatalf("reason lost: %+v", rec.Notes)
	}
}

func TestMergeRelearnsWhenOutcomeChanges(t *testing.T) {
	m := Memory{Records: map[string]Record{}}
	rec := Extract("o/r", 1, []Thread{thread(nil, 0, 1, false)}, now)
	if n := m.Merge(rec); n != 1 {
		t.Fatalf("first merge changed %d", n)
	}
	m.MarkLearned(rec)
	if n := m.Merge(rec); n != 0 || len(m.Unlearned()) != 0 {
		t.Fatalf("identical feedback must not trigger learning again (n=%d)", n)
	}
	flipped := Extract("o/r", 1, []Thread{thread([]string{"Fixed in abc. you were right"}, 0, 1, true)}, now)
	if n := m.Merge(flipped); n != 1 || len(m.Unlearned()) != 1 {
		t.Fatalf("a changed outcome must be learned again (n=%d)", n)
	}
}

func TestOpenFindingsAreNotLearnedFromButAreKnownForThePR(t *testing.T) {
	m := Memory{Records: map[string]Record{}}
	m.Merge(Extract("o/r", 5, []Thread{thread(nil, 0, 0, false)}, now))
	if len(m.Unlearned()) != 0 {
		t.Fatal("open finding must not be distilled")
	}
	if len(m.ForPR(5)) != 1 {
		t.Fatal("open finding must still be listed for its pull request")
	}
}

func TestParseDistillKeepsPinnedAndStableIDs(t *testing.T) {
	cur := []Lesson{
		{ID: "aaaa1111", Kind: "avoid", Rule: "Do not flag test helpers in lib.", Evidence: 2},
		{ID: "pinned01", Kind: "style", Rule: "Answer in Portuguese.", Pinned: true},
	}
	out := `noise {"lessons":[
	  {"id":"aaaa1111","kind":"avoid","rule":"Do not flag test helpers in lib.","evidence":3},
	  {"id":"","kind":"check","rule":"Check gain limits on mixer inputs.","evidence":1},
	  {"id":"","kind":"bogus","rule":"","evidence":1}
	]} trailing`
	got, err := ParseDistill(out, cur, now)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]Lesson{}
	for _, l := range got {
		ids[l.ID] = l
	}
	if _, ok := ids["pinned01"]; !ok {
		t.Fatal("pinned lesson dropped")
	}
	if ids["aaaa1111"].Evidence != 3 {
		t.Fatalf("existing lesson not updated: %+v", ids["aaaa1111"])
	}
	if len(got) != 3 {
		t.Fatalf("empty rule must be skipped, got %d lessons", len(got))
	}
	if got[0].ID != "pinned01" {
		t.Fatal("pinned lessons sort first")
	}
}

func TestParseDistillRejectsGarbageAndCapsSize(t *testing.T) {
	if _, err := ParseDistill("no json here", nil, now); err == nil {
		t.Fatal("expected error for non-JSON answer")
	}
	var sb strings.Builder
	sb.WriteString(`{"lessons":[`)
	for i := 0; i < MaxLessons+10; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"kind":"check","rule":"rule number ` + strings.Repeat("x", i+1) + `","evidence":1}`)
	}
	sb.WriteString(`]}`)
	got, err := ParseDistill(sb.String(), nil, now)
	if err != nil || len(got) != MaxLessons {
		t.Fatalf("want %d lessons, got %d (%v)", MaxLessons, len(got), err)
	}
}

func TestDistillPromptCarriesOutcomesAndReasons(t *testing.T) {
	recs := Extract("o/r", 1, []Thread{thread([]string{"Won't fix. only imported from test/"}, 0, 1, true)}, now)
	p, err := DistillPrompt([]Lesson{{ID: "x", Kind: "avoid", Rule: "Existing rule."}}, recs)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Existing rule.", "[rejected]", "Move helper out of lib", "only imported from test/"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestStoreRoundTripPerRepository(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	m, err := s.Load("o/r")
	if err != nil || len(m.Records) != 0 {
		t.Fatalf("fresh load: %v %+v", err, m)
	}
	m.Merge(Extract("o/r", 1, []Thread{thread(nil, 1, 0, false)}, now))
	m.Lessons = []Lesson{{ID: "a", Rule: "r"}}
	if err := s.Save("o/r", m); err != nil {
		t.Fatal(err)
	}
	back, err := s.Load("o/r")
	if err != nil || len(back.Records) != 1 || len(back.Lessons) != 1 {
		t.Fatalf("round trip lost data: %v %+v", err, back)
	}
	other, _ := s.Load("o/other")
	if len(other.Records) != 0 {
		t.Fatal("repositories must not share memory")
	}
}

func lessonsOf(ls ...Lesson) []Lesson { return ls }

func TestSelectLessonsSkipsScopesTheChangeDoesNotTouch(t *testing.T) {
	ls := lessonsOf(
		Lesson{ID: "dart", Kind: "avoid", Rule: "Dart rule.", Scope: "apps/**/*.dart", Evidence: 3},
		Lesson{ID: "web", Kind: "avoid", Rule: "Web rule.", Scope: "apps/web/**", Evidence: 3},
		Lesson{ID: "any", Kind: "check", Rule: "Unscoped rule.", Evidence: 1},
	)
	got := SelectLessons(ls, []string{"apps/live/lib/main.dart"}, "feat", now)
	ids := map[string]bool{}
	for _, l := range got {
		ids[l.ID] = true
	}
	if !ids["dart"] || !ids["any"] || ids["web"] {
		t.Fatalf("wrong selection: %v", ids)
	}
}

func TestSelectLessonsStaysWithinBudgetAndCount(t *testing.T) {
	var ls []Lesson
	for i := 0; i < 40; i++ {
		ls = append(ls, Lesson{ID: string(rune('a' + i)), Kind: "check", Evidence: 2,
			Rule: strings.Repeat("rule text ", 12) + string(rune('a'+i))})
	}
	got := SelectLessons(ls, []string{"x.go"}, "t", now)
	total := 0
	for _, l := range got {
		total += len(l.Rule) + len(l.Why) + len(l.Scope) + 16
	}
	if len(got) > LessonMax || total > LessonBudget {
		t.Fatalf("selected %d lessons, %d chars (limits %d/%d)", len(got), total, LessonMax, LessonBudget)
	}
}

func TestSelectLessonsPrefersPinnedThenRelevantThenEvidence(t *testing.T) {
	ls := lessonsOf(
		Lesson{ID: "strong", Rule: "Generic.", Evidence: 5},
		Lesson{ID: "relevant", Rule: "Mixer gain must not exceed twelve.", Evidence: 1},
		Lesson{ID: "pin", Rule: "Always pinned.", Pinned: true},
	)
	got := SelectLessons(ls, []string{"apps/live/lib/mixer.dart"}, "mixer gain", now)
	if got[0].ID != "pin" || got[1].ID != "relevant" {
		t.Fatalf("order = %s,%s,%s", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestSelectLessonsDropsStaleUnreinforcedButKeepsPinned(t *testing.T) {
	old := now.Add(-200 * 24 * time.Hour)
	ls := lessonsOf(
		Lesson{ID: "stale", Rule: "Old.", Evidence: 1, Updated: old},
		Lesson{ID: "pinned-old", Rule: "Old but pinned.", Evidence: 1, Pinned: true, Updated: old},
		Lesson{ID: "reinforced-old", Rule: "Old but proven.", Evidence: 4, Updated: old},
	)
	got := SelectLessons(ls, nil, "", now)
	ids := map[string]bool{}
	for _, l := range got {
		ids[l.ID] = true
	}
	if ids["stale"] || !ids["pinned-old"] || !ids["reinforced-old"] {
		t.Fatalf("selection = %v", ids)
	}
}

func TestSelectPriorKeepsOnlyRejectedAndOpenAndClipsReasons(t *testing.T) {
	long := strings.Repeat("because ", 80)
	recs := []Record{
		{ID: 1, Outcome: Accepted, Title: "fixed already"},
		{ID: 2, Outcome: Resolved, Title: "resolved"},
		{ID: 3, Outcome: Open, Priority: 1, Title: "still open"},
		{ID: 4, Outcome: Rejected, Priority: 3, Title: "rejected", Notes: []string{long, "second"}},
	}
	got := SelectPrior(recs)
	if len(got) != 2 || got[0].Outcome != Rejected || got[1].Outcome != Open {
		t.Fatalf("got %+v", got)
	}
	if len(got[0].Notes) != 1 || len([]rune(got[0].Notes[0])) > 161 {
		t.Fatalf("reason not clipped: %d", len(got[0].Notes[0]))
	}
}
