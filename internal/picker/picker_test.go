package picker

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var items = []Item{
	{Value: "", Title: "CLI default", Group: "Default"},
	{Value: "opencode/beam-free", Title: "Beam Free", Group: "Free", Tag: "Free"},
	{Value: "opencode/deepseek-free", Title: "DeepSeek V4.1 Flash Free", Group: "Free", Tag: "Free"},
	{Value: "anthropic/claude-sonnet-4", Title: "Claude Sonnet 4", Group: "Anthropic", Tag: "$3/$15"},
	{Value: "openai/gpt-5", Title: "GPT-5", Group: "OpenAI", Tag: "$1.25/$10"},
}

func start(t *testing.T, selected string) *Model {
	t.Helper()
	m := New(Options{Title: "Select model", Items: items, Selected: selected})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return next.(*Model)
}

func send(m *Model, msgs ...tea.Msg) *Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(*Model)
	}
	return m
}

func typeText(s string) []tea.Msg {
	var msgs []tea.Msg
	for _, r := range s {
		msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return msgs
}

func TestCursorStartsOnTheCurrentSelection(t *testing.T) {
	m := start(t, "anthropic/claude-sonnet-4")
	if got := m.Current().Value; got != "anthropic/claude-sonnet-4" {
		t.Errorf("cursor on %q, want the current model", got)
	}
}

func TestTypingFiltersAcrossTitleGroupAndTag(t *testing.T) {
	m := send(start(t, ""), typeText("free")...)

	if got := m.Visible(); len(got) != 2 || got[0].Value != "opencode/beam-free" || got[1].Value != "opencode/deepseek-free" {
		t.Errorf("visible = %+v, want only the two free models", got)
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if v, ok := m.Result(); !ok || v != "opencode/beam-free" {
		t.Errorf("result = %q, %v", v, ok)
	}
}

func TestEveryTermMustMatch(t *testing.T) {
	m := send(start(t, ""), typeText("anthropic sonnet")...)
	if got := m.Visible(); len(got) != 1 || got[0].Value != "anthropic/claude-sonnet-4" {
		t.Errorf("visible = %+v", got)
	}
	m = send(m, typeText(" nope")...)
	if len(m.Visible()) != 0 || !strings.Contains(m.View(), "No match") {
		t.Errorf("an unmatched search should say so:\n%s", m.View())
	}
	if _, ok := send(m, tea.KeyMsg{Type: tea.KeyEnter}).Result(); ok {
		t.Error("enter with nothing visible must not pick anything")
	}
}

func TestArrowsMoveAndWrapNeither(t *testing.T) {
	m := start(t, "")
	m = send(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.Current().Value != "" {
		t.Errorf("up at the top must stay at the top, got %q", m.Current().Value)
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown})
	if m.Current().Value != "anthropic/claude-sonnet-4" {
		t.Errorf("after three downs the cursor is on %q", m.Current().Value)
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.Current().Value != "openai/gpt-5" {
		t.Errorf("end goes to the last item, got %q", m.Current().Value)
	}
}

func TestEscCancels(t *testing.T) {
	m := send(start(t, ""), tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := m.Result(); ok || !m.Cancelled() {
		t.Error("esc must cancel without a result")
	}
}

func TestMouseHoverHighlightsAndClickPicks(t *testing.T) {
	m := start(t, "")
	row := m.RowOf("openai/gpt-5")
	if row < 0 {
		t.Fatal("gpt-5 should be on screen")
	}

	m = send(m, tea.MouseMsg{X: 10, Y: row, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	if m.Current().Value != "openai/gpt-5" {
		t.Errorf("hover should move the cursor to gpt-5, got %q", m.Current().Value)
	}
	m = send(m, tea.MouseMsg{X: 10, Y: row, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if v, ok := m.Result(); !ok || v != "openai/gpt-5" {
		t.Errorf("click result = %q, %v", v, ok)
	}
}

func TestClickOnGroupHeaderPicksNothing(t *testing.T) {
	m := start(t, "")
	header := m.RowOf("opencode/beam-free") - 1 // the "Free" header sits right above
	m = send(m, tea.MouseMsg{X: 10, Y: header, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if _, ok := m.Result(); ok {
		t.Error("clicking a section header must not pick anything")
	}
}

func TestViewShowsGroupsTagsAndCurrentMarker(t *testing.T) {
	v := start(t, "openai/gpt-5").View()
	for _, want := range []string{"Select model", "esc", "Free", "Anthropic", "$3/$15", "●"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestLongListsScrollToKeepTheCursorVisible(t *testing.T) {
	var many []Item
	for i := 0; i < 200; i++ {
		many = append(many, Item{Value: string(rune('a'+i%26)) + strings.Repeat("x", i/26), Title: "model", Group: "All"})
	}
	m := New(Options{Title: "t", Items: many})
	m = send(m, tea.WindowSizeMsg{Width: 80, Height: 20}, tea.KeyMsg{Type: tea.KeyEnd})
	if m.RowOf(many[199].Value) < 0 {
		t.Error("the last item must be on screen after End")
	}
	m = send(m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.RowOf(many[199].Value) >= 0 && m.offset == 0 {
		t.Error("wheel up should scroll the list")
	}
}

func TestMultiSelectTogglesWithSpaceAndConfirmsWithEnter(t *testing.T) {
	m := New(Options{Title: "Repos", Items: items, Multi: true, Checked: []string{"openai/gpt-5"}})
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m = send(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeySpace}) // check item 2
	m = send(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeySpace}) // check item 3
	m = send(m, tea.KeyMsg{Type: tea.KeySpace})                                // uncheck item 3 again
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})

	got := m.Values()
	if len(got) != 2 || got[0] != "opencode/beam-free" || got[1] != "openai/gpt-5" {
		t.Errorf("values = %v, want beam-free (toggled) and gpt-5 (pre-checked)", got)
	}
}

func TestMultiSelectWithSearchUsesTabToToggleAndKeepsTheQuery(t *testing.T) {
	m := New(Options{Title: "Repos", Items: items, Multi: true})
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m = send(m, typeText("gpt")...)
	m = send(m, tea.KeyMsg{Type: tea.KeyTab})

	if got := m.Values(); len(got) != 1 || got[0] != "openai/gpt-5" {
		t.Errorf("values = %v", got)
	}
	if m.Query() != "gpt" {
		t.Errorf("query = %q, tab must not clear it", m.Query())
	}
}

func TestMultiSelectRequiresAtLeastOneUnlessEmptyIsAllowed(t *testing.T) {
	m := send(New(Options{Title: "x", Items: items, Multi: true}), tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := m.Result(); ok || m.Action() != Waiting {
		t.Error("enter with nothing checked must not finish")
	}
	m = send(New(Options{Title: "x", Items: items, Multi: true, AllowEmpty: true}), tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyMsg{Type: tea.KeyEnter})
	if m.Action() != Picked {
		t.Error("enter with nothing checked is fine when AllowEmpty")
	}
}

func TestClickTogglesInMultiSelect(t *testing.T) {
	m := New(Options{Title: "x", Items: items, Multi: true})
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	row := m.RowOf("openai/gpt-5")
	m = send(m, tea.MouseMsg{X: 10, Y: row, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.Action() != Waiting || len(m.Values()) != 1 {
		t.Errorf("click should check the row and stay: action %v values %v", m.Action(), m.Values())
	}
}

func TestEscGoesBackAndCtrlCQuits(t *testing.T) {
	if a := send(start(t, ""), tea.KeyMsg{Type: tea.KeyEsc}).Action(); a != Back {
		t.Errorf("esc action = %v, want Back", a)
	}
	if a := send(start(t, ""), tea.KeyMsg{Type: tea.KeyCtrlC}).Action(); a != Quit {
		t.Errorf("ctrl+c action = %v, want Quit", a)
	}
}

func TestBoxIsCenteredOnScreen(t *testing.T) {
	m := start(t, "")
	lines := strings.Split(m.View(), "\n")
	if len(lines) < 20 {
		t.Fatalf("view has %d lines, want a full-screen frame", len(lines))
	}
	top := 0
	for top < len(lines) && strings.TrimSpace(lines[top]) == "" {
		top++
	}
	if top < 3 || !strings.Contains(lines[top], "╭") {
		t.Errorf("box should start a few lines down with a border, first content line %d: %q", top, lines[top])
	}
	if !strings.HasPrefix(lines[top], strings.Repeat(" ", 5)) {
		t.Errorf("box should be indented from the left edge: %q", lines[top])
	}
}

func TestInputTypesAndSubmits(t *testing.T) {
	in := NewInput("Model id", "anthropic/claude-sonnet-4")
	next, _ := in.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	in = next.(*InputModel)
	for _, r := range "abc" {
		next, _ = in.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		in = next.(*InputModel)
	}
	next, _ = in.Update(tea.KeyMsg{Type: tea.KeyEnter})
	in = next.(*InputModel)
	if v, a := in.Value(), in.Action(); v != "abc" || a != Picked {
		t.Errorf("value %q action %v", v, a)
	}
}
