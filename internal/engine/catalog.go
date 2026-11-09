package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	case "agy":
		out, err := command(20*time.Second, "agy", "models")
		if err == nil {
			if models := parseAgy(out); len(models) > 0 {
				return models
			}
		}
		return agyFallbackModels()
	}
	return nil
}

const copilotModelsURL = "https://api.githubcopilot.com/models"

// copilotModels reads the catalog for the authenticated GitHub account. The API
// applies the account's plan and organization model policies.
func copilotModels() []Model {
	token, err := command(10*time.Second, "gh", "auth", "token", "--hostname", "github.com")
	if err != nil {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, err := fetchCopilotModels(ctx, token, &http.Client{Timeout: 15 * time.Second})
	if err != nil {
		return nil
	}
	return models
}

func fetchCopilotModels(ctx context.Context, token string, client *http.Client) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	integrationID := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_INTEGRATION_ID"))
	if integrationID == "" {
		integrationID = "vscode-chat"
	}
	req.Header.Set("Copilot-Integration-Id", integrationID)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot model catalog returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseCopilotModels(body)
}

func parseCopilotModels(data []byte) ([]Model, error) {
	var catalog struct {
		Data []struct {
			ID                 string   `json:"id"`
			Name               string   `json:"name"`
			Vendor             string   `json:"vendor"`
			Category           string   `json:"model_picker_category"`
			Preview            bool     `json:"preview"`
			SupportedEndpoints []string `json:"supported_endpoints"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					ReasoningEfforts []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, err
	}

	var models []Model
	for _, entry := range catalog.Data {
		if !modelID.MatchString(entry.ID) || entry.Category == "" || entry.Policy.State == "disabled" {
			continue
		}
		if entry.Capabilities.Type != "" && entry.Capabilities.Type != "chat" {
			continue
		}
		if !slices.Contains(entry.SupportedEndpoints, "/chat/completions") &&
			!slices.Contains(entry.SupportedEndpoints, "/responses") {
			continue
		}

		name := entry.Name
		if name == "" {
			name = entry.ID
		}
		group := entry.Vendor
		if group == "" {
			group = "GitHub Copilot"
		}
		tag := strings.ToUpper(entry.Category[:1]) + entry.Category[1:]
		if entry.Preview {
			tag += " · Preview"
		}
		models = append(models, Model{
			ID: entry.ID, Name: name, Group: group, Tag: tag,
			Note:    "Available to this GitHub account.",
			Efforts: sortEfforts(entry.Capabilities.Supports.ReasoningEfforts),
		})
	}
	return models, nil
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
	case "agy":
		return []string{"low", "medium", "high", "xhigh", "max"}
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

func parseAgy(out string) []Model {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	var models []Model
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "Fetching") {
			continue
		}
		f := strings.Fields(trimmed)
		if len(f) > 0 && modelID.MatchString(f[0]) {
			id := f[0]
			name := strings.TrimSpace(trimmed[len(id):])
			if name == "" {
				name = id
			}
			label := id
			if name != id {
				label += " — " + name
			}
			models = append(models, Model{
				ID:      id,
				Label:   label,
				Name:    name,
				Group:   "Antigravity",
				Efforts: efforts,
			})
		}
	}
	return models
}

func agyFallbackModels() []Model {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	return []Model{
		{ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash (High)", Group: "Google", Efforts: efforts},
		{ID: "gemini-3.8-flash-medium", Name: "Gemini 3.8 Flash (Medium)", Group: "Google", Efforts: efforts},
		{ID: "gemini-3.8-flash-low", Name: "Gemini 3.8 Flash (Low)", Group: "Google", Efforts: efforts},
		{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6 (Thinking)", Group: "Anthropic", Efforts: efforts},
		{ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 (Thinking)", Group: "Anthropic", Efforts: efforts},
		{ID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)", Group: "OpenAI", Efforts: efforts},
	}
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
		if (engineName == "opencode" || engineName == "copilot") && model != "" {
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
