package progress

import (
	"errors"
	"io"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type statusMsg string
type stopMsg struct{}

// Review renders the temporary, single-line status for a manual review.
type Review struct {
	program *tea.Program
	done    chan struct{}
	once    sync.Once
}

type reviewModel struct {
	spinner spinner.Model
	status  string
	ready   chan struct{}
	stopped bool
}

func Start(out io.Writer) (*Review, error) {
	done := make(chan struct{})
	ready := make(chan struct{})
	model := reviewModel{
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
		status:  "Analisando pull request…",
		ready:   ready,
	}
	program := tea.NewProgram(model, tea.WithInput(nil), tea.WithOutput(out))
	r := &Review{program: program, done: done}
	errs := make(chan error, 1)
	go func() {
		_, err := program.Run()
		errs <- err
		close(done)
	}()
	select {
	case <-ready:
		return r, nil
	case err := <-errs:
		if err == nil {
			return nil, errors.New("review progress display exited before starting")
		}
		return nil, err
	}
}

func (r *Review) Update(status string) {
	select {
	case <-r.done:
		return
	default:
		r.program.Send(statusMsg(status))
	}
}

func (r *Review) Stop() {
	r.once.Do(func() {
		select {
		case <-r.done:
			return
		default:
			r.program.Send(stopMsg{})
			<-r.done
		}
	})
}

func (m reviewModel) Init() tea.Cmd {
	close(m.ready)
	return m.spinner.Tick
}

func (m reviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case statusMsg:
		m.status = string(msg)
	case stopMsg:
		m.stopped = true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m reviewModel) View() string {
	if m.stopped {
		return ""
	}
	spinnerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	return spinnerStyle.Render(m.spinner.View()) + " " + statusStyle.Render(m.status)
}
