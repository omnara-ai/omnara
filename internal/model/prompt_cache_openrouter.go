package model

import (
	"slices"
	"strings"
)

// Variant suffixes chain, e.g. model:free:online in
// https://openrouter.ai/docs/guides/features/web-search.
type openRouterModel struct {
	id       string
	variants []string
}

func parseOpenRouterModel(providerModelSlug string) openRouterModel {
	id, suffix, _ := strings.Cut(normalizedProviderModelSlug(providerModelSlug), ":")
	model := openRouterModel{id: id}
	if suffix != "" {
		model.variants = strings.Split(suffix, ":")
	}
	return model
}

func (m openRouterModel) hasVariant(variant string) bool {
	return slices.Contains(m.variants, variant)
}

// Explicit-caching models per https://openrouter.ai/docs/guides/best-practices/prompt-caching;
// the Alibaba-served entries hold for the paid endpoints, not the :free provider pool.
var openRouterExplicitCacheModels = map[string]bool{
	"deepseek/deepseek-v3.2": true,
	"qwen/qwen3-max":         true,
	"qwen/qwen-plus":         true,
	"qwen/qwen3.6-plus":      true,
	"qwen/qwen3-coder-plus":  true,
	"qwen/qwen3-coder-flash": true,
}

func openRouterPromptCacheCapability(model openRouterModel) promptCacheCapability {
	claude := strings.HasPrefix(model.id, "anthropic/claude-")
	return promptCacheCapability{
		explicit:      claude || (openRouterExplicitCacheModels[model.id] && !model.hasVariant("free")),
		longRetention: claude,
		affinity:      PromptCacheAffinitySessionID,
	}
}
