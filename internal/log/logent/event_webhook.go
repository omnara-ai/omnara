package logent

import (
	"context"
	"net/url"
	"time"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func EventWebhookDelivery(
	ctx context.Context, delivery executionstore.EventWebhookDelivery,
) (context.Context, *log.Event) {
	event := log.NewEvent(ctx, "event_webhook.delivery", log.Fields{
		"org.id":                    delivery.OrgID,
		"agent.id":                  delivery.AgentID,
		"event_webhook.delivery_id": delivery.ID,
		"event_webhook.attempt":     delivery.AttemptCount,
	})
	ctx = log.WithEvent(ctx, event)
	if delivery.EventSequence != nil {
		log.Attach(ctx, log.Fields{"event_webhook.event_sequence": *delivery.EventSequence})
	}
	if delivery.ToolCallID != nil {
		log.Attach(ctx, log.Fields{"tool_call.id": *delivery.ToolCallID})
	}
	return ctx, event
}

func EventWebhookTarget(ctx context.Context, target executionstore.EventWebhookTarget) {
	fields := log.Fields{"project.id": target.ProjectID, "event_webhook.enabled": target.URL != ""}
	if parsed, err := url.Parse(target.URL); err == nil {
		fields["event_webhook.target_host"] = parsed.Hostname()
	}
	log.Attach(ctx, fields)
}

func EventWebhookKind(ctx context.Context, kind string) {
	log.Attach(ctx, log.Fields{"event_webhook.event_kind": kind})
}

func EventWebhookResponse(ctx context.Context, status int) {
	log.Attach(ctx, log.Fields{"http.status_code": status})
}

func EventWebhookDeliveryResult(ctx context.Context, outcome string, err error, retryAt time.Time) {
	log.Attach(ctx, log.Fields{"event_webhook.outcome": outcome})
	log.Error(ctx, err)
	if !retryAt.IsZero() {
		log.Attach(ctx, log.Fields{"event_webhook.retry_at": retryAt})
	}
	switch outcome {
	case "gave_up":
		log.Attach(ctx, log.Fields{"event_webhook.gave_up": true})
		log.Level(ctx, log.ErrorLevel)
	case "retry_scheduled":
		log.Level(ctx, log.WarnLevel)
	case "retry_failed", "complete_failed":
		log.Level(ctx, log.ErrorLevel)
	case "canceled":
		log.Level(ctx, log.DebugLevel)
	}
}

func EventWebhookStoreFailed(ctx context.Context, operation string, err error) {
	event := log.NewEvent(ctx, "event_webhook.store_failed", log.Fields{"event_webhook.store_operation": operation})
	event.Error(err)
	event.Done(ctx)
}
