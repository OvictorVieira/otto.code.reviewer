// Package setup is the interactive `otto setup` wizard.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/github"
	"github.com/OvictorVieira/otto.code.reviewer/internal/service"
)

const (
	cliDefault = ""
	custom     = "\x00custom"
)

func ask(fields ...huh.Field) error { return huh.NewForm(huh.NewGroup(fields...)).Run() }

// Run walks the user through every setting and saves the config.
func Run(ctx context.Context, out io.Writer) error {
	login, err := github.Login(ctx)
	if err != nil {
		return fmt.Errorf("GitHub CLI is not logged in. Run `gh auth login` first, then `otto setup` again (%w)", err)
	}
	fmt.Fprintf(out, "GitHub: logged in as %s. Reviews will be posted as you.\n\n", login)

	cfg := config.Default()
	if old, err := config.Load(); err == nil {
		cfg = old
	}
	cfg.AllowedAuthors = []string{login}

	if err := chooseRepos(ctx, out, &cfg); err != nil {
		return err
	}
	if err := chooseEngines(&cfg); err != nil {
		return err
	}

	strategy := string(cfg.Strategy)
	if err := ask(huh.NewSelect[string]().
		Title("When more than one engine is configured").
		Options(
			huh.NewOption("Rotate: each review starts with the next engine (spreads usage across plans)", string(engine.RoundRobin)),
			huh.NewOption("Priority: always start with the first engine, use the others only if it fails", string(engine.Priority)),
		).Value(&strategy)); err != nil {
		return err
	}
	cfg.Strategy = engine.Strategy(strategy)

	if err := ask(
		huh.NewConfirm().
			Title("Run the project during reviews?").
			Description("Install, build, run tests and probe scenarios inside a disposable worktree. Recommended.").
			Affirmative("Yes").Negative("No, only read the code").Value(&cfg.Verify),
		huh.NewInput().Title("Trigger comment").Value(&cfg.Trigger),
		huh.NewSelect[string]().Title("Language of the review comments").
			Options(huh.NewOption("English", "English"), huh.NewOption("Português (Brasil)", "Brazilian Portuguese")).
			Value(&cfg.Language),
	); err != nil {
		return err
	}

	path, err := config.Save(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nSaved %s\n", path)

	install := true
	if err := ask(huh.NewConfirm().
		Title("Start Otto in the background now and at every login?").
		Description("Installs a launchd agent that watches your repositories for the trigger comment.").
		Value(&install)); err != nil {
		return err
	}
	if install {
		logPath, err := service.Install()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Otto is watching. Logs: %s\n", logPath)
	}
	fmt.Fprintf(out, "\nDone. Comment `%s` on a pull request, or run `otto review <pr-url>`.\n", cfg.Trigger)
	return nil
}

func chooseRepos(ctx context.Context, out io.Writer, cfg *config.Config) error {
	fmt.Fprintln(out, "Loading your repositories...")
	repos, err := github.Repos(ctx)
	if err != nil {
		return err
	}
	mode := "pick"
	if cfg.Repos.All {
		mode = "all"
	}
	if err := ask(huh.NewSelect[string]().
		Title(fmt.Sprintf("Which repositories should Otto watch? (%d found)", len(repos))).
		Options(
			huh.NewOption("Let me pick", "pick"),
			huh.NewOption("All of them, including new ones", "all"),
		).Value(&mode)); err != nil {
		return err
	}
	cfg.Repos.All = mode == "all"
	if cfg.Repos.All {
		cfg.Repos.Selected = nil
		return nil
	}

	var options []huh.Option[string]
	for _, r := range repos {
		options = append(options, huh.NewOption(r, r).Selected(slices.Contains(cfg.Repos.Selected, r)))
	}
	selected := cfg.Repos.Selected
	if err := ask(huh.NewMultiSelect[string]().
		Title("Pick repositories (space to toggle, / to filter, enter to confirm)").
		Options(options...).Filterable(true).Height(18).Value(&selected)); err != nil {
		return err
	}
	if len(selected) == 0 {
		return errors.New("no repository selected")
	}
	cfg.Repos.Selected = selected
	return nil
}

func chooseEngines(cfg *config.Config) error {
	previous := map[string]engine.Spec{}
	for _, s := range cfg.Engines {
		previous[s.Name] = s
	}

	var options []huh.Option[string]
	for _, name := range engine.Known {
		label := name
		installed := engine.Installed(name)
		if !installed {
			label += " (not installed)"
		}
		_, wasOn := previous[name]
		options = append(options, huh.NewOption(label, name).Selected(wasOn || (len(previous) == 0 && installed)))
	}
	var names []string
	if err := ask(huh.NewMultiSelect[string]().
		Title("Which AI engines can Otto use? They run with your own subscriptions.").
		Options(options...).Value(&names)); err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("pick at least one engine")
	}

	cfg.Engines = nil
	for _, name := range names {
		spec := previous[name]
		spec.Name = name
		model, efforts, err := pickModel(name, spec.Model)
		if err != nil {
			return err
		}
		spec.Model = model
		effort, err := pickEffort(name, model, efforts, spec.Effort)
		if err != nil {
			return err
		}
		spec.Effort = effort
		cfg.Engines = append(cfg.Engines, spec)
	}
	return nil
}

// pickModel shows the engine's live model list (searchable) and returns the
// chosen model with the reasoning levels it accepts.
func pickModel(name, current string) (string, []string, error) {
	fmt.Printf("Loading %s models...\n", name)
	catalog := engine.Catalog(name)

	free := 0
	options := []huh.Option[string]{huh.NewOption("CLI default", cliDefault)}
	efforts := map[string][]string{cliDefault: engine.Efforts(name)}
	known := false
	for _, m := range catalog {
		options = append(options, huh.NewOption(m.Label, m.ID))
		efforts[m.ID] = m.Efforts
		known = known || m.ID == current
		if m.Free {
			free++
		}
	}
	if current != "" && !known {
		options = append(options, huh.NewOption(current+" (current)", current))
		efforts[current] = engine.Efforts(name)
	}
	options = append(options, huh.NewOption("Type a model id…", custom))

	title := fmt.Sprintf("%s: model", name)
	switch {
	case len(catalog) == 0:
		title += " (the CLI did not list any; pick its default or type one)"
	case free > 0:
		title += fmt.Sprintf(" (%d available, %d free listed first · press / to search)", len(catalog), free)
	default:
		title += fmt.Sprintf(" (%d available · press / to search)", len(catalog))
	}

	model := current
	if err := ask(huh.NewSelect[string]().Title(title).Options(options...).
		Height(min(len(options)+2, 18)).Value(&model)); err != nil {
		return "", nil, err
	}
	if model != custom {
		return model, efforts[model], nil
	}
	model = ""
	if err := ask(huh.NewInput().Title(name + ": model id").Value(&model)); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(model), engine.Efforts(name), nil
}

func pickEffort(name, model string, levels []string, current string) (string, error) {
	if len(levels) == 0 {
		return "", nil
	}
	options := []huh.Option[string]{huh.NewOption("CLI default", cliDefault)}
	for _, l := range levels {
		options = append(options, huh.NewOption(l, l))
	}
	effort := current
	if !slices.Contains(levels, effort) {
		effort = cliDefault
	}
	title := name + ": reasoning effort"
	if model != "" {
		title += " for " + model
	}
	err := ask(huh.NewSelect[string]().Title(title).Options(options...).Value(&effort))
	return effort, err
}
