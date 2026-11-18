package logent

import (
	"context"
	"net/url"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func OtelExportDeliveryAttempt(
	ctx context.Context, delivery executionstore.OtelExportDelivery,
) (context.Context, *log.Event) {
	event := log.NewEvent(ctx, "otel_export.delivery_attempt", log.Fields{
		"org.id":                  delivery.OrgID,
		"agent.id":                delivery.AgentID,
		"otel_export.delivery_id": delivery.ID,
		"otel_export.exporter_id": delivery.ExporterID,
		"otel_export.attempt":     delivery.AttemptCount,
	})
	ctx = log.WithEvent(ctx, event)
	if !delivery.CreatedAt.IsZero() {
		log.Attach(ctx, log.Fields{"otel_export.age_seconds": max(time.Since(delivery.CreatedAt).Seconds(), 0)})
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

// OtelExportPartialSuccess records only the count. The endpoint's message is
// receiver-controlled text that can echo prompts or tool results.
func OtelExportPartialSuccess(ctx context.Context, rejectedSpans int64) {
	log.Attach(ctx, log.Fields{"otel_export.rejected_spans": rejectedSpans})
}

func OtelExportDeliveryResult(ctx context.Context, outcome, cause string, err error) {
	log.Attach(ctx, log.Fields{"otel_export.outcome": outcome, "otel_export.cause": cause})
	log.Error(ctx, err)
	switch outcome {
	case "dropped", "retry_scheduled", "partially_rejected", "unacknowledged":
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
