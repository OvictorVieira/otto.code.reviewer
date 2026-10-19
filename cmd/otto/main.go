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
	"strings"
	"syscall"

	"github.com/OvictorVieira/otto.code.reviewer/internal/app"
	"github.com/OvictorVieira/otto.code.reviewer/internal/config"
	"github.com/OvictorVieira/otto.code.reviewer/internal/service"
	"github.com/OvictorVieira/otto.code.reviewer/internal/setup"
	"github.com/OvictorVieira/otto.code.reviewer/internal/update"
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
