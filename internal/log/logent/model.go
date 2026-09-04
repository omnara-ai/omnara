package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
)

type ModelInputTokenEstimateRecord struct {
	RequestedProviderModelSlug string
	ServedProviderModelSlug    string
	APIFormat                  modelprotocol.APIFormat
	APIVariant                 modelprotocol.APIVariant
	EstimatedTokens            int
	LocalEstimatedTokens       int
	EstimatedMediaTokens       int
	ProviderReportedTokens     int
}

func ModelInputTokenEstimate(ctx context.Context, record ModelInputTokenEstimateRecord) {
	if record.EstimatedTokens <= 0 || record.ProviderReportedTokens <= 0 {
		return
	}
	log.Attach(ctx, log.Fields{
		"model_request.requested_provider_model_slug": record.RequestedProviderModelSlug,
		"model_request.api_format":                    record.APIFormat,
		"model_request.api_variant":                   record.APIVariant,
		"model_request.input_tokens.estimated":        record.EstimatedTokens,
		"model_request.input_tokens.local_estimated":  record.LocalEstimatedTokens,
		"model_request.input_tokens.media_estimated":  record.EstimatedMediaTokens,
		"model_response.served_provider_model_slug":   record.ServedProviderModelSlug,
		"model_response.input_tokens.reported":        record.ProviderReportedTokens,
	})
}

func ModelCatalogProbeFailed(ctx context.Context, modelProviderConfigID storage.ID, message string) {
	log.Attach(ctx, log.Fields{
		"model_catalog.probe.result":             "failed",
		"model_catalog.probe.error":              message,
		"model_catalog.model_provider_config_id": modelProviderConfigID,
	})
	log.Level(ctx, log.WarnLevel)
}

func ModelContextMediaOmitted(ctx context.Context, count int, bytes int64, budget int64) {
	if count <= 0 {
		return
	}
	log.Attach(ctx, log.Fields{
		"model_context.media_omitted.count":  count,
		"model_context.media_omitted.bytes":  bytes,
		"model_context.media_omitted.reason": "resolved_media_byte_budget",
		"model_context.media_byte_budget":    budget,
	})
	log.Level(ctx, log.WarnLevel)
}

func ModelRequestMediaOmittedForBodyLimit(
	ctx context.Context,
	artifactCount, bodyBytes, bodyByteLimit int,
) {
	if artifactCount <= 0 {
		return
	}
	log.Attach(ctx, log.Fields{
		"model_request.media_omitted.artifact_count": artifactCount,
		"model_request.media_omitted.reason":         "provider_body_limit",
		"model_request.body_bytes_before_omission":   bodyBytes,
		"model_request.body_byte_limit":              bodyByteLimit,
	})
	log.Level(ctx, log.WarnLevel)
}

func ModelResponseProviderCostInvalid(ctx context.Context) {
	log.Attach(ctx, log.Fields{
		"model_response.provider_reported_cost_usd.unavailable_reason": "invalid_provider_value",
	})
	log.Level(ctx, log.WarnLevel)
}

func ModelResponseProviderCostBYOKStateMissing(ctx context.Context) {
	log.Attach(ctx, log.Fields{
		"model_response.provider_reported_cost_usd.accounting_limitation": "byok_state_missing",
	})
}

func ModelResponseProviderCostBYOKStateInvalid(ctx context.Context) {
	log.Attach(ctx, log.Fields{
		"model_response.provider_reported_cost_usd.accounting_limitation": "invalid_byok_state",
	})
	log.Level(ctx, log.WarnLevel)
}

func ModelResponseProviderCostBYOKComponentMissing(ctx context.Context) {
	log.Attach(ctx, log.Fields{
		"model_response.provider_reported_cost_usd.unavailable_reason": "byok_cost_component_missing",
	})
	log.Level(ctx, log.WarnLevel)
}
