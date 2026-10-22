package setup

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/picker"
)

// openingConfig says how `otto setup` starts. A usable saved config opens the menu, so a person
// changes one thing without walking through everything again; a first setup (or a config that
// cannot be used) starts from the defaults and goes through every step, with no menu.
func openingConfig(load func() (config.Config, error)) (config.Config, bool) {
	if saved, err := load(); err == nil {
		return saved, true
	}
	return config.Default(), false
}

// enableMenu makes the wizard open on a menu of what can be changed and come back to it after
// each change, instead of running every step once. The background service is left alone unless
// the person asks for it: a running Otto reloads the saved config by itself.
func (w *wizard) enableMenu() {
	w.menu = true
	w.service = false
	w.steps = []step{w.stepMenu()}
}

// toMenu shows the menu again.
func (w *wizard) toMenu() tea.Cmd {
	w.steps = []step{w.stepMenu()}
	w.dir = 1
	return w.enter(0)
}

// insertAfterCurrent queues steps to run right after the current one.
func (w *wizard) insertAfterCurrent(next ...step) {
	w.steps = slices.Insert(w.steps, w.i+1, next...)
}

func (w *wizard) engineChain() string {
	var parts []string
	for _, e := range w.cfg.Engines {
		parts = append(parts, fmt.Sprintf("%s (%s)", e.Name, orDefault(e.Model)))
	}
	return strings.Join(parts, " → ")
}

func (w *wizard) repoSummary() string {
	if w.cfg.Repos.All {
		return "all repositories"
	}
	return fmt.Sprintf("%d selected", len(w.cfg.Repos.Selected))
}

func (w *wizard) identitySummary() string {
	if w.cfg.App.ID != 0 {
		return fmt.Sprintf("GitHub App %d", w.cfg.App.ID)
	}
	return "you"
}

func (w *wizard) stepMenu() step {
	return step{show: func() (ui, func([]string) *nested) {
		group := "What do you want to change?"
		verify := "only read the code"
		if w.cfg.Verify {
			verify = "run the project"
		}
		items := []picker.Item{
			{Value: "models", Title: "Models and fallback", Hint: w.engineChain(), Group: group},
			{Value: "repos", Title: "Repositories to watch", Hint: w.repoSummary(), Group: group},
			{Value: "identity", Title: "Who posts the reviews", Hint: w.identitySummary(), Group: group},
			{Value: "language", Title: "Language of the comments", Hint: w.cfg.Language, Group: group},
			{Value: "verify", Title: "Run the project while reviewing", Hint: verify, Group: group},
			{Value: "service", Title: "Background service", Hint: "start Otto now and at every login", Group: group},
			{Value: "full", Title: "Everything (full setup)", Hint: "go through every step", Group: group},
			{Value: "save", Title: "Save and exit", Group: "Done"},
			{Value: "discard", Title: "Exit without saving", Group: "Done"},
		}
		sections := map[string][]step{
			"models":   {w.stepChain()},
			"repos":    {w.stepRepos()},
			"identity": {w.stepIdentity()},
			"language": {w.stepLanguage()},
			"verify":   {w.stepVerify()},
			"service":  {w.stepService()},
			"full":     {w.stepRepos(), w.stepEngines()},
		}
		m := picker.New(picker.Options{Title: "Otto setup", Items: items, Selected: w.menuLast, Footer: "esc leaves without saving"})
		return m, func(vals []string) *nested {
			if len(vals) == 0 {
				return nil
			}
			switch vals[0] {
			case "save":
				w.finishing = true
			case "discard":
				w.quit = true
			default:
				w.menuLast = vals[0]
				w.steps = append([]step{w.stepMenu()}, sections[vals[0]]...)
			}
			return nil
		}
	}}
}

// stepChain edits the models in the order they are tried: the first is the primary one and the
// others are used when it fails (with the priority strategy), or take turns (with rotation).
func (w *wizard) stepChain() step {
	return step{root: true, show: func() (ui, func([]string) *nested) {
		group := "Models, in the order they are tried"
		var items []picker.Item
		for i, e := range w.cfg.Engines {
			role := "primary"
			if i > 0 {
				role = "used if the one above fails"
			}
			if w.cfg.Strategy != engine.Priority && len(w.cfg.Engines) > 1 {
				role = "takes turns"
			}
			hint := role
			if e.Effort != "" {
				hint += " · effort " + e.Effort
			}
			items = append(items, picker.Item{Value: fmt.Sprintf("e%d", i), Title: fmt.Sprintf("%d. %s · %s", i+1, e.Name, orDefault(e.Model)), Hint: hint, Group: group})
		}
		order := "Rotate"
		if w.cfg.Strategy == engine.Priority {
			order = "Priority (the first one, then the others only if it fails)"
		}
		items = append(items,
			picker.Item{Value: "add", Title: "+ Add a model (fallback)", Hint: "another engine, or another model of the same engine", Group: "Actions"},
			picker.Item{Value: "strategy", Title: "Order: " + order, Hint: "how the models are used", Group: "Actions"},
			picker.Item{Value: "done", Title: "Done", Group: "Actions"},
		)
		m := picker.New(picker.Options{Title: "Models and fallback", Items: items, Selected: w.chainLast, Footer: "pick a model to change, move or remove it"})
		return m, func(vals []string) *nested {
			if len(vals) == 0 {
				return nil
			}
			w.chainLast = vals[0]
			switch v := vals[0]; v {
			case "done":
				return nil
			case "add":
				return w.pickEngineToAdd()
			case "strategy":
				w.insertAfterCurrent(w.stepStrategy(), w.stepChain())
				return nil
			default:
				var k int
				fmt.Sscanf(v, "e%d", &k)
				return w.modelActions(k)
			}
		}
	}}
}

func (w *wizard) pickEngineToAdd() *nested {
	var items []picker.Item
	for _, name := range engine.Known {
		tag := ""
		if !w.deps.Installed(name) {
			tag = "not installed"
		}
		items = append(items, picker.Item{Value: name, Title: name, Group: "AI engines", Tag: tag})
	}
	return &nested{
		ui: picker.New(picker.Options{Title: "Which engine runs the new model?", Items: items, Footer: "the same engine can be added again with another model"}),
		done: func(vals []string) *nested {
			if len(vals) == 0 {
				w.insertAfterCurrent(w.stepChain())
				return nil
			}
			w.cfg.Engines = append(w.cfg.Engines, engine.Spec{Name: vals[0]})
			k := len(w.cfg.Engines) - 1
			next := []step{w.stepModel(k), w.stepEffort(k)}
			if len(w.cfg.Engines) > 1 && w.cfg.Strategy != engine.Priority {
				next = append(next, w.stepStrategy())
			}
			w.insertAfterCurrent(append(next, w.stepChain())...)
			return nil
		},
	}
}

func (w *wizard) modelActions(k int) *nested {
	e := w.cfg.Engines[k]
	group := fmt.Sprintf("%d. %s · %s", k+1, e.Name, orDefault(e.Model))
	items := []picker.Item{{Value: "model", Title: "Change model", Hint: "pick another model, or another effort", Group: group}}
	if k > 0 {
		items = append(items, picker.Item{Value: "up", Title: "Move up", Hint: "tried earlier", Group: group})
	}
	if k < len(w.cfg.Engines)-1 {
		items = append(items, picker.Item{Value: "down", Title: "Move down", Hint: "tried later", Group: group})
	}
	items = append(items,
		picker.Item{Value: "remove", Title: "Remove", Group: group},
		picker.Item{Value: "back", Title: "Back", Group: group},
	)
	return &nested{
		ui: picker.New(picker.Options{Title: "What to do with this model?", Items: items}),
		done: func(vals []string) *nested {
			if len(vals) == 0 {
				vals = []string{"back"}
			}
			switch vals[0] {
			case "model":
				w.insertAfterCurrent(w.stepModel(k), w.stepEffort(k), w.stepChain())
			case "up":
				w.cfg.Engines[k-1], w.cfg.Engines[k] = w.cfg.Engines[k], w.cfg.Engines[k-1]
				w.chainLast = fmt.Sprintf("e%d", k-1)
				w.insertAfterCurrent(w.stepChain())
			case "down":
				w.cfg.Engines[k+1], w.cfg.Engines[k] = w.cfg.Engines[k], w.cfg.Engines[k+1]
				w.chainLast = fmt.Sprintf("e%d", k+1)
				w.insertAfterCurrent(w.stepChain())
			case "remove":
				if len(w.cfg.Engines) == 1 {
					return &nested{ui: picker.NewNotice("Cannot remove it", "Otto needs at least one model to review with."), done: func([]string) *nested {
						w.insertAfterCurrent(w.stepChain())
						return nil
					}}
				}
				w.cfg.Engines = slices.Delete(w.cfg.Engines, k, k+1)
				w.chainLast = ""
				w.insertAfterCurrent(w.stepChain())
			default:
				w.insertAfterCurrent(w.stepChain())
			}
			return nil
		},
	}
}
