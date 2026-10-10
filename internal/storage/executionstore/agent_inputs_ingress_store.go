package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type AgentInputRecord struct {
	ID                  uuid.UUID              `json:"id"`
	ProjectID           uuid.UUID              `json:"project_id"`
	AgentID             uuid.UUID              `json:"agent_id"`
	State               string                 `json:"state"`
	InputRank           int64                  `json:"input_rank"`
	ActorID             uuid.UUID              `json:"actor_id,omitzero"`
	InputKind           string                 `json:"input_kind"`
	IntegrationTargetID uuid.UUID              `json:"integration_target_id,omitempty"`
	IdempotencyScope    string                 `json:"idempotency_scope,omitempty"`
	InputIdempotencyKey string                 `json:"input_idempotency_key,omitempty"`
	QueuedAt            time.Time              `json:"queued_at"`
	AdmittedEventID     uuid.UUID              `json:"admitted_event_id,omitempty"`
	AdmittedAt          *time.Time             `json:"admitted_at,omitempty"`
	CanceledAt          *time.Time             `json:"canceled_at,omitempty"`
	DeliveryMode        AgentInputDeliveryMode `json:"delivery_mode"`
	ControlType         string                 `json:"control_type,omitempty"`
	TargetInteractionID uuid.UUID              `json:"target_interaction_id,omitempty"`
	AgentConfigID       uuid.UUID              `json:"agent_config_id,omitempty"`
	ResolvedAt          *time.Time             `json:"resolved_at,omitempty"`
	RejectedReason      string                 `json:"rejected_reason,omitempty"`
	Metadata            json.RawMessage        `json:"metadata"`
	ContentBlocks       json.RawMessage        `json:"-"`
}

type AgentInputQueueCursor struct {
	Set          bool
	DeliveryMode AgentInputDeliveryMode
	InputRank    int64
	QueuedAt     time.Time
	ID           uuid.UUID
}

type ListQueuedBacklogInputsInput struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
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

func loadAgentInputByIdempotencyMaybeTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	projectID, agentID uuid.UUID,
	scope, key string,
) (AgentInputRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || scope == "" || key == "" {
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
