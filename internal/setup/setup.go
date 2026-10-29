// Package setup is the interactive `otto setup` wizard. It is one full-screen
// program that stays open from the first step to the last: a step change swaps
// the screen, loading shows a spinner inside the same screen, esc goes back.
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

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/ghapp"
	"github.com/OvictorVieira/otto.code.reviewer/internal/github"
	"github.com/OvictorVieira/otto.code.reviewer/internal/picker"
	"github.com/OvictorVieira/otto.code.reviewer/internal/service"
)

const (
	cliDefault = ""
	// tiersFree and tiersPaid pick the recommended model for each size of pull request.
	tiersFree = "\x00tiers-free"
	tiersPaid = "\x00tiers-paid"
	allRepos  = "\x00all"
	typeModel = "\x00type"
)

// ui is a screen the wizard can show: a list, a text field or a notice.
type ui interface {
	Init() tea.Cmd
	Update(tea.Msg) (tea.Model, tea.Cmd)
	View() string
	Action() picker.Action
	Values() []string
}

// nested is a screen shown on top of a step (a text field after a list pick).
type nested struct {
	ui   ui
	done func(vals []string) *nested
}

type step struct {
	// root marks a screen that esc leaves for the menu, not for the screen before it.
	root bool
	// prep returns a label and a function when the step must fetch something
	// first; the wizard shows a spinner while it runs.
	prep func() (label string, fetch func())
	// show builds the step's screen, or returns a nil ui to skip the step.
	show func() (ui, func(vals []string) *nested)
}

// Deps are the outside world the wizard talks to, replaceable in tests.
type Deps struct {
	Catalog     func(name string) []engine.Model
	Efforts     func(name string) []string
	Installed   func(name string) bool
	ValidateApp func(ctx context.Context, id int64, keyPath string) (string, error)
	// Installations lists the accounts the GitHub App is installed on.
	Installations func(ctx context.Context, id int64, keyPath string) ([]ghapp.Installation, error)
}

func defaultDeps() Deps {
	return Deps{
		Catalog: engine.Catalog, Efforts: engine.Efforts, Installed: engine.Installed,
		ValidateApp: func(ctx context.Context, id int64, key string) (string, error) {
			return (&ghapp.Client{AppID: id, KeyPath: key}).Validate(ctx)
		},
		Installations: func(ctx context.Context, id int64, key string) ([]ghapp.Installation, error) {
			return (&ghapp.Client{AppID: id, KeyPath: key}).Installations(ctx)
		},
	}
}

type fetchedMsg struct{ step int }

// wizard is the bubbletea model of the whole setup.
type wizard struct {
	deps  Deps
	login string // the GitHub account whose repositories Otto reviews
	// installLink is where to install the app when the person continued without it being
	// installed; the summary printed after setup repeats it as selectable text.
	installLink, appSlug string
	cfg                  config.Config
	repos                []string
	previous             map[string]engine.Spec
	catalogs             map[string][]engine.Model
	service              bool

	// menu mode: a saved config opens a menu of what to change, and each change comes back to it.
	menu      bool
	finishing bool   // "Save and exit" was chosen
	menuLast  string // the menu row to open on
	chainLast string // the models row to open on

	steps  []step
	i, dir int

	cur     ui
	curDone func([]string) *nested
	nested  bool

	loading       string
	spin          spinner.Model
	width, height int
	finished      bool
	quit          bool
}

func newWizard(deps Deps, cfg config.Config, repos []string) *wizard {
	w := &wizard{
		deps: deps, cfg: cfg, repos: repos, service: true, dir: 1, width: 100, height: 30,
		previous: map[string]engine.Spec{}, catalogs: map[string][]engine.Model{},
		spin: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	for _, s := range cfg.Engines {
		w.previous[s.Name] = s
	}
	w.steps = []step{w.stepRepos(), w.stepEngines()}
	return w
}

// Init implements tea.Model.
func (w *wizard) Init() tea.Cmd { return w.enter(0) }

func (w *wizard) enter(i int) tea.Cmd {
	w.i = i
	if prep := w.steps[i].prep; prep != nil {
		if label, fetch := prep(); fetch != nil {
			w.loading, w.cur = label, nil
			return tea.Batch(w.spin.Tick, func() tea.Msg { fetch(); return fetchedMsg{i} })
		}
	}
	return w.display()
}

func (w *wizard) display() tea.Cmd {
	w.loading = ""
	u, done := w.steps[w.i].show()
	if u == nil {
		return w.advance(w.dir)
	}
	return w.set(u, done, false)
}

func (w *wizard) set(u ui, done func([]string) *nested, isNested bool) tea.Cmd {
	w.cur, w.curDone, w.nested = u, done, isNested
	u.Update(tea.WindowSizeMsg{Width: w.width, Height: w.height})
	return u.Init()
}

func (w *wizard) advance(dir int) tea.Cmd {
	w.dir = dir
	if w.quit {
		return tea.Quit
	}
	n := w.i + dir
	if n < 0 {
		w.quit = true // esc on the first step leaves the setup, like ctrl+c, saving nothing
		return tea.Quit
	}
	if n >= len(w.steps) {
		if w.menu && !w.finishing {
			return w.toMenu()
		}
		w.finished = true
		return tea.Quit
	}
	return w.enter(n)
}

// Update implements tea.Model.
func (w *wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
	case fetchedMsg:
		if msg.step == w.i && w.loading != "" {
			return w, w.display()
		}
		return w, nil
	case spinner.TickMsg:
		if w.loading != "" {
			var cmd tea.Cmd
			w.spin, cmd = w.spin.Update(msg)
			return w, cmd
		}
		return w, nil
	case tea.KeyMsg:
		if w.loading != "" {
			if msg.Type == tea.KeyCtrlC {
				w.quit = true
				return w, tea.Quit
			}
			return w, nil
		}
	}
	if w.cur == nil {
		return w, nil
	}

	_, cmd := w.cur.Update(msg)
	switch w.cur.Action() {
	case picker.Quit:
		w.quit = true
		return w, tea.Quit
	case picker.Back:
		if w.nested {
			return w, w.display() // rebuild the step the nested screen came from
		}
		if w.menu && w.steps[w.i].root {
			return w, w.toMenu()
		}
		return w, w.advance(-1)
	case picker.Picked:
		if w.curDone != nil {
			if nx := w.curDone(w.cur.Values()); nx != nil {
				return w, w.set(nx.ui, nx.done, true)
			}
		}
		return w, w.advance(1)
	}
	return w, cmd
}

// View implements tea.Model.
func (w *wizard) View() string {
	if w.loading != "" {
		return picker.Frame(w.spin.View()+" "+w.loading, w.width, w.height)
	}
	if w.cur == nil {
		return ""
	}
	return w.cur.View()
}

// ---- Steps -----------------------------------------------------------------

func owner(repo string) string { return repo[:strings.Index(repo, "/")] }

func orDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

// choice is a single-choice screen whose pick is handed to apply.
func choice(title string, items []picker.Item, selected, footer string, apply func(string)) (ui, func([]string) *nested) {
	m := picker.New(picker.Options{Title: title, Items: items, Selected: selected, Footer: footer})
	return m, func(vals []string) *nested {
		if len(vals) > 0 {
			apply(vals[0])
		}
		return nil
	}
}

func (w *wizard) stepRepos() step {
	return step{show: func() (ui, func([]string) *nested) {
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
		m := picker.New(picker.Options{
			Title: "Step 1 · Which repositories should Otto watch?", Items: items, Multi: true, Checked: checked,
			Footer: fmt.Sprintf("%d repositories", len(w.repos)),
		})
		return m, func(vals []string) *nested {
			w.cfg.Repos = config.Repos{}
			if slices.Contains(vals, allRepos) {
				w.cfg.Repos.All = true
			} else {
				w.cfg.Repos.Selected = vals
			}
			return nil
		}
	}}
}

func (w *wizard) stepEngines() step {
	return step{show: func() (ui, func([]string) *nested) {
		var items []picker.Item
		var checked []string
		for _, name := range engine.Known {
			tag := ""
			if !w.deps.Installed(name) {
				tag = "not installed"
			}
			items = append(items, picker.Item{Value: name, Title: name, Group: "AI engines", Tag: tag})
			if _, was := w.previous[name]; was {
				checked = append(checked, name)
			}
		}
		m := picker.New(picker.Options{
			Title: "Step 2 · Which AI engines can Otto use? (space selects, enter continues)",
			Items: items, Multi: true, Checked: checked, Footer: "space toggles · enter keeps the checked ones, or picks the row you are on",
		})
		return m, func(vals []string) *nested {
			w.cfg.Engines = nil
			for _, name := range vals {
				spec := w.previous[name]
				spec.Name = name
				w.cfg.Engines = append(w.cfg.Engines, spec)
			}
			w.steps = append(w.steps[:w.i+1], w.engineSteps()...)
			return nil
		}
	}}
}

// engineSteps are the steps that depend on which engines were picked.
func (w *wizard) engineSteps() []step {
	var s []step
	for i := range w.cfg.Engines {
		s = append(s, w.stepModel(i))
	}
	if len(w.cfg.Engines) > 1 {
		s = append(s, w.stepStrategy())
	}
	return append(s, w.stepLanguage(), w.stepIdentity(), w.stepService())
}

func (w *wizard) stepModel(i int) step {
	name := func() string { return w.cfg.Engines[i].Name }
	return step{
		prep: func() (string, func()) {
			n := name()
			if _, ok := w.catalogs[n]; ok {
				return "", nil
			}
			return "Loading " + n + " models…", func() { w.catalogs[n] = w.deps.Catalog(n) }
		},
		show: func() (ui, func([]string) *nested) {
			spec := &w.cfg.Engines[i]
			catalog := w.catalogs[spec.Name]

			var items []picker.Item
			for _, free := range []bool{true, false} {
				_, ok := engine.RecommendedTiers(spec.Name, free)
				if !ok {
					continue
				}
				title, value := "Recommended for each size", tiersPaid
				if free {
					title, value = "Recommended free models for each size", tiersFree
				}
				// The hint names no model, so typing a model's name never lands on this row.
				items = append(items, picker.Item{Value: value, Title: title, Group: "By pull request size",
					Hint: "a lighter model for small changes, a stronger one for large or risky ones"})
			}
			items = append(items, picker.Item{Value: cliDefault, Title: "CLI default", Hint: "whatever " + spec.Name + " is set to use", Group: "Default"})
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
			if len(catalog) == 0 || spec.Name == "copilot" {
				items = append(items, picker.Item{Value: typeModel, Title: "Type a model id…", Group: "Other"})
			}
			footer := fmt.Sprintf("%d models", len(catalog))
			if free > 0 {
				footer += fmt.Sprintf(" · %d free", free)
			}
			if slices.ContainsFunc(catalog, func(m engine.Model) bool { return m.Tier != "" }) {
				footer += " · Recommended is a pick, not a benchmark"
			}
			if len(catalog) == 0 {
				footer = spec.Name + " did not list any model"
			}

			set := func(model string) {
				if model == tiersFree || model == tiersPaid {
					spec.Tiers, _ = engine.RecommendedTiers(spec.Name, model == tiersFree)
					spec.Model = spec.Tiers.Medium
					return
				}
				spec.Model, spec.Tiers = model, engine.TierModels{}
			}
			m := picker.New(picker.Options{Title: spec.Name + " · select model", Items: items, Selected: spec.Model, Footer: footer})
			return m, func(vals []string) *nested {
				if len(vals) == 0 {
					return nil
				}
				if vals[0] != typeModel {
					set(vals[0])
					return nil
				}
				return &nested{
					ui:   picker.NewInput(spec.Name+" · model id", "model id"),
					done: func(typed []string) *nested { set(typed[0]); return nil },
				}
			}
		},
	}
}

func (w *wizard) stepStrategy() step {
	return step{show: func() (ui, func([]string) *nested) {
		items := []picker.Item{
			{Value: string(engine.RoundRobin), Title: "Rotate", Hint: "each review starts with the next engine, spreading usage across plans", Group: "Strategy"},
			{Value: string(engine.Priority), Title: "Priority", Hint: "always start with the first engine, use the others only if it fails", Group: "Strategy"},
		}
		return choice("When more than one engine is configured", items, string(w.cfg.Strategy), "",
			func(v string) { w.cfg.Strategy = engine.Strategy(v) })
	}}
}

func (w *wizard) stepLanguage() step {
	return step{show: func() (ui, func([]string) *nested) {
		items := []picker.Item{
			{Value: "English", Title: "English", Group: "Language"},
			{Value: "Brazilian Portuguese", Title: "Português (Brasil)", Group: "Language"},
		}
		return choice("Language of the review comments", items, w.cfg.Language, "", func(v string) { w.cfg.Language = v })
	}}
}

func (w *wizard) stepIdentity() step {
	return step{show: func() (ui, func([]string) *nested) {
		group := "Who posts a review asked for with " + w.cfg.Trigger
		items := []picker.Item{
			{Value: "user", Title: "As you", Hint: "reviews appear under your own GitHub account", Group: group},
			{Value: "app", Title: "As Otto", Hint: "a GitHub App, shown as <name>[bot]; the command line still posts as you", Group: group},
		}
		cur := "user"
		if w.cfg.App.ID != 0 {
			cur = "app"
		}
		m := picker.New(picker.Options{Title: "Which identity answers /otto review on GitHub?", Items: items, Selected: cur, Footer: "README: Post as Otto"})
		return m, func(vals []string) *nested {
			if len(vals) == 0 || vals[0] == "user" {
				w.cfg.App = config.GitHubApp{}
				return nil
			}
			return w.askAppID()
		}
	}}
}

func (w *wizard) askAppID() *nested {
	placeholder := "123456"
	if w.cfg.App.ID != 0 {
		placeholder = fmt.Sprint(w.cfg.App.ID)
	}
	return &nested{
		ui: picker.NewInput("GitHub App ID (github.com/settings/apps → your app → About)", placeholder),
		done: func(vals []string) *nested {
			var id int64
			if _, err := fmt.Sscan(vals[0], &id); err != nil || id <= 0 {
				return w.problem(fmt.Sprintf("%q is not an app id. It is the number under About on the app's page.", vals[0]))
			}
			return w.askKey(id)
		},
	}
}

func (w *wizard) askKey(id int64) *nested {
	placeholder := "~/.config/otto/otto.pem"
	if w.cfg.App.KeyPath != "" {
		placeholder = w.cfg.App.KeyPath
	}
	return &nested{
		ui: picker.NewInput("Path of the app's private key (.pem)", placeholder),
		done: func(vals []string) *nested {
			key := expandHome(vals[0])
			slug, err := w.deps.ValidateApp(context.Background(), id, key)
			if err != nil {
				return w.problem(err.Error())
			}
			w.cfg.App = config.GitHubApp{ID: id, KeyPath: key}
			return w.checkInstallation(id, key, slug)
		},
	}
}

const manageInstallationsURL = "https://github.com/settings/installations"

// installReminder is what the summary after setup says when the app was not confirmed installed.
func installReminder(link, slug string) string {
	if link == "" {
		return ""
	}
	return fmt.Sprintf("\nWarning: installation of %s not confirmed. Until it is installed Otto posts as you.\n  Install it (choose All repositories): %s\n", slug, picker.Link(link, link))
}

// checkInstallation asks GitHub where the app is installed. Otto can only post as the app on the
// accounts and repositories it is installed on, so a missing or partial installation is explained
// here, with the link to fix it, instead of being found later in a log.
func (w *wizard) checkInstallation(id int64, key, slug string) *nested {
	installs, err := w.deps.Installations(context.Background(), id, key)
	w.installLink, w.appSlug = "", slug
	link := func(title, body, url string) *nested {
		return &nested{ui: picker.NewLinkNotice(title, body, url, "check again", "continue anyway"), done: func(vals []string) *nested {
			if len(vals) > 0 && vals[0] == "skip" {
				w.installLink = ghapp.InstallURL(slug)
				return nil
			}
			return w.checkInstallation(id, key, slug)
		}}
	}
	if err != nil {
		return link("Could not check the installation",
			fmt.Sprintf("GitHub did not say where %s is installed (%v). Otto can only post as %s[bot] where the app is installed. If you already installed it, you can continue.", slug, err, slug),
			ghapp.InstallURL(slug))
	}

	var elsewhere []string
	for _, in := range installs {
		if strings.EqualFold(in.Account, w.login) {
			if in.AllRepositories {
				return &nested{ui: picker.NewNotice("Connected", fmt.Sprintf("Otto will post as %s[bot]. The app is installed on %s, on all repositories.", slug, in.Account)), done: func([]string) *nested { return nil }}
			}
			return link("The app sees only some repositories",
				fmt.Sprintf("%s is installed on %s, but only on the repositories picked at install time. In any other repository Otto posts as you.\n\nTo cover every repository, open the link, click Configure next to %s and choose All repositories.", slug, in.Account, slug),
				manageInstallationsURL)
		}
		elsewhere = append(elsewhere, in.Account)
	}
	body := fmt.Sprintf("%s is not installed on %s yet, and Otto cannot post as %s[bot] until it is: it would post as you instead.", slug, w.login, slug)
	if len(elsewhere) > 0 {
		body += fmt.Sprintf(" It is installed on %s, but not on %s.", strings.Join(elsewhere, ", "), w.login)
	}
	body += fmt.Sprintf("\n\nOpen the link, pick %s and choose All repositories: one install covers every repository, including new ones. Then come back and press enter.", w.login)
	return link("Install the app on "+w.login, body, ghapp.InstallURL(slug))
}

// problem shows what went wrong and, on enter, asks for the app id again.
func (w *wizard) problem(msg string) *nested {
	return &nested{ui: picker.NewNotice("That did not work", msg), done: func([]string) *nested { return w.askAppID() }}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (w *wizard) stepService() step {
	return step{show: func() (ui, func([]string) *nested) {
		items := []picker.Item{
			{Value: "yes", Title: "Yes, run in the background", Hint: "starts at login and answers " + w.cfg.Trigger, Group: "Background service"},
			{Value: "no", Title: "No, I will run `otto review` myself", Group: "Background service"},
		}
		return choice("Start Otto in the background now and at every login?", items, "yes", "", func(v string) { w.service = v == "yes" })
	}}
}

// ---- Entry point -------------------------------------------------------------

// Run opens the setup screen, and when it closes saves the result and prints a summary.
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

	cfg, menu := openingConfig(config.Load)
	cfg.AllowedAuthors = []string{login}

	w := newWizard(defaultDeps(), cfg, repos)
	w.login = login
	if menu {
		w.enableMenu()
	}
	if _, err := tea.NewProgram(w, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		return err
	}
	if w.quit || !w.finished {
		return picker.ErrInterrupted
	}

	path, err := config.Save(w.cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved %s\n\n", path)
	if w.cfg.Repos.All {
		fmt.Fprintln(out, "  watching   all repositories")
	} else {
		fmt.Fprintf(out, "  watching   %d repositories\n", len(w.cfg.Repos.Selected))
	}
	for _, e := range w.cfg.Engines {
		fmt.Fprintf(out, "  engine     %-9s model %s · effort auto (by pull request size)\n", e.Name, orDefault(e.Model))
	}
	if len(w.cfg.Engines) > 1 {
		fmt.Fprintf(out, "  strategy   %s\n", w.cfg.Strategy)
	}
	fmt.Fprintf(out, "  language   %s\n", w.cfg.Language)
	if w.cfg.App.ID != 0 {
		fmt.Fprintf(out, "  posts as   GitHub App %d for `%s`\n", w.cfg.App.ID, w.cfg.Trigger)
	} else {
		fmt.Fprintf(out, "  posts as   you\n")
	}
	fmt.Fprint(out, installReminder(w.installLink, w.appSlug))
	if w.service {
		logPath, err := service.Install()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\nOtto is watching in the background. If it was already running, it loads this config by itself once the review in progress finishes. Logs: %s\n", logPath)
	}
	fmt.Fprintf(out, "\nDone. Comment `%s` on a pull request, or run `otto review <pr-url>`.\n", w.cfg.Trigger)
	return nil
}
