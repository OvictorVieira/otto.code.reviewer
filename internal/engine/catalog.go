package engine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// modelID matches CLI model identifiers such as "sonnet-4-thinking" or "gpt-5".
var modelID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Models lists the models the user can pick for an engine. An empty list
// means "type it yourself"; the CLI default is always available too.
func Models(name string) []string {
	switch name {
	case "claude":
		return []string{"opus", "sonnet", "haiku"}
	case "codex":
		var slugs []string
		for _, m := range codexModels() {
			slugs = append(slugs, m.Slug)
		}
		return slugs
	case "cursor":
		out, err := exec.Command("cursor-agent", "--list-models").Output()
		if err != nil {
			return nil
		}
		var models []string
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(strings.TrimLeft(line, "-* "))
			if len(f) > 0 && modelID.MatchString(f[0]) {
				models = append(models, f[0])
			}
		}
		return models
	case "opencode":
		out, err := exec.Command("opencode", "models").Output()
		if err != nil {
			return nil
		}
		var models []string
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); strings.Contains(line, "/") {
				models = append(models, line)
			}
		}
		return models
	}
	return nil
}

// Efforts lists the reasoning effort levels an engine (and model) accepts.
func Efforts(name, model string) []string {
	switch name {
	case "claude":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "codex":
		for _, m := range codexModels() {
			if m.Slug == model && len(m.Levels) > 0 {
				var levels []string
				for _, l := range m.Levels {
					levels = append(levels, l.Effort)
				}
				return levels
			}
		}
		return []string{"minimal", "low", "medium", "high", "xhigh"}
	case "opencode":
		return []string{"minimal", "low", "medium", "high", "max"}
	}
	return nil
}

type codexModel struct {
	Slug   string `json:"slug"`
	Levels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// codexModels reads the model list the Codex CLI caches locally.
func codexModels() []codexModel {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(home, ".codex", "models_cache.json"))
	if err != nil {
		return nil
	}
	var cache struct {
		Models []codexModel `json:"models"`
	}
	if json.Unmarshal(b, &cache) != nil {
		return nil
	}
	return cache.Models
}
