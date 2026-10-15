package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	type webhookTarget struct {
		orgID  uuid.UUID
		events []string
	}
	webhookTargets := make(map[uuid.UUID]webhookTarget)
	for agentID := range pending.AgentEvents() {
		webhookTargets[agentID] = webhookTarget{}
	}
	for _, update := range pending.ToolCallUpdates() {
		webhookTargets[update.AgentID] = webhookTarget{}
	}
	for agentID := range webhookTargets {
		target, err := q.GetAgentEventWebhookTarget(ctx, dbsqlc.GetAgentEventWebhookTargetParams{ID: agentID})
		if errors.Is(err, pgx.ErrNoRows) {
			delete(webhookTargets, agentID)
			continue
		}
		if err != nil {
			return fmt.Errorf("load event webhook target: %w", err)
		}
		if target.Url == "" {
			delete(webhookTargets, agentID)
			continue
		}
		var events []string
		if err := json.Unmarshal(target.Events, &events); err != nil {
			return fmt.Errorf("decode event webhook events: %w", err)
		}
		webhookTargets[agentID] = webhookTarget{orgID: target.OrgID, events: events}
	}
	for agentID, events := range pending.AgentEvents() {
		target, enabled := webhookTargets[agentID]
		if !enabled {
			continue
		}
		for _, event := range events {
			if !slices.Contains(target.events, event.Kind) {
				continue
			}
			if err := q.EnqueueEventWebhookDelivery(ctx, dbsqlc.EnqueueEventWebhookDeliveryParams{
				OrgID: target.orgID, AgentID: agentID, EventSequence: &event.Sequence,
			}); err != nil {
				return fmt.Errorf("enqueue event webhook: %w", err)
			}
		}
	}
	for _, update := range pending.ToolCallUpdates() {
		target, enabled := webhookTargets[update.AgentID]
		if !enabled || !slices.Contains(target.events, "tool_call_update") {
			continue
		}
		var interactionUpdate json.RawMessage
		if update.InteractionUpdate != nil {
			var err error
			interactionUpdate, err = json.Marshal(update.InteractionUpdate)
			if err != nil {
				return fmt.Errorf("encode webhook interaction update: %w", err)
			}
		}
		if err := q.EnqueueEventWebhookDelivery(ctx, dbsqlc.EnqueueEventWebhookDeliveryParams{
			OrgID: target.orgID, AgentID: update.AgentID,
			ToolCallID: &update.ToolCallID, ToolState: &update.State,
			InteractionUpdate: webhookJSON(interactionUpdate),
		}); err != nil {
			return fmt.Errorf("enqueue tool webhook: %w", err)
		}
	}
	return nil
}
