package openaichatcompletions

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestCompatForVariant(t *testing.T) {
	for variant, want := range map[modelprotocol.APIVariant]compat{
		modelprotocol.APIVariantDefault: {
			sendsStoreFalse: true, reasoningFormat: reasoningFormatOpenAI,
			conversationKeyField: conversationKeyFieldPromptCacheKey, usageViaStreamOptions: true,
		},
		modelprotocol.APIVariantBedrock: {
			sendsStoreFalse: true, reasoningFormat: reasoningFormatOpenAI,
			conversationKeyField: conversationKeyFieldPromptCacheKey, usageViaStreamOptions: true,
			usageChunkCompletesStream: true,
		},
		modelprotocol.APIVariantOpenRouter: {
			reasoningFormat: reasoningFormatOpenRouter, conversationKeyField: conversationKeyFieldSessionID,
			refinesErrorsFromRawDetails:  true,
			reportsServedProviderAndCost: true, parsesPDFDocuments: true, routesFallbackModels: true,
		},
	} {
		if got := compatFor(model.ProviderRoute{APIVariant: variant}); got != want {
			t.Fatalf("compatFor(%q) = %+v, want %+v", variant, got, want)
		}
	}
}
