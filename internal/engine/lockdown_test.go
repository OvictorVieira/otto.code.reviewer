package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeRunsLockedDownWhateverTheRepositoryConfigures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700)
	os.WriteFile(filepath.Join(home, ".config", "opencode", "opencode.json"),
		[]byte(`{"mcp":{"Neon":{"type":"remote","url":"https://mcp.neon.tech/mcp"}}}`), 0o600)

	c, err := BuildCommand(Spec{Name: "opencode"}, Request{Prompt: "p", Dir: "/wt"})
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, e := range c.Env {
		if v, ok := strings.CutPrefix(e, "OPENCODE_CONFIG_CONTENT="); ok {
			content = v
		}
	}
	var cfg struct {
		Tools      map[string]bool            `json:"tools"`
		Permission map[string]string          `json:"permission"`
		MCP        map[string]map[string]bool `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT must be set to valid JSON (it outranks a repository's opencode.json): %v %q", err, content)
	}
	if cfg.Tools["*"] || !cfg.Tools["read"] || cfg.Tools["bash"] {
		t.Errorf("only reading tools may be enabled: %+v", cfg.Tools)
	}
	for _, p := range []string{"bash", "edit", "webfetch", "websearch", "external_directory"} {
		if cfg.Permission[p] != "deny" {
			t.Errorf("permission %s = %q, want deny", p, cfg.Permission[p])
		}
	}
	if enabled, ok := cfg.MCP["Neon"]["enabled"]; !ok || enabled {
		t.Errorf("the user's MCP servers must be disabled: %+v", cfg.MCP)
	}
}

func TestARefusedToolCallDoesNotStopOpencode(t *testing.T) {
	// Regression: the lockdown refuses a read outside the checkout and opencode prints "Error: ...";
	// Otto used to treat that line as a crash and kill a review that was carrying on fine.
	n := 0
	refused := "Error: The user has specified a rule which prevents you from using this specific tool call."
	if got := stuckSignal("opencode", refused, &n); got != "" {
		t.Fatalf("a refused tool call stopped the engine: %q", got)
	}
	if got := stuckSignal("opencode", "Error: model not found", &n); got == "" {
		t.Fatal("a real error must still stop it")
	}
}
