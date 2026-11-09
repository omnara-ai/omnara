package logent

import (
	"context"
	"time"

	"github.com/google/uuid"

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

type ModelCallOutcome string

const (
	ModelCallSuccess  ModelCallOutcome = "success"
	ModelCallError    ModelCallOutcome = "error"
	ModelCallCanceled ModelCallOutcome = "canceled"
)

type ModelCallUsage struct {
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	CacheReported     bool
	OutputTokens      int
	ReasoningTokens   int
}

type ModelCallRecord struct {
	CallID             string
	Purpose            string
	ModelCallContextID string
	Attempt            int
	APIFormat          string
	APIVariant         string
	Model              string
	ServedModel        string
	EndpointHost       string
	ProviderRequestID  string
	Streamed           bool
	StatusCode         int
	Outcome            ModelCallOutcome
	ErrorKind          string
	ErrorCode          string
	RequestBytes       int
	Usage              *ModelCallUsage

	StartedAt      time.Time
	Headers        *time.Duration
	TTFT           *time.Duration
	StreamComplete *time.Duration
	Duration       time.Duration
}

func (r ModelCallRecord) CacheRatio() (float64, bool) {
	if r.Usage == nil || !r.Usage.CacheReported || r.Usage.InputTokens <= 0 {
		return 0, false
	}
	return float64(r.Usage.CachedInputTokens) / float64(r.Usage.InputTokens), true
}

func ModelCall(ctx context.Context, r ModelCallRecord) {
	var input, cached, cacheWrite, output, reasoning, cacheRatio any
	if u := r.Usage; u != nil {
		input, output, reasoning = u.InputTokens, u.OutputTokens, u.ReasoningTokens
		if u.CacheReported {
			cached, cacheWrite = u.CachedInputTokens, u.CacheWriteTokens
		}
	}
	if ratio, ok := r.CacheRatio(); ok {
		cacheRatio = ratio
	}
	event := log.NewEvent(ctx, "model.call", log.Fields{
		"model_call.id":                       r.CallID,
		"model_call.purpose":                  r.Purpose,
		"model_call.attempt":                  r.Attempt,
		"model_call.model_call_context_id":    optional(r.ModelCallContextID),
		"model_call.api_format":               r.APIFormat,
		"model_call.api_variant":              r.APIVariant,
		"model_call.model":                    r.Model,
		"model_call.served_model":             optional(r.ServedModel),
		"model_call.endpoint_host":            r.EndpointHost,
		"model_call.provider_request_id":      optional(r.ProviderRequestID),
		"model_call.streamed":                 r.Streamed,
		"model_call.outcome":                  string(r.Outcome),
		"model_call.status_code":              optional(r.StatusCode),
		"model_call.error_kind":               optional(r.ErrorKind),
		"model_call.error_code":               optional(r.ErrorCode),
		"model_call.request_bytes":            r.RequestBytes,
		"model_call.cache_ratio":              cacheRatio,
		"model_call.usage.reported":           r.Usage != nil,
		"model_call.usage.input_tokens":       input,
		"model_call.usage.cached_tokens":      cached,
		"model_call.usage.cache_write_tokens": cacheWrite,
		"model_call.usage.output_tokens":      output,
		"model_call.usage.reasoning_tokens":   reasoning,
		"model_call.started_at":               r.StartedAt.UTC(),
		"model_call.headers_ms":               optionalMilliseconds(r.Headers),
		"model_call.ttft_ms":                  optionalMilliseconds(r.TTFT),
		"model_call.stream_complete_ms":       optionalMilliseconds(r.StreamComplete),
		"model_call.duration_ms":              r.Duration.Milliseconds(),
	})
	if r.Outcome == ModelCallError {
		event.Level(log.WarnLevel)
	}
	event.Done(ctx)
}

func optional[T comparable](value T) any {
	var zero T
	if value == zero {
		return nil
	}
	return value
}

func optionalMilliseconds(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return d.Milliseconds()
}
