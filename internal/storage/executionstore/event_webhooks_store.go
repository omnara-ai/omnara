package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const eventWebhookDeliveryWindow = 10 * time.Minute

type EventWebhookTarget struct {
	ProjectID       uuid.UUID
	OrgID           uuid.UUID
	URL             string
	SigningSecretID uuid.UUID
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

type EventWebhookDelivery struct {
	OrgID             uuid.UUID
	ID                uuid.UUID
	AgentID           uuid.UUID
	EventSequence     *int64
	ToolCallID        *uuid.UUID
	ToolState         *string
	InteractionUpdate *notifications.AgentInteractionUpdate
	AttemptCount      int32
	ClaimToken        uuid.UUID
}

func (s *Store) ClaimEventWebhookDelivery(
	ctx context.Context, perOrgLimit int,
) (EventWebhookDelivery, error) {
	row, err := s.q.ClaimEventWebhookDelivery(ctx, dbsqlc.ClaimEventWebhookDeliveryParams{
		DeliveryWindowSeconds: eventWebhookDeliveryWindow.Seconds(),
		PerOrgLimit:           int64(perOrgLimit),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookDelivery{}, storeerr.ErrNotFound
	}
	if err != nil {
		return EventWebhookDelivery{}, fmt.Errorf("claim event webhook: %w", err)
	}
	var interactionUpdate *notifications.AgentInteractionUpdate
	if row.InteractionUpdate != nil {
		if err := json.Unmarshal(*row.InteractionUpdate, &interactionUpdate); err != nil {
			return EventWebhookDelivery{}, fmt.Errorf("decode interaction update for delivery %s: %w", row.ID, err)
		}
	}
	return EventWebhookDelivery{
		ID: row.ID, AgentID: row.AgentID, OrgID: row.OrgID,
		EventSequence: row.EventSequence, ToolCallID: row.ToolCallID, ToolState: row.ToolState,
		InteractionUpdate: interactionUpdate,
		AttemptCount:      row.AttemptCount, ClaimToken: *row.ClaimToken,
	}, nil
}

func (s *Store) CompleteEventWebhookDelivery(ctx context.Context, id, claimToken uuid.UUID) error {
	return s.q.CompleteEventWebhookDelivery(ctx, dbsqlc.CompleteEventWebhookDeliveryParams{
		ID: id, ClaimToken: &claimToken,
	})
}

type EventWebhookRetryResult struct {
	NextAttemptAt time.Time
	GaveUp        bool
}

func (s *Store) RetryEventWebhookDelivery(
	ctx context.Context, id, claimToken uuid.UUID, delay time.Duration,
) (EventWebhookRetryResult, error) {
	row, err := s.q.RetryEventWebhookDelivery(ctx, dbsqlc.RetryEventWebhookDeliveryParams{
		ID: id, ClaimToken: &claimToken, DelaySeconds: delay.Seconds(),
		DeliveryWindowSeconds: eventWebhookDeliveryWindow.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookRetryResult{}, storeerr.ErrNotFound
	}
	if err != nil {
		return EventWebhookRetryResult{}, err
	}
	return EventWebhookRetryResult{NextAttemptAt: row.NextAttemptAt, GaveUp: row.GaveUp}, nil
}

func (s *Store) DeleteExpiredEventWebhookDeliveries(ctx context.Context, limit int32) (int64, error) {
	return s.q.DeleteExpiredEventWebhookDeliveries(ctx, dbsqlc.DeleteExpiredEventWebhookDeliveriesParams{
		LimitCount: limit, DeliveryWindowSeconds: eventWebhookDeliveryWindow.Seconds(),
	})
}

func (s *Store) ReadEventWebhookSigningSecret(ctx context.Context, target EventWebhookTarget) (string, error) {
	if s.secrets == nil {
		return "", errors.New("secret store is required")
	}
	record, err := s.secrets.ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: target.OrgID, ProjectID: target.ProjectID, SecretID: target.SigningSecretID, Kind: secrets.KindGeneric,
	})
	if err != nil {
		return "", err
	}
	return record.Payload[secrets.KeyValue], nil
}
