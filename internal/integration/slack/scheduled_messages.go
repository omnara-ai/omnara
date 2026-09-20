package slack

import (
	"context"
	"time"
)

const scheduledMarker = "omnara_scheduled_thread"

// PostScheduledMessage publishes the opening before the agent exists, so its
// receipt (not an agent ID) identifies the operation. Marker contents grant no
// Omnara authority.
func PostScheduledMessage(
	ctx context.Context,
	config OAuthConfig,
	target MessageTarget,
	receiptID, text string,
) (APIResult, error) {
	return postMessageAt(ctx, config.HTTPClient, config.APIURL, target, map[string]any{
		"channel": target.Channel, "text": text,
		"metadata": messageMetadata{EventType: scheduledMarker, EventPayload: map[string]any{"receipt_id": receiptID}},
	})
}

func ReconcileScheduledMessage(
	ctx context.Context,
	config OAuthConfig,
	target MessageTarget,
	receiptID, botUserID string,
	since time.Time,
) (string, bool, APIResult, error) {
	// Allow modest clock skew between the database's attempt time and Slack.
	// The receipt marker and bot identity, not the window, identify the message.
	since = since.Add(-time.Minute)
	return reconcileMessageAt(ctx, config.HTTPClient, config.APIURL, target, since, func(message readbackMessage) bool {
		return botUserID != "" && message.User == botUserID && message.Metadata != nil &&
			message.Metadata.EventType == scheduledMarker && message.Metadata.EventPayload["receipt_id"] == receiptID
	})
}
