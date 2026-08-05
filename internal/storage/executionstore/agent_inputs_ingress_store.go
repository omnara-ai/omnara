package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentInputRecord struct {
	ID                  ID                     `json:"id"`
	ProjectID           ID                     `json:"project_id"`
	AgentID             ID                     `json:"agent_id"`
	State               string                 `json:"state"`
	InputRank           int64                  `json:"input_rank"`
	ActorID             ID                     `json:"actor_id,omitzero"`
	InputKind           string                 `json:"input_kind"`
	IntegrationTargetID ID                     `json:"integration_target_id,omitempty"`
	IdempotencyScope    string                 `json:"idempotency_scope,omitempty"`
	InputIdempotencyKey string                 `json:"input_idempotency_key,omitempty"`
	QueuedAt            time.Time              `json:"queued_at"`
	AdmittedEventID     ID                     `json:"admitted_event_id,omitempty"`
	AdmittedAt          *time.Time             `json:"admitted_at,omitempty"`
	CanceledAt          *time.Time             `json:"canceled_at,omitempty"`
	DeliveryMode        AgentInputDeliveryMode `json:"delivery_mode"`
	ControlType         string                 `json:"control_type,omitempty"`
	TargetInteractionID ID                     `json:"target_interaction_id,omitempty"`
	AgentConfigID       ID                     `json:"agent_config_id,omitempty"`
	ResolvedAt          *time.Time             `json:"resolved_at,omitempty"`
	RejectedReason      string                 `json:"rejected_reason,omitempty"`
	Metadata            json.RawMessage        `json:"metadata"`
}

type AgentInputQueueCursor struct {
	Set       bool
	InputRank int64
	QueuedAt  time.Time
	ID        ID
}

const agentInputRankStride int64 = 1024

type ListQueuedBacklogInputsInput struct {
	ProjectID ID
	AgentID   ID
	Limit     int
	After     AgentInputQueueCursor
}

type ListQueuedBacklogInputsResult struct {
	Inputs  []AgentInputRecord
	HasMore bool
}

const (
	DeliveryModeQueued    AgentInputDeliveryMode = "queued"
	DeliveryModeSteering  AgentInputDeliveryMode = "steering"
	DeliveryModeImmediate AgentInputDeliveryMode = "immediate"
)

type AgentInputDeliveryMode string

type insertAgentInputInput struct {
	ID                  ID
	ProjectID           ID
	AgentID             ID
	DeliveryMode        AgentInputDeliveryMode
	ActorID             ID
	IntegrationTargetID ID
	IdempotencyScope    string
	InputIdempotencyKey string
	Metadata            json.RawMessage
}

func insertAgentInputTx(
	ctx context.Context,
	tx pgx.Tx,
	input insertAgentInputInput,
) (AgentInputRecord, error) {
	if input.DeliveryMode == "" {
		input.DeliveryMode = DeliveryModeQueued
	}
	if input.DeliveryMode != DeliveryModeQueued && input.DeliveryMode != DeliveryModeSteering {
		return AgentInputRecord{}, fmt.Errorf(
			"unsupported agent input delivery_mode %q",
			input.DeliveryMode,
		)
	}
	input.Metadata = normalizedJSON(input.Metadata)
	row, err := dbsqlc.New(tx).InsertAgentInput(ctx, dbsqlc.InsertAgentInputParams{
		RankStride:          agentInputRankStride,
		ProjectID:           input.ProjectID,
		AgentID:             input.AgentID,
		ID:                  sqlcIDFromNil(input.ID),
		DeliveryMode:        string(input.DeliveryMode),
		ActorID:             sqlcIDFromNil(input.ActorID),
		IntegrationTargetID: sqlcIDFromNil(input.IntegrationTargetID),
		IdempotencyScope:    sqlcTextFromEmpty(input.IdempotencyScope),
		InputIdempotencyKey: sqlcTextFromEmpty(input.InputIdempotencyKey),
		Metadata:            input.Metadata,
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return AgentInputRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentInputRecord{}, fmt.Errorf("insert agent input: %w", err)
	}
	return agentInputRecordFromInsertSQLC(row), nil
}

func loadAgentInputByIdempotencyMaybeTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
	scope, key string,
) (AgentInputRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || scope == "" || key == "" {
		return AgentInputRecord{}, false, nil
	}
	row, err := dbsqlc.New(tx).
		GetAgentInputByIdempotency(ctx, dbsqlc.GetAgentInputByIdempotencyParams{
			ProjectID:           projectID,
			AgentID:             agentID,
			IdempotencyScope:    scope,
			InputIdempotencyKey: key,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInputRecord{}, false, nil
	}
	if err != nil {
		return AgentInputRecord{}, false, fmt.Errorf("load agent input by idempotency: %w", err)
	}
	return agentInputRecordFromIdempotencySQLC(row), true, nil
}
