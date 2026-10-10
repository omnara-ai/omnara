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

type webhookTarget struct {
	orgID  uuid.UUID
	events []string
}

func (u *Unit) CaptureWebhookEligibility(ctx context.Context, agents ...uuid.UUID) error {
	ids := make(map[uuid.UUID]struct{})
	for _, id := range agents {
		ids[id] = struct{}{}
	}
	for id := range u.notifications.AgentEvents() {
		ids[id] = struct{}{}
	}
	for _, update := range u.notifications.ToolCallUpdates() {
		ids[update.AgentID] = struct{}{}
	}
	q := dbsqlc.New(u.DB())
	for id := range ids {
		if _, captured := u.webhooks[id]; captured {
			continue
		}
		row, err := q.GetAgentEventWebhookTarget(ctx, dbsqlc.GetAgentEventWebhookTargetParams{ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			u.webhooks[id] = webhookTarget{}
			continue
		}
		if err != nil {
			return fmt.Errorf("load event webhook target: %w", err)
		}
		if row.Url == "" {
			u.webhooks[id] = webhookTarget{}
			continue
		}
		var events []string
		if err := json.Unmarshal(row.Events, &events); err != nil {
			return fmt.Errorf("decode event webhook events: %w", err)
		}
		u.webhooks[id] = webhookTarget{orgID: row.OrgID, events: events}
	}
	return nil
}

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
	if err := u.CaptureWebhookEligibility(ctx); err != nil {
		return err
	}
	q := dbsqlc.New(u.DB())
	webhookTargets := u.webhooks
	for agentID, events := range pending.AgentEvents() {
		target, enabled := webhookTargets[agentID]
		if !enabled || target.orgID == uuid.Nil {
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
