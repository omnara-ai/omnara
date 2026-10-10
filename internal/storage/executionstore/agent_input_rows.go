package executionstore

import (
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func agentInputRecordFromIdempotencySQLC(row dbsqlc.GetAgentInputByIdempotencyRow) AgentInputRecord {
	return agentInputRecordFromNewFields(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.State,
		row.InputRank,
		row.ActorID,
		row.InputKind,
		row.IntegrationTargetID,
		row.IdempotencyScope,
		row.InputIdempotencyKey,
		row.QueuedAt,
		row.AdmittedEventID,
		row.AdmittedAt,
		row.CanceledAt,
		row.DeliveryMode,
		row.ControlType,
		row.TargetInteractionID,
		row.AgentConfigID,
		row.ResolvedAt,
		row.RejectedReason,
		row.Metadata,
	)
}

func agentInputRecordFromBacklogSQLC(row dbsqlc.ListQueuedBacklogInputsRow) AgentInputRecord {
	return agentInputRecordFromNewFields(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.State,
		row.InputRank,
		row.ActorID,
		row.InputKind,
		row.IntegrationTargetID,
		row.IdempotencyScope,
		row.InputIdempotencyKey,
		row.QueuedAt,
		row.AdmittedEventID,
		row.AdmittedAt,
		row.CanceledAt,
		row.DeliveryMode,
		row.ControlType,
		row.TargetInteractionID,
		nil,
		row.ResolvedAt,
		row.RejectedReason,
		row.Metadata,
	)
}

func agentInputRecordFromNewFields(
	id uuid.UUID,
	projectID uuid.UUID,
	agentID uuid.UUID,
	state string,
	inputRank int64,
	actorID *uuid.UUID,
	inputKind string,
	integrationTargetID *uuid.UUID,
	idempotencyScope, inputIdempotencyKey string,
	queuedAt time.Time,
	admittedEventID *uuid.UUID,
	admittedAt, canceledAt *time.Time,
	deliveryMode string,
	controlType string,
	targetInteractionID *uuid.UUID,
	agentConfigID *uuid.UUID,
	resolvedAt *time.Time,
	rejectedReason string,
	metadata []byte,
) AgentInputRecord {
	return AgentInputRecord{
		ID:                  id,
		ProjectID:           projectID,
		AgentID:             agentID,
		State:               state,
		InputRank:           inputRank,
		ActorID:             storeutil.IDFromPtr(actorID),
		InputKind:           inputKind,
		IntegrationTargetID: storeutil.IDFromPtr(integrationTargetID),
		IdempotencyScope:    idempotencyScope,
		InputIdempotencyKey: inputIdempotencyKey,
		QueuedAt:            queuedAt,
		AdmittedEventID:     storeutil.IDFromPtr(admittedEventID),
		AdmittedAt:          admittedAt,
		CanceledAt:          canceledAt,
		DeliveryMode:        AgentInputDeliveryMode(deliveryMode),
		ControlType:         controlType,
		TargetInteractionID: storeutil.IDFromPtr(targetInteractionID),
		AgentConfigID:       storeutil.IDFromPtr(agentConfigID),
		ResolvedAt:          resolvedAt,
		RejectedReason:      rejectedReason,
		Metadata:            metadata,
	}
}

func agentInputRecordFromGetSQLC(row dbsqlc.GetAgentInputRow) AgentInputRecord {
	return agentInputRecordFromNewFields(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.State,
		row.InputRank,
		row.ActorID,
		row.InputKind,
		row.IntegrationTargetID,
		row.IdempotencyScope,
		row.InputIdempotencyKey,
		row.QueuedAt,
		row.AdmittedEventID,
		row.AdmittedAt,
		row.CanceledAt,
		row.DeliveryMode,
		row.ControlType,
		row.TargetInteractionID,
		row.AgentConfigID,
		row.ResolvedAt,
		row.RejectedReason,
		row.Metadata,
	)
}
