package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/tooloutput"
)

func ModelContextToolResultsReduced(
	ctx context.Context,
	count, originalBytes, maxResultBytes int,
) {
	if count <= 0 {
		return
	}
	log.Attach(ctx, log.Fields{
		"model_context.tool_results_reduced.count":          count,
		"model_context.tool_results_reduced.original_bytes": originalBytes,
		"model_context.tool_result_max_bytes":               maxResultBytes,
	})
	if maxResultBytes >= tooloutput.MaxInlineToolResultBytes {
		log.Level(ctx, log.WarnLevel)
	} else {
		log.Level(ctx, log.InfoLevel)
	}
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
