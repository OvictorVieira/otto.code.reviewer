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
		model, err := pickModel(name, spec.Model)
		if err != nil {
			return err
		}
		spec.Model = model
		effort, err := pickEffort(name, model, spec.Effort)
		if err != nil {
			return err
		}
		spec.Effort = effort
		cfg.Engines = append(cfg.Engines, spec)
	}
	return nil
}

func pickModel(name, current string) (string, error) {
	models := engine.Models(name)
	options := []huh.Option[string]{huh.NewOption("CLI default", cliDefault)}
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}
	if current != "" && !slices.Contains(models, current) {
		options = append(options, huh.NewOption(current, current))
	}
	options = append(options, huh.NewOption("Type another model…", custom))

	model := current
	if err := ask(huh.NewSelect[string]().Title(name + ": model").Options(options...).
		Filtering(len(options) > 10).Height(min(len(options)+2, 16)).Value(&model)); err != nil {
		return "", err
	}
	if model != custom {
		return model, nil
	}
	model = ""
	err := ask(huh.NewInput().Title(name + ": model id").Value(&model))
	return strings.TrimSpace(model), err
}

func pickEffort(name, model, current string) (string, error) {
	levels := engine.Efforts(name, model)
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
	err := ask(huh.NewSelect[string]().Title(name + ": reasoning effort").Options(options...).Value(&effort))
	return effort, err
}
