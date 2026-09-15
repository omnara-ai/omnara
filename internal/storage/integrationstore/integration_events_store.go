package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// MaxIntegrationEventPayloadBytes matches the gateway's default webhook body
// budget. Media is materialized after receipt, not inside this transaction or
// the provider acknowledgement window.
const MaxIntegrationEventPayloadBytes = 24 * 1024 * 1024

type IntegrationEventState string

const (
	IntegrationEventPending    IntegrationEventState = "pending"
	IntegrationEventProcessing IntegrationEventState = "processing"
	IntegrationEventCompleted  IntegrationEventState = "completed"
	IntegrationEventFailed     IntegrationEventState = "failed"
)

type IntegrationEventReceipt struct {
	ID                   uuid.UUID
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	IntegrationAppID     uuid.UUID
	ConnectorKey         string
	Provider             string
	EventID              string
	Payload              json.RawMessage
	State                IntegrationEventState
	AttemptCount         int
	AvailableAt          time.Time
	LeaseToken           uuid.UUID
	LeaseGeneration      int64
	LeaseExpiresAt       *time.Time
	LastError            json.RawMessage
	CompletedAt          *time.Time
	CreatedAt            time.Time
}

type ReceiveIntegrationEventInput struct {
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	EventID              string
	Payload              json.RawMessage
	Capabilities         []channelconnector.Capability
	RuntimeLease         *IntegrationRuntimeLeaseProof
}

type ClaimNextIntegrationEventInput struct {
	Capability    channelconnector.Capability
	LeaseDuration time.Duration
}

type FinishIntegrationEventInput struct {
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	ID                   uuid.UUID
	LeaseToken           uuid.UUID
	LeaseGeneration      int64
	State                IntegrationEventState
	LastError            json.RawMessage
	Capabilities         []channelconnector.Capability
}

// ReceiveIntegrationEvent returns only after durable receipt. Reusing an event
// identity with different content is a conflict, never an overwrite or an ACK.
func (s *Store) ReceiveIntegrationEvent(
	ctx context.Context,
	input ReceiveIntegrationEventInput,
) (IntegrationEventReceipt, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationInstallID == uuid.Nil {
		return IntegrationEventReceipt{}, storeerr.InvalidRequest(errors.New("project and installation are required"))
	}
	if strings.TrimSpace(input.EventID) == "" || len(input.EventID) > 512 {
		return IntegrationEventReceipt{}, storeerr.InvalidRequest(errors.New("event_id must contain between 1 and 512 bytes"))
	}
	if err := dbsafe.Text(input.EventID); err != nil {
		return IntegrationEventReceipt{}, storeerr.InvalidRequest(err)
	}
	payload, err := normalizeIntegrationEventPayload(input.Payload)
	if err != nil {
		return IntegrationEventReceipt{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationEventReceipt{}, fmt.Errorf("begin integration event receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	authority, err := s.lockConnectorIntegrationAuthority(
		ctx, tx, input.ProjectID, input.IntegrationInstallID, input.Capabilities,
	)
	if err != nil {
		return IntegrationEventReceipt{}, err
	}
	if err := LockIntegrationRuntimeLeaseForMutation(
		ctx, qtx, input.RuntimeLease, input.ProjectID, input.IntegrationInstallID,
	); err != nil {
		return IntegrationEventReceipt{}, err
	}
	if err := qtx.InsertIntegrationEventReceipt(ctx, dbsqlc.InsertIntegrationEventReceiptParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
		IntegrationAppID: authority.ID, ConnectorKey: authority.ConnectorKey, Provider: authority.Provider,
		EventID: input.EventID, Payload: payload,
	}); err != nil {
		return IntegrationEventReceipt{}, integrationChannelWriteError("insert integration event receipt", err)
	}
	row, err := qtx.GetIntegrationEventReceiptByIdentity(ctx, dbsqlc.GetIntegrationEventReceiptByIdentityParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID, EventID: input.EventID,
	})
	if err != nil {
		return IntegrationEventReceipt{}, fmt.Errorf("read integration event receipt: %w", err)
	}
	if !jsoncanonical.Equal(row.Payload, payload) {
		return IntegrationEventReceipt{}, storeerr.ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationEventReceipt{}, fmt.Errorf("commit integration event receipt: %w", err)
	}
	return integrationEventReceiptFromSQLC(row), nil
}

// ClaimNextIntegrationEvent reserves one payload at a time. A single receipt
// may carry 24 MiB, so neither storage nor HTTP aggregates a payload batch.
func (s *Store) ClaimNextIntegrationEvent(
	ctx context.Context,
	input ClaimNextIntegrationEventInput,
) (IntegrationEventReceipt, bool, error) {
	if input.LeaseDuration < time.Millisecond || input.LeaseDuration > 5*time.Minute {
		return IntegrationEventReceipt{}, false, storeerr.InvalidRequest(
			errors.New("event lease must be at least one millisecond and cannot exceed five minutes"))
	}
	capability, err := normalizedClaimCapability(input.Capability)
	if err != nil {
		return IntegrationEventReceipt{}, false, err
	}
	row, err := s.q.ClaimNextIntegrationEventReceipt(ctx, dbsqlc.ClaimNextIntegrationEventReceiptParams{
		ConnectorKey: capability.ConnectorKey, Provider: capability.Provider,
		LeaseMicroseconds: input.LeaseDuration.Microseconds(), MaxAttempts: MaxIntegrationEventAttempts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationEventReceipt{}, false, nil
	}
	if err != nil {
		return IntegrationEventReceipt{}, false, fmt.Errorf("claim integration event: %w", err)
	}
	return integrationEventReceiptFromSQLC(row), true, nil
}

// FinishIntegrationEvent settles a claim. Pending means a transient failure and
// schedules a capped, jittered retry.
// Failed is an explicit permanent processing rejection. An expired lease can
// never complete work belonging to a replacement consumer.
func (s *Store) FinishIntegrationEvent(
	ctx context.Context,
	input FinishIntegrationEventInput,
) (IntegrationEventReceipt, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationInstallID == uuid.Nil || input.ID == uuid.Nil ||
		input.LeaseToken == uuid.Nil || input.LeaseGeneration <= 0 {
		return IntegrationEventReceipt{}, storeerr.InvalidRequest(errors.New("event scope and current lease are required"))
	}
	switch input.State {
	case IntegrationEventPending, IntegrationEventCompleted, IntegrationEventFailed:
	default:
		return IntegrationEventReceipt{}, storeerr.InvalidRequest(
			errors.New("event outcome must be pending, completed, or failed"))
	}
	lastError, err := normalizedJSONObject(input.LastError, "event error")
	if err != nil {
		return IntegrationEventReceipt{}, err
	}
	if input.State == IntegrationEventCompleted != jsoncanonical.Equal(lastError, json.RawMessage(`{}`)) {
		return IntegrationEventReceipt{}, storeerr.InvalidRequest(errors.New("only completed events may omit an error"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationEventReceipt{}, fmt.Errorf("begin integration event completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := s.lockConnectorIntegrationAuthority(
		ctx, tx, input.ProjectID, input.IntegrationInstallID, input.Capabilities,
	); err != nil {
		return IntegrationEventReceipt{}, err
	}
	row, err := s.q.WithTx(tx).FinishIntegrationEventReceipt(ctx, dbsqlc.FinishIntegrationEventReceiptParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID, ID: input.ID,
		LeaseToken: input.LeaseToken, LeaseGeneration: input.LeaseGeneration,
		NextState: string(input.State), LastError: lastError,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationEventReceipt{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return IntegrationEventReceipt{}, integrationChannelWriteError("finish integration event", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationEventReceipt{}, fmt.Errorf("commit integration event completion: %w", err)
	}
	return integrationEventReceiptFromSQLC(row), nil
}

func normalizeIntegrationEventPayload(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > MaxIntegrationEventPayloadBytes {
		return nil, errors.New("event payload is required and cannot exceed 24 MiB")
	}
	object, err := jsoncanonical.ParseObject(raw, MaxIntegrationEventPayloadBytes)
	if err != nil {
		return nil, fmt.Errorf("event payload: %w", err)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode event payload: %w", err)
	}
	if err := dbsafe.JSONB(normalized, MaxIntegrationEventPayloadBytes); err != nil {
		return nil, fmt.Errorf("event payload: %w", err)
	}
	return normalized, nil
}

func integrationEventReceiptFromSQLC(row dbsqlc.IntegrationEventReceipt) IntegrationEventReceipt {
	return IntegrationEventReceipt{
		ID: row.ID, ProjectID: row.ProjectID, IntegrationInstallID: row.IntegrationInstallID,
		IntegrationAppID: row.IntegrationAppID, ConnectorKey: row.ConnectorKey, Provider: row.Provider,
		EventID: row.EventID, Payload: row.Payload, State: IntegrationEventState(row.State),
		AttemptCount: int(row.AttemptCount), AvailableAt: row.AvailableAt,
		LeaseToken: storeutil.IDFromPtr(row.LeaseToken), LeaseGeneration: row.LeaseGeneration,
		LeaseExpiresAt: row.LeaseExpiresAt, LastError: row.LastError,
		CompletedAt: row.CompletedAt, CreatedAt: row.CreatedAt,
	}
}
