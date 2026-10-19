// Package engine runs the AI coding CLIs the user already pays for
// (Claude Code, Codex, OpenCode) inside a disposable checkout, with rotation
// and fallback.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Spec is one configured engine.
type Spec struct {
	Name   string `yaml:"name"`
	Model  string `yaml:"model,omitempty"`  // empty = CLI default
	Effort string `yaml:"effort,omitempty"` // empty = CLI default
}

// Request is one review run.
type Request struct {
	Prompt  string
	Dir     string // disposable checkout of the pull request head
	OutFile string // scratch file the engine may write its answer to
	Verify  bool   // allow running commands (install, test, start) inside Dir
}

// Engine runs a prompt and returns the final answer text.
type Engine interface {
	Name() string
	Run(ctx context.Context, req Request) (string, error)
}

// Known lists the supported engines in display order.
var Known = []string{"claude", "codex", "cursor", "opencode"}

// Binary is the executable each engine runs.
func Binary(name string) string {
	if name == "cursor" {
		return "cursor-agent"
	}
	return name
}

// Command is a fully resolved CLI invocation.
type Command struct {
	Bin        string
	Args       []string
	Dir        string
	Stdin      string
	OutputFile string // read after the run when set (codex)
	PromptFile string // written before the run when set (opencode)
}

// BuildCommand maps a spec to the CLI flags of each tool. With Verify the
// engine may run commands inside the disposable checkout; file-editing tools
// stay disabled either way and the prompt forbids commits and pushes.
func BuildCommand(s Spec, req Request) (Command, error) {
	c := Command{Bin: Binary(s.Name), Dir: req.Dir}
	switch s.Name {
	case "claude":
		c.Stdin = req.Prompt
		c.Args = []string{"-p", "--output-format", "json", "--permission-mode", "dontAsk"}
		if s.Model != "" {
			c.Args = append(c.Args, "--model", s.Model)
		}
		if s.Effort != "" {
			c.Args = append(c.Args, "--effort", s.Effort)
		}
		if req.Verify {
			c.Args = append(c.Args, "--allowedTools", "Read", "Grep", "Glob", "Bash")
		} else {
			c.Args = append(c.Args, "--allowedTools", "Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)")
		}
		c.Args = append(c.Args, "--disallowedTools", "Edit", "Write", "NotebookEdit")
	case "codex":
		c.Stdin = req.Prompt
		c.OutputFile = req.OutFile
		if req.Verify {
			c.Args = []string{"exec", "-s", "workspace-write", "-c", "sandbox_workspace_write.network_access=true"}
		} else {
			c.Args = []string{"exec", "-s", "read-only"}
		}
		c.Args = append(c.Args, "-o", req.OutFile)
		if s.Model != "" {
			c.Args = append(c.Args, "-m", s.Model)
		}
		if s.Effort != "" {
			c.Args = append(c.Args, "-c", fmt.Sprintf("model_reasoning_effort=%q", s.Effort))
		}
		c.Args = append(c.Args, "-")
	case "cursor":
		// Cursor has no effort flag: reasoning depth comes with the model name.
		c.Args = []string{"-p", "--output-format", "json", "--trust", "--workspace", req.Dir}
		if req.Verify {
			c.Args = append(c.Args, "--force")
		} else {
			c.Args = append(c.Args, "--mode", "plan")
		}
		if s.Model != "" {
			c.Args = append(c.Args, "--model", s.Model)
		}
		c.Args = append(c.Args, req.Prompt)
	case "opencode":
		c.PromptFile = req.OutFile + ".prompt.md"
		if req.Verify {
			c.Args = []string{"run", "--auto"}
		} else {
			c.Args = []string{"run", "--agent", "plan"}
		}
		c.Args = append(c.Args, "--dir", req.Dir)
		if s.Model != "" {
			c.Args = append(c.Args, "-m", s.Model)
		}
		if s.Effort != "" {
			c.Args = append(c.Args, "--variant", s.Effort)
		}
		c.Args = append(c.Args, "-f", c.PromptFile, "--",
			"Follow the code review instructions in the attached file exactly and answer only with the JSON it asks for.")
	default:
		return Command{}, fmt.Errorf("unknown engine %q (supported: %s)", s.Name, strings.Join(Known, ", "))
	}
	return c, nil
}

// ExtractOutput returns the final answer text from what the CLI produced.
func ExtractOutput(name, stdout, fileContent string) (string, error) {
	switch name {
	case "claude", "cursor-agent":
		var r struct {
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &r); err != nil {
			if name == "cursor-agent" {
				return stdout, nil
			}
			return "", fmt.Errorf("%s: unexpected output: %w", name, err)
		}
		if r.IsError {
			return "", fmt.Errorf("%s: %s", name, r.Result)
		}
		return r.Result, nil
	case "codex":
		if strings.TrimSpace(fileContent) != "" {
			return fileContent, nil
		}
		return stdout, nil
	default:
		return stdout, nil
	}
}

// CLI is an Engine backed by a local command line tool.
type CLI struct{ Spec Spec }

// Name implements Engine.
func (e CLI) Name() string { return e.Spec.Name }

// Run implements Engine.
func (e CLI) Run(ctx context.Context, req Request) (string, error) {
	c, err := BuildCommand(e.Spec, req)
	if err != nil {
		return "", err
	}
	if c.PromptFile != "" {
		if err := os.WriteFile(c.PromptFile, []byte(req.Prompt), 0o600); err != nil {
			return "", err
		}
		defer os.Remove(c.PromptFile)
	}
	if c.OutputFile != "" {
		defer os.Remove(c.OutputFile)
	}

	cmd := exec.CommandContext(ctx, c.Bin, c.Args...)
	cmd.Dir = c.Dir
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	isolate(cmd)

	err = cmd.Run()
	_ = killGroup(cmd) // stop anything the engine left running
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s: %w", c.Bin, ctx.Err())
		}
		return "", fmt.Errorf("%s: %w: %s", c.Bin, err, tail(stderr.String()+stdout.String(), 400))
	}

	var file string
	if c.OutputFile != "" {
		b, _ := os.ReadFile(c.OutputFile)
		file = string(b)
	}
	return ExtractOutput(c.Bin, stdout.String(), file)
}

// Installed reports whether the engine's CLI is on PATH.
func Installed(name string) bool {
	_, err := exec.LookPath(Binary(name))
	return err == nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// ErrAllFailed is returned when no engine produced an accepted answer.
var ErrAllFailed = errors.New("every engine failed")
