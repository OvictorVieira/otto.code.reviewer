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
	"sync"
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

	Brand string // who made the model: the section it sits in unless it is recommended
	Tier  Tier   // the size of review it is recommended for; empty when it is not recommended
	// The price per million tokens when the catalog gives one. PriceKnown is false when it does not,
	// and the price is then never guessed.
	InUSD, OutUSD float64
	PriceKnown    bool
}

// Catalog lists the models available to the user for an engine, in reading order: the recommended
// ones first, then each brand with its cheapest models first.
func Catalog(name string) []Model { return OrderModels(name, catalog(name)) }

func catalog(name string) []Model {
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
	case "copilot":
		return copilotModels()
	}
	return nil
}

// copilotModels is the published premium catalog; the CLI checks actual access
// when a selected model is used because availability varies by account and version.
func copilotModels() []Model {
	models := []Model{
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-5.4-nano", Name: "GPT-5.4 nano", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-6-astra", Name: "GPT-6 Astra", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-6-luna", Name: "GPT-6 Luna", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-6-sol", Name: "GPT-6 Sol", Group: "OpenAI", Tag: "Premium"},
		{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol", Group: "OpenAI", Tag: "Premium"},
		{ID: "claude-fable-5", Name: "Claude Fable 5", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-fable-5.1", Name: "Claude Fable 5.1", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-haiku-4.5", Name: "Claude Haiku 4.5", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-haiku-5.5", Name: "Claude Haiku 5.5", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-opus-4.8", Name: "Claude Opus 4.8", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-opus-4.8-fast", Name: "Claude Opus 4.8 (fast)", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-opus-5", Name: "Claude Opus 5", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-opus-5.5", Name: "Claude Opus 5.5", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-sonnet-4.6", Name: "Claude Sonnet 4.6", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Group: "Anthropic", Tag: "Premium"},
		{ID: "claude-sonnet-5.5", Name: "Claude Sonnet 5.5", Group: "Anthropic", Tag: "Premium"},
		{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash", Group: "Google", Tag: "Premium"},
		{ID: "gemini-4-argon", Name: "Gemini 4 Argon", Group: "Google", Tag: "Premium"},
		{ID: "mai-code-1.1-flash", Name: "MAI-Code-1.1-Flash", Group: "Microsoft", Tag: "Premium"},
		{ID: "kimi-k3", Name: "Kimi K3", Group: "Moonshot AI", Tag: "Premium"},
		{ID: "grok-4.5", Name: "Grok 4.5", Group: "xAI", Tag: "Premium"},
		{ID: "grok-4.6", Name: "Grok 4.6", Group: "xAI", Tag: "Premium"},
		{ID: "grok-4.7", Name: "Grok 4.7", Group: "xAI", Tag: "Premium"},
	}
	for i := range models {
		models[i].Note = "Uses premium requests; access depends on your Copilot plan and CLI version."
	}
	return models
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
	case "copilot":
		return []string{"low", "medium", "high", "xhigh"}
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

		m := Model{ID: e.ProviderID + "/" + e.ID, Name: e.Name, Free: e.Cost.Input == 0 && e.Cost.Output == 0,
			InUSD: e.Cost.Input, OutUSD: e.Cost.Output, PriceKnown: true}
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

// CachedLevels returns a lookup of the effort levels a model accepts, reading each engine's model
// list at most once an hour. Models the list does not describe fall back to the engine's levels.
func CachedLevels() func(engineName, model string) []string {
	var mu sync.Mutex
	cache := map[string][]Model{}
	read := map[string]time.Time{}
	return func(engineName, model string) []string {
		mu.Lock()
		defer mu.Unlock()
		if engineName == "opencode" && model != "" {
			if time.Since(read[engineName]) > time.Hour {
				cache[engineName], read[engineName] = catalog(engineName), time.Now()
			}
			for _, m := range cache[engineName] {
				if m.ID == model && len(m.Efforts) > 0 {
					return m.Efforts
				}
			}
		}
		return Efforts(engineName)
	}
}
