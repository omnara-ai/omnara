package logent

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model/calltrace"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func ModelCatalogProbeFailed(ctx context.Context, modelProviderConfigID uuid.UUID, message string) {
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

// ModelCall emits one model.call event per provider attempt. It carries sizes,
// token counts, and timings only; never prompt or response content, headers,
// or credentials. Unreached phases are logged as null.
func ModelCall(ctx context.Context, r calltrace.Record) {
	fields := log.Fields{
		"model_call.id":                    r.CallID,
		"model_call.purpose":               string(r.Scope.Purpose),
		"model_call.attempt":               r.Scope.Attempt,
		"model_call.api_format":            r.APIFormat,
		"model_call.api_variant":           r.APIVariant,
		"model_call.model":                 r.Model,
		"model_call.endpoint_host":         r.EndpointHost,
		"model_call.streamed":              r.Streamed,
		"model_call.outcome":               string(r.Outcome),
		"model_call.cache":                 string(r.Cache()),
		"model_call.request_bytes":         r.RequestBytes,
		"model_call.started_at":            r.StartedAt.UTC(),
		"model_call.upload_complete_ms":    optionalMilliseconds(r.UploadComplete),
		"model_call.headers_ms":            optionalMilliseconds(r.Headers),
		"model_call.first_content_ms":      optionalMilliseconds(r.FirstContent),
		"model_call.first_text_ms":         optionalMilliseconds(r.FirstText),
		"model_call.stream_complete_ms":    optionalMilliseconds(r.StreamComplete),
		"model_call.duration_ms":           r.Duration.Milliseconds(),
		"model_call.usage.reported":        r.Usage != nil,
		"model_call.usage.input_tokens":    nil,
		"model_call.usage.cached_tokens":   nil,
		"model_call.usage.output_tokens":   nil,
		"model_call.first_content_kind":    nil,
		"model_call.model_call_context_id": nil,
	}
	if r.Scope.ModelCallContextID != "" {
		fields["model_call.model_call_context_id"] = r.Scope.ModelCallContextID
	}
	if r.FirstContentKind != "" {
		fields["model_call.first_content_kind"] = string(r.FirstContentKind)
	}
	if r.ServedModel != "" {
		fields["model_call.served_model"] = r.ServedModel
	}
	if r.ProviderRequestID != "" {
		fields["model_call.provider_request_id"] = r.ProviderRequestID
	}
	if r.StatusCode != 0 {
		fields["model_call.status_code"] = r.StatusCode
	}
	if r.ErrorKind != "" {
		fields["model_call.error_kind"] = r.ErrorKind
	}
	if r.ErrorCode != "" {
		fields["model_call.error_code"] = r.ErrorCode
	}
	if r.Usage != nil {
		fields["model_call.usage.input_tokens"] = r.Usage.InputTokens
		fields["model_call.usage.cached_tokens"] = r.Usage.CachedInputTokens
		fields["model_call.usage.cache_write_tokens"] = r.Usage.CacheWriteTokens
		fields["model_call.usage.output_tokens"] = r.Usage.OutputTokens
		fields["model_call.usage.reasoning_tokens"] = r.Usage.ReasoningTokens
	}
	event := log.NewEvent(ctx, "model.call", fields)
	if r.Outcome == calltrace.OutcomeError {
		event.Level(log.WarnLevel)
	}
	event.Done(ctx)
}

func optionalMilliseconds(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return d.Milliseconds()
}
