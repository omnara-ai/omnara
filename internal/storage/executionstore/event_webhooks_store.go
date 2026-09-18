package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type EventWebhookTarget struct {
	ProjectID       uuid.UUID
	OrgID           uuid.UUID
	URL             string
	SigningSecretID string
}

func (s *Store) GetAgentEventWebhookTarget(ctx context.Context, agentID uuid.UUID) (EventWebhookTarget, error) {
	row, err := s.q.GetAgentEventWebhookTarget(ctx, dbsqlc.GetAgentEventWebhookTargetParams{ID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookTarget{}, nil
	}
	if err != nil {
		return EventWebhookTarget{}, fmt.Errorf("load event webhook target: %w", err)
	}
	return EventWebhookTarget{
		ProjectID: row.ProjectID, OrgID: row.OrgID, URL: row.Url, SigningSecretID: row.SigningSecretID,
	}, nil
}

func (s *Store) GetAgentEventForWebhook(
	ctx context.Context, projectID, agentID uuid.UUID, sequence int64,
) (AgentEventReadRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || sequence <= 0 {
		return AgentEventReadRecord{}, errors.New("project, agent, and positive event sequence are required")
	}
	rows, err := s.q.ListAgentEventsForRead(ctx, dbsqlc.ListAgentEventsForReadParams{
		ProjectID: projectID, AgentID: agentID, AfterSequence: sequence - 1, PageLimit: 1,
	})
	if err != nil {
		return AgentEventReadRecord{}, fmt.Errorf("load event for webhook: %w", err)
	}
	if len(rows) != 1 || rows[0].Sequence != sequence {
		return AgentEventReadRecord{}, storeerr.ErrNotFound
	}
	return agentEventReadRecordFromSQLC(rows[0]), nil
}

func enqueueEventWebhooksTx(ctx context.Context, tx pgx.Tx, pending *notifications.TxNotifications) error {
	if pending == nil || (len(pending.AgentEvents()) == 0 && len(pending.ToolCallUpdates()) == 0) {
		return nil
	}
	q := dbsqlc.New(tx)
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
			if len(target.events) > 0 && !slices.Contains(target.events, event.Kind) {
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
		if !enabled || (len(target.events) > 0 && !slices.Contains(target.events, "tool_call_update")) {
			continue
		}
		if err := q.EnqueueEventWebhookDelivery(ctx, dbsqlc.EnqueueEventWebhookDeliveryParams{
			OrgID: target.orgID, AgentID: update.AgentID,
			ToolCallID: &update.ToolCallID, ToolState: &update.State,
		}); err != nil {
			return fmt.Errorf("enqueue tool webhook: %w", err)
		}
	}
	return nil
}

type EventWebhookDelivery struct {
	OrgID         uuid.UUID
	ID            uuid.UUID
	AgentID       uuid.UUID
	EventSequence *int64
	ToolCallID    *uuid.UUID
	ToolState     *string
	AttemptCount  int32
	ClaimToken    uuid.UUID
	Retryable     bool
}

func (s *Store) ClaimEventWebhookDelivery(
	ctx context.Context, excludedOrgIDs []uuid.UUID,
) (EventWebhookDelivery, error) {
	row, err := s.q.ClaimEventWebhookDelivery(ctx, dbsqlc.ClaimEventWebhookDeliveryParams{ExcludedOrgIds: excludedOrgIDs})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookDelivery{}, storeerr.ErrNotFound
	}
	if err != nil {
		return EventWebhookDelivery{}, fmt.Errorf("claim event webhook: %w", err)
	}
	return EventWebhookDelivery{
		ID: row.ID, AgentID: row.AgentID, OrgID: row.OrgID,
		EventSequence: row.EventSequence, ToolCallID: row.ToolCallID, ToolState: row.ToolState,
		AttemptCount: row.AttemptCount, ClaimToken: *row.ClaimToken,
		Retryable: eventWebhookRetryable(row.ToolState, row.ToolType, row.ToolName),
	}, nil
}

func eventWebhookRetryable(state *string, toolType, toolName string) bool {
	if state == nil {
		return false
	}
	switch ToolCallState(*state) {
	case ToolCallStateAwaitingPermission:
		return true
	case ToolCallStateReady:
		return toolType == toolcatalog.ToolTypeCustom
	case ToolCallStateRunning:
		return toolType == toolcatalog.ToolTypeBuiltIn && toolName == toolcatalog.ToolNameAskQuestion
	default:
		return false
	}
}

func (s *Store) CompleteEventWebhookDelivery(ctx context.Context, id, claimToken uuid.UUID) error {
	return s.q.CompleteEventWebhookDelivery(ctx, dbsqlc.CompleteEventWebhookDeliveryParams{
		ID: id, ClaimToken: &claimToken,
	})
}

func (s *Store) RetryEventWebhookDelivery(ctx context.Context, id, claimToken uuid.UUID, delay time.Duration) error {
	return s.q.RetryEventWebhookDelivery(ctx, dbsqlc.RetryEventWebhookDeliveryParams{
		ID: id, ClaimToken: &claimToken, DelaySeconds: delay.Seconds(),
	})
}

func (s *Store) DeleteExpiredEventWebhookDeliveries(ctx context.Context, limit int32) (int64, error) {
	return s.q.DeleteExpiredEventWebhookDeliveries(ctx, dbsqlc.DeleteExpiredEventWebhookDeliveriesParams{
		LimitCount: limit,
	})
}

func (s *Store) ReadEventWebhookSigningSecret(ctx context.Context, target EventWebhookTarget) (string, error) {
	id, err := publicid.Decode(publicid.KindSecret, target.SigningSecretID)
	if err != nil {
		return "", err
	}
	if s.secrets == nil {
		return "", errors.New("secret store is required")
	}
	record, err := s.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: target.OrgID, ProjectID: target.ProjectID, SecretID: id, Kind: secrets.KindGeneric,
	})
	if err != nil {
		return "", err
	}
	return record.Payload[secrets.KeyValue], nil
}
