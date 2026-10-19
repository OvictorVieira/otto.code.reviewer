// Command otto is a local code reviewer that answers "/otto review" on your
// pull requests using the AI coding CLI you already pay for.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/OvictorVieira/otto.code.reviewer/internal/app"
	"github.com/OvictorVieira/otto.code.reviewer/internal/service"
	"github.com/OvictorVieira/otto.code.reviewer/internal/setup"
	"github.com/OvictorVieira/otto.code.reviewer/internal/workspace"
)

var version = "dev"

const usage = `otto: local code reviewer for GitHub pull requests

Usage:
  otto setup                          configure repositories, engines and the background service
  otto review <pr-url> [flags]        review one pull request now
      --engine claude|codex|cursor|opencode   use only this engine
      --dry-run                       print the review instead of posting it
      --focus "text"                  extra instructions for the reviewer
  otto watch [--once]                 poll repositories and answer the trigger comment
  otto service install|uninstall|status
  otto clean                          delete cached clones and worktrees
  otto version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
		return a.Watch(ctx, *once)
	case "service":
		return serviceCmd(args[1:])
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
	report, err := a.Review(ctx, repo, number, app.Options{Engine: *engineName, DryRun: *dryRun, Instructions: *focus})
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
