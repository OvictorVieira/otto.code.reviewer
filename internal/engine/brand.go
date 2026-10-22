package engine

import "strings"

// vendorBrand names the company behind the vendor segment of an OpenRouter-style id
// ("openrouter/z-ai/glm-5" has the vendor "z-ai"). A vendor not listed here is shown as its
// own id written in words, so a new vendor still gets a readable group.
var vendorBrand = map[string]string{
	"openai": "OpenAI", "anthropic": "Anthropic", "google": "Google", "deepseek": "DeepSeek",
	"qwen": "Qwen", "mistralai": "Mistral AI", "z-ai": "Z.ai", "nvidia": "NVIDIA", "minimax": "MiniMax",
	"meta-llama": "Meta", "meta": "Meta", "moonshotai": "Moonshot AI", "x-ai": "xAI", "tencent": "Tencent",
	"cohere": "Cohere", "amazon": "Amazon", "perplexity": "Perplexity", "xiaomi": "Xiaomi",
	"bytedance-seed": "ByteDance", "bytedance": "ByteDance", "arcee-ai": "Arcee AI", "stepfun": "StepFun", "ibm-granite": "IBM", "openrouter": "OpenRouter",
}

// zenBrand maps a word in the name of an opencode Zen model to its maker. Zen serves models from
// several makers under one "opencode/" prefix, so the prefix says nothing about the brand.
var zenBrand = []struct{ word, brand string }{
	{"deepseek", "DeepSeek"}, {"glm", "Z.ai"}, {"mimo", "Xiaomi"}, {"nemotron", "NVIDIA"},
}

// cursorBrand maps the start of a cursor model id to its maker.
var cursorBrand = []struct{ prefix, brand string }{
	{"claude", "Anthropic"}, {"gpt", "OpenAI"}, {"gemini", "Google"}, {"cursor-grok", "xAI"}, {"grok", "xAI"},
	{"kimi", "Moonshot AI"}, {"glm", "Z.ai"},
}

// BrandOf says whose model it is, for engines that list models from many makers: opencode and
// cursor. A maker Otto does not know falls back to the engine's own name, never to a guess.
func BrandOf(engineName, id string) string {
	switch engineName {
	case "opencode":
		return opencodeBrand(id)
	case "cursor":
		for _, b := range cursorBrand {
			if strings.HasPrefix(id, b.prefix) {
				return b.brand
			}
		}
		return "Cursor"
	}
	return ""
}

func opencodeBrand(id string) string {
	provider, rest, _ := strings.Cut(id, "/")
	switch provider {
	case "opencode":
		for _, b := range zenBrand {
			if strings.Contains(rest, b.word) {
				return b.brand
			}
		}
		return "OpenCode Zen"
	case "openrouter":
		vendor, _, _ := strings.Cut(rest, "/")
		return brandOfVendor(vendor)
	}
	return brandOfVendor(provider)
}

func brandOfVendor(vendor string) string {
	vendor = strings.TrimPrefix(vendor, "~")
	if b, ok := vendorBrand[vendor]; ok {
		return b
	}
	words := strings.FieldsFunc(vendor, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
