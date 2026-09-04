package model

import (
	"net/url"
	"regexp"
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

type PromptCacheAffinity string

const (
	PromptCacheAffinityNone           PromptCacheAffinity = ""
	PromptCacheAffinitySessionID      PromptCacheAffinity = "session_id"
	PromptCacheAffinityPromptCacheKey PromptCacheAffinity = "prompt_cache_key"
)

type PromptCacheRoute struct {
	APIFormat         modelprotocol.APIFormat
	APIVariant        modelprotocol.APIVariant
	BaseURL           string
	ProviderModelSlug string
}

type PromptCachePlan struct {
	ConversationKey string
	Affinity        PromptCacheAffinity
	Explicit        bool
	LongRetention   bool
}

func PlanPromptCache(
	route PromptCacheRoute,
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
	if capability.affinity != PromptCacheAffinityNone && bundle.AgentID != uuid.Nil {
		plan.Affinity = capability.affinity
		plan.ConversationKey = bundle.AgentID.String()
	}
	return plan
}

type promptCacheCapability struct {
	explicit      bool
	longRetention bool
	affinity      PromptCacheAffinity
}

func promptCacheCapabilityFor(route PromptCacheRoute) promptCacheCapability {
	switch route.APIFormat {
	case modelprotocol.APIFormatAnthropicMessages:
		return promptCacheCapability{
			explicit: true,
			longRetention: route.APIVariant != modelprotocol.APIVariantBedrock ||
				bedrockOneHourCacheModel.MatchString(normalizedProviderModelSlug(route.ProviderModelSlug)),
		}
	case modelprotocol.APIFormatOpenAIResponses:
		return openAIPromptCacheCapability(route.BaseURL)
	case modelprotocol.APIFormatOpenAIChatCompletions:
		if route.APIVariant == modelprotocol.APIVariantOpenRouter {
			return openRouterPromptCacheCapability(parseOpenRouterModel(route.ProviderModelSlug))
		}
		return openAIPromptCacheCapability(route.BaseURL)
	}
	return promptCacheCapability{}
}

func openAIPromptCacheCapability(baseURL string) promptCacheCapability {
	if !isOpenAIHost(baseURL) {
		return promptCacheCapability{}
	}
	return promptCacheCapability{affinity: PromptCacheAffinityPromptCacheKey}
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

// Bedrock offers the one-hour cache on Claude 4.5 and newer:
// https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html
var bedrockOneHourCacheModel = regexp.MustCompile(`claude-(?:[a-z]+-)?(?:4-[5-9]|[5-9])(?:[-:]|$)`)
