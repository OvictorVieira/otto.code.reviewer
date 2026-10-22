package setup

import (
	"context"
	"errors"
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
		ValidateApp: func(_ context.Context, id int64, key string) (string, error) {
			if id != 5384980 {
				return "", errors.New("GitHub rejected this app id and key")
			}
			return "otto-review", nil
		},
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

func TestOttoIdentityIsValidatedAndSaved(t *testing.T) {
	d := start(t)
	finishToIdentity(d)
	if !strings.Contains(d.screen(), "Which identity") {
		t.Fatalf("expected the identity step:\n%s", d.screen())
	}
	d.key(tea.KeyDown, tea.KeyEnter) // As Otto
	d.text("5384980")
	d.key(tea.KeyEnter)
	d.text("~/.config/otto/otto.pem")
	d.key(tea.KeyEnter)

	if !strings.Contains(d.screen(), "otto-review[bot]") {
		t.Fatalf("expected the confirmation:\n%s", d.screen())
	}
	if d.w.cfg.App.ID != 5384980 || !strings.HasSuffix(d.w.cfg.App.KeyPath, "/.config/otto/otto.pem") || strings.HasPrefix(d.w.cfg.App.KeyPath, "~") {
		t.Errorf("app = %+v (the path must have ~ expanded)", d.w.cfg.App)
	}
}

func TestWrongAppIdShowsTheReasonAndAsksAgain(t *testing.T) {
	d := start(t)
	finishToIdentity(d)
	d.key(tea.KeyDown, tea.KeyEnter)
	d.text("42")
	d.key(tea.KeyEnter)
	d.text("/tmp/k.pem")
	d.key(tea.KeyEnter)

	if s := d.screen(); !strings.Contains(s, "did not work") || !strings.Contains(s, "rejected") {
		t.Fatalf("the reason must be shown:\n%s", s)
	}
	if d.w.cfg.App.ID != 0 {
		t.Error("nothing is saved for a failed validation")
	}
	d.key(tea.KeyEnter)
	if !strings.Contains(d.screen(), "GitHub App ID") {
		t.Errorf("after the notice it asks for the id again:\n%s", d.screen())
	}
}

func TestFinishingTheLastStepEndsTheProgramAndKeepsEverything(t *testing.T) {
	d := start(t)
	finishToIdentity(d)
	d.key(tea.KeyEnter) // identity: As you
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

// startWith opens the wizard the way `otto setup` does when a config already exists.
func startWith(t *testing.T, cfg config.Config, catalog map[string][]engine.Model) *driver {
	t.Helper()
	dep := deps()
	dep.Catalog = func(name string) []engine.Model { return catalog[name] }
	w := newWizard(dep, cfg, []string{"acme/api", "acme/web"})
	w.spin.Spinner.FPS = time.Millisecond
	d := &driver{t: t, w: w}
	d.run(w.Init())
	return d
}

func TestChangingTheModelOfAnEngineAlreadyConfiguredReplacesTheSavedOne(t *testing.T) {
	cfg := config.Default()
	cfg.Engines = []engine.Spec{{Name: "codex", Model: "gpt-6-luna", Effort: "medium"}}
	catalog := map[string][]engine.Model{"codex": {
		{ID: "gpt-6-luna", Name: "gpt-6-luna", Efforts: []string{"low", "medium", "high"}},
		{ID: "gpt-6-sol", Name: "gpt-6-sol", Efforts: []string{"low", "medium", "high"}},
		{ID: "gpt-5.6-terra", Name: "gpt-5.6-terra", Efforts: []string{"low", "medium", "high"}},
	}}
	d := startWith(t, cfg, catalog)

	d.key(tea.KeyEnter) // repos
	d.key(tea.KeyEnter) // engines: codex is already checked, enter keeps it
	if !strings.Contains(d.screen(), "codex · select model") {
		t.Fatalf("expected codex's model list:\n%s", d.screen())
	}
	d.key(tea.KeyDown, tea.KeyEnter) // move off the current model and pick the next one

	got := d.w.cfg.Engines
	if len(got) != 1 || got[0].Name != "codex" {
		t.Fatalf("engines = %+v", got)
	}
	if got[0].Model == "gpt-6-luna" || got[0].Model == "" {
		t.Errorf("model = %q: the model picked in the wizard was not applied", got[0].Model)
	}
	if got[0].Effort != "" {
		t.Errorf("effort = %q, want it cleared until chosen again for the new model", got[0].Effort)
	}
}

func TestAddingAnotherEngineKeepsTheOneAlreadyConfiguredAndItsModel(t *testing.T) {
	cfg := config.Default()
	cfg.Engines = []engine.Spec{{Name: "codex", Model: "gpt-6-luna", Effort: "medium"}}
	d := startWith(t, cfg, catalogs)

	d.key(tea.KeyEnter)                           // repos
	d.key(tea.KeyDown, tea.KeyDown, tea.KeySpace) // the cursor starts on codex: cursor, opencode, then also check opencode
	d.key(tea.KeyEnter)

	var names []string
	for _, e := range d.w.cfg.Engines {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "codex,opencode" {
		t.Fatalf("engines = %v, want codex and opencode", names)
	}
	if got := d.w.cfg.Engines[0]; got.Model != "gpt-6-luna" || got.Effort != "medium" {
		t.Errorf("codex = %+v, want its saved model and effort kept", got)
	}
}

func TestWithCodexAlreadyConfiguredPickingOpencodeOpensOpencodeNotCodex(t *testing.T) {
	// Regression: codex was pre-checked from the saved config, so arrowing to opencode and
	// pressing enter opened codex's model list.
	cfg := config.Default()
	cfg.Engines = []engine.Spec{{Name: "codex", Model: "gpt-6-luna", Effort: "medium"}}
	d := startWith(t, cfg, catalogs)

	d.key(tea.KeyEnter)                           // repos
	d.key(tea.KeyDown, tea.KeyDown, tea.KeyEnter) // cursor starts on codex: cursor, opencode

	if got := d.w.cfg.Engines; len(got) != 1 || got[0].Name != "opencode" {
		t.Fatalf("engines = %+v, want only opencode", got)
	}
	if s := d.screen(); !strings.Contains(s, "opencode · select model") || strings.Contains(s, "codex · select") {
		t.Errorf("the next screen must be opencode's model list:\n%s", s)
	}
}

func TestPressingEnterOnTheEnginesStepKeepsTheConfiguredEngine(t *testing.T) {
	cfg := config.Default()
	cfg.Engines = []engine.Spec{{Name: "codex", Model: "gpt-6-luna", Effort: "medium"}}
	d := startWith(t, cfg, catalogs)

	d.key(tea.KeyEnter, tea.KeyEnter) // repos, then engines without touching anything

	if got := d.w.cfg.Engines; len(got) != 1 || got[0].Name != "codex" || got[0].Model != "gpt-6-luna" {
		t.Fatalf("engines = %+v, want the configured codex kept as it was", got)
	}
}

func TestEscOnTheFirstStepLeavesTheSetupWithoutSaving(t *testing.T) {
	// Regression: esc on step 1 reopened step 1 instead of leaving.
	d := start(t)

	d.key(tea.KeyEsc)

	if !d.w.quit || d.w.finished {
		t.Errorf("quit = %v, finished = %v: esc on the first step must leave the setup unfinished", d.w.quit, d.w.finished)
	}
}
