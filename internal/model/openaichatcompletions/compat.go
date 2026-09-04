package openaichatcompletions

import "github.com/omnara-ai/omnara/internal/modelprotocol"

type reasoningFormat string

const (
	reasoningFormatOpenAI     reasoningFormat = "openai"
	reasoningFormatOpenRouter reasoningFormat = "openrouter"
)

type compat struct {
	sendsStoreFalse              bool
	reasoningFormat              reasoningFormat
	usageViaStreamOptions        bool
	usageChunkCompletesStream    bool
	refinesErrorsFromRawDetails  bool
	reportsServedProviderAndCost bool
	parsesPDFDocuments           bool
	routesFallbackModels         bool
}

func compatFor(variant modelprotocol.APIVariant) compat {
	switch variant {
	case modelprotocol.APIVariantOpenRouter:
		return compat{
			reasoningFormat:              reasoningFormatOpenRouter,
			refinesErrorsFromRawDetails:  true,
			reportsServedProviderAndCost: true,
			parsesPDFDocuments:           true,
			routesFallbackModels:         true,
		}
	case modelprotocol.APIVariantBedrock:
		return compat{
			sendsStoreFalse:           true,
			reasoningFormat:           reasoningFormatOpenAI,
			usageViaStreamOptions:     true,
			usageChunkCompletesStream: true,
		}
	default:
		return compat{
			sendsStoreFalse:       true,
			reasoningFormat:       reasoningFormatOpenAI,
			usageViaStreamOptions: true,
		}
	}
}

func (c Client) compat() compat {
	return compatFor(c.ModelAPIVariant())
}
