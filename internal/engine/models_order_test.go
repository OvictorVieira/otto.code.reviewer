package engine

import (
	"slices"
	"strings"
	"testing"
)

func TestBrandComesFromTheModelNotFromTheRouteItIsServedThrough(t *testing.T) {
	// Real ids from the live catalogs. opencode serves 372 models through openrouter and nine through
	// its own Zen service, so the provider in the id says nothing about who made the model.
	cases := []struct{ engine, id, want string }{
		{"opencode", "openrouter/anthropic/claude-haiku-5.5", "Anthropic"},
		{"opencode", "openrouter/~openai/gpt-latest", "OpenAI"},
		{"opencode", "openrouter/z-ai/glm-5", "Z.ai"},
		{"opencode", "openrouter/mistralai/mistral-large", "Mistral AI"},
		{"opencode", "openrouter/x-ai/grok-4.7", "xAI"},
		{"opencode", "openrouter/meta-llama/llama-4", "Meta"},
		{"opencode", "openrouter/moonshotai/kimi-k3", "Moonshot AI"},
		{"opencode", "openrouter/aion-labs/aion-2", "Aion Labs"},
		{"opencode", "openrouter/bytedance/ui-tars-1.5-7b", "ByteDance"}, // the same maker as bytedance-seed
		{"opencode", "openrouter/bytedance-seed/seed-2.0-mini", "ByteDance"},
		{"opencode", "openrouter/arcee-ai/trinity-large-thinking", "Arcee AI"},
		{"opencode", "opencode/deepseek-v4.1-flash-free", "DeepSeek"},
		{"opencode", "opencode/glm-5.3-flash-free", "Z.ai"},
		{"opencode", "opencode/mimo-v2.6-flash-free", "Xiaomi"},
		{"opencode", "opencode/nemotron-3-ultra-free", "NVIDIA"},
		{"opencode", "opencode/beam-free", "OpenCode Zen"},
		{"opencode", "anthropic/claude-sonnet-4", "Anthropic"},
		{"cursor", "claude-opus-5-5-high", "Anthropic"},
		{"cursor", "gpt-5.6-sol-high", "OpenAI"},
		{"cursor", "gpt-5.3-codex-low", "OpenAI"},
		{"cursor", "gemini-3.7-flash-high", "Google"},
		{"cursor", "cursor-grok-4.5-high", "xAI"},
		{"cursor", "grok-4.7-low", "xAI"},
		{"cursor", "kimi-k3-low", "Moonshot AI"},
		{"cursor", "glm-5.2-high", "Z.ai"},
		{"cursor", "composer-2.5", "Cursor"},
		{"cursor", "auto", "Cursor"},
		{"cursor", "muse-spark-1.3-low", "Cursor"}, // a brand Otto does not know falls back to the engine
	}
	for _, c := range cases {
		if got := BrandOf(c.engine, c.id); got != c.want {
			t.Errorf("BrandOf(%s, %s) = %q, want %q", c.engine, c.id, got, c.want)
		}
	}
}

func TestEveryEngineRecommendsModelsThatReallyExistInItsCatalog(t *testing.T) {
	// Ids copied from the live catalogs. The table is a judgment, not a benchmark, so this guards the
	// part that can silently break: a pick that no longer matches any real id.
	cases := []struct {
		engine, id string
		tier       Tier
	}{
		{"claude", "haiku", Light}, {"claude", "sonnet", Medium}, {"claude", "opus", Heavy},
		{"codex", "gpt-6-luna", Medium},
		{"cursor", "claude-haiku-5-5-medium", Light}, {"cursor", "claude-sonnet-5-5-high", Medium}, {"cursor", "claude-opus-5-5-high", Heavy},
		{"opencode", "openrouter/anthropic/claude-haiku-5.5", Light}, {"opencode", "openrouter/anthropic/claude-sonnet-5.5", Medium}, {"opencode", "openrouter/anthropic/claude-opus-5.5", Heavy},
		{"copilot", "claude-haiku-5.5", Light}, {"copilot", "claude-sonnet-5.5", Medium}, {"copilot", "claude-opus-5.5", Heavy},
	}
	for _, c := range cases {
		got, ok := RecommendedTier(c.engine, c.id)
		if !ok || got != c.tier {
			t.Errorf("RecommendedTier(%s, %s) = %q, %v, want %q", c.engine, c.id, got, ok, c.tier)
		}
	}
}

func TestOnlyTheListedModelsAreRecommended(t *testing.T) {
	// deepseek-v4.1-flash-free is deliberately left out: in a real review it took 8 to 11 minutes, wrote a plan
	// and asked for confirmation instead of the answer, and broke the JSON format.
	for _, c := range []struct{ engine, id string }{
		{"opencode", "opencode/deepseek-v4.1-flash-free"},
		{"opencode", "openrouter/anthropic/claude-opus-5"},
		{"opencode", "openrouter/anthropic/claude-fable-5.1"},
		{"claude", "fable"},
		{"cursor", "gpt-5.6-sol-high"},
		{"codex", "gpt-5.6-terra"},
		{"copilot", "grok-4.7"},
		{"nobody", "sonnet"},
	} {
		if tier, ok := RecommendedTier(c.engine, c.id); ok {
			t.Errorf("RecommendedTier(%s, %s) = %q, want it left unrecommended", c.engine, c.id, tier)
		}
	}
}

func TestThereAreRecommendationsForLightMediumAndHeavyReviews(t *testing.T) {
	seen := map[Tier]bool{}
	for _, engine := range Known {
		for _, id := range []string{"haiku", "sonnet", "opus", "gpt-6-luna", "claude-haiku-5-5-medium", "claude-sonnet-5-5-high", "claude-opus-5-5-high",
			"openrouter/anthropic/claude-haiku-5.5", "openrouter/anthropic/claude-sonnet-5.5", "openrouter/anthropic/claude-opus-5.5",
			"claude-haiku-5.5", "claude-sonnet-5.5", "claude-opus-5.5"} {
			if tier, ok := RecommendedTier(engine, id); ok {
				seen[tier] = true
			}
		}
	}
	if !seen[Light] || !seen[Medium] || !seen[Heavy] {
		t.Errorf("tiers recommended = %v, want light, medium and heavy", seen)
	}
}

func opencodeModels() []Model {
	return []Model{
		{ID: "openrouter/openai/gpt-6-sol", Name: "GPT-6 Sol", Group: "openrouter", Tag: "$2 / $10", InUSD: 2, OutUSD: 10, PriceKnown: true},
		{ID: "opencode/glm-5.3-flash-free", Name: "GLM-5.3-Flash Free", Group: "Free", Tag: "Free", Free: true, PriceKnown: true},
		{ID: "openrouter/anthropic/claude-opus-5.5", Name: "Claude Opus 5.5", Group: "openrouter", Tag: "$4 / $20", InUSD: 4, OutUSD: 20, PriceKnown: true},
		{ID: "openrouter/deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash", Group: "openrouter", Tag: "$0.091 / $0.182", InUSD: 0.091, OutUSD: 0.182, PriceKnown: true},
		{ID: "opencode/deepseek-v4.1-flash-free", Name: "DeepSeek V4.1 Flash Free", Group: "Free", Tag: "Free", Free: true, PriceKnown: true},
		{ID: "openrouter/anthropic/claude-haiku-5.5", Name: "Claude Haiku 5.5", Group: "openrouter", Tag: "$0.1 / $0.5", InUSD: 0.1, OutUSD: 0.5, PriceKnown: true},
		{ID: "openrouter/anthropic/claude-sonnet-5.5", Name: "Claude Sonnet 5.5", Group: "openrouter", Tag: "$2 / $10", InUSD: 2, OutUSD: 10, PriceKnown: true},
		{ID: "openrouter/anthropic/claude-fable-5.1", Name: "Claude Fable 5.1", Group: "openrouter", Tag: "$10 / $50", InUSD: 10, OutUSD: 50, PriceKnown: true},
		{ID: "opencode/beam-free", Name: "Beam Free", Group: "Free", Tag: "Free", Free: true, PriceKnown: true},
	}
}

func ids(models []Model) []string {
	var out []string
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func TestRecommendedModelsComeFirstFromLightToHeavyThenEachBrandCheapestFirst(t *testing.T) {
	got := OrderModels("opencode", opencodeModels())

	want := []string{
		// recommended, light to heavy
		"openrouter/anthropic/claude-haiku-5.5", "openrouter/anthropic/claude-sonnet-5.5", "openrouter/anthropic/claude-opus-5.5",
		// then brands A to Z, free before paid and cheaper before dearer inside each
		"openrouter/anthropic/claude-fable-5.1",
		"opencode/deepseek-v4.1-flash-free", "openrouter/deepseek/deepseek-v4-flash",
		"openrouter/openai/gpt-6-sol",
		"opencode/beam-free",
		"opencode/glm-5.3-flash-free",
	}
	if !slices.Equal(ids(got), want) {
		t.Errorf("order =\n  %v\nwant\n  %v", ids(got), want)
	}
}

func TestGroupsAreBrandsWithRecommendedOnTopAndEachGroupIsContiguous(t *testing.T) {
	got := OrderModels("opencode", opencodeModels())

	var groups []string
	for _, m := range got {
		if len(groups) == 0 || groups[len(groups)-1] != m.Group {
			groups = append(groups, m.Group)
		}
	}
	want := []string{"Recommended", "Anthropic", "DeepSeek", "OpenAI", "OpenCode Zen", "Z.ai"}
	if !slices.Equal(groups, want) {
		t.Errorf("groups = %v, want %v (a group that appears twice would be shown twice)", groups, want)
	}
}

func TestARecommendedModelSaysWhichReviewsItIsFor(t *testing.T) {
	got := OrderModels("opencode", opencodeModels())

	haiku := got[0]
	if haiku.Tier != Light || !strings.Contains(haiku.Tag, "Recommended") || !strings.Contains(haiku.Tag, "light") || !strings.Contains(haiku.Tag, "$0.1 / $0.5") {
		t.Errorf("tag = %q, want the tier and the price kept", haiku.Tag)
	}
	if !strings.Contains(haiku.Note, "small") {
		t.Errorf("note = %q, want what a light review is", haiku.Note)
	}
	if got[2].Tier != Heavy || !strings.Contains(got[2].Note, "risky") {
		t.Errorf("heavy = %+v, want it described", got[2])
	}
	for _, m := range got[3:] {
		if m.Tier != "" || strings.Contains(m.Tag, "Recommended") {
			t.Errorf("%s must not look recommended: %+v", m.ID, m)
		}
	}
}

func TestModelsWithoutAPriceKeepTheirPlaceAfterTheOnesWithAPrice(t *testing.T) {
	got := OrderModels("cursor", []Model{
		{ID: "grok-4.7-low", Name: "grok-4.7-low", Group: "Cursor"},
		{ID: "gpt-5.6-sol-high", Name: "gpt-5.6-sol-high", Group: "Cursor"},
		{ID: "claude-opus-5-5-high", Name: "claude-opus-5-5-high", Group: "Cursor"},
	})

	if got[0].ID != "claude-opus-5-5-high" || got[0].Group != "Recommended" {
		t.Errorf("first = %+v, want the recommended one on top", got[0])
	}
	var groups []string
	for _, m := range got {
		groups = append(groups, m.Group)
	}
	if !slices.Equal(groups, []string{"Recommended", "OpenAI", "xAI"}) {
		t.Errorf("groups = %v, want brands for cursor too", groups)
	}
}

func TestEnginesThatAreOneBrandKeepTheirOwnGroup(t *testing.T) {
	got := OrderModels("claude", []Model{
		{ID: "opus", Name: "opus", Group: "Claude", Tag: "always latest"},
		{ID: "fable", Name: "fable", Group: "Claude", Tag: "always latest"},
		{ID: "haiku", Name: "haiku", Group: "Claude", Tag: "always latest"},
	})

	if got[0].ID != "haiku" || got[1].ID != "opus" || got[2].ID != "fable" {
		t.Errorf("order = %v, want haiku, opus, then the unrecommended fable", ids(got))
	}
	if got[2].Group != "Claude" || !strings.Contains(got[0].Tag, "always latest") {
		t.Errorf("fable = %+v, haiku tag = %q, want the engine group and the old tag kept", got[2], got[0].Tag)
	}
}

func TestCopilotKeepsItsBrandGroups(t *testing.T) {
	got := OrderModels("copilot", copilotModels())
	for _, m := range got {
		if m.ID == "claude-opus-5.5" && (m.Tier != Heavy || m.Group != "Recommended") {
			t.Errorf("opus = %+v", m)
		}
		if m.ID == "kimi-k3" && m.Group != "Moonshot AI" {
			t.Errorf("kimi = %+v, want its brand group", m)
		}
	}
}

func TestOpencodeCatalogKeepsThePriceAsNumbersAndNeverInventsOne(t *testing.T) {
	models := parseOpencode(opencodeVerbose)
	var free, paid Model
	for _, m := range models {
		if m.Free {
			free = m
		} else {
			paid = m
		}
	}
	if !free.PriceKnown || free.InUSD != 0 || free.OutUSD != 0 {
		t.Errorf("free = %+v, want a known zero price", free)
	}
	if !paid.PriceKnown || paid.InUSD != 3 || paid.OutUSD != 15 {
		t.Errorf("paid = %+v, want $3 / $15 as numbers", paid)
	}
	if cursor := parseCursor("Available models:\n- sonnet-4\n"); cursor[0].PriceKnown {
		t.Errorf("cursor lists no price, so none may be made up: %+v", cursor[0])
	}
}

func TestBrandsThatOnlyDifferInCaseAreOneGroup(t *testing.T) {
	// A group written two ways would be shown as two sections with the same name.
	got := OrderModels("copilot", []Model{
		{ID: "a", Name: "A", Group: "Google", Tag: "Premium"},
		{ID: "b", Name: "B", Group: "OpenAI", Tag: "Premium"},
		{ID: "c", Name: "C", Group: "google", Tag: "Premium"},
	})
	var groups []string
	for _, m := range got {
		if len(groups) == 0 || groups[len(groups)-1] != m.Group {
			groups = append(groups, m.Group)
		}
	}
	if len(groups) != 2 || groups[0] != "Google" || groups[1] != "OpenAI" {
		t.Errorf("groups = %v, want Google and OpenAI once each", groups)
	}
}
