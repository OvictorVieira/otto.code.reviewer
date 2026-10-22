package picker

import (
	"errors"
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

	m = send(m, tea.KeyMsg{Type: tea.KeyHome})                                 // the cursor starts on the pre-checked gpt-5; go to the top
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

func TestEnterOnAnUncheckedItemPicksThatItemWhenNothingIsChecked(t *testing.T) {
	// Regression: choosing "opencode" with arrows + enter used to confirm the
	// default (claude) because enter only confirmed what was already checked.
	m := New(Options{Title: "Engines", Items: items, Multi: true})
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m = send(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})

	if m.Action() != Picked {
		t.Fatalf("action = %v, want Picked", m.Action())
	}
	if got := m.Values(); len(got) != 1 || got[0] != "opencode/deepseek-free" {
		t.Errorf("values = %v, want the item under the cursor", got)
	}
}

func TestEnterConfirmsTheCheckedItemsWhenSomeAreChecked(t *testing.T) {
	m := New(Options{Title: "Engines", Items: items, Multi: true, Checked: []string{"openai/gpt-5"}})
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.Values(); len(got) != 1 || got[0] != "openai/gpt-5" {
		t.Errorf("values = %v: enter must not add the cursor item when something is already checked", got)
	}
}

func TestEmptyMultiSelectIsAllowedWhenAskedFor(t *testing.T) {
	m := send(New(Options{Title: "x", Items: items, Multi: true, AllowEmpty: true}), tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyMsg{Type: tea.KeyEnter})
	if m.Action() != Picked || len(m.Values()) != 0 {
		t.Errorf("action %v values %v", m.Action(), m.Values())
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

var engines = []Item{
	{Value: "claude", Title: "claude"},
	{Value: "codex", Title: "codex"},
	{Value: "cursor", Title: "cursor"},
	{Value: "opencode", Title: "opencode"},
}

func startMulti(t *testing.T, checked ...string) *Model {
	t.Helper()
	m := New(Options{Title: "Engines", Items: engines, Multi: true, Checked: checked})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return next.(*Model)
}

var (
	down  = tea.KeyMsg{Type: tea.KeyDown}
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	space = tea.KeyMsg{Type: tea.KeySpace}
)

func TestMultiCursorStartsOnTheFirstCheckedItem(t *testing.T) {
	m := startMulti(t, "codex", "opencode")
	if got := m.Current().Value; got != "codex" {
		t.Errorf("cursor on %q, want the first checked item", got)
	}
}

func TestMultiEnterKeepsTheCheckedItemsWhenNothingWasChanged(t *testing.T) {
	m := send(startMulti(t, "codex"), enter)
	if got := m.Values(); len(got) != 1 || got[0] != "codex" {
		t.Errorf("values = %v, want the pre-checked codex kept", got)
	}
}

func TestMultiEnterOnAnUncheckedItemPicksThatItemInsteadOfThePreChecked(t *testing.T) {
	// Regression: with codex pre-checked from the saved config, arrowing to opencode and pressing
	// enter confirmed only codex and ignored the item the user was on.
	m := send(startMulti(t, "codex"), down, down, enter)
	if got := m.Values(); len(got) != 1 || got[0] != "opencode" {
		t.Errorf("values = %v, want only the item under the cursor", got)
	}
}

func TestMultiSpaceAddsToTheCheckedItemsAndEnterKeepsBoth(t *testing.T) {
	m := send(startMulti(t, "codex"), down, down, space, enter)
	got := m.Values()
	if len(got) != 2 || got[0] != "codex" || got[1] != "opencode" {
		t.Errorf("values = %v, want codex and opencode", got)
	}
}

func TestMultiUncheckingEverythingThenEnterPicksTheCursorItem(t *testing.T) {
	m := send(startMulti(t, "codex"), space, enter)
	if got := m.Values(); len(got) != 1 || got[0] != "codex" {
		t.Errorf("values = %v, want enter to pick the item under the cursor when nothing is checked", got)
	}
}

const installURL = "https://github.com/apps/otto-connector/installations/new"

func linkNotice(t *testing.T) (*NoticeModel, *[]string) {
	t.Helper()
	var calls []string
	oldOpen, oldCopy, oldLinks := OpenURL, CopyText, LinksEnabled
	t.Cleanup(func() { OpenURL, CopyText, LinksEnabled = oldOpen, oldCopy, oldLinks })
	OpenURL = func(u string) error { calls = append(calls, "open "+u); return nil }
	CopyText = func(s string) error { calls = append(calls, "copy "+s); return nil }
	LinksEnabled = true
	m := NewLinkNotice("Install the app", "Otto cannot post as the app until it is installed.", installURL, "check again", "continue anyway")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return next.(*NoticeModel), &calls
}

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func TestLinkNoticeShowsTheLinkAsPlainTextAndAsAClickableLink(t *testing.T) {
	m, _ := linkNotice(t)
	view := m.View()
	if !strings.Contains(view, installURL) {
		t.Errorf("the link must be visible as text so it can be read and typed:\n%s", view)
	}
	if !strings.Contains(view, "\x1b]8;;"+installURL+"\x1b\\") {
		t.Errorf("the link must also be a clickable terminal hyperlink (OSC 8):\n%q", view)
	}
	for _, hint := range []string{"o open", "c copy", "check again", "continue anyway"} {
		if !strings.Contains(view, hint) {
			t.Errorf("the screen must say which keys do what, missing %q:\n%s", hint, view)
		}
	}
}

func TestLinkNoticeIsPlainTextWhereTheTerminalCannotDoLinks(t *testing.T) {
	m, _ := linkNotice(t)
	LinksEnabled = false
	if view := m.View(); strings.Contains(view, "\x1b]8;;") || !strings.Contains(view, installURL) {
		t.Errorf("without hyperlink support the link must be plain text only:\n%q", view)
	}
}

func TestLinkNoticeOpensTheLinkInTheBrowserAndCopiesIt(t *testing.T) {
	m, calls := linkNotice(t)

	m = send2(m, key('o'))
	if len(*calls) != 1 || (*calls)[0] != "open "+installURL || !strings.Contains(m.View(), "Opened") {
		t.Errorf("calls = %v, view:\n%s", *calls, m.View())
	}
	m = send2(m, key('c'))
	if len(*calls) != 2 || (*calls)[1] != "copy "+installURL || !strings.Contains(m.View(), "copied") {
		t.Errorf("calls = %v, view:\n%s", *calls, m.View())
	}
}

func TestLinkNoticeSaysSoWhenTheBrowserCannotBeOpened(t *testing.T) {
	m, _ := linkNotice(t)
	OpenURL = func(string) error { return errors.New("no browser") }

	m = send2(m, key('o'))

	if view := m.View(); !strings.Contains(view, "Could not open") || !strings.Contains(view, installURL) {
		t.Errorf("a failed open must say so and keep the link on screen:\n%s", view)
	}
}

func TestLinkNoticeKeysCheckAgainContinueAnywayOrGoBack(t *testing.T) {
	cases := []struct {
		name   string
		msg    tea.Msg
		action Action
		values []string
	}{
		{"enter checks again", tea.KeyMsg{Type: tea.KeyEnter}, Picked, nil},
		{"s continues anyway", key('s'), Picked, []string{"skip"}},
		{"esc goes back", tea.KeyMsg{Type: tea.KeyEsc}, Back, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := linkNotice(t)
			m = send2(m, c.msg)
			if m.Action() != c.action || strings.Join(m.Values(), ",") != strings.Join(c.values, ",") {
				t.Errorf("action = %v, values = %v, want %v %v", m.Action(), m.Values(), c.action, c.values)
			}
		})
	}
}

func TestAPlainNoticeIgnoresTheLinkKeys(t *testing.T) {
	m := NewNotice("Connected", "Otto will post as otto-connector[bot].")
	next, _ := m.Update(key('o'))
	if next.(*NoticeModel).Action() != Waiting {
		t.Error("a notice without a link must not react to o")
	}
}

func send2(m *NoticeModel, msgs ...tea.Msg) *NoticeModel {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(*NoticeModel)
	}
	return m
}
