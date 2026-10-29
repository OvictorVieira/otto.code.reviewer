// Command otto is a local code reviewer that answers "/otto review" on your
// pull requests using the AI coding CLI you already pay for.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/app"
	_ "github.com/OvictorVieira/otto.code.reviewer/internal/boot" // must initialize before bubbletea
	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
	"github.com/OvictorVieira/otto.code.reviewer/internal/eval"
	reviewprogress "github.com/OvictorVieira/otto.code.reviewer/internal/progress"
	"github.com/OvictorVieira/otto.code.reviewer/internal/service"
	"github.com/OvictorVieira/otto.code.reviewer/internal/setup"
	"github.com/OvictorVieira/otto.code.reviewer/internal/trigger"
	"github.com/OvictorVieira/otto.code.reviewer/internal/update"
	"github.com/OvictorVieira/otto.code.reviewer/internal/workspace"
)

var version = "dev"

const usage = `otto: local code reviewer for GitHub pull requests

Usage:
  otto setup                          configure repositories, engines and the background service
  otto review <pr-url> [flags]        review one pull request now
      --engine claude|codex|cursor|opencode|copilot   use only this engine
      --dry-run                       print the review instead of posting it
      --focus "text"                  extra instructions for the reviewer
  otto watch [--once]                 poll repositories and answer the trigger comment
  otto queue                          list review requests seen but not finished (waiting, running, retrying)
  otto learn [owner/repo ...]         read maintainer feedback now and update what Otto learned
  otto lessons <owner/repo> [pin|unpin|disable|enable|forget <id>]
                                      show or edit what Otto learned about a repository
  otto eval models [flags] [model ...]  evaluate models against three planted review bugs
      --engine opencode|claude|codex|cursor|copilot   engine to evaluate (default: opencode)
      --timeout 4m                   time limit per model
      --parallel 4                   maximum simultaneous models
  otto service install|uninstall|status
  otto update                         check GitHub and install the latest release
  otto clean                          delete cached clones and worktrees
  otto version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	maybeUpdate(ctx, os.Args[1:])
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "otto:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "setup":
		return setup.Run(ctx, os.Stdout)
	case "review":
		return review(ctx, args[1:])
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ContinueOnError)
		once := fs.Bool("once", false, "poll once and exit")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		a, err := app.New(os.Stdout)
		if err != nil {
			return err
		}
		if u := newUpdater(); u != nil {
			a.Upgrade = func(ctx context.Context) (bool, error) {
				tag, ok := u.Available(ctx, true)
				if !ok {
					return false, nil
				}
				return true, u.Install(ctx, tag)
			}
		}
		return a.Watch(ctx, *once)
	case "queue":
		dir, err := config.Dir()
		if err != nil {
			return err
		}
		st, err := trigger.LoadState(filepath.Join(dir, "state.json"))
		if err != nil {
			return err
		}
		fmt.Print(app.QueueReport(st.Pending, time.Now()))
		return nil
	case "learn":
		return learnCmd(ctx, args[1:])
	case "lessons":
		return lessonsCmd(args[1:])
	case "eval":
		return evalCmd(ctx, args[1:])
	case "service":
		return serviceCmd(args[1:])
	case "update":
		return updateNow(ctx)
	case "clean":
		if err := workspace.Purge(); err != nil {
			return err
		}
		fmt.Println("Removed cached clones and worktrees.")
		return nil
	case "version", "--version", "-v":
		fmt.Println("otto", version)
		return nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
}

func evalCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "models" {
		return fmt.Errorf("usage: otto eval models [--engine opencode] [--timeout 4m] [--parallel 4] [model ...]")
	}
	fs := flag.NewFlagSet("eval models", flag.ContinueOnError)
	engineName := fs.String("engine", "opencode", "model engine")
	timeout := fs.Duration("timeout", 4*time.Minute, "time limit per model")
	parallel := fs.Int("parallel", 4, "maximum simultaneous models")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !slices.Contains(engine.Known, *engineName) {
		return fmt.Errorf("unknown engine %q", *engineName)
	}
	if *timeout <= 0 || *parallel <= 0 {
		return fmt.Errorf("timeout and parallel must be positive")
	}
	models := fs.Args()
	if len(models) == 0 {
		if t, ok := engine.RecommendedTiers(*engineName, true); ok {
			models = []string{t.Light, t.Medium, t.Heavy}
		} else {
			return fmt.Errorf("specify one or more model IDs for %s (no default free set)", *engineName)
		}
	}
	fmt.Fprintf(os.Stdout, "Evaluating %d %s model(s) with Otto's review prompt...\n", len(models), *engineName)
	results, err := eval.Run(ctx, *engineName, models, eval.Options{Timeout: *timeout, Parallel: *parallel, Progress: os.Stdout})
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, eval.Table(results))
	return nil
}

func review(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	engineName := fs.String("engine", "", "use only this engine")
	dryRun := fs.Bool("dry-run", false, "print instead of posting")
	focus := fs.String("focus", "", "extra instructions")

	// Accept flags before or after the pull request reference.
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("missing pull request reference\n\n%s", usage)
	}
	ref := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return err
	}

	repo, number, err := app.ParseRef(ref)
	if err != nil {
		return err
	}
	a, err := app.New(os.Stdout)
	if err != nil {
		return err
	}
	var display *reviewprogress.Review
	if interactive() {
		display, err = reviewprogress.Start(os.Stdout)
		if err != nil {
			return fmt.Errorf("start review progress display: %w", err)
		}
	}
	options := app.Options{
		Engine: *engineName, DryRun: *dryRun, Instructions: *focus, ShowProgress: true, PostQuotaFailure: true,
	}
	if display != nil {
		options.Progress = display.Update
		options.ProgressDone = display.Stop
	}
	report, err := a.Review(ctx, repo, number, options)
	if err != nil {
		return err
	}
	if !report.Posted && !*dryRun {
		return fmt.Errorf("review was not posted")
	}
	return nil
}

func serviceCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: otto service install|uninstall|status")
	}
	switch args[0] {
	case "install":
		logPath, err := service.Install()
		if err != nil {
			return err
		}
		fmt.Println("Otto is running in the background. Logs:", logPath)
	case "uninstall":
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Background service removed.")
	case "status":
		running, logPath := service.Status()
		if running {
			fmt.Println("running · logs:", logPath)
		} else {
			fmt.Println("not running")
		}
	default:
		return fmt.Errorf("usage: otto service install|uninstall|status")
	}
	return nil
}

func newUpdater() *update.Updater {
	if os.Getenv("OTTO_NO_UPDATE") != "" {
		return nil
	}
	dir, err := config.Dir()
	if err != nil {
		return nil
	}
	return &update.Updater{Current: version, StatePath: filepath.Join(dir, "update.json")}
}

func updateNow(ctx context.Context) error {
	dir, err := config.Dir()
	if err != nil {
		return fmt.Errorf("find Otto configuration directory: %w", err)
	}
	u := &update.Updater{Current: version, StatePath: filepath.Join(dir, "update.json")}
	latest, err := u.LatestVersion(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(version, latest) {
		if version == "dev" {
			return fmt.Errorf("this is a development build; latest published release is %s", latest)
		}
		fmt.Printf("Otto is up to date (%s; latest: %s).\n", version, latest)
		return nil
	}

	fmt.Printf("Updating Otto %s → %s...\n", version, latest)
	if err := u.Install(ctx, latest); err != nil {
		return fmt.Errorf("install Otto %s: %w", latest, err)
	}
	fmt.Printf("Updated to %s. Restarting...\n", latest)
	fmt.Println("A background Otto restarts on its own once the review in progress, if any, is finished.")
	if err := update.Restart(); err != nil {
		return fmt.Errorf("Otto was updated to %s but could not restart; run `otto update` again: %w", latest, err)
	}
	return nil
}

func interactive() bool {
	in, err1 := os.Stdin.Stat()
	out, err2 := os.Stdout.Stat()
	return err1 == nil && err2 == nil && in.Mode()&os.ModeCharDevice != 0 && out.Mode()&os.ModeCharDevice != 0
}

// maybeUpdate offers a newer release when a person runs Otto by hand.
// The background watcher updates on its own and never asks.
func maybeUpdate(ctx context.Context, args []string) {
	if len(args) == 0 || !interactive() {
		return
	}
	switch args[0] {
	case "setup", "review", "clean", "service":
	default:
		return // watch updates itself; version and help stay instant
	}
	u := newUpdater()
	if u == nil {
		return
	}
	tag, ok := u.Available(ctx, false)
	if !ok {
		return
	}
	fmt.Printf("Otto %s is available (you have %s). Update now? [Y/n] ", tag, version)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "" && a != "y" && a != "yes" {
		u.Decline(tag)
		fmt.Println("Skipping. I will ask again tomorrow, or run the installer to update.")
		return
	}
	if err := u.Install(ctx, tag); err != nil {
		fmt.Fprintln(os.Stderr, "otto: update failed, continuing with the current version:", err)
		return
	}
	fmt.Println("Updated to", tag)
	if err := update.Restart(); err != nil {
		fmt.Fprintln(os.Stderr, "otto: restart failed, run the command again:", err)
		os.Exit(0)
	}
}

func learnCmd(ctx context.Context, repos []string) error {
	a, err := app.New(os.Stdout)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		repos = a.Cfg.Repos.Selected
	}
	if len(repos) == 0 {
		return fmt.Errorf("name a repository: otto learn owner/repo")
	}
	for _, repo := range repos {
		sum, err := a.LearnRepo(ctx, repo, true)
		if err != nil {
			return fmt.Errorf("%s: %w", repo, err)
		}
		switch {
		case sum.Distill:
			fmt.Printf("%s: learned from %d signal(s); %d lesson(s)\n", repo, sum.Signals, sum.Lessons)
		default:
			fmt.Printf("%s: nothing new to learn; %d lesson(s)\n", repo, sum.Lessons)
		}
	}
	return nil
}

func lessonsCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: otto lessons <owner/repo> [pin|unpin|disable|enable|forget <id>]")
	}
	a, err := app.New(os.Stdout)
	if err != nil {
		return err
	}
	repo := args[0]
	if len(args) >= 3 {
		return a.EditLesson(repo, args[2], args[1])
	}
	lessons, err := a.Lessons(repo)
	if err != nil {
		return err
	}
	if len(lessons) == 0 {
		fmt.Printf("%s: nothing learned yet. Otto learns from reactions and replies on its findings.\n", repo)
		return nil
	}
	for _, l := range lessons {
		flags := ""
		if l.Pinned {
			flags += " pinned"
		}
		if l.Disabled {
			flags += " disabled"
		}
		scope := ""
		if l.Scope != "" {
			scope = " " + l.Scope
		}
		fmt.Printf("%s  [%s]%s%s  (%d finding(s))\n    %s\n", l.ID, l.Kind, scope, flags, l.Evidence, l.Rule)
		if l.Why != "" {
			fmt.Printf("    why: %s\n", l.Why)
		}
	}
	return nil
}
