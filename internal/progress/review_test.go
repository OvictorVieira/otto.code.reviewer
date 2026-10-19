package progress

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
)

func TestReviewModelShowsCurrentActivityAndClearsOnStop(t *testing.T) {
	model := reviewModel{
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
		status:  "Lendo internal/engine.go…",
		ready:   make(chan struct{}),
	}

	if got := model.View(); !strings.Contains(got, "Lendo internal/engine.go") {
		t.Fatalf("view should show the current activity: %q", got)
	}
	next, _ := model.Update(statusMsg("Analisando pull request…"))
	if got := next.(reviewModel).View(); !strings.Contains(got, "Analisando pull request") {
		t.Errorf("updated view should show the new activity: %q", got)
	}
	next, _ = next.(reviewModel).Update(stopMsg{})
	if got := next.(reviewModel).View(); got != "" {
		t.Errorf("stopped view should be empty, got %q", got)
	}
}

func TestReviewProgressCanStartUpdateAndStop(t *testing.T) {
	display, err := Start(io.Discard)
	if err != nil {
		t.Fatalf("start progress display: %v", err)
	}
	display.Update("Lendo internal/engine.go…")
	display.Stop()
	display.Stop()
}
