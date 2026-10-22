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

// Values is the typed text, so a text screen can stand in for a list.
func (m *InputModel) Values() []string { return []string{m.Value()} }

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

// NoticeModel shows a message and waits for enter. With a link it also offers to open or copy it
// and, when asked to, to continue without acting on it.
type NoticeModel struct {
	title, body   string
	width, height int
	action        Action
	values        []string

	url, primary, skip string
	status             string
}

// NewNotice builds a message screen.
func NewNotice(title, body string) *NoticeModel {
	return &NoticeModel{title: title, body: body, width: 100, height: 30}
}

// NewLinkNotice builds a message screen about a link the person has to follow. Enter runs the
// primary action ("check again"), o opens the link, c copies it, s skips ("continue anyway"; empty
// hides it) and esc goes back.
func NewLinkNotice(title, body, url, primary, skip string) *NoticeModel {
	n := NewNotice(title, body)
	n.url, n.primary, n.skip = url, primary, skip
	return n
}

// Init implements tea.Model.
func (m *NoticeModel) Init() tea.Cmd { return nil }

// Action says how the screen ended.
func (m *NoticeModel) Action() Action { return m.action }

// Values is empty for enter, and ["skip"] when the person chose to continue anyway.
func (m *NoticeModel) Values() []string { return m.values }

// Update implements tea.Model.
func (m *NoticeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.action = Quit
			return m, tea.Quit
		case tea.KeyEsc:
			m.action = Back
			return m, tea.Quit
		case tea.KeyEnter:
			m.action = Picked
			return m, tea.Quit
		case tea.KeyRunes:
			if m.url == "" || len(msg.Runes) != 1 {
				return m, nil
			}
			switch msg.Runes[0] {
			case 'o':
				if err := OpenURL(m.url); err != nil {
					m.status = "Could not open the browser. Copy the link above (c) or type it."
				} else {
					m.status = "Opened in your browser."
				}
			case 'c':
				if err := CopyText(m.url); err != nil {
					m.status = "Could not copy. Select the link above or type it."
				} else {
					m.status = "Link copied."
				}
			case 's':
				if m.skip != "" {
					m.values = []string{"skip"}
					m.action = Picked
					return m, tea.Quit
				}
			}
		}
	}
	return m, nil
}

// View implements tea.Model.
func (m *NoticeModel) View() string {
	w := max(46, min(m.width-8, 84)) - 6
	body := "\n" + titleSt.Render(m.title) + "\n\n" + wrap(m.body, w)
	keys := "enter continue · esc back"
	if m.url != "" {
		body += "\n\n" + Link(m.url, m.url)
		keys = "enter " + m.primary + " · o open · c copy"
		if m.skip != "" {
			keys += " · s " + m.skip
		}
		keys += " · esc back"
		if m.status != "" {
			body += "\n\n" + m.status
		}
	}
	body += "\n\n" + dimSt.Render(keys)
	box := boxSt.Width(w + 4).Render(body)
	return placeCenter(box, m.width, m.height, max(0, (m.height-strings.Count(body, "\n")-4)/2))
}

func wrap(s string, w int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		for len([]rune(line)) > w {
			cut := strings.LastIndex(string([]rune(line)[:w]), " ")
			if cut <= 0 {
				cut = w
			}
			out = append(out, string([]rune(line)[:cut]))
			line = strings.TrimSpace(string([]rune(line)[cut:]))
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// Frame draws a centered message box, for screens that wait on something.
func Frame(text string, width, height int) string {
	box := boxSt.Padding(1, 3).Render(text)
	return placeCenter(box, width, height, max(0, (height-5)/2))
}
