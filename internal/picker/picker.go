// Package picker is Otto's setup screen: a centered box with a search field
// and a grouped list. Type to filter, arrows or mouse to move, enter or click
// to pick, esc to go back.
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

// Options configure one screen.
type Options struct {
	Title      string
	Items      []Item
	Selected   string   // single mode: the current value, marked and focused first
	Multi      bool     // several choices, toggled with space or tab
	Checked    []string // multi mode: values checked at the start
	AllowEmpty bool     // multi mode: enter may confirm with nothing checked
	Footer     string   // e.g. "399 models · 34 free"
}

// Action says how the screen ended.
type Action int

// How a screen can end.
const (
	Waiting Action = iota
	Picked
	Back // esc
	Quit // ctrl+c
)

// ErrInterrupted is returned by Run when the user presses ctrl+c.
var ErrInterrupted = errors.New("interrupted")

var (
	accent    = lipgloss.Color("#6366F1")
	titleSt   = lipgloss.NewStyle().Bold(true)
	dimSt     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	headerSt  = lipgloss.NewStyle().Foreground(lipgloss.Color("#818CF8")).Bold(true)
	freeSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	currentSt = lipgloss.NewStyle().Foreground(lipgloss.Color("#818CF8"))
	activeSt  = lipgloss.NewStyle().Background(accent).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	boxSt     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#4B5563")).Padding(0, 2)
)

// Lines of the box above the list: blank, title, blank, search, blank.
const linesAboveList = 5

type row struct {
	header string
	item   int // index into Options.Items; -1 for a header
}

// Model is the bubbletea model behind one screen.
type Model struct {
	opts    Options
	query   textinput.Model
	matches []int // item indexes that match the query
	rows    []row
	checked map[string]bool
	order   []string // checked values in list order, for stable results
	cursor  int      // position in matches
	offset  int      // first visible row
	listH   int      // rows shown at once, fixed so the box does not jump
	width   int
	height  int
	chosen  string
	action  Action
	// touched is set once the user checks or unchecks something, so enter knows whether they
	// only moved the cursor (the row under it is the pick) or built a selection of their own.
	touched bool
}

// New builds a screen focused on the selected value.
func New(opts Options) *Model {
	q := textinput.New()
	q.Placeholder = "Search"
	q.Prompt = "› "
	q.Focus()
	m := &Model{opts: opts, query: q, width: 100, height: 30, checked: map[string]bool{}}
	for _, v := range opts.Checked {
		m.checked[v] = true
	}
	m.refilter()
	m.listH = max(3, min(len(m.rows), 18))
	for i, idx := range m.matches {
		if opts.Items[idx].Value == opts.Selected {
			m.cursor = i
		}
	}
	if opts.Multi && opts.Selected == "" {
		for i, idx := range m.matches {
			if m.checked[opts.Items[idx].Value] {
				m.cursor = i // start on the first checked row, so enter alone keeps what was configured
				break
			}
		}
	}
	m.ensureVisible()
	return m
}

// Result is what one screen produced.
type Result struct {
	Values []string // the pick (single mode) or every checked value (multi)
	Action Action
}

// Run shows the screen full screen and waits for the user.
func Run(opts Options) (Result, error) {
	final, err := tea.NewProgram(New(opts), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err != nil {
		return Result{}, err
	}
	m := final.(*Model)
	if m.action == Quit {
		return Result{Action: Quit}, ErrInterrupted
	}
	return Result{Values: m.Values(), Action: m.action}, nil
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
			m.action = Quit
			return m, tea.Quit
		case tea.KeyEsc:
			if m.query.Value() != "" {
				m.query.SetValue("")
				m.refilter()
				return m, nil
			}
			m.action = Back
			return m, tea.Quit
		case tea.KeyEnter:
			return m, m.confirm()
		case tea.KeySpace:
			if m.opts.Multi && m.query.Value() == "" {
				m.toggle()
				return m, nil
			}
		case tea.KeyTab:
			if m.opts.Multi {
				m.toggle()
			} else {
				m.move(1)
			}
			return m, nil
		case tea.KeyCtrlA:
			if m.opts.Multi {
				m.toggleAll()
			}
			return m, nil
		case tea.KeyUp, tea.KeyCtrlP, tea.KeyCtrlK:
			m.move(-1)
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN, tea.KeyCtrlJ:
			m.move(1)
			return m, nil
		case tea.KeyPgUp:
			m.move(-m.listH)
			return m, nil
		case tea.KeyPgDown:
			m.move(m.listH)
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

func (m *Model) confirm() tea.Cmd {
	if m.opts.Multi {
		if !m.touched && len(m.checked) > 0 && len(m.matches) > 0 {
			// Items came pre-checked and the user only moved the cursor: the row they stopped on
			// is their pick, not the pre-checked ones they never looked at.
			if row := m.opts.Items[m.matches[m.cursor]].Value; !m.checked[row] {
				m.checked = map[string]bool{row: true}
			}
		}
		if len(m.checked) == 0 && !m.opts.AllowEmpty {
			// Nothing checked yet: enter picks the item under the cursor, the
			// way it does in a single-choice list.
			if len(m.matches) == 0 {
				return nil
			}
			m.checked[m.opts.Items[m.matches[m.cursor]].Value] = true
		}
		m.action = Picked
		return tea.Quit
	}
	if len(m.matches) == 0 {
		return nil
	}
	m.chosen, m.action = m.opts.Items[m.matches[m.cursor]].Value, Picked
	return tea.Quit
}

func (m *Model) toggle() {
	if len(m.matches) == 0 {
		return
	}
	m.touched = true
	v := m.opts.Items[m.matches[m.cursor]].Value
	if m.checked[v] {
		delete(m.checked, v)
	} else {
		m.checked[v] = true
	}
}

func (m *Model) toggleAll() {
	m.touched = true
	all := true
	for _, idx := range m.matches {
		all = all && m.checked[m.opts.Items[idx].Value]
	}
	for _, idx := range m.matches {
		v := m.opts.Items[idx].Value
		if all {
			delete(m.checked, v)
		} else {
			m.checked[v] = true
		}
	}
}

func (m *Model) boxHeight() int { return linesAboveList + m.listH + 2 + 2 } // list, blank+help, border

func (m *Model) boxTop() int { return max(0, (m.height-m.boxHeight())/2) }

func (m *Model) listTop() int { return m.boxTop() + 1 + linesAboveList }

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scroll(-3)
		return nil
	case tea.MouseButtonWheelDown:
		m.scroll(3)
		return nil
	}
	r := msg.Y - m.listTop() + m.offset
	if msg.Y < m.listTop() || msg.Y >= m.listTop()+m.listH || r < 0 || r >= len(m.rows) || m.rows[r].item < 0 {
		return nil
	}
	for i, idx := range m.matches {
		if idx == m.rows[r].item {
			m.cursor = i
		}
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		if m.opts.Multi {
			m.toggle()
			return nil
		}
		return m.confirm()
	}
	return nil
}

func (m *Model) move(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.matches)-1, m.cursor+delta))
	m.ensureVisible()
}

func (m *Model) scroll(delta int) {
	m.offset = max(0, min(max(0, len(m.rows)-m.listH), m.offset+delta))
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

func (m *Model) ensureVisible() {
	r := m.cursorRow()
	if r < 0 || m.listH == 0 {
		return
	}
	if r > 0 && m.rows[r-1].item < 0 {
		r-- // keep the section header with its first item
	}
	if r < m.offset {
		m.offset = r
	}
	if cr := m.cursorRow(); cr >= m.offset+m.listH {
		m.offset = cr - m.listH + 1
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

// Query is the current search text.
func (m *Model) Query() string { return m.query.Value() }

// Action says how the screen ended (Waiting while it is still open).
func (m *Model) Action() Action { return m.action }

// Values are the picked value (single mode) or every checked value in list order.
func (m *Model) Values() []string {
	if !m.opts.Multi {
		if m.action == Picked {
			return []string{m.chosen}
		}
		return nil
	}
	var out []string
	for _, it := range m.opts.Items {
		if m.checked[it.Value] {
			out = append(out, it.Value)
		}
	}
	return out
}

// Result is the picked value in single mode; ok is false if nothing was picked.
func (m *Model) Result() (string, bool) { return m.chosen, m.action == Picked && !m.opts.Multi }

// Cancelled reports whether the user left without picking.
func (m *Model) Cancelled() bool { return m.action == Back || m.action == Quit }

// RowOf is the screen line of an item, or -1 when it is not on screen.
func (m *Model) RowOf(value string) int {
	for r := m.offset; r < len(m.rows) && r < m.offset+m.listH; r++ {
		if it := m.rows[r].item; it >= 0 && m.opts.Items[it].Value == value {
			return m.listTop() + r - m.offset
		}
	}
	return -1
}

func (m *Model) boxWidth() int { return max(46, min(m.width-8, 84)) }

// View implements tea.Model.
func (m *Model) View() string {
	w := m.boxWidth() - 6 // border and padding
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(spread(titleSt.Render(m.opts.Title), dimSt.Render("esc"), w) + "\n\n")
	b.WriteString(m.query.View() + "\n\n")

	cr := m.cursorRow()
	shown := 0
	for r := m.offset; r < len(m.rows) && shown < m.listH; r++ {
		shown++
		rw := m.rows[r]
		if rw.item < 0 {
			b.WriteString(headerSt.Render(rw.header) + "\n")
			continue
		}
		it := m.opts.Items[rw.item]
		marker := "  "
		switch {
		case m.opts.Multi && m.checked[it.Value]:
			marker = "◉ "
		case m.opts.Multi:
			marker = "○ "
		case it.Value == m.opts.Selected:
			marker = "● "
		}
		if r == cr {
			b.WriteString(activeSt.Render(plainLine(marker, it, w)) + "\n")
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
		b.WriteString(spread(currentSt.Render(marker)+title, tag, w) + "\n")
	}
	if len(m.matches) == 0 {
		b.WriteString(dimSt.Render(fmt.Sprintf("No match for %q", m.query.Value())) + "\n")
		shown++
	}
	for ; shown < m.listH; shown++ {
		b.WriteString("\n")
	}

	help := "↑↓ move · enter pick · type to search · esc back"
	if m.opts.Multi {
		help = fmt.Sprintf("space select · enter continue (%d selected) · esc back", len(m.checked))
	}
	b.WriteString("\n" + spread(dimSt.Render(help), dimSt.Render(m.opts.Footer), w))

	box := boxSt.Width(m.boxWidth() - 2).Render(b.String())
	return placeCenter(box, m.width, m.height, m.boxTop())
}

// placeCenter puts the box horizontally centered, starting at line top.
func placeCenter(box string, width, height, top int) string {
	lines := strings.Split(box, "\n")
	left := max(0, (width-lipgloss.Width(box))/2)
	var out strings.Builder
	out.WriteString(strings.Repeat("\n", top))
	for _, l := range lines {
		out.WriteString(strings.Repeat(" ", left) + l + "\n")
	}
	for n := top + len(lines); n < height-1; n++ {
		out.WriteString("\n")
	}
	return out.String()
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
