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
