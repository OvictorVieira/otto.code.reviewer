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
