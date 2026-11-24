package modelprovider

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func (r Resolver) LookupGenerationUsage(
	ctx context.Context,
	orgID, providerConfigID uuid.UUID,
	generationID string,
) (model.GenerationUsage, error) {
	if r.Models == nil || r.Secrets == nil {
		return model.GenerationUsage{}, errors.New("model provider resolver model and secret stores are required")
	}
	providerConfig, err := r.Models.GetModelProviderConfig(ctx, orgID, providerConfigID)
	if err != nil {
		return model.GenerationUsage{}, err
	}
	if providerConfig.APIFormat != modelprotocol.APIFormatOpenAIChatCompletions ||
		providerConfig.APIVariant != modelprotocol.APIVariantOpenRouter {
		return model.GenerationUsage{}, fmt.Errorf(
			"model provider config %s does not report generation usage", providerConfig.ID,
		)
	}
	auth, err := r.providerAuth(ctx, providerConfig)
	if err != nil {
		return model.GenerationUsage{}, err
	}
	httpClient, err := r.httpClientForProviderConfig(providerConfig)
	if err != nil {
		return model.GenerationUsage{}, err
	}
	return openaichatcompletions.Client{
		ModelProviderConfigID: providerConfig.ID.String(),
		Auth:                  auth,
		BaseURL:               providerConfig.BaseURL,
		EndpointPath:          providerConfig.EndpointPath,
		HTTPClient:            httpClient,
		APIVariant:            providerConfig.APIVariant,
	}.LookupGenerationUsage(ctx, generationID)
}
