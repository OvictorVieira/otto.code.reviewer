// Package engine runs AI coding CLIs inside a disposable checkout, with
// rotation and fallback.
package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Spec is one configured engine.
type Spec struct {
	Name   string `yaml:"name"`
	Model  string `yaml:"model,omitempty"`  // empty = CLI default
	Effort string `yaml:"effort,omitempty"` // empty = CLI default
}

// Request is one review run.
type Request struct {
	Prompt   string
	Dir      string // disposable checkout of the pull request head
	OutFile  string // scratch file the engine may write its answer to
	Verify   bool   // allow running commands (install, test, start) inside Dir
	Progress func(string)
}

// Engine runs a prompt and returns the final answer text.
type Engine interface {
	Name() string
	Run(ctx context.Context, req Request) (string, error)
}

// Known lists the supported engines in display order.
var Known = []string{"claude", "codex", "cursor", "opencode", "copilot"}

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
	case "copilot":
		c.Args = []string{
			"-p", req.Prompt,
			"--output-format", "json", "--stream", "on", "--no-ask-user",
			"--allow-all-tools", "--deny-tool=write",
			"--deny-tool=shell(git commit)", "--deny-tool=shell(git push)",
			"--disable-builtin-mcps", "--no-color",
		}
		if !req.Verify {
			c.Args = append(c.Args, "--deny-tool=shell")
		}
		if s.Model != "" {
			c.Args = append(c.Args, "--model", s.Model)
		}
		if s.Effort != "" {
			c.Args = append(c.Args, "--reasoning-effort", s.Effort)
		}
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
	case "copilot":
		return extractCopilotOutput(stdout)
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

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, c.Bin, c.Args...)
	cmd.Dir = c.Dir
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return "", err
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return "", err
	}
	cmd.Stdout, cmd.Stderr = stdoutWriter, stderrWriter
	isolate(cmd)

	if err := cmd.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return "", err
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	var stdoutText, stderrText strings.Builder
	var readers sync.WaitGroup
	var progressMu sync.Mutex
	emit := func(message string) {
		if req.Progress == nil || message == "" {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		req.Progress(message)
	}

	var quotaMu sync.Mutex
	var quotaMessage string
	cancelForQuota := func(message string) {
		quotaMu.Lock()
		defer quotaMu.Unlock()
		if quotaMessage == "" {
			quotaMessage = message
			cancel()
		}
	}

	read := func(r io.Reader, dst *strings.Builder, isStderr bool) {
		defer readers.Done()
		reader := bufio.NewReader(r)
		for {
			line, readErr := reader.ReadString('\n')
			if line != "" {
				dst.WriteString(line)
				if isStderr {
					if isQuotaMessage(line) {
						cancelForQuota(line)
					}
					continue
				}
				if c.Bin == "copilot" {
					message, quota := copilotEvent(line, c.Dir)
					if message != "" {
						emit(message)
					}
					if quota != "" {
						cancelForQuota(quota)
					}
				}
			}
			if readErr != nil {
				return
			}
		}
	}
	readers.Add(2)
	go read(stdoutReader, &stdoutText, false)
	go read(stderrReader, &stderrText, true)
	readersDone := make(chan struct{})
	go func() {
		readers.Wait()
		close(readersDone)
	}()

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	started := time.Now()
	ctxDone := ctx.Done()
	var runErr error
	for {
		select {
		case runErr = <-wait:
			_ = killGroup(cmd) // close pipes inherited by any child the engine left behind
			select {
			case <-readersDone:
			case <-time.After(time.Second):
				_ = stdoutReader.Close()
				_ = stderrReader.Close()
				<-readersDone
			}
			goto finished
		case <-ticker.C:
			emit(fmt.Sprintf("Analisando código… (%s)", time.Since(started).Round(time.Second)))
		case <-ctxDone:
			cancel()
			ctxDone = nil
		}
	}

finished:
	quotaMu.Lock()
	quotaErr := quotaMessage
	quotaMu.Unlock()
	if quotaErr != "" {
		return "", &QuotaError{Details: strings.TrimSpace(quotaErr)}
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s: %w", c.Bin, ctx.Err())
		}
		stderrOutput := stderrText.String()
		if isQuotaMessage(stderrOutput) || (c.Bin != "copilot" && isQuotaMessage(stdoutText.String())) {
			details := stderrOutput
			if details == "" {
				details = stdoutText.String()
			}
			return "", &QuotaError{Details: tail(details, 1000)}
		}
		combined := stderrOutput + stdoutText.String()
		return "", fmt.Errorf("%s: %w: %s", c.Bin, runErr, tail(combined, 400))
	}

	var file string
	if c.OutputFile != "" {
		b, _ := os.ReadFile(c.OutputFile)
		file = string(b)
	}
	return ExtractOutput(c.Bin, stdoutText.String(), file)
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

// ErrQuotaExceeded identifies an engine that stopped because its usage quota
// is exhausted, allowing callers to report the failure without guessing a reset time.
var ErrQuotaExceeded = errors.New("AI usage quota exceeded")

// QuotaError keeps any reset details the provider included without inventing one.
type QuotaError struct {
	Details string
}

func (e *QuotaError) Error() string {
	if e.Details == "" {
		return ErrQuotaExceeded.Error()
	}
	return ErrQuotaExceeded.Error() + ": " + e.Details
}

func (e *QuotaError) Unwrap() error { return ErrQuotaExceeded }

func isQuotaMessage(s string) bool {
	s = strings.ToLower(s)
	for _, marker := range []string{
		"rate limit exceeded", "rate_limit_exceeded", "quota exceeded", "quota_exceeded",
		"premium request limit", "premium request quota", "out of premium requests",
		"usage limit reached", "hit your usage limit", "reached your usage limit", "monthly quota exhausted", "monthly request limit", "too many requests",
		"status code 429", "http 429", "error 429",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func copilotEvent(line, workDir string) (progress, quota string) {
	var event struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &event) != nil {
		return "", ""
	}
	typeName := strings.ToLower(event.Type)
	switch {
	case strings.Contains(typeName, "error"):
		var data struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(event.Data, &data)
		details := data.Message
		if details == "" {
			details = data.Error
		}
		if details == "" {
			details = string(event.Data)
		}
		if isQuotaMessage(details) {
			return "", details
		}
	case strings.Contains(typeName, "tool.execution_start"):
		var data struct {
			ToolName  string          `json:"toolName"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Args      json.RawMessage `json:"args"`
			Input     json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(event.Data, &data)
		toolName := strings.ToLower(data.ToolName)
		if toolName == "" {
			toolName = strings.ToLower(data.Name)
		}
		switch toolName {
		case "view", "read", "read_file", "readfile":
			toolPath := data.Arguments
			if len(toolPath) == 0 {
				toolPath = data.Args
			}
			if len(toolPath) == 0 {
				toolPath = data.Input
			}
			if path := copilotFilePath(toolPath, workDir); path != "" {
				return fmt.Sprintf("Lendo %s…", path), ""
			}
			return "Lendo arquivos do repositório…", ""
		case "grep", "glob":
			return "Buscando nos arquivos do repositório…", ""
		case "bash", "shell":
			return "Executando uma verificação…", ""
		default:
			return "Copilot está analisando o repositório…", ""
		}
	case strings.Contains(typeName, "turn_start"):
		return "Analisando o diff e o contexto do repositório…", ""
	}
	return "", ""
}

func copilotFilePath(raw json.RawMessage, workDir string) string {
	var args struct {
		Path     string `json:"path"`
		FilePath string `json:"filePath"`
		File     string `json:"file"`
		Target   string `json:"target"`
	}
	if json.Unmarshal(raw, &args) != nil {
		var encoded string
		if json.Unmarshal(raw, &encoded) != nil || json.Unmarshal([]byte(encoded), &args) != nil {
			return ""
		}
	}
	path := args.Path
	if path == "" {
		path = args.FilePath
	}
	if path == "" {
		path = args.File
	}
	if path == "" {
		path = args.Target
	}
	if path == "" {
		return ""
	}

	if filepath.IsAbs(path) {
		if workDir != "" {
			if relative, err := filepath.Rel(workDir, path); err == nil &&
				relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				path = relative
			} else {
				path = filepath.Base(path)
			}
		} else {
			path = filepath.Base(path)
		}
	}
	path = filepath.Clean(path)
	if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return filepath.ToSlash(path)
}

func extractCopilotOutput(stdout string) (string, error) {
	var answer string
	for _, line := range strings.Split(stdout, "\n") {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Content string `json:"content"`
				Message string `json:"message"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &event); err != nil {
			continue
		}
		if event.Type == "assistant.message" {
			answer = event.Data.Content
			if answer == "" {
				answer = event.Data.Message
			}
		}
	}
	if strings.TrimSpace(answer) == "" {
		return "", errors.New("copilot: no final assistant message found in JSONL output")
	}
	return answer, nil
}
