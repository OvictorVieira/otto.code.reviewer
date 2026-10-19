// Package setup is the interactive `otto setup` wizard: one full-screen step
// at a time, esc goes back, ctrl+c leaves.
package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/ghapp"
	"github.com/OvictorVieira/otto.code.reviewer/internal/github"
	"github.com/OvictorVieira/otto.code.reviewer/internal/picker"
	"github.com/OvictorVieira/otto.code.reviewer/internal/service"
)

const (
	cliDefault = ""
	allRepos   = "\x00all"
	typeModel  = "\x00type"
)

type step func() (picker.Action, error)

type wizard struct {
	out      io.Writer
	cfg      config.Config
	repos    []string
	catalogs map[string][]engine.Model
	service  bool
}

// Run walks the user through every setting and saves the config.
func Run(ctx context.Context, out io.Writer) error {
	login, err := github.Login(ctx)
	if err != nil {
		return fmt.Errorf("GitHub CLI is not logged in. Run `gh auth login` first, then `otto setup` again (%w)", err)
	}
	fmt.Fprintf(out, "GitHub: logged in as %s. Reviews will be posted as you.\nLoading your repositories...\n", login)
	repos, err := github.Repos(ctx)
	if err != nil {
		return err
	}

	w := &wizard{out: out, cfg: config.Default(), repos: repos, catalogs: map[string][]engine.Model{}, service: true}
	if old, err := config.Load(); err == nil {
		w.cfg = old
	}
	w.cfg.AllowedAuthors = []string{login}
	previous := map[string]engine.Spec{}
	for _, s := range w.cfg.Engines {
		previous[s.Name] = s
	}

	// The engine steps depend on which engines are picked, so that part of the
	// list is rebuilt whenever the engine step is passed.
	steps := []step{w.stepRepos, nil}
	tail := func() []step {
		var s []step
		for i := range w.cfg.Engines {
			s = append(s, w.stepModel(i), w.stepEffort(i))
		}
		if len(w.cfg.Engines) > 1 {
			s = append(s, w.stepStrategy)
		}
		return append(s, w.stepVerify, w.stepLanguage, w.stepIdentity, w.stepService)
	}
	steps[1] = func() (picker.Action, error) {
		act, err := w.stepEngines(previous)
		if err == nil && act == picker.Picked {
			steps = append(steps[:2], tail()...)
		}
		return act, err
	}

	for i, dir := 0, 1; i < len(steps); {
		act, err := steps[i]()
		if err != nil {
			return err
		}
		switch act {
		case picker.Back:
			dir, i = -1, max(0, i-1)
		case picker.Picked:
			dir, i = 1, i+1
		case skipped:
			i += dir
			if i < 0 {
				i, dir = 0, 1
			}
		}
	}

	path, err := config.Save(w.cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved %s\n", path)
	for _, e := range w.cfg.Engines {
		fmt.Fprintf(out, "  %s  model %s  effort %s\n", e.Name, orDefault(e.Model), orDefault(e.Effort))
	}
	if w.service {
		logPath, err := service.Install()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Otto is watching in the background. Logs: %s\n", logPath)
	}
	fmt.Fprintf(out, "\nDone. Comment `%s` on a pull request, or run `otto review <pr-url>`.\n", w.cfg.Trigger)
	return nil
}

// skipped is returned by a step that has nothing to ask (no effort levels).
const skipped picker.Action = -1

func orDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

// choose is a single-choice screen.
func choose(title string, items []picker.Item, selected, footer string) (string, picker.Action, error) {
	res, err := picker.Run(picker.Options{Title: title, Items: items, Selected: selected, Footer: footer})
	if err != nil || res.Action != picker.Picked || len(res.Values) == 0 {
		return "", res.Action, err
	}
	return res.Values[0], picker.Picked, nil
}

func (w *wizard) stepRepos() (picker.Action, error) {
	items := []picker.Item{{Value: allRepos, Title: "All repositories", Hint: "including new ones", Group: "Watch"}}
	sorted := slices.Clone(w.repos)
	sort.SliceStable(sorted, func(i, j int) bool { return owner(sorted[i]) < owner(sorted[j]) })
	for _, r := range sorted {
		items = append(items, picker.Item{Value: r, Title: r[strings.Index(r, "/")+1:], Group: owner(r)})
	}
	checked := w.cfg.Repos.Selected
	if w.cfg.Repos.All {
		checked = []string{allRepos}
	}
	res, err := picker.Run(picker.Options{
		Title: "Step 1 · Which repositories should Otto watch?", Items: items, Multi: true, Checked: checked,
		Footer: fmt.Sprintf("%d repositories", len(w.repos)),
	})
	if err != nil || res.Action != picker.Picked {
		return res.Action, err
	}
	w.cfg.Repos = config.Repos{}
	if slices.Contains(res.Values, allRepos) {
		w.cfg.Repos.All = true
	} else {
		w.cfg.Repos.Selected = res.Values
	}
	return picker.Picked, nil
}

func owner(repo string) string { return repo[:strings.Index(repo, "/")] }

func (w *wizard) stepEngines(previous map[string]engine.Spec) (picker.Action, error) {
	var items []picker.Item
	var checked []string
	for _, name := range engine.Known {
		tag := ""
		if !engine.Installed(name) {
			tag = "not installed"
		}
		items = append(items, picker.Item{Value: name, Title: name, Group: "AI engines", Tag: tag})
		if _, was := previous[name]; was || (len(previous) == 0 && tag == "") {
			checked = append(checked, name)
		}
	}
	res, err := picker.Run(picker.Options{
		Title: "Step 2 · Which AI engines can Otto use?", Items: items, Multi: true, Checked: checked,
		Footer: "they run on your own subscriptions",
	})
	if err != nil || res.Action != picker.Picked {
		return res.Action, err
	}
	w.cfg.Engines = nil
	for _, name := range res.Values {
		spec := previous[name]
		spec.Name = name
		w.cfg.Engines = append(w.cfg.Engines, spec)
	}
	return picker.Picked, nil
}

func (w *wizard) catalog(name string) []engine.Model {
	if c, ok := w.catalogs[name]; ok {
		return c
	}
	fmt.Fprintf(w.out, "Loading %s models...\n", name)
	w.catalogs[name] = engine.Catalog(name)
	return w.catalogs[name]
}

func (w *wizard) stepModel(i int) step {
	return func() (picker.Action, error) {
		spec := &w.cfg.Engines[i]
		catalog := w.catalog(spec.Name)

		items := []picker.Item{{Value: cliDefault, Title: "CLI default", Hint: "whatever " + spec.Name + " is set to use", Group: "Default"}}
		free, known := 0, false
		for _, m := range catalog {
			hint := m.Note
			if m.ID != m.Name {
				hint = strings.TrimSpace(m.ID + " " + m.Note)
			}
			items = append(items, picker.Item{Value: m.ID, Title: m.Name, Hint: hint, Group: m.Group, Tag: m.Tag})
			known = known || m.ID == spec.Model
			if m.Free {
				free++
			}
		}
		if spec.Model != "" && !known {
			items = append(items, picker.Item{Value: spec.Model, Title: spec.Model, Group: "Current", Tag: "current"})
		}
		if len(catalog) == 0 {
			items = append(items, picker.Item{Value: typeModel, Title: "Type a model id…", Group: "Other"})
		}

		footer := fmt.Sprintf("%d models", len(catalog))
		if free > 0 {
			footer += fmt.Sprintf(" · %d free", free)
		}
		if len(catalog) == 0 {
			footer = spec.Name + " did not list any model"
		}
		title := fmt.Sprintf("%s · select model", spec.Name)
		model, act, err := choose(title, items, spec.Model, footer)
		if err != nil || act != picker.Picked {
			return act, err
		}
		if model == typeModel {
			typed, act, err := picker.RunInput(spec.Name+" · model id", "model id")
			if err != nil || act != picker.Picked {
				return act, err
			}
			model = typed
		}
		if model != spec.Model {
			spec.Effort = "" // the old effort may not exist for the new model
		}
		spec.Model = model
		return picker.Picked, nil
	}
}

func (w *wizard) stepEffort(i int) step {
	return func() (picker.Action, error) {
		spec := &w.cfg.Engines[i]
		levels := engine.Efforts(spec.Name)
		for _, m := range w.catalog(spec.Name) {
			if m.ID == spec.Model {
				levels = m.Efforts
			}
		}
		if len(levels) == 0 {
			spec.Effort = ""
			return skipped, nil
		}
		items := []picker.Item{{Value: cliDefault, Title: "Default", Hint: "the CLI's default effort"}}
		for _, l := range levels {
			items = append(items, picker.Item{Value: l, Title: l})
		}
		current := spec.Effort
		if !slices.Contains(levels, current) {
			current = cliDefault
		}
		title := fmt.Sprintf("%s · select effort for %s", spec.Name, orDefault(spec.Model))
		effort, act, err := choose(title, items, current, fmt.Sprintf("%d levels", len(levels)))
		if err != nil || act != picker.Picked {
			return act, err
		}
		spec.Effort = effort
		return picker.Picked, nil
	}
}

func (w *wizard) stepStrategy() (picker.Action, error) {
	items := []picker.Item{
		{Value: string(engine.RoundRobin), Title: "Rotate", Hint: "each review starts with the next engine, spreading usage across plans", Group: "Strategy"},
		{Value: string(engine.Priority), Title: "Priority", Hint: "always start with the first engine, use the others only if it fails", Group: "Strategy"},
	}
	v, act, err := choose("When more than one engine is configured", items, string(w.cfg.Strategy), "")
	if err == nil && act == picker.Picked {
		w.cfg.Strategy = engine.Strategy(v)
	}
	return act, err
}

func (w *wizard) stepVerify() (picker.Action, error) {
	items := []picker.Item{
		{Value: "yes", Title: "Run the project", Hint: "install, build, run tests and probe scenarios in a disposable worktree (recommended)", Group: "Reviews"},
		{Value: "no", Title: "Only read the code", Hint: "safer for repositories with outside contributors", Group: "Reviews"},
	}
	cur := "no"
	if w.cfg.Verify {
		cur = "yes"
	}
	v, act, err := choose("Should reviews run the project?", items, cur, "")
	if err == nil && act == picker.Picked {
		w.cfg.Verify = v == "yes"
	}
	return act, err
}

func (w *wizard) stepLanguage() (picker.Action, error) {
	items := []picker.Item{
		{Value: "English", Title: "English", Group: "Language"},
		{Value: "Brazilian Portuguese", Title: "Português (Brasil)", Group: "Language"},
	}
	v, act, err := choose("Language of the review comments", items, w.cfg.Language, "")
	if err == nil && act == picker.Picked {
		w.cfg.Language = v
	}
	return act, err
}

func (w *wizard) stepIdentity() (picker.Action, error) {
	items := []picker.Item{
		{Value: "user", Title: "As you", Hint: "reviews appear under your own GitHub account", Group: "Who posts the review when someone comments " + w.cfg.Trigger},
		{Value: "app", Title: "As Otto", Hint: "a GitHub App, shown as otto[bot] (the command line still posts as you)", Group: "Who posts the review when someone comments " + w.cfg.Trigger},
	}
	cur := "user"
	if w.cfg.App.ID != 0 {
		cur = "app"
	}
	v, act, err := choose("Which identity answers /otto review on GitHub?", items, cur, "see README: Post as Otto")
	if err != nil || act != picker.Picked {
		return act, err
	}
	if v == "user" {
		w.cfg.App = config.GitHubApp{}
		return picker.Picked, nil
	}

	for {
		id, act, err := picker.RunInput("GitHub App ID (github.com/settings/apps → your app → About)", fmt.Sprint(w.cfg.App.ID))
		if err != nil || act != picker.Picked {
			return act, err
		}
		key, act, err := picker.RunInput("Path of the app's private key (.pem)", orDefault(w.cfg.App.KeyPath))
		if err != nil || act != picker.Picked {
			return act, err
		}
		var appID int64
		if _, err := fmt.Sscan(id, &appID); err != nil || appID <= 0 {
			fmt.Fprintf(w.out, "%q is not an app id, try again.\n", id)
			continue
		}
		key = expandHome(key)
		slug, err := (&ghapp.Client{AppID: appID, KeyPath: key}).Validate(context.Background())
		if err != nil {
			fmt.Fprintf(w.out, "%v\nTry again, or press esc to go back.\n", err)
			continue
		}
		w.cfg.App = config.GitHubApp{ID: appID, KeyPath: key}
		fmt.Fprintf(w.out, "Otto will post as %s[bot].\n", slug)
		return picker.Picked, nil
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (w *wizard) stepService() (picker.Action, error) {
	items := []picker.Item{
		{Value: "yes", Title: "Yes, run in the background", Hint: "starts at login and answers " + w.cfg.Trigger, Group: "Background service"},
		{Value: "no", Title: "No, I will run `otto review` myself", Group: "Background service"},
	}
	v, act, err := choose("Start Otto in the background now and at every login?", items, "yes", "")
	if err == nil && act == picker.Picked {
		w.service = v == "yes"
	}
	return act, err
}
