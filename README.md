# Otto

A local code reviewer for GitHub pull requests that uses the AI coding CLI you already pay for.

Comment `/otto review` on a pull request. Otto, running on your own machine, checks the branch out in a
disposable worktree, has your agent review it like a senior engineer (reading the spec, running the tests,
probing edge cases), and posts the review as you: colored `P0`–`P3` badges, inline comments, a verdict,
and what it verified.

Works with **Claude Code**, **Codex**, **Cursor** and **OpenCode**. Rotate between them to spread usage across
plans, with automatic fallback when one fails or hits a limit.

## Requirements

- macOS (the background service uses launchd; `otto review` works anywhere Go and the CLIs run)
- [GitHub CLI](https://cli.github.com) logged in: `gh auth login`
- At least one engine CLI installed and logged in: `claude`, `codex`, `cursor-agent` or `opencode`
- Go 1.25+ only to build from source

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/OvictorVieira/otto.code.reviewer/main/install.sh | sh
```

The installer downloads the release binary for your OS and CPU, verifies its checksum, installs the GitHub CLI
with Homebrew if it is missing, logs you in to GitHub, lists the AI engines it found, and starts `otto setup`.

```sh
# a specific version, or skip the setup step
curl -fsSL https://raw.githubusercontent.com/OvictorVieira/otto.code.reviewer/main/install.sh | sh -s -- --version v0.1.0 --no-setup

# remove Otto (binary, background service, cache); --purge also removes the config
curl -fsSL https://raw.githubusercontent.com/OvictorVieira/otto.code.reviewer/main/install.sh | sh -s -- --uninstall --purge
```

From source: `go install github.com/OvictorVieira/otto.code.reviewer/cmd/otto@latest`.

`otto setup` asks which repositories to watch (pick from a checklist or choose all), which engines to use,
the model and reasoning effort for each, the rotation strategy, the trigger phrase and the review language.
It can then install Otto as a background service that starts at login.

## Use

```sh
# On GitHub: comment on any pull request in a watched repository
/otto review
/otto review focus on the payment retry logic

# From the terminal, on any pull request you can access
otto review https://github.com/owner/repo/pull/123
otto review owner/repo#123 --engine codex --dry-run

otto service status    # is the background watcher running?
otto clean             # delete cached clones and worktrees
```

While a review runs, Otto reacts 👀 to the trigger comment. A clean review gets 👍. A failure gets 😕 and a
comment explaining why.

## How it works

1. **Trigger.** The watcher polls the selected repositories every minute for a new comment starting with the
   trigger phrase, written by you. Handled comments are remembered, so a restart never repeats or loses a request:
   if the machine was off, pending requests are picked up when it comes back.
2. **Checkout.** Each repository is cloned once into a cache. Every review gets its own worktree at the pull
   request head, deleted when the review ends.
3. **Review.** The engine gets one harness prompt (`internal/review/prompt.md`) that works the same on every CLI:
   understand the spec (description and linked issues) and the repository rules (`AGENTS.md`, `CLAUDE.md`,
   `CONTRIBUTING.md`...), install and run the tests, exercise the scenarios, then review on two separate axes:
   **Spec** (missing requirements, scope creep, wrong behavior) and **Standards** (documented rules, plus a few
   code smells as judgement calls). Over-engineering counts as a problem; trivial style does not.
4. **Post.** The JSON answer is validated against the diff. Findings on changed lines become inline comments;
   the rest go in the summary. The review is posted with your `gh` login.

## Safety

- Engines run **inside the disposable worktree** with file-editing tools disabled, and the harness forbids commits,
  pushes and touching remotes. With verification on, they can run commands (install, test, start the app).
- Every process an engine starts is killed when the review ends. Worktrees are deleted, and leftovers from a crash
  are swept on the next start.
- Only comments written by you trigger a review. Running a pull request's code executes it on your machine:
  watch repositories whose contributors you trust. Turn verification off in `otto setup` to only read the code.

## Configuration

`~/.config/otto/config.yaml` (set `OTTO_HOME` to use another directory):

```yaml
trigger: /otto review
language: English
strategy: round-robin        # or: priority
verify: true                 # run the project during reviews
min_confidence: 0.3          # drop findings the engine is unsure about
poll_interval: 1m0s
lookback: 24h0m0s            # how far back to look the first time a repo is watched
timeout: 30m0s
repos:
  all: false
  selected: [owner/repo]
engines:
  - name: claude
    model: sonnet
    effort: high
  - name: codex
    model: gpt-6-luna
    effort: high
```

## License

[Functional Source License 1.1, ALv2 Future License](LICENSE.md): free to use, modify and self-host;
you may not offer it as a competing commercial product. Each release becomes Apache 2.0 two years after it is published.

The review rubric is adapted from [OpenAI Codex](https://github.com/openai/codex) (Apache-2.0). See [NOTICE](NOTICE).
