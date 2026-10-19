package setup

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
)

var catalogs = map[string][]engine.Model{
	"claude": {{ID: "sonnet", Name: "sonnet", Group: "Claude", Efforts: []string{"low", "high"}}},
	"opencode": {
		{ID: "opencode/beam-free", Name: "Beam Free", Group: "Free", Tag: "Free", Free: true, Efforts: []string{"low", "high"}},
		{ID: "opencode/big-pickle", Name: "Big Pickle", Group: "Free", Tag: "Free", Free: true}, // no effort levels
		{ID: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4", Group: "anthropic", Tag: "$3 / $15", Efforts: []string{"low", "high", "max"}},
	},
	"copilot": {
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Group: "OpenAI", Tag: "Premium", Note: "Uses premium requests; access depends on your Copilot plan and CLI version.", Efforts: []string{"low", "medium", "high", "xhigh"}},
	},
}

func deps() Deps {
	return Deps{
		Catalog:   func(name string) []engine.Model { return catalogs[name] },
		Efforts:   func(string) []string { return nil },
		Installed: func(string) bool { return true },
	}
}

// drive plays keys into the wizard the way bubbletea would, running the
// commands it returns (loading, spinner ticks are skipped) until idle.
type driver struct {
	t *testing.T
	w *wizard
}

func start(t *testing.T) *driver {
	t.Helper()
	cfg := config.Default()
	w := newWizard(deps(), cfg, []string{"acme/api", "acme/web", "me/notes"})
	w.spin.Spinner.FPS = time.Millisecond // do not wait 100ms per frame in tests
	d := &driver{t: t, w: w}
	d.run(w.Init())
	return d
}

// call runs a command but gives up on slow ones (cursor blink, spinner frames):
// only the quick loading commands matter to the flow under test.
func call(cmd tea.Cmd) tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case m := <-ch:
		return m
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

func (d *driver) run(cmd tea.Cmd) {
	d.t.Helper()
	for cmd != nil {
		msg := call(cmd)
		switch msg := msg.(type) {
		case fetchedMsg:
			_, cmd = d.w.Update(msg)
		case tea.BatchMsg:
			var next tea.Cmd
			for _, c := range msg {
				if c == nil {
					continue
				}
				if m := call(c); m != nil {
					if f, ok := m.(fetchedMsg); ok {
						_, next = d.w.Update(f)
					}
				}
			}
			cmd = next
		default:
			cmd = nil
		}
	}
}

func (d *driver) key(types ...tea.KeyType) {
	d.t.Helper()
	for _, k := range types {
		_, cmd := d.w.Update(tea.KeyMsg{Type: k})
		d.run(cmd)
	}
}

func (d *driver) text(s string) {
	d.t.Helper()
	for _, r := range s {
		_, cmd := d.w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		d.run(cmd)
	}
}

func (d *driver) screen() string { return d.w.View() }

func TestSettingUpOnlyOpencodeNeverVisitsTheOtherEngines(t *testing.T) {
	// Regression: arrowing to "opencode" and pressing enter opened claude,
	// because claude was pre-checked and enter only confirmed the checked ones.
	d := start(t)
	d.key(tea.KeyEnter) // step 1: repos, enter picks the cursor item ("All repositories")
	if !strings.Contains(d.screen(), "Which AI engines") {
		t.Fatalf("expected the engines step:\n%s", d.screen())
	}
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter) // claude, codex, cursor, opencode

	if got := d.w.cfg.Engines; len(got) != 1 || got[0].Name != "opencode" {
		t.Fatalf("engines = %+v, want only opencode", got)
	}
	if s := d.screen(); !strings.Contains(s, "opencode · select model") || strings.Contains(s, "claude · select") {
		t.Errorf("the next screen must be opencode's model list:\n%s", s)
	}
}

func TestCopilotCanBeSelectedWithDefaultModel(t *testing.T) {
	d := start(t)
	d.key(tea.KeyEnter) // repositories
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter)

	if got := d.w.cfg.Engines; len(got) != 1 || got[0].Name != "copilot" {
		t.Fatalf("engines = %+v, want only copilot", got)
	}
	if !strings.Contains(d.screen(), "copilot · select model") {
		t.Fatalf("expected Copilot's model step:\n%s", d.screen())
	}
	d.key(tea.KeyEnter) // use Copilot CLI's configured default model
	if !strings.Contains(d.screen(), "Should reviews run the project") {
		t.Fatalf("Copilot's default model should proceed to review permissions:\n%s", d.screen())
	}
}

func TestCopilotListsPremiumModelsAndAllowsTypingOtherIds(t *testing.T) {
	d := start(t)
	d.key(tea.KeyEnter) // repositories
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter)

	if screen := d.screen(); !strings.Contains(screen, "GPT-5.3 Codex") ||
		!strings.Contains(screen, "Premium") || !strings.Contains(screen, "Type a model id") {
		t.Fatalf("Copilot picker should show paid models and allow custom IDs:\n%s", screen)
	}
	d.key(tea.KeyDown, tea.KeyEnter) // select GPT-5.3 Codex
	if got := d.w.cfg.Engines[0].Model; got != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want gpt-5.3-codex", got)
	}
	if !strings.Contains(d.screen(), "select effort") {
		t.Fatalf("expected Copilot effort step:\n%s", d.screen())
	}
}

func TestEverythingHappensInOneProgramWithLoadingInsideIt(t *testing.T) {
	d := start(t)
	d.key(tea.KeyEnter)                                  // repos
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyDown)         // opencode
	_, cmd := d.w.Update(tea.KeyMsg{Type: tea.KeyEnter}) // engines → model step must load first

	if d.w.loading == "" || !strings.Contains(d.screen(), "Loading opencode models") {
		t.Fatalf("a loading screen should be shown inside the program:\n%s", d.screen())
	}
	if d.w.finished || d.w.quit {
		t.Fatal("changing step must not close the program")
	}
	d.run(cmd)
	if !strings.Contains(d.screen(), "Beam Free") {
		t.Errorf("after loading the list appears:\n%s", d.screen())
	}
}

func TestPickingAModelThenItsOwnEffortLevelsAndSkippingWhenThereAreNone(t *testing.T) {
	d := start(t)
	d.key(tea.KeyEnter)
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter) // opencode only
	// Rows: CLI default, beam-free, big-pickle, claude-sonnet-4.
	// Two downs from CLI default lands on big-pickle, which has no effort levels.
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyEnter)
	spec := d.w.cfg.Engines[0]
	if spec.Model != "opencode/big-pickle" {
		t.Fatalf("model = %q", spec.Model)
	}
	if strings.Contains(d.screen(), "select effort") {
		t.Errorf("a model without effort levels must skip the effort screen:\n%s", d.screen())
	}
	if !strings.Contains(d.screen(), "Should reviews run the project") {
		t.Errorf("expected to land on the verify step:\n%s", d.screen())
	}
}

func TestEscGoesBackOneStepAndKeepsTheEarlierChoices(t *testing.T) {
	d := start(t)
	d.key(tea.KeyEnter)                                        // repos → all
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter) // opencode
	d.key(tea.KeyEsc)                                          // back to engines

	if !strings.Contains(d.screen(), "Which AI engines") {
		t.Fatalf("esc should return to the engines step:\n%s", d.screen())
	}
	d.key(tea.KeyEsc) // back to repos
	if !strings.Contains(d.screen(), "Which repositories") || !d.w.cfg.Repos.All {
		t.Errorf("repos choice must be kept:\n%s", d.screen())
	}
}

func finishToIdentity(d *driver) {
	d.key(tea.KeyEnter)                                        // repos
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter) // opencode
	d.key(tea.KeyEnter)                                        // model: CLI default (effort is skipped)
	d.key(tea.KeyEnter)                                        // verify
	d.key(tea.KeyEnter)                                        // language
}

func TestSetupNeverAsksForAGitHubAppForAnOrdinaryUser(t *testing.T) {
	d := start(t)
	finishToIdentity(d)

	if s := d.screen(); strings.Contains(s, "App ID") || strings.Contains(s, "identity") || !strings.Contains(s, "background") {
		t.Errorf("after the language step setup goes straight to the background service question:\n%s", s)
	}
}

func TestFinishingTheLastStepEndsTheProgramAndKeepsEverything(t *testing.T) {
	d := start(t)
	finishToIdentity(d)
	d.key(tea.KeyEnter) // service: yes

	if !d.w.finished || d.w.quit {
		t.Fatalf("finished %v quit %v", d.w.finished, d.w.quit)
	}
	if !d.w.cfg.Repos.All || d.w.cfg.Engines[0].Name != "opencode" || !d.w.cfg.Verify || !d.w.service {
		t.Errorf("config = %+v", d.w.cfg)
	}
}

func TestCtrlCQuitsWithoutFinishing(t *testing.T) {
	d := start(t)
	d.key(tea.KeyCtrlC)
	if !d.w.quit || d.w.finished {
		t.Errorf("quit %v finished %v", d.w.quit, d.w.finished)
	}
}
