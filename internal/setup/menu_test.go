package setup

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
)

// configured is a setup that already ran: opencode with a model, everything else default.
func configured() config.Config {
	cfg := config.Default()
	cfg.Repos.All = true
	cfg.Language = "English"
	cfg.Engines = []engine.Spec{{Name: "opencode", Model: "opencode/beam-free"}}
	return cfg
}

func startMenu(t *testing.T, cfg config.Config) *driver {
	t.Helper()
	w := newWizard(deps(), cfg, []string{"acme/api", "me/notes"})
	w.enableMenu()
	w.login = "me"
	w.spin.Spinner.FPS = time.Millisecond
	d := &driver{t: t, w: w}
	d.run(w.Init())
	return d
}

// pick filters the open list by what is typed and picks the first match, the way a person would.
func (d *driver) pick(filter string) {
	d.t.Helper()
	d.text(filter)
	d.key(tea.KeyEnter)
}

func (d *driver) shows(want ...string) {
	d.t.Helper()
	s := d.screen()
	for _, w := range want {
		if !strings.Contains(s, w) {
			d.t.Fatalf("the screen is missing %q:\n%s", w, s)
		}
	}
}

func engineNames(d *driver) string {
	var out []string
	for _, e := range d.w.cfg.Engines {
		out = append(out, e.Name+"="+e.Model)
	}
	return strings.Join(out, ",")
}

func TestFirstTimeSetupIsTheFullOnboardingWithNoMenu(t *testing.T) {
	d := start(t) // no saved config
	d.shows("Which repositories")
	if strings.Contains(d.screen(), "What do you want to change") {
		t.Errorf("the first setup must not show a menu:\n%s", d.screen())
	}
}

func TestAConfiguredSetupOpensAMenuShowingWhatIsCurrentlySet(t *testing.T) {
	d := startMenu(t, configured())
	d.shows("What do you want to change", "Models and fallback", "Repositories", "Who posts", "Language", "Save and exit", "opencode")
	if strings.Contains(d.screen(), "Which repositories should Otto watch") {
		t.Errorf("a configured setup must not go through every step:\n%s", d.screen())
	}
}

func TestOnlyAConfiguredSetupGetsTheMenu(t *testing.T) {
	cfg, menu := openingConfig(func() (config.Config, error) { return configured(), nil })
	if !menu || len(cfg.Engines) != 1 {
		t.Errorf("menu = %v, cfg = %+v, want the saved config and the menu", menu, cfg)
	}
	for name, err := range map[string]error{"not configured": config.ErrNotConfigured, "no engines": errors.New("no engines configured"), "unreadable": errors.New("permission denied")} {
		cfg, menu := openingConfig(func() (config.Config, error) { return config.Config{}, err })
		if menu {
			t.Errorf("%s: the menu must not open without a usable config", name)
		}
		if cfg.Trigger == "" || cfg.PollInterval == 0 {
			t.Errorf("%s: onboarding must start from the defaults, got %+v", name, cfg)
		}
	}
}

func TestChangingOneThingReturnsToTheMenuInsteadOfEnding(t *testing.T) {
	d := startMenu(t, configured())

	d.pick("Language")
	d.shows("Language of the review comments")
	d.pick("Portuguese")

	d.shows("What do you want to change")
	if d.w.cfg.Language != "Brazilian Portuguese" {
		t.Errorf("language = %q, want the choice applied", d.w.cfg.Language)
	}
	if d.w.finished {
		t.Error("changing one setting must not end the setup")
	}
}

func TestSaveAndExitEndsTheSetupKeepingTheChanges(t *testing.T) {
	d := startMenu(t, configured())
	d.pick("Language")
	d.pick("Portuguese")

	d.pick("Save")

	if !d.w.finished || d.w.quit {
		t.Errorf("finished = %v, quit = %v, want a finished setup", d.w.finished, d.w.quit)
	}
	if d.w.cfg.Language != "Brazilian Portuguese" {
		t.Errorf("language = %q", d.w.cfg.Language)
	}
	if d.w.service {
		t.Error("the menu must not touch the background service unless the person asks for it")
	}
}

func TestExitWithoutSavingAndEscOnTheMenuLeaveWithoutFinishing(t *testing.T) {
	d := startMenu(t, configured())
	d.pick("without")
	if !d.w.quit || d.w.finished {
		t.Errorf("exit without saving: quit = %v, finished = %v", d.w.quit, d.w.finished)
	}

	d = startMenu(t, configured())
	d.key(tea.KeyEsc)
	if !d.w.quit || d.w.finished {
		t.Errorf("esc on the menu: quit = %v, finished = %v", d.w.quit, d.w.finished)
	}
}

func TestEscInsideASectionGoesBackToTheMenu(t *testing.T) {
	d := startMenu(t, configured())
	d.pick("Language")
	d.key(tea.KeyEsc)

	d.shows("What do you want to change")
	if d.w.quit {
		t.Error("esc inside a section must go back to the menu, not leave the setup")
	}
}

func TestChangingOnlyTheModelKeepsTheEngineAndTheRest(t *testing.T) {
	cfg := configured()
	cfg.Language = "Brazilian Portuguese"
	d := startMenu(t, cfg)

	d.pick("Models")
	d.shows("Models and fallback", "1. opencode")
	d.pick("1.")
	d.pick("Change model")
	d.shows("opencode · select model")
	d.pick("Big Pickle")
	// big-pickle has no effort levels, so the next screen is the model list again
	d.shows("Models and fallback", "big-pickle")
	d.pick("Done")

	if got := engineNames(d); got != "opencode=opencode/big-pickle" {
		t.Errorf("engines = %s, want only the model changed", got)
	}
	if d.w.cfg.Language != "Brazilian Portuguese" || !d.w.cfg.Repos.All {
		t.Errorf("the rest of the config must stay as it was: %+v", d.w.cfg)
	}
	d.shows("What do you want to change")
}

func TestAddingAFallbackModelKeepsTheFirstOneAndAsksHowToUseThem(t *testing.T) {
	d := startMenu(t, configured())

	d.pick("Models")
	d.pick("Add a model")
	d.shows("Which engine")
	d.pick("claude")
	d.shows("claude · select model")
	d.pick("sonnet")
	d.shows("select effort")
	d.key(tea.KeyEnter) // default effort
	d.shows("When more than one engine")
	d.pick("Priority")

	d.shows("Models and fallback", "1. opencode", "2. claude")
	if got := engineNames(d); got != "opencode=opencode/beam-free,claude=sonnet" {
		t.Errorf("engines = %s, want the new model after the first one", got)
	}
	if d.w.cfg.Strategy != engine.Priority {
		t.Errorf("strategy = %q, want priority so the fallback is used only when the first fails", d.w.cfg.Strategy)
	}
	d.pick("Done")
	d.shows("What do you want to change")
}

func TestTheFallbackCanBeAnotherModelOfTheSameEngine(t *testing.T) {
	d := startMenu(t, configured())

	d.pick("Models")
	d.pick("Add a model")
	d.pick("opencode")
	d.shows("opencode · select model")
	d.pick("Big Pickle")
	d.shows("When more than one engine")
	d.pick("Priority")

	if got := engineNames(d); got != "opencode=opencode/beam-free,opencode=opencode/big-pickle" {
		t.Errorf("engines = %s, want opencode twice, with a different model each", got)
	}
}

func TestModelsCanBeReorderedAndRemovedButNeverTheLastOne(t *testing.T) {
	cfg := configured()
	cfg.Engines = []engine.Spec{{Name: "opencode", Model: "opencode/beam-free"}, {Name: "claude", Model: "sonnet"}}
	cfg.Strategy = engine.Priority
	d := startMenu(t, cfg)
	d.pick("Models")

	d.pick("2.")
	d.pick("Move up")
	if got := engineNames(d); got != "claude=sonnet,opencode=opencode/beam-free" {
		t.Fatalf("after moving up: %s", got)
	}

	d.pick("1.")
	d.pick("Remove")
	if got := engineNames(d); got != "opencode=opencode/beam-free" {
		t.Fatalf("after removing: %s", got)
	}

	d.pick("1.")
	d.pick("Remove")
	if len(d.w.cfg.Engines) != 1 {
		t.Errorf("engines = %s, the last model must stay", engineNames(d))
	}
	d.shows("at least one")
}

func TestEverythingRunsTheFullSetupAndComesBackToTheMenu(t *testing.T) {
	d := startMenu(t, configured())

	d.pick("Everything")
	d.shows("Which repositories")
	d.key(tea.KeyEnter) // repos
	d.shows("Which AI engines")
}
