package modelprovider

import (
	"fmt"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/openrouterbatch"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

// batchAuth authenticates Batch API calls. The batch endpoints take a bearer
// token whatever header the wrapped request shape normally uses.
func batchAuth(
	providerConfig modelstore.ModelProviderConfigRecord,
	apiKey string,
	configured route.Auth,
) route.Auth {
	if providerConfig.AuthKind == modelstore.ModelProviderAuthKindBearerToken || apiKey == "" {
		return configured
	}
	return route.Chain{configured, route.BearerToken{Token: apiKey}}
}

// resolveOpenRouterBatchClient wraps the synchronous client for the provider's
// API format. The wrapped client keeps the variant it would have had without
// batching, so request building, parsing, and replay identity are unchanged.
func (r Resolver) resolveOpenRouterBatchClient(
	resolved model.ResolvedClient,
	providerConfig modelstore.ModelProviderConfigRecord,
	revision modelstore.ConfiguredModelRevisionRecord,
	capabilities model.Capabilities,
	auth route.Auth,
	httpClient *http.Client,
	batchesAuth route.Auth,
) (model.ResolvedClient, error) {
	batchesURL, err := openrouterbatch.BatchesURLForBaseURL(providerConfig.BaseURL)
	if err != nil {
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"invalid_model_provider_config",
			"The configured model provider base URL cannot be used with the OpenRouter Batch API.",
			err,
		)
	}
	idleTimeout := time.Duration(providerConfig.IdleTimeoutMS) * time.Millisecond
	var inner openrouterbatch.RouteProvider
	switch providerConfig.APIFormat {
	case modelprotocol.APIFormatOpenAIResponses:
		inner = openairesponses.Client{
			ModelProviderConfigID: providerConfig.ID.String(),
			Auth:                  auth,
			BaseURL:               providerConfig.BaseURL,
			EndpointPath:          providerConfig.EndpointPath,
			ProviderModelSlug:     revision.ProviderModelSlug,
			HTTPClient:            httpClient,
			IdleTimeout:           idleTimeout,
			ModelCapabilities:     capabilities,
			APIVariant:            modelprotocol.APIVariantDefault,
			APIVariantOptions:     revision.APIVariantOptions,
		}
	case modelprotocol.APIFormatOpenAIChatCompletions:
		inner = openaichatcompletions.Client{
			ModelProviderConfigID: providerConfig.ID.String(),
			Auth:                  auth,
			BaseURL:               providerConfig.BaseURL,
			EndpointPath:          providerConfig.EndpointPath,
			ProviderModelSlug:     revision.ProviderModelSlug,
			HTTPClient:            httpClient,
			IdleTimeout:           idleTimeout,
			ModelCapabilities:     capabilities,
			APIVariant:            modelprotocol.APIVariantOpenRouter,
			APIVariantOptions:     revision.APIVariantOptions,
		}
	case modelprotocol.APIFormatAnthropicMessages:
		inner = anthropicmessages.Client{
			ModelProviderConfigID: providerConfig.ID.String(),
			Auth:                  auth,
			BaseURL:               providerConfig.BaseURL,
			EndpointPath:          providerConfig.EndpointPath,
			ProviderModelSlug:     revision.ProviderModelSlug,
			HTTPClient:            httpClient,
			IdleTimeout:           idleTimeout,
			ModelCapabilities:     capabilities,
			APIVariant:            modelprotocol.APIVariantDefault,
			APIVariantOptions:     revision.APIVariantOptions,
		}
	default:
		return model.ResolvedClient{}, resolverError(
			model.ErrorKindInvalidRequest,
			"unsupported_model_api_format",
			"The configured model provider uses an unsupported API format.",
			fmt.Errorf("unsupported api_format %q", providerConfig.APIFormat),
		)
	}
	resolved.Client = openrouterbatch.Client{
		Inner:      inner,
		BatchesURL: batchesURL,
		Auth:       batchesAuth,
		HTTPClient: httpClient,
	}
	return resolved, nil
}
