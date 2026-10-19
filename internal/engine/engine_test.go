package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestClaudeCommandIsReadOnlyAndMapsModelAndEffort(t *testing.T) {
	c, err := BuildCommand(Spec{Name: "claude", Model: "sonnet", Effort: "high"}, Request{Prompt: "review this", Dir: "/wt", OutFile: "/tmp/out", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bin != "claude" || c.Stdin != "review this" || c.Dir != "/wt" {
		t.Errorf("command = %+v", c)
	}
	for _, want := range [][]string{
		{"-p"},
		{"--output-format", "json"},
		{"--model", "sonnet"},
		{"--effort", "high"},
		{"--permission-mode", "dontAsk"},
		{"--allowedTools", "Read", "Grep", "Glob", "Bash"},
		{"--disallowedTools", "Edit", "Write", "NotebookEdit"},
	} {
		if !containsSeq(c.Args, want) {
			t.Errorf("args %v missing %v", c.Args, want)
		}
	}
}

func TestCodexCommandWritesLastMessageToFile(t *testing.T) {
	c, err := BuildCommand(Spec{Name: "codex", Model: "gpt-5.5", Effort: "xhigh"}, Request{Prompt: "review this", Dir: "/wt", OutFile: "/tmp/out", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"exec"},
		{"-s", "workspace-write"},
		{"-c", "sandbox_workspace_write.network_access=true"},
		{"-o", "/tmp/out"},
		{"-m", "gpt-5.5"},
		{"-c", `model_reasoning_effort="xhigh"`},
	} {
		if !containsSeq(c.Args, want) {
			t.Errorf("args %v missing %v", c.Args, want)
		}
	}
	if c.Args[len(c.Args)-1] != "-" || c.Stdin != "review this" {
		t.Errorf("codex must read the prompt from stdin: %+v", c)
	}
	if c.OutputFile != "/tmp/out" {
		t.Errorf("output file = %q", c.OutputFile)
	}
}

func TestOpencodeCommandUsesPlanAgentAndVariant(t *testing.T) {
	c, err := BuildCommand(Spec{Name: "opencode", Model: "anthropic/claude-sonnet", Effort: "high"}, Request{Prompt: "review this", Dir: "/wt", OutFile: "/tmp/out", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"run"},
		{"--auto"},
		{"--dir", "/wt"},
		{"-m", "anthropic/claude-sonnet"},
		{"--variant", "high"},
	} {
		if !containsSeq(c.Args, want) {
			t.Errorf("args %v missing %v", c.Args, want)
		}
	}
	if c.PromptFile == "" || !slices.Contains(c.Args, c.PromptFile) {
		t.Errorf("opencode must attach the prompt as a file: %+v", c)
	}
}

func TestCopilotCommandUsesHeadlessJSONAndRestrictsWrites(t *testing.T) {
	c, err := BuildCommand(Spec{Name: "copilot", Model: "gpt-6-luna", Effort: "high"}, Request{
		Prompt: "review this", Dir: "/wt", OutFile: "/tmp/out", Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bin != "copilot" {
		t.Fatalf("bin = %q, want copilot", c.Bin)
	}
	for _, want := range [][]string{
		{"-p", "review this"}, {"--output-format", "json"}, {"--stream", "on"},
		{"--allow-all-tools"}, {"--deny-tool=write"},
		{"--deny-tool=shell(git commit)"}, {"--deny-tool=shell(git push)"},
		{"--disable-builtin-mcps"}, {"--model", "gpt-6-luna"},
		{"--reasoning-effort", "high"},
	} {
		if !containsSeq(c.Args, want) {
			t.Errorf("args %v missing %v", c.Args, want)
		}
	}

	readOnly, err := BuildCommand(Spec{Name: "copilot"}, Request{Prompt: "p", Dir: "/wt"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(readOnly.Args, "--deny-tool=shell") {
		t.Errorf("read-only Copilot must not run shell commands: %v", readOnly.Args)
	}
}

func TestCopilotOutputExtractsFinalAssistantMessageFromJSONL(t *testing.T) {
	out, err := ExtractOutput("copilot", strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"abc"}}`,
		`{"type":"assistant.message","data":{"content":"{\"findings\":[]}"}}`,
	}, "\n"), "")
	if err != nil || out != `{"findings":[]}` {
		t.Errorf("out = %q, err = %v", out, err)
	}
	if _, err := ExtractOutput("copilot", `{"type":"session.idle","data":{}}`, ""); err == nil {
		t.Error("missing final assistant message must fail explicitly")
	}
}

func TestCopilotProgressShowsToolCategoriesButNeverReasoningText(t *testing.T) {
	progress, quota := copilotEvent(`{"type":"tool.execution_start","data":{"toolName":"view","arguments":{"path":"/worktree/internal/secret.go"}}}`, "/worktree")
	if quota != "" || !strings.Contains(progress, "internal/secret.go") {
		t.Errorf("progress = %q, quota = %q", progress, quota)
	}
	progress, quota = copilotEvent(`{"type":"assistant.reasoning","data":{"content":"private chain of thought"}}`, "/worktree")
	if progress != "" || quota != "" {
		t.Errorf("reasoning events must not be surfaced: progress = %q, quota = %q", progress, quota)
	}
}

func TestCopilotQuotaErrorIsRecognized(t *testing.T) {
	_, quota := copilotEvent(`{"type":"session.error","data":{"message":"Premium request limit reached; resets at 2026-10-20T00:00:00Z"}}`, "/worktree")
	if !strings.Contains(quota, "resets at") {
		t.Errorf("quota details = %q", quota)
	}
	if !isQuotaMessage("HTTP 429 Too Many Requests") {
		t.Error("HTTP 429 must be recognized as quota exhaustion")
	}
}

func TestDefaultModelAndEffortAreOmitted(t *testing.T) {
	c, err := BuildCommand(Spec{Name: "claude"}, Request{Prompt: "p", Dir: "/wt", OutFile: "/tmp/out"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(c.Args, "--model") || slices.Contains(c.Args, "--effort") {
		t.Errorf("empty model/effort must use the CLI default: %v", c.Args)
	}
}

func TestReadOnlyModeWhenVerificationIsOff(t *testing.T) {
	req := Request{Prompt: "p", Dir: "/wt", OutFile: "/tmp/out", Verify: false}

	claude, _ := BuildCommand(Spec{Name: "claude"}, req)
	if slices.Contains(claude.Args, "Bash") {
		t.Errorf("claude read-only must not allow every shell command: %v", claude.Args)
	}
	codex, _ := BuildCommand(Spec{Name: "codex"}, req)
	if !containsSeq(codex.Args, []string{"-s", "read-only"}) {
		t.Errorf("codex read-only sandbox missing: %v", codex.Args)
	}
	opencode, _ := BuildCommand(Spec{Name: "opencode"}, req)
	if !containsSeq(opencode.Args, []string{"--agent", "plan"}) || slices.Contains(opencode.Args, "--auto") {
		t.Errorf("opencode read-only must use the plan agent: %v", opencode.Args)
	}
}

func TestUnknownEngine(t *testing.T) {
	if _, err := BuildCommand(Spec{Name: "nope"}, Request{Prompt: "p", Dir: "/wt", OutFile: "/tmp/out"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestClaudeResultIsExtractedFromJSON(t *testing.T) {
	out, err := ExtractOutput("claude", `{"type":"result","is_error":false,"result":"{\"findings\":[]}"}`, "")
	if err != nil || out != `{"findings":[]}` {
		t.Errorf("out = %q, err = %v", out, err)
	}
	if _, err := ExtractOutput("claude", `{"type":"result","is_error":true,"result":"usage limit reached"}`, ""); err == nil {
		t.Error("is_error must become an error so the pool falls back")
	}
}

type fake struct {
	name string
	out  string
	err  error
	runs *[]string
}

func (f fake) Name() string { return f.name }
func (f fake) Run(_ context.Context, _ Request) (string, error) {
	*f.runs = append(*f.runs, f.name)
	return f.out, f.err
}

func accept(out string) error {
	if out != "ok" {
		return errors.New("bad output")
	}
	return nil
}

func TestRoundRobinStartsAtCursorAndAdvances(t *testing.T) {
	var runs []string
	p := Pool{Strategy: RoundRobin, Engines: []Engine{
		fake{"claude", "ok", nil, &runs},
		fake{"codex", "ok", nil, &runs},
		fake{"opencode", "ok", nil, &runs},
	}}

	res, err := p.Run(context.Background(), Request{}, 1, accept)
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "codex" || res.Next != 2 {
		t.Errorf("result = %+v, want codex and next cursor 2", res)
	}
	if !slices.Equal(runs, []string{"codex"}) {
		t.Errorf("runs = %v", runs)
	}
}

func TestFallbackToNextEngineOnErrorOrBadOutput(t *testing.T) {
	var runs []string
	p := Pool{Strategy: RoundRobin, Engines: []Engine{
		fake{"claude", "", errors.New("rate limited"), &runs},
		fake{"codex", "garbage", nil, &runs},
		fake{"opencode", "ok", nil, &runs},
	}}

	res, err := p.Run(context.Background(), Request{}, 0, accept)
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "opencode" || res.Next != 0 {
		t.Errorf("result = %+v, want opencode and cursor wrapping to 0", res)
	}
	if !slices.Equal(runs, []string{"claude", "codex", "opencode"}) {
		t.Errorf("runs = %v", runs)
	}
	if len(res.Failures) != 2 {
		t.Errorf("failures = %v", res.Failures)
	}
}

func TestPriorityAlwaysStartsWithFirstEngine(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		fake{"claude", "ok", nil, &runs},
		fake{"codex", "ok", nil, &runs},
	}}
	res, err := p.Run(context.Background(), Request{}, 1, accept)
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "claude" {
		t.Errorf("engine = %s, want claude", res.Engine)
	}
}

func TestAllEnginesFail(t *testing.T) {
	var runs []string
	p := Pool{Strategy: RoundRobin, Engines: []Engine{
		fake{"claude", "", errors.New("down"), &runs},
		fake{"codex", "", errors.New("down"), &runs},
	}}
	if _, err := p.Run(context.Background(), Request{}, 0, accept); err == nil {
		t.Fatal("expected error when every engine fails")
	}
}

func TestAllFailedErrorPreservesQuotaCause(t *testing.T) {
	var runs []string
	p := Pool{Strategy: Priority, Engines: []Engine{
		fake{"copilot", "", &QuotaError{Details: "limit resets tomorrow"}, &runs},
	}}
	_, err := p.Run(context.Background(), Request{}, 0, accept)
	if !errors.Is(err, ErrAllFailed) || !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("error = %v, must preserve ErrAllFailed and ErrQuotaExceeded", err)
	}
}

func containsSeq(args, seq []string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

func TestCursorCommandRunsHeadlessInTheWorktree(t *testing.T) {
	c, err := BuildCommand(Spec{Name: "cursor", Model: "sonnet-4-thinking"}, Request{Prompt: "review this", Dir: "/wt", OutFile: "/tmp/out", Verify: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bin != "cursor-agent" {
		t.Errorf("bin = %q, want cursor-agent", c.Bin)
	}
	for _, want := range [][]string{
		{"-p"}, {"--output-format", "json"}, {"--trust"}, {"--force"},
		{"--workspace", "/wt"}, {"--model", "sonnet-4-thinking"},
	} {
		if !containsSeq(c.Args, want) {
			t.Errorf("args %v missing %v", c.Args, want)
		}
	}
	if c.Args[len(c.Args)-1] != "review this" {
		t.Errorf("prompt must be the last argument: %v", c.Args)
	}

	ro, _ := BuildCommand(Spec{Name: "cursor"}, Request{Prompt: "p", Dir: "/wt", OutFile: "/tmp/out"})
	if slices.Contains(ro.Args, "--force") || !containsSeq(ro.Args, []string{"--mode", "plan"}) {
		t.Errorf("read-only cursor must use plan mode without --force: %v", ro.Args)
	}
}

func TestCursorResultIsExtractedFromJSON(t *testing.T) {
	out, err := ExtractOutput("cursor-agent", `{"type":"result","is_error":false,"result":"{\"findings\":[]}"}`, "")
	if err != nil || out != `{"findings":[]}` {
		t.Errorf("out = %q, err = %v", out, err)
	}
}
