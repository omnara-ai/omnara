package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage"
)

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
