# Otto

[![ci](https://github.com/OvictorVieira/otto.code.reviewer/actions/workflows/ci.yml/badge.svg)](https://github.com/OvictorVieira/otto.code.reviewer/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/OvictorVieira/otto.code.reviewer)](https://github.com/OvictorVieira/otto.code.reviewer/releases)
[![license: FSL-1.1-ALv2](https://img.shields.io/badge/license-FSL--1.1--ALv2-blue)](LICENSE.md)

**Senior-level code review on your pull requests, running on your machine, with the AI access you already use.**

Comment `/otto review` on a pull request. Otto checks the branch out, reads the spec and changed code, checks CI
results, and posts the review as you: colored `P0`–`P3` findings on the exact lines, a verdict, and the evidence it
could verify from the repository and CI.

It works with **Claude Code**, **Codex**, **Cursor**, **OpenCode** and **GitHub Copilot CLI**. Use one, or rotate between them so no single
plan hits its limit, with automatic fallback when one fails.

## Installation (60-second setup)

### 1. Install Otto

```bash
curl -fsSL https://raw.githubusercontent.com/OvictorVieira/otto.code.reviewer/main/install.sh | sh
```

The installer downloads the binary for your OS and CPU, verifies its checksum, installs the GitHub CLI with
Homebrew if it is missing, logs you in to GitHub, and tells you which AI engines it found.

### 2. Have at least one engine logged in

<details>
<summary><strong>Claude Code</strong></summary>

```bash
curl -fsSL https://claude.ai/install.sh | bash
claude auth login
```

Models: `opus`, `sonnet`, `haiku`. Effort: `low`, `medium`, `high`, `xhigh`, `max`.

</details>

<details>
<summary><strong>Codex</strong></summary>

```bash
npm install -g @openai/codex
codex login
```

Otto lists the models your Codex CLI knows about, and the effort levels each one supports.

</details>

<details>
<summary><strong>Cursor</strong></summary>

```bash
curl https://cursor.com/install -fsS | bash
cursor-agent login
```

Cursor has no separate effort setting: pick a model whose name includes it, such as a `-thinking` variant.

</details>

<details>
<summary><strong>OpenCode</strong></summary>

```bash
curl -fsSL https://opencode.ai/install | bash
opencode auth login
```

Models use the `provider/model` format from `opencode models`. Effort maps to OpenCode's `--variant`.

</details>

<details>
<summary><strong>GitHub Copilot CLI</strong></summary>

```bash
npm install -g @github/copilot
copilot login
```

`otto setup` lists Copilot's supported premium models, and you can also enter a model ID not shown in the list or use
the CLI default. Premium-model access depends on your GitHub plan, organization policy, and Copilot CLI version; choosing
a model does not guarantee access. If the CLI rejects the model or your account cannot use it, Otto reports that error
and tries configured fallback engines. Otto cannot bypass usage limits. For manual reviews, Otto posts reset details
returned by the provider if all engines fail; it never guesses a reset time.

</details>

### 3. Run `otto setup`

The installer starts it for you. It asks:

- which repositories to watch: pick from a checklist, or watch all of them (new ones included)
- which engines and models to use
- whether to rotate between engines or always start with the first
- the trigger phrase and the language of the comments

It can then install Otto as a background service that starts at login.

**Running `otto setup` again opens a menu** instead of repeating every question: pick only what you want to change
(models and fallback, repositories, who posts the reviews, language, the background
service) and come back to the menu after each change. Choose **Save and exit** to keep the changes, or leave with esc
to discard them. Only the very first setup walks through everything. A running Otto reloads the saved config by itself
after the review in progress.

**Reading the model list.** The models are read live from each engine, so new ones appear without an Otto release. At the
top is a **Recommended** section with one model per size of review (light for small or quick pull requests, medium for
everyday ones, heavy for large or risky changes such as auth, payments or concurrency), then every maker (Anthropic,
OpenAI, DeepSeek, Google...) in alphabetical order, free models first and then from the cheapest to the dearest. Prices are
shown only when the engine's own catalog gives one. The paid recommendations are maintainer choices; the OpenCode
free recommendations passed `otto eval models` on the planted-bug fixture. The picks and their evidence live in
`internal/engine/recommended.go`.

**Fallback models.** Under **Models and fallback** the models are a list in the order they are tried. Add another model
(from another engine, or another model of the same engine), move it up or down, change its model, or remove
it. With the **Priority** order the first model does every review and the others are used only when it fails; with
**Rotate** they take turns. Otto determines light, medium or heavy effort from the changed code, tests, contracts and
sensitive paths. A maintainer can request `/otto review heavy` to force the size.

### 4. Bam. Comment `/otto review` on a pull request.

Otto reacts 👀 within a minute, and posts the review when it is done.

## What a review looks like

Each finding lands on the changed line, like this:

> **<sub><sub>![P1 Badge](https://img.shields.io/badge/P1-orange?style=flat)</sub></sub>  Reserve stock atomically**
>
> Two orders for the last unit can both read `saldo = 1` and both succeed, because the check and the update run as
> separate queries. The code does not synchronize the check and update. Use a
> single `UPDATE ... SET saldo = saldo - 1 WHERE saldo >= 1` and treat zero affected rows as out of stock.

And the summary tells you what was checked:

> ### 💡 Otto Review
> **Reviewed commit:** `9763fcd410`
> **Engine:** `claude` · model `sonnet` · effort `high`
> **Verdict:** patch is incorrect. Concurrent reservations can oversell.
>
> #### Verification
> - ✅ CI test check passed at the reviewed commit
> - ❌ CI build check failed; see the attached, redacted failure excerpt
> - ⏭️ No CI check covers concurrent reservation
>
> #### Spec
> - 🟢 Reserve stock when the order is created (met)
> - 🔴 Release the reservation when payment fails (missing)

| Badge | Meaning |
| --- | --- |
| ![P0](https://img.shields.io/badge/P0-red?style=flat) | Drop everything. Blocks release or major usage. |
| ![P1](https://img.shields.io/badge/P1-orange?style=flat) | Urgent. Fix in the next cycle. |
| ![P2](https://img.shields.io/badge/P2-yellow?style=flat) | Normal. Fix eventually. |
| ![P3](https://img.shields.io/badge/P3-lightgrey?style=flat) | Low. Nice to have. |

## Using it

**On GitHub**, in any repository Otto watches:

```text
/otto review
/otto review focus on the payment retry logic and the new migration
```

Anything after the trigger becomes extra instructions for the reviewer. The trigger can follow other text in the comment, as long as it starts its own line outside a code block. Only comments written by you trigger a review. The background service runs up to three reviews at once. Additional requests wait in a persisted queue; Otto comments on the PR with the queue position. If the service restarts, pending reviews resume. A review has a configurable deadline (`timeout`), and GitHub calls time out after 30 seconds so one stalled repository cannot block polling indefinitely.

**From the terminal**, on any pull request you can access, watched or not. Handy at work, when you want a review
without setting anything up in the repository:

```bash
otto review https://github.com/acme/api/pull/482
otto review acme/api#482 --engine codex            # use only this engine
otto review acme/api#482 --engine copilot          # use only GitHub Copilot CLI
otto review acme/api#482 --dry-run                 # print it, post nothing
otto review acme/api#482 --focus "security only"
otto eval models                     # evaluate the recommended free OpenCode models
otto eval models --engine opencode --timeout 4m --parallel 2 opencode/big-pickle
```

In an interactive terminal, manual reviews show one animated status line with the current stage and, when Copilot
reports a file read, the file path. Otto clears this temporary display when the review finishes and then prints only
the final summary. It does not stream private model reasoning or file contents. The background watcher keeps its
concise log output.

**Housekeeping:**

```bash
otto service status      # is the background watcher running?
otto service uninstall   # stop it
otto clean               # delete cached clones and worktrees
tail -f ~/Library/Logs/otto.log
```

## Quick configurations

Everything lives in `~/.config/otto/config.yaml`. `otto setup` writes it, and you can edit it by hand.

<details>
<summary><strong>Just Claude Code</strong></summary>

```yaml
engines:
  - name: claude
    model: sonnet
```

</details>

<details>
<summary><strong>Codex first, Claude only when Codex fails</strong></summary>

```yaml
strategy: priority
engines:
  - name: codex
  - name: claude
    model: opus
```

</details>

<details>
<summary><strong>Rotate across the AI plans you use</strong></summary>

Each review starts with the next engine. If one is down or out of quota, the next one picks it up.

```yaml
strategy: round-robin
engines:
  - name: claude
    model: sonnet
  - name: codex
  - name: cursor
  - name: opencode
    model: anthropic/claude-sonnet-4
  - name: copilot
    # omit model to use Copilot CLI's default, or set a model ID
```

</details>

<details>
<summary><strong>Reviews in Portuguese, a custom trigger, every repository</strong></summary>

```yaml
language: Brazilian Portuguese
trigger: /revisa
repos:
  all: true
```

</details>

<details>
<summary><strong>Full reference</strong></summary>

```yaml
trigger: /otto review        # comment prefix that requests a review
language: English            # language of titles and comments
allowed_authors: [you]       # who may trigger; setup sets it to your GitHub login
strategy: round-robin        # round-robin | priority
min_confidence: 0.3          # drop findings the engine is unsure about
poll_interval: 1m0s          # how often the watcher checks for new comments
lookback: 24h0m0s            # how far back to look the first time a repo is watched
timeout: 30m0s               # per review
ci_wait: 10m0s               # wait for running checks before reading CI evidence
learning: true               # learn from maintainer replies and reactions
repos:
  all: false
  selected: [acme/api, acme/web]
engines:
  - name: claude             # claude | codex | cursor | opencode | copilot
    model: sonnet            # empty = the CLI's default model
```

`OTTO_HOME` points Otto at another config directory.

For repository-specific risky paths, add `.otto.yml` at the repository root:

```yaml
sensitive:
  - "apps/live/**/audio/**"
  - billing
```

Changes in these paths receive heavy review effort. Otto also treats authentication, payments, security,
migrations and contracts as sensitive by default.

</details>

## Learning from your feedback

Otto gets better at your repository the way a developer does: by reading how you react to its findings.

- **What it reads:** your replies in each finding's thread, your 👍 / 👎 and whether the thread was resolved.
  A reply is enough on its own: no reaction needed, and no magic words. `Fixed in abc` and `Won't fix. <reason>` are
  read as verdicts; any other reply is read by the engine for what it says. Replies carry the most weight, because the
  reason is what teaches.
- **What it keeps:** a short list of lessons per repository in `~/.config/otto/learning/` (at most 25, editable).
  The watcher refreshes them in the background every 30 minutes once at least 3 new signals arrived; `otto learn`
  does it on demand.
- **What it uses, without growing the prompt:** before each review Otto brings in only the lessons that match the
  changed files (a lesson can be scoped to a path glob), ranked by relevance and evidence, inside a fixed budget
  (about 400 tokens). On a pull request it was already asked to review, it also remembers its earlier findings that
  were rejected (with your reason) or are still open, and skips the ones already fixed.
- **You stay in control:** `otto lessons owner/repo` lists them; `pin`, `disable`, `enable` or `forget` an id.
  Pinned lessons are never dropped. Turn it off with `learning: false` in `config.yaml`.

Lessons stay on your machine and are never posted to GitHub.

## Why Otto exists

### #1: Good reviewers only look at the diff

**The problem.** Most AI reviewers read the patch and guess. They can miss failing CI, code paths that do not
implement the issue, or new behavior without regression coverage.

**The fix.** Otto reads the description and linked issues as the spec, follows repository rules (`AGENTS.md`,
`CLAUDE.md`, `CONTRIBUTING.md`), traces each requested scenario through the code, and checks CI at the reviewed commit.
It waits up to 10 minutes for running checks and includes a redacted failure excerpt when one fails. It flags changed
behavior without a relevant regression test. Spec and code standards are reviewed as separate axes.

### #2: Review bots drown you in noise

**The problem.** Style nits, speculative "this might break something", and suggestions to add three layers of
abstraction.

**The fix.** A finding has to be real, introduced by this pull request, and something the author would want to
fix. Over-engineering counts as a problem; trivial style does not. The rubric is adapted from the one OpenAI Codex
uses.

### #3: Your review tool is tied to one vendor

**The problem.** The reviewer you like comes with one subscription, and its limits.

**The fix.** Otto drives whichever AI CLI you already use, through one prompt that behaves the same on every engine.
Switch, rotate or fall back without changing how reviews look.

### #4: Large pull requests lose context

**The problem.** One reviewer can overlook files or interactions when the patch is too large.

**The fix.** Above 40 files or 1500 changed lines, Otto groups code with its tests, reviews up to three parts at once,
then makes a pass across the parts. It merges duplicate findings and names any files it could not review.

## How it works

```text
/otto review comment
        |
        v
  watcher (every minute, on your machine)  --- remembers handled comments,
        |                                      catches up after a reboot
        v
  disposable worktree at the PR head
        |
        v
  engine: claude | codex | cursor | opencode | copilot   (rotation + fallback)
    read spec and repo rules -> trace behavior -> inspect CI -> report findings
        |
        v
  JSON answer -> validated against the diff -> review posted with your gh login
        |
        v
  processes killed, worktree deleted
```

## Post as Otto (optional)

By default every review is posted under your own GitHub account. If you want reviews asked for with `/otto review`
to appear as a separate identity (`otto[bot]`), give Otto a GitHub App:

1. Open <https://github.com/settings/apps/new>. Name it (for example `otto-review-<you>`), put any URL as the homepage,
   and untick **Webhook → Active**.
2. Repository permissions: **Pull requests: Read & write**, **Issues: Read & write**, **Contents: Read-only**.
3. Create the app, note its **App ID**, and under **Private keys** generate and download a `.pem` file.
4. **Install App** on your account and choose **All repositories**: one install covers every repository, including new
   ones. Otto can only post as the app where it is installed. `otto setup` checks this for you and, if the app is missing
   or sees only some repositories, shows the link; press `o` to open it in the browser or `c` to copy it.
5. Run `otto setup`, pick **As Otto** at the identity step, and enter the App ID and the `.pem` path.
   Setup checks them against GitHub before saving.

| How the review is requested | Who posts it |
| --- | --- |
| Comment `/otto review` on GitHub | `otto[bot]`, with a "Requested by @you" line |
| `otto review <pr-url>` in the terminal | you |

If the app is not installed on a repository, Otto logs why, with the link to install it, and posts as you instead, so a review is never lost.
In `config.yaml` this is `github_app: {id: 123456, private_key: ~/.config/otto/otto.pem}`.

## Updates

- **Check or install the latest release**: run `otto update`. It checks GitHub immediately, installs the latest release
  after verifying its checksum, and restarts Otto; if already current, it says so. This updates only published releases.
- **By hand** (`otto setup`, `otto review`, ...): when a newer release exists, Otto asks `Update now? [Y/n]`, installs it
  after verifying the checksum, and carries on with the new version. Say no and it asks again tomorrow.
- **From GitHub** (the background watcher answering `/otto review`): Otto updates itself first, restarts, and then
  handles the request, so a review always runs on the newest version. It never asks.
- **A running background Otto never loses a review to an update.** When the binary on disk is replaced (by `otto update`, the installer or its own self-update), the watcher finishes the review in progress, then replaces itself in place with the new version, so there is never an old process left running next to the new one.
- **Changing the config reloads Otto too.** When `otto setup` (or a hand edit) rewrites `config.yaml`, the running watcher reloads it the same way: after the review in progress, replacing itself in place. `otto setup` no longer restarts a running service, which would have cut a review short. The log line `engines: ...` at every start shows which engines and models the running process loaded.
- A check happens at most once an hour, and no network just means no update. `OTTO_NO_UPDATE=1` turns it off.

## Safety

- Reviews are read-only. Engines run inside a disposable worktree with editing tools disabled; Otto does not execute
  the pull request's build or tests locally. Claude and OpenCode are restricted to reading inside that worktree with
  shell, web and MCP access disabled. Codex, Cursor and Copilot may still read outside the worktree.
- Pull request content is bracketed as untrusted input in the prompt. Otto redacts secrets before publishing and
  learns only from allowed maintainers. Prompt injection attempts are reported as P1 findings. Otto never approves
  or blocks a pull request.
- Otto never stores a GitHub token: it uses your `gh` login.

## Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/OvictorVieira/otto.code.reviewer/main/install.sh | sh -s -- --uninstall --purge
```

Removes the binary, the background service and the cache. `--purge` also removes `~/.config/otto`.
Other options: `--version v0.1.0` installs a specific release, `--no-setup` skips the setup step.

## Requirements

- macOS for the background service (`otto review` also runs on Linux)
- [GitHub CLI](https://cli.github.com) and `git`
- One of: Claude Code, Codex, Cursor, OpenCode or GitHub Copilot CLI

GitLab and other hosts are not supported yet.

## Development

```bash
go test ./...                 # behavior tests for the review and watch flows
otto eval models             # live model evaluation; not run in CI
go run ./cmd/otto review acme/api#1 --dry-run
git tag v0.2.0 && git push origin v0.2.0   # CI builds and publishes the release
```

## License

[Functional Source License 1.1, ALv2 Future License](LICENSE.md). You can use, modify and self-host Otto, including
at work. You cannot sell it as a competing product. Each release becomes Apache 2.0 two years after it is published.

The review rubric is adapted from [OpenAI Codex](https://github.com/openai/codex) (Apache-2.0). See [NOTICE](NOTICE).
