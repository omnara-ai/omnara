package logent

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
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
	Usage              modelenvelope.Usage

	StartedAt      time.Time
	Headers        *time.Duration
	TTFT           *time.Duration
	StreamComplete *time.Duration
	Duration       time.Duration
}

func (r ModelCallRecord) UsageReported() bool {
	return r.Usage.InputTokens > 0 || r.Usage.OutputTokens > 0
}

func (r ModelCallRecord) CacheRatio() (float64, bool) {
	if !r.Usage.CacheReported || r.Usage.InputTokens <= 0 {
		return 0, false
	}
	return float64(r.Usage.CacheReadTokens) / float64(r.Usage.InputTokens), true
}

func ModelCall(ctx context.Context, r ModelCallRecord) {
	var input, cached, cacheWrite, output, reasoning, cacheRatio any
	if u := r.Usage; r.UsageReported() {
		input, cacheWrite, output, reasoning = u.InputTokens, u.CacheWriteTokens, u.OutputTokens, u.ReasoningTokens
		if u.CacheReported {
			cached = u.CacheReadTokens
		}
	}
	if ratio, ok := r.CacheRatio(); ok {
		cacheRatio = ratio
	}
	fields := log.Fields{
		"id":                       r.CallID,
		"attempt":                  r.Attempt,
		"model_call_context_id":    optional(r.ModelCallContextID),
		"api_format":               r.APIFormat,
		"api_variant":              r.APIVariant,
		"model":                    r.Model,
		"served_model":             optional(r.ServedModel),
		"endpoint_host":            r.EndpointHost,
		"provider_request_id":      optional(r.ProviderRequestID),
		"streamed":                 r.Streamed,
		"outcome":                  string(r.Outcome),
		"status_code":              optional(r.StatusCode),
		"error_kind":               optional(r.ErrorKind),
		"error_code":               optional(r.ErrorCode),
		"request_bytes":            r.RequestBytes,
		"cache_ratio":              cacheRatio,
		"usage.reported":           r.UsageReported(),
		"usage.input_tokens":       input,
		"usage.cached_tokens":      cached,
		"usage.cache_write_tokens": cacheWrite,
		"usage.output_tokens":      output,
		"usage.reasoning_tokens":   reasoning,
		"started_at":               r.StartedAt.UTC(),
		"headers_ms":               optionalMilliseconds(r.Headers),
		"ttft_ms":                  optionalMilliseconds(r.TTFT),
		"stream_complete_ms":       optionalMilliseconds(r.StreamComplete),
		"duration_ms":              r.Duration.Milliseconds(),
	}
	prefixed := make(log.Fields, len(fields))
	for key, value := range fields {
		prefixed["model_call."+r.Purpose+"."+key] = value
	}
	log.Attach(ctx, prefixed)
	if r.Outcome == ModelCallError {
		log.Escalate(ctx, log.WarnLevel)
	}
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
