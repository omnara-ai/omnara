package model

import (
	"testing"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestPlanPromptCache(t *testing.T) {
	agentID := uuid.MustParse("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	bundle := modelcontext.Bundle{AgentID: agentID}
	anthropic := ProviderRoute{
		APIFormat:  modelprotocol.APIFormatAnthropicMessages,
		APIVariant: modelprotocol.APIVariantDefault,
	}
	bedrock := anthropic
	bedrock.APIVariant = modelprotocol.APIVariantBedrock
	bedrock.ProviderModelSlug = "anthropic.claude-sonnet-5"
	openRouterClaude := ProviderRoute{
		APIFormat:         modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:        modelprotocol.APIVariantOpenRouter,
		BaseURL:           "https://openrouter.ai/api/v1",
		ProviderModelSlug: "~Anthropic/Claude-Sonnet-4",
	}
	openRouterKimi := openRouterClaude
	openRouterKimi.ProviderModelSlug = "moonshotai/kimi-k3"
	openRouterClaudeVariant := openRouterClaude
	openRouterClaudeVariant.ProviderModelSlug = "anthropic/claude-sonnet-5:nitro"
	openAIChat := ProviderRoute{
		APIFormat:  modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant: modelprotocol.APIVariantDefault,
		BaseURL:    "https://api.openai.com/v1",
	}
	openAIResponses := ProviderRoute{
		APIFormat:  modelprotocol.APIFormatOpenAIResponses,
		APIVariant: modelprotocol.APIVariantDefault,
		BaseURL:    "HTTPS://API.OPENAI.COM/v1/",
	}
	deepSeekChat := openAIChat
	deepSeekChat.BaseURL = "https://api.deepseek.com/v1"
	lookalikeResponses := openAIResponses
	lookalikeResponses.BaseURL = "https://example.openai.com.evil.test/v1"
	withKey := func(plan PromptCachePlan) PromptCachePlan {
		plan.ConversationKey = agentID.String()
		return plan
	}
	for _, tc := range []struct {
		name      string
		route     ProviderRoute
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
		{name: "none disables everything", route: openRouterClaude, bundle: bundle, retention: CacheRetentionNone},
		{
			name: "openrouter claude", route: openRouterClaude, bundle: bundle,
			want: withKey(PromptCachePlan{Explicit: true}),
		},
		{
			name: "openrouter claude long", route: openRouterClaude, bundle: bundle, retention: CacheRetentionLong,
			want: withKey(PromptCachePlan{Explicit: true, LongRetention: true}),
		},
		{
			name: "openrouter automatic model long", route: openRouterKimi, bundle: bundle, retention: CacheRetentionLong,
			want: withKey(PromptCachePlan{}),
		},
		{
			name: "openrouter claude routing variant long", route: openRouterClaudeVariant, bundle: bundle,
			retention: CacheRetentionLong,
			want:      withKey(PromptCachePlan{Explicit: true, LongRetention: true}),
		},
		{
			name: "openai chat completions", route: openAIChat, bundle: bundle,
			want: withKey(PromptCachePlan{}),
		},
		{
			name: "openai responses long keeps only the key", route: openAIResponses, bundle: bundle,
			retention: CacheRetentionLong,
			want:      withKey(PromptCachePlan{}),
		},
		{name: "openai-compatible host long", route: deepSeekChat, bundle: bundle, retention: CacheRetentionLong},
		{name: "openai lookalike host", route: lookalikeResponses, bundle: bundle},
		{name: "unknown format", route: ProviderRoute{APIFormat: "other"}, bundle: bundle},
		{name: "no agent means no key", route: openRouterKimi},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanPromptCache(tc.route, tc.bundle, tc.retention); got != tc.want {
				t.Fatalf("PlanPromptCache = %+v, want %+v", got, tc.want)
			}
		})
	}
}
