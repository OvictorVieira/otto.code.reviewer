package picker

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// InputModel is a one-line text screen, used when a CLI lists no models.
type InputModel struct {
	title         string
	field         textinput.Model
	width, height int
	action        Action
}

// NewInput builds the text screen.
func NewInput(title, placeholder string) *InputModel {
	f := textinput.New()
	f.Placeholder = placeholder
	f.Prompt = "› "
	f.Focus()
	return &InputModel{title: title, field: f, width: 100, height: 30}
}

// RunInput shows the text screen and returns what was typed.
func RunInput(title, placeholder string) (string, Action, error) {
	final, err := tea.NewProgram(NewInput(title, placeholder), tea.WithAltScreen()).Run()
	if err != nil {
		return "", Quit, err
	}
	m := final.(*InputModel)
	if m.action == Quit {
		return "", Quit, ErrInterrupted
	}
	return m.Value(), m.action, nil
}

// Value is the typed text.
func (m *InputModel) Value() string { return strings.TrimSpace(m.field.Value()) }

// Action says how the screen ended.
func (m *InputModel) Action() Action { return m.action }

// Init implements tea.Model.
func (m *InputModel) Init() tea.Cmd { return textinput.Blink }

// Update implements tea.Model.
func (m *InputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.action = Quit
			return m, tea.Quit
		case tea.KeyEsc:
			m.action = Back
			return m, tea.Quit
		case tea.KeyEnter:
			if m.Value() != "" {
				m.action = Picked
				return m, tea.Quit
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.field, cmd = m.field.Update(msg)
	return m, cmd
}

// View implements tea.Model.
func (m *InputModel) View() string {
	w := max(46, min(m.width-8, 84)) - 6
	body := "\n" + spread(titleSt.Render(m.title), dimSt.Render("esc"), w) + "\n\n" + m.field.View() + "\n\n" +
		dimSt.Render("enter confirm · esc back")
	box := boxSt.Width(w + 4).Render(body)
	return placeCenter(box, m.width, m.height, max(0, (m.height-9)/2))
}
