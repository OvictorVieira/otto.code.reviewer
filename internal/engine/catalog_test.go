package engine

import (
	"context"
	"io"
	"net/http"
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

func TestCopilotCatalogFiltersUnavailableAndUnsupportedModels(t *testing.T) {
	models, err := parseCopilotModels([]byte(`{"data":[
		{"id":"gpt-6-luna","name":"GPT-6 Luna","vendor":"OpenAI","model_picker_category":"lightweight",
		 "supported_endpoints":["/responses"],"policy":{"state":"enabled"},
		 "capabilities":{"type":"chat","supports":{"reasoning_effort":["high","low"]}}},
		{"id":"claude-sonnet-5.5","name":"Claude Sonnet 5.5","vendor":"Anthropic","model_picker_category":"versatile",
		 "supported_endpoints":["/chat/completions"],"policy":{"state":"disabled"}},
		{"id":"copilot-search-a","name":"Copilot Agent A","supported_endpoints":["/chat/completions"]},
		{"id":"embedding-model","name":"Embedding","model_picker_category":"lightweight","supported_endpoints":[]},
		{"id":"new-model","name":"New Model","vendor":"Example","model_picker_category":"versatile",
		 "supported_endpoints":["/chat/completions"],"capabilities":{"type":"chat"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want the enabled and policy-unspecified chat models", models)
	}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if !slices.Contains(ids, "gpt-6-luna") || !slices.Contains(ids, "new-model") {
		t.Errorf("models = %v, want only selectable models", ids)
	}
	for _, model := range models {
		if model.ID == "gpt-6-luna" {
			if model.Group != "OpenAI" || model.Tag != "Lightweight" ||
				!slices.Equal(model.Efforts, []string{"low", "high"}) {
				t.Errorf("model metadata = %+v", model)
			}
		}
	}
	if got := Efforts("copilot"); !slices.Equal(got, []string{"low", "medium", "high", "xhigh"}) {
		t.Errorf("efforts = %v", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFetchCopilotModelsUsesAuthenticatedAccountCatalog(t *testing.T) {
	t.Setenv("GITHUB_COPILOT_INTEGRATION_ID", "otto-test")
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != copilotModelsURL {
			t.Errorf("request = %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization header was not set")
		}
		if req.Header.Get("Copilot-Integration-Id") != "otto-test" {
			t.Errorf("integration id = %q", req.Header.Get("Copilot-Integration-Id"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-6-luna","name":"GPT-6 Luna","vendor":"OpenAI","model_picker_category":"lightweight","supported_endpoints":["/responses"]}]}`)),
			Header:     make(http.Header),
		}, nil
	})}

	models, err := fetchCopilotModels(context.Background(), "test-token", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-6-luna" {
		t.Fatalf("models = %+v", models)
	}
}

func TestFetchCopilotModelsUsesDefaultIntegrationID(t *testing.T) {
	t.Setenv("GITHUB_COPILOT_INTEGRATION_ID", "")
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Copilot-Integration-Id"); got != "vscode-chat" {
			t.Errorf("integration id = %q, want vscode-chat", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
			Header:     make(http.Header),
		}, nil
	})}
	if _, err := fetchCopilotModels(context.Background(), "test-token", client); err != nil {
		t.Fatal(err)
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

func TestAgyCatalogParsesModelsAndIgnoresFetching(t *testing.T) {
	out := `Fetching available models...
gemini-3.8-flash-high	Gemini 3.8 Flash (High)
gemini-3.8-flash-medium	Gemini 3.8 Flash (Medium)
gemini-3.8-flash-low	Gemini 3.8 Flash (Low)
claude-sonnet-4-6	Claude Sonnet 4.6 (Thinking)
claude-opus-4-6-thinking	Claude Opus 4.6 (Thinking)
gpt-oss-120b-medium	GPT-OSS 120B (Medium)
`
	models := parseAgy(out)
	if len(models) != 6 {
		t.Fatalf("len = %d, want 6", len(models))
	}
	if models[0].ID != "gemini-3.8-flash-high" || models[0].Name != "Gemini 3.8 Flash (High)" {
		t.Errorf("model 0 = %+v", models[0])
	}
	if !slices.Equal(models[0].Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("efforts = %v", models[0].Efforts)
	}
	if got := Efforts("agy"); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("Efforts(agy) = %v", got)
	}
}
