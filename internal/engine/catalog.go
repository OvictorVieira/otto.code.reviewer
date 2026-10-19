package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Model is one choice in the setup wizard. The lists are read from each CLI
// at setup time, so new models show up without an Otto release.
type Model struct {
	ID      string
	Label   string   // one-line description with id, name and price
	Name    string   // human name, e.g. "Claude Sonnet 4"
	Group   string   // section in the picker: "Free", a provider, the engine
	Tag     string   // right-hand note: "Free", a price, "always latest"
	Note    string   // short description
	Efforts []string // reasoning levels this model accepts; empty = none to pick
	Free    bool
}

// Catalog lists the models available to the user for an engine.
func Catalog(name string) []Model {
	switch name {
	case "claude":
		help, _ := command(20*time.Second, "claude", "--help")
		return parseClaudeHelp(help)
	case "codex":
		// `codex debug models` refreshes the catalog; the cache file is the offline fallback.
		if out, err := command(60*time.Second, "codex", "debug", "models"); err == nil {
			if models := parseCodexCache([]byte(out)); len(models) > 0 {
				return models
			}
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json"))
		if err != nil {
			return nil
		}
		return parseCodexCache(b)
	case "cursor":
		out, err := command(20*time.Second, "cursor-agent", "--list-models")
		if err != nil {
			return nil
		}
		return parseCursor(out)
	case "opencode":
		out, err := command(60*time.Second, "opencode", "models", "--refresh", "--verbose")
		if err != nil {
			if out, err = command(30*time.Second, "opencode", "models", "--verbose"); err != nil {
				return nil
			}
		}
		return parseOpencode(out)
	}
	return nil
}

// Efforts lists the default reasoning levels of an engine, used when the
// model is the CLI default or was typed by hand.
func Efforts(name string) []string {
	switch name {
	case "claude":
		return claudeEfforts
	case "codex":
		return []string{"minimal", "low", "medium", "high", "xhigh"}
	case "opencode":
		return []string{"minimal", "low", "medium", "high", "max"}
	}
	return nil // cursor: reasoning comes with the model name
}

var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

var (
	claudeEffortHelp = regexp.MustCompile(`--effort <level>[^(]*\(([^)]*)\)`)
	claudeModelHelp  = regexp.MustCompile(`(?s)--model <model>.*?\(e\.g\.(.*?)\)`)
	quoted           = regexp.MustCompile(`'([a-z0-9][a-z0-9.-]*)'`)
)

var claudeAliasNote = map[string]string{
	"opus":   "most capable Opus",
	"sonnet": "balanced Sonnet",
	"haiku":  "fastest Haiku",
}

// parseClaudeHelp reads the model aliases and effort levels the installed
// Claude Code documents in `claude --help`, so new ones appear without an
// Otto release. Known aliases fill in anything the help text does not mention.
func parseClaudeHelp(help string) []Model {
	efforts := claudeEfforts
	if m := claudeEffortHelp.FindStringSubmatch(help); m != nil {
		var levels []string
		for _, l := range strings.Split(m[1], ",") {
			if l = strings.TrimSpace(l); modelID.MatchString(l) {
				levels = append(levels, l)
			}
		}
		if len(levels) > 0 {
			efforts = levels
		}
	}

	var aliases []string
	if m := claudeModelHelp.FindStringSubmatch(help); m != nil {
		for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
			aliases = append(aliases, q[1])
		}
	}
	for _, known := range []string{"opus", "sonnet", "haiku"} {
		if !slices.Contains(aliases, known) {
			aliases = append(aliases, known)
		}
	}

	var models []Model
	for _, a := range aliases {
		note := claudeAliasNote[a]
		if note == "" {
			note = "Claude " + a
		}
		models = append(models, Model{ID: a, Label: a + " — " + note + " (always the latest)",
			Name: a, Group: "Claude", Tag: "always latest", Note: note, Efforts: efforts})
	}
	return models
}

// effortOrder sorts reasoning levels from lightest to heaviest.
var effortOrder = map[string]int{"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}

func sortEfforts(levels []string) []string {
	sort.SliceStable(levels, func(i, j int) bool {
		a, okA := effortOrder[levels[i]]
		b, okB := effortOrder[levels[j]]
		switch {
		case okA && okB:
			return a < b
		case okA != okB:
			return okA
		default:
			return levels[i] < levels[j]
		}
	})
	return levels
}

func command(timeout time.Duration, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
		return string(out), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("%s timed out", name)
	}
}

func parseCodexCache(b []byte) []Model {
	var cache struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Description string `json:"description"`
			Visibility  string `json:"visibility"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &cache) != nil {
		return nil
	}
	var models []Model
	for _, m := range cache.Models {
		if m.Visibility == "hide" {
			continue // Codex hides these from its own model picker
		}
		label := m.Slug
		if m.DisplayName != "" {
			label += " — " + m.DisplayName
		}
		if m.Description != "" {
			label += " · " + m.Description
		}
		var efforts []string
		for _, l := range m.Levels {
			efforts = append(efforts, l.Effort)
		}
		name := m.DisplayName
		if name == "" {
			name = m.Slug
		}
		models = append(models, Model{ID: m.Slug, Label: label, Name: name, Group: "Codex", Note: m.Description, Efforts: efforts})
	}
	return models
}

// modelID matches CLI model identifiers such as "sonnet-4-thinking" or "gpt-5".
var modelID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func parseCursor(out string) []Model {
	var models []Model
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimLeft(line, "-* "))
		if len(f) > 0 && modelID.MatchString(f[0]) {
			models = append(models, Model{ID: f[0], Label: strings.Join(f, " "), Name: f[0], Group: "Cursor",
				Note: strings.Join(f[1:], " ")})
		}
	}
	return models
}

// parseOpencode reads `opencode models --verbose`: a "provider/id" line
// followed by that model's JSON. Free models come first, then paid ones.
func parseOpencode(out string) []Model {
	type entry struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Cost       struct {
			Input  float64 `json:"input"`
			Output float64 `json:"output"`
		} `json:"cost"`
		Variants map[string]json.RawMessage `json:"variants"`
	}

	var models []Model
	var block strings.Builder
	depth := 0
	for _, line := range strings.Split(out, "\n") {
		if depth == 0 && !strings.HasPrefix(line, "{") {
			continue // the "provider/id" header line
		}
		block.WriteString(line)
		block.WriteByte('\n')
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth > 0 {
			continue
		}
		var e entry
		err := json.Unmarshal([]byte(block.String()), &e)
		block.Reset()
		depth = 0
		if err != nil || e.ID == "" || e.Status == "deprecated" {
			continue
		}

		m := Model{ID: e.ProviderID + "/" + e.ID, Name: e.Name, Free: e.Cost.Input == 0 && e.Cost.Output == 0}
		price := "free"
		m.Group, m.Tag = "Free", "Free"
		if !m.Free {
			price = fmt.Sprintf("$%g in / $%g out per 1M tokens", e.Cost.Input, e.Cost.Output)
			m.Group, m.Tag = e.ProviderID, fmt.Sprintf("$%g / $%g", e.Cost.Input, e.Cost.Output)
		}
		m.Label = fmt.Sprintf("%s — %s · %s", m.ID, e.Name, price)
		for v := range e.Variants {
			m.Efforts = append(m.Efforts, v)
		}
		m.Efforts = sortEfforts(m.Efforts)
		models = append(models, m)
	}

	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Free != models[j].Free {
			return models[i].Free
		}
		if models[i].Group != models[j].Group {
			return models[i].Group < models[j].Group
		}
		return models[i].ID < models[j].ID
	})
	return models
}
