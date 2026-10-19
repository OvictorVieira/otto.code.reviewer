// Package picker is Otto's full-screen, searchable list: type to filter,
// arrows or mouse to move, enter or click to pick.
package picker

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Item is one choice.
type Item struct {
	Value string
	Title string
	Group string // section header
	Tag   string // shown on the right ("Free", a price, "always latest")
	Hint  string // dim text after the title
}

// Options configure one picker.
type Options struct {
	Title    string
	Items    []Item
	Selected string // value marked as current and focused first
	Footer   string // e.g. "399 models · 34 free"
}

// ErrInterrupted is returned when the user presses ctrl+c.
var ErrInterrupted = errors.New("interrupted")

// Layout: blank, title, blank, search, blank, then the list.
const listTop = 5

var (
	accent    = lipgloss.Color("#6366F1")
	titleSt   = lipgloss.NewStyle().Bold(true)
	dimSt     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	headerSt  = lipgloss.NewStyle().Foreground(lipgloss.Color("#818CF8")).Bold(true)
	freeSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	currentSt = lipgloss.NewStyle().Foreground(lipgloss.Color("#818CF8"))
	activeSt  = lipgloss.NewStyle().Background(accent).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
)

type row struct {
	header string
	item   int // index into Options.Items; -1 for a header
}

// Model is the bubbletea model behind the picker.
type Model struct {
	opts      Options
	query     textinput.Model
	matches   []int // item indexes that match the query
	rows      []row
	cursor    int // position in matches
	offset    int // first visible row
	width     int
	height    int
	chosen    string
	picked    bool
	cancelled bool
	interrupt bool
}

// New builds a picker focused on the selected value.
func New(opts Options) *Model {
	q := textinput.New()
	q.Placeholder = "Search"
	q.Prompt = "› "
	q.Focus()
	m := &Model{opts: opts, query: q, width: 80, height: 24}
	m.refilter()
	for i, idx := range m.matches {
		if opts.Items[idx].Value == opts.Selected {
			m.cursor = i
		}
	}
	m.ensureVisible()
	return m
}

// Run shows the picker full screen and returns the picked value.
// ok is false when the user cancelled with esc.
func Run(opts Options) (value string, ok bool, err error) {
	final, err := tea.NewProgram(New(opts), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err != nil {
		return "", false, err
	}
	m := final.(*Model)
	if m.interrupt {
		return "", false, ErrInterrupted
	}
	value, ok = m.Result()
	return value, ok, nil
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return textinput.Blink }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
		return m, nil

	case tea.MouseMsg:
		return m, m.mouse(msg)

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.cancelled, m.interrupt = true, true
			return m, tea.Quit
		case tea.KeyEsc:
			if m.query.Value() != "" {
				m.query.SetValue("")
				m.refilter()
				return m, nil
			}
			m.cancelled = true
			return m, tea.Quit
		case tea.KeyEnter:
			if len(m.matches) == 0 {
				return m, nil
			}
			m.pick()
			return m, tea.Quit
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyCtrlK:
			m.move(-1)
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyCtrlJ, tea.KeyTab:
			m.move(1)
			return m, nil
		case tea.KeyPgUp:
			m.move(-m.listHeight())
			return m, nil
		case tea.KeyPgDown:
			m.move(m.listHeight())
			return m, nil
		case tea.KeyHome:
			m.move(-len(m.matches))
			return m, nil
		case tea.KeyEnd:
			m.move(len(m.matches))
			return m, nil
		}
	}

	before := m.query.Value()
	var cmd tea.Cmd
	m.query, cmd = m.query.Update(msg)
	if m.query.Value() != before {
		m.refilter()
	}
	return m, cmd
}

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scroll(-3)
		return nil
	case tea.MouseButtonWheelDown:
		m.scroll(3)
		return nil
	}
	r := msg.Y - listTop + m.offset
	if msg.Y < listTop || msg.Y >= listTop+m.listHeight() || r < 0 || r >= len(m.rows) || m.rows[r].item < 0 {
		return nil
	}
	for i, idx := range m.matches {
		if idx == m.rows[r].item {
			m.cursor = i
		}
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		m.pick()
		return tea.Quit
	}
	return nil
}

func (m *Model) pick() {
	m.chosen, m.picked = m.opts.Items[m.matches[m.cursor]].Value, true
}

func (m *Model) move(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.matches)-1, m.cursor+delta))
	m.ensureVisible()
}

func (m *Model) scroll(delta int) {
	m.offset = max(0, min(max(0, len(m.rows)-m.listHeight()), m.offset+delta))
}

func (m *Model) refilter() {
	terms := strings.Fields(strings.ToLower(m.query.Value()))
	m.matches = m.matches[:0]
	for i, it := range m.opts.Items {
		hay := strings.ToLower(strings.Join([]string{it.Title, it.Group, it.Tag, it.Value, it.Hint}, " "))
		ok := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			m.matches = append(m.matches, i)
		}
	}
	m.rows = m.rows[:0]
	prev := "\x00"
	for _, idx := range m.matches {
		if g := m.opts.Items[idx].Group; g != prev {
			if g != "" {
				m.rows = append(m.rows, row{header: g, item: -1})
			}
			prev = g
		}
		m.rows = append(m.rows, row{item: idx})
	}
	m.cursor, m.offset = 0, 0
	m.ensureVisible()
}

func (m *Model) cursorRow() int {
	if len(m.matches) == 0 {
		return -1
	}
	want := m.matches[m.cursor]
	for r, rw := range m.rows {
		if rw.item == want {
			return r
		}
	}
	return -1
}

func (m *Model) listHeight() int { return max(3, m.height-listTop-3) }

func (m *Model) ensureVisible() {
	r := m.cursorRow()
	if r < 0 {
		return
	}
	if r > 0 && m.rows[r-1].item < 0 {
		r-- // keep the section header with its first item
	}
	if r < m.offset {
		m.offset = r
	}
	if cr := m.cursorRow(); cr >= m.offset+m.listHeight() {
		m.offset = cr - m.listHeight() + 1
	}
}

// Current is the item under the cursor.
func (m *Model) Current() Item {
	if len(m.matches) == 0 {
		return Item{}
	}
	return m.opts.Items[m.matches[m.cursor]]
}

// Visible lists the items that match the search.
func (m *Model) Visible() []Item {
	out := make([]Item, 0, len(m.matches))
	for _, idx := range m.matches {
		out = append(out, m.opts.Items[idx])
	}
	return out
}

// Result is the picked value; ok is false if nothing was picked.
func (m *Model) Result() (string, bool) { return m.chosen, m.picked }

// Cancelled reports whether the user left with esc.
func (m *Model) Cancelled() bool { return m.cancelled }

// RowOf is the screen line of an item, or -1 when it is not on screen.
func (m *Model) RowOf(value string) int {
	for r := m.offset; r < len(m.rows) && r < m.offset+m.listHeight(); r++ {
		if it := m.rows[r].item; it >= 0 && m.opts.Items[it].Value == value {
			return listTop + r - m.offset
		}
	}
	return -1
}

// View implements tea.Model.
func (m *Model) View() string {
	w := max(40, min(m.width-4, 100))
	pad := "  "
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(pad + spread(titleSt.Render(m.opts.Title), dimSt.Render("esc"), w) + "\n\n")
	b.WriteString(pad + m.query.View() + "\n\n")

	cr := m.cursorRow()
	shown := 0
	for r := m.offset; r < len(m.rows) && shown < m.listHeight(); r++ {
		shown++
		rw := m.rows[r]
		if rw.item < 0 {
			b.WriteString(pad + headerSt.Render(rw.header) + "\n")
			continue
		}
		it := m.opts.Items[rw.item]
		marker := "  "
		if it.Value == m.opts.Selected {
			marker = "● "
		}
		if r == cr {
			b.WriteString(pad + activeSt.Render(plainLine(marker, it, w)) + "\n")
			continue
		}
		title := it.Title
		if it.Hint != "" {
			title += " " + dimSt.Render(it.Hint)
		}
		tag := dimSt.Render(it.Tag)
		if strings.EqualFold(it.Tag, "free") {
			tag = freeSt.Render(it.Tag)
		}
		b.WriteString(pad + spread(currentSt.Render(marker)+title, tag, w) + "\n")
	}
	if len(m.matches) == 0 {
		b.WriteString(pad + dimSt.Render(fmt.Sprintf("No match for %q", m.query.Value())) + "\n")
		shown++
	}
	for ; shown < m.listHeight(); shown++ {
		b.WriteString("\n")
	}

	help := "↑↓ move · enter or click to pick · type to search · esc cancel"
	b.WriteString("\n" + pad + spread(dimSt.Render(help), dimSt.Render(m.opts.Footer), w))
	return b.String()
}

// plainLine is an unstyled row, so the highlight bar is one solid color.
func plainLine(marker string, it Item, w int) string {
	left := marker + it.Title
	if it.Hint != "" {
		left += " " + it.Hint
	}
	return spread(left, it.Tag, w)
}

// spread places left and right text at the edges of a w-wide line.
func spread(left, right string, w int) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if lw+rw+1 > w {
		left = truncate(left, max(1, w-rw-2))
		lw = lipgloss.Width(left)
	}
	return left + strings.Repeat(" ", max(1, w-lw-rw)) + right
}

func truncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > n-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
