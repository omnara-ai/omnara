package openaichatcompletions

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestCompatForVariant(t *testing.T) {
	for variant, want := range map[modelprotocol.APIVariant]compat{
		modelprotocol.APIVariantDefault: {
			sendsStoreFalse: true, reasoningFormat: reasoningFormatOpenAI, usageViaStreamOptions: true,
		},
		modelprotocol.APIVariantBedrock: {
			sendsStoreFalse: true, reasoningFormat: reasoningFormatOpenAI, usageViaStreamOptions: true,
			usageChunkCompletesStream: true,
		},
		modelprotocol.APIVariantOpenRouter: {
			reasoningFormat: reasoningFormatOpenRouter, refinesErrorsFromRawDetails: true,
			reportsServedProviderAndCost: true, parsesPDFDocuments: true, routesFallbackModels: true,
		},
	} {
		if got := compatFor(variant); got != want {
			t.Fatalf("compatFor(%q) = %+v, want %+v", variant, got, want)
		}
	}
}
