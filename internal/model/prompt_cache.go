package model

import (
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type CacheRetention string

const (
	CacheRetentionUnset CacheRetention = ""
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

func EffectiveCacheRetention(retention CacheRetention) CacheRetention {
	if retention == CacheRetentionUnset {
		return CacheRetentionShort
	}
	return retention
}

type ProviderRoute struct {
	APIFormat         modelprotocol.APIFormat
	APIVariant        modelprotocol.APIVariant
	BaseURL           string
	ProviderModelSlug string
}

type PromptCachePlan struct {
	ConversationKey string
	Explicit        bool
	LongRetention   bool
}

func PlanPromptCache(
	route ProviderRoute,
	bundle modelcontext.Bundle,
	retention CacheRetention,
) PromptCachePlan {
	retention = EffectiveCacheRetention(retention)
	if retention == CacheRetentionNone {
		return PromptCachePlan{}
	}
	capability := promptCacheCapabilityFor(route)
	plan := PromptCachePlan{
		Explicit:      capability.explicit,
		LongRetention: retention == CacheRetentionLong && capability.longRetention,
	}
	if capability.conversationKey && bundle.AgentID != uuid.Nil {
		plan.ConversationKey = bundle.AgentID.String()
	}
	return plan
}

type promptCacheCapability struct {
	explicit        bool
	longRetention   bool
	conversationKey bool
}

func promptCacheCapabilityFor(route ProviderRoute) promptCacheCapability {
	switch route.APIFormat {
	case modelprotocol.APIFormatAnthropicMessages:
		return promptCacheCapability{explicit: true, longRetention: true}
	case modelprotocol.APIFormatOpenAIResponses:
		return openAIPromptCacheCapability(route.BaseURL)
	case modelprotocol.APIFormatOpenAIChatCompletions:
		if route.APIVariant == modelprotocol.APIVariantOpenRouter {
			anthropic := strings.HasPrefix(normalizedProviderModelSlug(route.ProviderModelSlug), "anthropic/")
			return promptCacheCapability{
				explicit:        anthropic,
				longRetention:   anthropic,
				conversationKey: true,
			}
		}
		return openAIPromptCacheCapability(route.BaseURL)
	}
	return promptCacheCapability{}
}

func openAIPromptCacheCapability(baseURL string) promptCacheCapability {
	if !isOpenAIHost(baseURL) {
		return promptCacheCapability{}
	}
	return promptCacheCapability{conversationKey: true}
}

func isOpenAIHost(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func normalizedProviderModelSlug(providerModelSlug string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(providerModelSlug)), "~")
}
