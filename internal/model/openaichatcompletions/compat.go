package openaichatcompletions

import (
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type reasoningFormat string

const (
	reasoningFormatOpenAI     reasoningFormat = "openai"
	reasoningFormatOpenRouter reasoningFormat = "openrouter"
)

type conversationKeyField string

const (
	conversationKeyFieldPromptCacheKey conversationKeyField = "prompt_cache_key"
	conversationKeyFieldSessionID      conversationKeyField = "session_id"
)

type compat struct {
	sendsStoreFalse              bool
	reasoningFormat              reasoningFormat
	conversationKeyField         conversationKeyField
	usageViaStreamOptions        bool
	usageChunkCompletesStream    bool
	refinesErrorsFromRawDetails  bool
	reportsServedProviderAndCost bool
	parsesPDFDocuments           bool
	routesFallbackModels         bool
}

func compatFor(route model.ProviderRoute) compat {
	switch route.APIVariant {
	case modelprotocol.APIVariantOpenRouter:
		return compat{
			reasoningFormat:              reasoningFormatOpenRouter,
			conversationKeyField:         conversationKeyFieldSessionID,
			refinesErrorsFromRawDetails:  true,
			reportsServedProviderAndCost: true,
			parsesPDFDocuments:           true,
			routesFallbackModels:         true,
		}
	case modelprotocol.APIVariantBedrock:
		return compat{
			sendsStoreFalse:           true,
			reasoningFormat:           reasoningFormatOpenAI,
			conversationKeyField:      conversationKeyFieldPromptCacheKey,
			usageViaStreamOptions:     true,
			usageChunkCompletesStream: true,
		}
	default:
		return compat{
			sendsStoreFalse:       true,
			reasoningFormat:       reasoningFormatOpenAI,
			conversationKeyField:  conversationKeyFieldPromptCacheKey,
			usageViaStreamOptions: true,
		}
	}
}

func (c Client) providerRoute() model.ProviderRoute {
	return model.ProviderRoute{
		APIFormat:         modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:        c.ModelAPIVariant(),
		BaseURL:           c.endpoint().ResolvedBaseURL(),
		ProviderModelSlug: c.RequestedProviderModelSlug(),
	}
}

func (c Client) compat() compat {
	return compatFor(c.providerRoute())
}
