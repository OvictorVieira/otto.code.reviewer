package engine

import (
	"slices"
	"strings"
	"testing"
)

// Shape of `opencode models --verbose`: a "provider/id" line, then a JSON object.
const opencodeVerbose = `anthropic/claude-sonnet-4
{
  "id": "claude-sonnet-4",
  "providerID": "anthropic",
  "name": "Claude Sonnet 4",
  "status": "active",
  "cost": {"input": 3, "output": 15, "cache": {"read": 0.3, "write": 3.75}},
  "capabilities": {"reasoning": true},
  "variants": {"high": {}, "low": {}, "max": {}}
}
opencode/beam-free
{
  "id": "beam-free",
  "providerID": "opencode",
  "name": "Beam Free",
  "status": "active",
  "cost": {"input": 0, "output": 0, "cache": {"read": 0, "write": 0}},
  "capabilities": {"reasoning": true},
  "variants": {"medium": {}, "low": {}}
}
openai/gpt-old
{
  "id": "gpt-old",
  "providerID": "openai",
  "name": "GPT Old",
  "status": "deprecated",
  "cost": {"input": 1, "output": 2},
  "capabilities": {"reasoning": false}
}
`

func TestOpencodeCatalogListsFreeFirstWithPricesAndModelEfforts(t *testing.T) {
	models := parseOpencode(opencodeVerbose)

	if len(models) != 2 {
		t.Fatalf("models = %+v, want 2 (deprecated hidden)", models)
	}
	free, paid := models[0], models[1]
	if free.ID != "opencode/beam-free" || !free.Free || !strings.Contains(free.Label, "free") {
		t.Errorf("first model should be the free one: %+v", free)
	}
	if !slices.Equal(free.Efforts, []string{"low", "medium"}) {
		t.Errorf("free efforts = %v, want ordered low, medium", free.Efforts)
	}
	if paid.ID != "anthropic/claude-sonnet-4" || paid.Free || !strings.Contains(paid.Label, "$3") || !strings.Contains(paid.Label, "$15") {
		t.Errorf("paid model should show its price: %+v", paid)
	}
	if !slices.Equal(paid.Efforts, []string{"low", "high", "max"}) {
		t.Errorf("paid efforts = %v", paid.Efforts)
	}
}

func TestCodexCatalogReadsNamesAndSupportedEfforts(t *testing.T) {
	cache := []byte(`{"models":[{"slug":"gpt-6-luna","display_name":"GPT-6 Luna","description":"Frontier coding model.",
	"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"xhigh"}]}]}`)

	models := parseCodexCache(cache)

	if len(models) != 1 || models[0].ID != "gpt-6-luna" || !strings.Contains(models[0].Label, "GPT-6 Luna") {
		t.Fatalf("models = %+v", models)
	}
	if !slices.Equal(models[0].Efforts, []string{"low", "high", "xhigh"}) {
		t.Errorf("efforts = %v", models[0].Efforts)
	}
}

func TestCursorCatalogIgnoresMessages(t *testing.T) {
	if got := parseCursor("No models available for this account.\n"); len(got) != 0 {
		t.Errorf("a message is not a model: %+v", got)
	}
	got := parseCursor("Available models:\n- sonnet-4\n- sonnet-4-thinking (default)\n- gpt-5\n")
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"sonnet-4", "sonnet-4-thinking", "gpt-5"}) {
		t.Errorf("ids = %v", ids)
	}
}

func TestCodexCatalogHidesModelsCodexHidesItself(t *testing.T) {
	cache := []byte(`{"models":[
	{"slug":"gpt-6-luna","visibility":"list","supported_reasoning_levels":[{"effort":"high"}]},
	{"slug":"codex-auto-review","visibility":"hide","supported_reasoning_levels":[{"effort":"high"}]}]}`)

	models := parseCodexCache(cache)

	if len(models) != 1 || models[0].ID != "gpt-6-luna" {
		t.Errorf("models = %+v, want only the listed one", models)
	}
}

func TestCopilotCatalogIncludesPremiumModelsAndCanBeCustomized(t *testing.T) {
	models := Catalog("copilot")
	if len(models) == 0 {
		t.Fatal("Copilot should list known premium models")
	}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
		premium := model.Tag == "Premium" || (model.Tier != "" && strings.HasSuffix(model.Tag, "· Premium"))
		if !premium || !strings.Contains(model.Note, "access depends") {
			t.Errorf("model should explain premium access limits: %+v", model)
		}
	}
	for _, id := range []string{"gpt-5.3-codex", "claude-sonnet-4.6", "gemini-4-argon", "grok-4.7"} {
		if !slices.Contains(ids, id) {
			t.Errorf("Copilot catalog missing %q: %v", id, ids)
		}
	}
	if got := Efforts("copilot"); !slices.Equal(got, []string{"low", "medium", "high", "xhigh"}) {
		t.Errorf("efforts = %v", got)
	}
}

// Excerpt of `claude --help` from the installed CLI.
const claudeHelp = `  --effort <level>                      Effort level for the current session
                                        (low, medium, high, xhigh, max)
  --model <model>                       Model for the current session. Provide
                                        an alias for the latest model (e.g.
                                        'fable', 'opus', or 'sonnet') or a
                                        model's full name.
`

func TestClaudeCatalogComesFromTheInstalledCLIHelp(t *testing.T) {
	models := parseClaudeHelp(claudeHelp)

	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"fable", "opus", "sonnet", "haiku"}) {
		t.Errorf("ids = %v, want the aliases from help plus haiku", ids)
	}
	if !slices.Equal(models[0].Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("efforts = %v", models[0].Efforts)
	}
}

func TestClaudeCatalogFallsBackWhenHelpChanges(t *testing.T) {
	models := parseClaudeHelp("usage: claude [options]")
	if len(models) != 3 || len(models[0].Efforts) == 0 {
		t.Errorf("fallback = %+v, want opus, sonnet, haiku with default efforts", models)
	}
}
