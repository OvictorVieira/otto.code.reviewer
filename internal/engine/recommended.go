package engine

// Tier is the size of review a model is recommended for.
type Tier string

const (
	Light  Tier = "light"
	Medium Tier = "medium"
	Heavy  Tier = "heavy"
)

// tierDescription says, in the setup screen, what each size of review is.
var tierDescription = map[Tier]string{
	Light:  "for small or quick PRs",
	Medium: "for everyday PRs",
	Heavy:  "for large or risky PRs (auth, payments)",
}

// recommended is the maintainer's judgment of which model to start with, per engine and size of
// review. It is NOT a benchmark: change it whenever experience says otherwise. Each pick is a model
// that really exists in that engine's live catalog (a test guards the ids), and a model with no
// evidence for it is simply not listed.
//
// What the picks rest on:
//   - claude sonnet at high effort reviewed a 45-file pull request in about two minutes and returned
//     the expected format; the haiku and opus picks are the cheaper and the stronger of the same family.
//   - the same Anthropic trio is what opencode (through OpenRouter), cursor and copilot offer under
//     their own ids, so those engines get the same three picks.
//   - codex lists no sizes and no prices; gpt-6-luna, the model Otto used first, is the one pick.
//   - free models for opencode come from `otto eval models` (2026-10-29): a pull request with three planted
//     bugs (a race, an ignored error, a division by zero) reviewed with Otto's real prompt. Picked if the
//     answer was valid JSON, found all three bugs and took under three minutes: north-mini-code (33s),
//     big-pickle (40s, 4 findings) and inkling (51s, 5 findings, the most thorough). deepseek-v4.1-flash-free,
//     glm-5.3, mimo-v2.6, flint-40b and the nemotron-3.5 Zen models did not answer within four minutes.
//     Free tiers are rationed by the provider (OpenRouter: ~50 requests a day without credits), so they are
//     good picks while available but must never be a configuration's only engine.
var recommended = []struct {
	engine string
	tier   Tier
	id     string
	free   bool
}{
	{"claude", Light, "haiku", false}, {"claude", Medium, "sonnet", false}, {"claude", Heavy, "opus", false},
	{"codex", Medium, "gpt-6-luna", false},
	{"cursor", Light, "claude-haiku-5-5-medium", false}, {"cursor", Medium, "claude-sonnet-5-5-high", false}, {"cursor", Heavy, "claude-opus-5-5-high", false},
	{"opencode", Light, "openrouter/anthropic/claude-haiku-5.5", false}, {"opencode", Medium, "openrouter/anthropic/claude-sonnet-5.5", false}, {"opencode", Heavy, "openrouter/anthropic/claude-opus-5.5", false},
	{"opencode", Light, "openrouter/cohere/north-mini-code:free", true}, {"opencode", Medium, "opencode/big-pickle", true}, {"opencode", Heavy, "openrouter/thinkingmachines/inkling:free", true},
	{"copilot", Light, "claude-haiku-5.5", false}, {"copilot", Medium, "claude-sonnet-5.5", false}, {"copilot", Heavy, "claude-opus-5.5", false},
}

// RecommendedTier says which size of review the model is recommended for, if it is.
func RecommendedTier(engineName, id string) (Tier, bool) {
	for _, r := range recommended {
		if r.engine == engineName && r.id == id {
			return r.tier, true
		}
	}
	return "", false
}

// RecommendedTiers returns the recommended model for each size of pull request, from the free
// picks or the paid ones. ok is false when the engine has no full set.
func RecommendedTiers(engineName string, free bool) (TierModels, bool) {
	var t TierModels
	for _, r := range recommended {
		if r.engine != engineName || r.free != free {
			continue
		}
		switch r.tier {
		case Light:
			t.Light = r.id
		case Medium:
			t.Medium = r.id
		case Heavy:
			t.Heavy = r.id
		}
	}
	return t, t.Light != "" && t.Medium != "" && t.Heavy != ""
}

// RecommendedFree reports whether a recommended model is one of the free picks.
func RecommendedFree(engineName, id string) bool {
	for _, r := range recommended {
		if r.engine == engineName && r.id == id {
			return r.free
		}
	}
	return false
}
