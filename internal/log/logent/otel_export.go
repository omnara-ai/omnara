package logent

import (
	"context"
	"net/url"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func OtelExportBatchAttempt(
	ctx context.Context, batch executionstore.OtelExportBatch,
) (context.Context, *log.Event) {
	var attempt int32
	var oldest time.Time
	for _, delivery := range batch.Deliveries {
		attempt = max(attempt, delivery.AttemptCount)
		if oldest.IsZero() || delivery.CreatedAt.Before(oldest) {
			oldest = delivery.CreatedAt
		}
	}
	event := log.NewEvent(ctx, "otel_export.batch_attempt", log.Fields{
		"org.id":                  batch.OrgID,
		"otel_export.exporter_id": batch.ExporterID,
		"otel_export.turns":       len(batch.Deliveries),
		"otel_export.attempt":     attempt,
	})
	ctx = log.WithEvent(ctx, event)
	if !oldest.IsZero() {
		log.Attach(ctx, log.Fields{"otel_export.age_seconds": max(time.Since(oldest).Seconds(), 0)})
	}
	return ctx, event
}

func OtelExportDestination(ctx context.Context, destination executionstore.OtelExportDestination) {
	fields := log.Fields{
		"otel_export.variant":  string(destination.Variant),
		"otel_export.encoding": string(destination.Encoding),
	}
	if parsed, err := url.Parse(destination.Endpoint); err == nil {
		fields["otel_export.endpoint_host"] = parsed.Hostname()
	}
	log.Attach(ctx, fields)
}

func OtelExportPayload(ctx context.Context, spans, bytes int) {
	log.Attach(ctx, log.Fields{"otel_export.spans": spans, "otel_export.body_bytes": bytes})
}

func OtelExportResponse(ctx context.Context, status int) {
	log.Attach(ctx, log.Fields{"http.status_code": status})
}

func OtelExportBatchResult(ctx context.Context, outcome, cause string, err error) {
	log.Attach(ctx, log.Fields{"otel_export.outcome": outcome, "otel_export.cause": cause})
	log.Error(ctx, err)
	switch outcome {
	case "dropped", "retry_scheduled":
		log.Level(ctx, log.WarnLevel)
	case "retry_failed", "complete_failed":
		log.Level(ctx, log.ErrorLevel)
	case "canceled":
		log.Level(ctx, log.DebugLevel)
	}
}

func OtelExportStoreFailed(ctx context.Context, operation string, err error) {
	event := log.NewEvent(ctx, "otel_export.store_failed", log.Fields{"otel_export.store_operation": operation})
	event.Error(err)
	event.Done(ctx)
}
