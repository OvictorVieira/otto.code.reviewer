package trigger

import (
	"path/filepath"
	"testing"
	"time"
)

func comment(id int64, author, body, url string) Comment {
	return Comment{ID: id, Repo: "OvictorVieira/elevra.ai", Author: author, Body: body, HTMLURL: url}
}

func TestPendingFindsTriggerOnPullRequestsFromAllowedAuthors(t *testing.T) {
	comments := []Comment{
		comment(1, "OvictorVieira", "/otto review", "https://github.com/OvictorVieira/elevra.ai/pull/165#issuecomment-1"),
		comment(2, "OvictorVieira", "  /OTTO Review  focus on security\nthanks", "https://github.com/OvictorVieira/elevra.ai/pull/166#issuecomment-2"),
		comment(3, "stranger", "/otto review", "https://github.com/OvictorVieira/elevra.ai/pull/165#issuecomment-3"),
		comment(4, "OvictorVieira", "/otto review", "https://github.com/OvictorVieira/elevra.ai/issues/9#issuecomment-4"),
		comment(5, "OvictorVieira", "please /otto review later", "https://github.com/OvictorVieira/elevra.ai/pull/165#issuecomment-5"),
		comment(6, "OvictorVieira", "/otto reviewer", "https://github.com/OvictorVieira/elevra.ai/pull/165#issuecomment-6"),
	}

	got := Pending(comments, "/otto review", []string{"ovictorvieira"}, func(int64) bool { return false })

	if len(got) != 2 {
		t.Fatalf("pending = %+v, want comments 1 and 2", got)
	}
	if got[0].CommentID != 1 || got[0].PR != 165 || got[0].Repo != "OvictorVieira/elevra.ai" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].CommentID != 2 || got[1].PR != 166 || got[1].Instructions != "focus on security" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestPendingSkipsAlreadyProcessed(t *testing.T) {
	comments := []Comment{
		comment(1, "me", "/otto review", "https://github.com/o/r/pull/1#issuecomment-1"),
		comment(2, "me", "/otto review", "https://github.com/o/r/pull/1#issuecomment-2"),
	}
	done := map[int64]bool{1: true}
	got := Pending(comments, "/otto review", []string{"me"}, func(id int64) bool { return done[id] })
	if len(got) != 1 || got[0].CommentID != 2 {
		t.Errorf("pending = %+v", got)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	s, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s.MarkDone(42, now)
	s.SetCursor("o/r", now)
	s.Rotation = 2
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Done(42) || reloaded.Done(43) {
		t.Error("processed comments must survive a restart")
	}
	if !reloaded.Cursor("o/r", time.Time{}).Equal(now) || reloaded.Rotation != 2 {
		t.Errorf("state = %+v", reloaded)
	}
}

func TestCursorFallsBackToLookbackForNewRepos(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	fallback := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if got := s.Cursor("new/repo", fallback); !got.Equal(fallback) {
		t.Errorf("cursor = %v, want fallback %v", got, fallback)
	}
}

func TestPruneForgetsOldEntries(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.MarkDone(1, now.AddDate(0, 0, -40))
	s.MarkDone(2, now.AddDate(0, 0, -1))
	s.Prune(now, 30*24*time.Hour)
	if s.Done(1) || !s.Done(2) {
		t.Errorf("prune kept the wrong entries: %+v", s.Processed)
	}
}

func TestTriggerOnItsOwnLineAfterOtherText(t *testing.T) {
	url := "https://github.com/OvictorVieira/elevra.ai/pull/165#issuecomment-1"
	cases := []struct {
		name         string
		body         string
		want         bool
		instructions string
	}{
		{"after a paragraph", "Fixed both findings.\n\n/otto review", true, ""},
		{"with instructions on a later line", "Done.\n/otto review focus on the retry\nthanks", true, "focus on the retry"},
		{"with Windows line endings", "Done.\r\n/otto review\r\n", true, ""},
		{"first matching line wins", "/otto review one\n/otto review two", true, "one"},
		{"inside a fenced code block", "Run this:\n```\n/otto review\n```", false, ""},
		{"inside a fenced block with a language", "```bash\n/otto review\n```\nthanks", false, ""},
		{"quoted from another comment", "> /otto review\nagreed", false, ""},
		{"mentioned in the middle of a sentence", "Fixed. Please /otto review again", false, ""},
		{"similar word on a later line", "Done.\n/otto reviewer", false, ""},
		{"after a closed code block", "```\ncode\n```\n/otto review", true, ""},
		{"text on the lines below a bare trigger", "/otto review\nFoque em concorrência.\nE em erros ignorados.", true, "Foque em concorrência.\nE em erros ignorados."},
		{"followed by a colon", "/otto review: foco em segurança", true, "foco em segurança"},
		{"in bold", "**/otto review**", true, ""},
		{"as inline code with text below", "`/otto review`\n\nRevise o fallback.", true, "Revise o fallback."},
		{"a long multi-paragraph request", "/otto review\n\nContexto longo.\n\nMais contexto.", true, "Contexto longo.\n\nMais contexto."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Pending([]Comment{comment(1, "me", c.body, url)}, "/otto review", []string{"me"}, func(int64) bool { return false })
			if (len(got) == 1) != c.want {
				t.Fatalf("pending = %+v, want match=%v", got, c.want)
			}
			if c.want && got[0].Instructions != c.instructions {
				t.Errorf("instructions = %q, want %q", got[0].Instructions, c.instructions)
			}
		})
	}
}
