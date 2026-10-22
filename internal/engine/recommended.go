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
//   - opencode's free Zen models are not recommended: deepseek-v4.1-flash-free took 8 to 11 minutes,
//     wrote a plan asking for confirmation instead of the answer, and broke the JSON format.
var recommended = []struct {
	engine string
	tier   Tier
	id     string
}{
	{"claude", Light, "haiku"}, {"claude", Medium, "sonnet"}, {"claude", Heavy, "opus"},
	{"codex", Medium, "gpt-6-luna"},
	{"cursor", Light, "claude-haiku-5-5-medium"}, {"cursor", Medium, "claude-sonnet-5-5-high"}, {"cursor", Heavy, "claude-opus-5-5-high"},
	{"opencode", Light, "openrouter/anthropic/claude-haiku-5.5"}, {"opencode", Medium, "openrouter/anthropic/claude-sonnet-5.5"}, {"opencode", Heavy, "openrouter/anthropic/claude-opus-5.5"},
	{"copilot", Light, "claude-haiku-5.5"}, {"copilot", Medium, "claude-sonnet-5.5"}, {"copilot", Heavy, "claude-opus-5.5"},
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
