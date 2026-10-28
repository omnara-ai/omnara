package agentexecution

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func webhookJSON(value json.RawMessage) *json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return &value
}

func (u *Unit) enqueueWebhooks(ctx context.Context) error {
	pending := u.notifications
	if pending == nil || (len(pending.AgentEvents()) == 0 && len(pending.ToolCallUpdates()) == 0) {
		return nil
	}
	q := dbsqlc.New(u.DB())
	webhooksByAgent := make(map[uuid.UUID][]dbsqlc.ListMatchingEventWebhooksRow)
	for agentID := range pending.AgentEvents() {
		webhooksByAgent[agentID] = nil
	}
	for _, update := range pending.ToolCallUpdates() {
		webhooksByAgent[update.AgentID] = nil
	}
	for agentID := range webhooksByAgent {
		webhooks, err := q.ListMatchingEventWebhooks(ctx, dbsqlc.ListMatchingEventWebhooksParams{AgentID: agentID})
		if err != nil {
			return fmt.Errorf("load matching event webhooks: %w", err)
		}
		webhooksByAgent[agentID] = webhooks
	}
	for agentID, events := range pending.AgentEvents() {
		for _, webhook := range webhooksByAgent[agentID] {
			for _, event := range events {
				if !slices.Contains(webhook.EventTypes, event.Kind) {
					continue
				}
				if err := q.EnqueueEventWebhookDelivery(ctx, dbsqlc.EnqueueEventWebhookDeliveryParams{
					OrgID: webhook.OrgID, WebhookID: webhook.ID, AgentID: agentID, EventSequence: &event.Sequence,
				}); err != nil {
					return fmt.Errorf("enqueue event webhook delivery: %w", err)
				}
			}
		}
	}
	for _, update := range pending.ToolCallUpdates() {
		var interactionUpdate json.RawMessage
		if update.InteractionUpdate != nil {
			var err error
			interactionUpdate, err = json.Marshal(update.InteractionUpdate)
			if err != nil {
				return fmt.Errorf("encode webhook interaction update: %w", err)
			}
		}
		for _, webhook := range webhooksByAgent[update.AgentID] {
			if !slices.Contains(webhook.EventTypes, "tool_call_update") {
				continue
			}
			if err := q.EnqueueEventWebhookDelivery(ctx, dbsqlc.EnqueueEventWebhookDeliveryParams{
				OrgID: webhook.OrgID, WebhookID: webhook.ID, AgentID: update.AgentID,
				ToolCallID: &update.ToolCallID, ToolState: &update.State,
				InteractionUpdate: webhookJSON(interactionUpdate),
			}); err != nil {
				return fmt.Errorf("enqueue tool-call webhook delivery: %w", err)
			}
		}
	}
	return nil
}
