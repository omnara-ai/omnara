package model

import (
	"testing"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestEffectiveCacheRetentionDefaultsToShort(t *testing.T) {
	for _, tc := range []struct {
		retention CacheRetention
		want      CacheRetention
	}{
		{retention: CacheRetentionUnset, want: CacheRetentionShort},
		{retention: CacheRetentionNone, want: CacheRetentionNone},
		{retention: CacheRetentionShort, want: CacheRetentionShort},
		{retention: CacheRetentionLong, want: CacheRetentionLong},
	} {
		if got := EffectiveCacheRetention(tc.retention); got != tc.want {
			t.Fatalf("EffectiveCacheRetention(%q) = %q, want %q", tc.retention, got, tc.want)
		}
	}
}

func TestPlanPromptCache(t *testing.T) {
	agentID := uuid.MustParse("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	bundle := modelcontext.Bundle{AgentID: agentID}
	anthropic := PromptCacheRoute{
		APIFormat:  modelprotocol.APIFormatAnthropicMessages,
		APIVariant: modelprotocol.APIVariantDefault,
	}
	bedrock := anthropic
	bedrock.APIVariant = modelprotocol.APIVariantBedrock
	bedrock.ProviderModelSlug = "anthropic.claude-sonnet-5"
	bedrockClaude3 := bedrock
	bedrockClaude3.ProviderModelSlug = "anthropic.claude-3-7-sonnet-20250219-v1:0"
	openRouterClaude := PromptCacheRoute{
		APIFormat:         modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		BaseURL:           "https://openrouter.ai/api/v1",
		ProviderModelSlug: "~Anthropic/Claude-Sonnet-4",
	}
	openRouterKimi := openRouterClaude
	openRouterKimi.ProviderModelSlug = "moonshotai/kimi-k3"
	openRouterQwen := openRouterClaude
	openRouterQwen.ProviderModelSlug = "Qwen/Qwen3-Coder-Plus"
	openRouterDeepSeekV4 := openRouterClaude
	openRouterDeepSeekV4.ProviderModelSlug = "deepseek/deepseek-v4"
	openAIChat := PromptCacheRoute{
		APIFormat:  modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant: modelprotocol.APIVariantDefault,
		BaseURL:    "https://api.openai.com/v1",
	}
	openAIResponses := PromptCacheRoute{
		APIFormat:  modelprotocol.APIFormatOpenAIResponses,
		APIVariant: modelprotocol.APIVariantDefault,
		BaseURL:    "HTTPS://API.OPENAI.COM/v1/",
	}
	deepSeekChat := openAIChat
	deepSeekChat.BaseURL = "https://api.deepseek.com/v1"
	lookalikeResponses := openAIResponses
	lookalikeResponses.BaseURL = "https://example.openai.com.evil.test/v1"
	withKey := func(plan PromptCachePlan, affinity PromptCacheAffinity) PromptCachePlan {
		plan.Affinity = affinity
		plan.ConversationKey = agentID.String()
		return plan
	}
	for _, tc := range []struct {
		name      string
		route     PromptCacheRoute
		bundle    modelcontext.Bundle
		retention CacheRetention
		want      PromptCachePlan
	}{
		{name: "anthropic unset", route: anthropic, bundle: bundle, want: PromptCachePlan{Explicit: true}},
		{
			name: "anthropic long", route: anthropic, bundle: bundle, retention: CacheRetentionLong,
			want: PromptCachePlan{Explicit: true, LongRetention: true},
		},
		{
			name: "bedrock long", route: bedrock, bundle: bundle, retention: CacheRetentionLong,
			want: PromptCachePlan{Explicit: true, LongRetention: true},
		},
		{
			name: "bedrock long on a five-minute-only model", route: bedrockClaude3, bundle: bundle,
			retention: CacheRetentionLong,
			want:      PromptCachePlan{Explicit: true},
		},
		{name: "none disables everything", route: openRouterClaude, bundle: bundle, retention: CacheRetentionNone},
		{
			name: "openrouter claude", route: openRouterClaude, bundle: bundle,
			want: withKey(PromptCachePlan{Explicit: true}, PromptCacheAffinitySessionID),
		},
		{
			name: "openrouter claude long", route: openRouterClaude, bundle: bundle, retention: CacheRetentionLong,
			want: withKey(PromptCachePlan{Explicit: true, LongRetention: true}, PromptCacheAffinitySessionID),
		},
		{
			name: "openrouter automatic model long", route: openRouterKimi, bundle: bundle, retention: CacheRetentionLong,
			want: withKey(PromptCachePlan{}, PromptCacheAffinitySessionID),
		},
		{
			name: "openrouter explicit five-minute-only model long", route: openRouterQwen, bundle: bundle,
			retention: CacheRetentionLong,
			want:      withKey(PromptCachePlan{Explicit: true}, PromptCacheAffinitySessionID),
		},
		{
			name: "openrouter deepseek v4 stays automatic", route: openRouterDeepSeekV4, bundle: bundle,
			want: withKey(PromptCachePlan{}, PromptCacheAffinitySessionID),
		},
		{
			name: "openai chat completions", route: openAIChat, bundle: bundle,
			want: withKey(PromptCachePlan{}, PromptCacheAffinityPromptCacheKey),
		},
		{
			name: "openai responses long", route: openAIResponses, bundle: bundle, retention: CacheRetentionLong,
			want: withKey(PromptCachePlan{LongRetention: true}, PromptCacheAffinityPromptCacheKey),
		},
		{name: "openai-compatible host long", route: deepSeekChat, bundle: bundle, retention: CacheRetentionLong},
		{name: "openai lookalike host", route: lookalikeResponses, bundle: bundle},
		{name: "unknown format", route: PromptCacheRoute{APIFormat: "other"}, bundle: bundle},
		{name: "no agent means no key", route: openRouterKimi},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanPromptCache(tc.route, tc.bundle, tc.retention); got != tc.want {
				t.Fatalf("PlanPromptCache = %+v, want %+v", got, tc.want)
			}
		})
	}
}
