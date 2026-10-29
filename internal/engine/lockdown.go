package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// opencodeLockdown is the configuration Otto passes to opencode in OPENCODE_CONFIG_CONTENT, which
// takes precedence over the user's and the repository's opencode.json. A pull request can ship an
// opencode.json that allows the shell or the web; this keeps the reviewer to reading the checkout:
// read, grep, glob and list only, nothing outside the directory, no web, and no MCP servers (they
// can hold credentials, such as a database token).
func opencodeLockdown() string {
	mcp := map[string]any{}
	for _, name := range opencodeMCPServers() {
		mcp[name] = map[string]any{"enabled": false}
	}
	cfg := map[string]any{
		"tools": map[string]bool{"*": false, "read": true, "grep": true, "glob": true, "list": true},
		"permission": map[string]string{
			"edit": "deny", "bash": "deny", "webfetch": "deny", "websearch": "deny", "codesearch": "deny",
			"external_directory": "deny",
		},
		"mcp": mcp,
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

var jsonComment = regexp.MustCompile(`(?m)^\s*//.*$`)

// opencodeMCPServers names the MCP servers in the user's opencode configuration.
func opencodeMCPServers() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".config", "opencode")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dir = filepath.Join(x, "opencode")
	}
	var names []string
	for _, f := range []string{"opencode.json", "opencode.jsonc", "config.json"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		var c struct {
			MCP map[string]json.RawMessage `json:"mcp"`
		}
		if json.Unmarshal(jsonComment.ReplaceAll(b, nil), &c) != nil {
			continue
		}
		for n := range c.MCP {
			names = append(names, n)
		}
	}
	return names
}
