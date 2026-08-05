package executionstore

import (
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func agentInputRecordFromInsertSQLC(row dbsqlc.InsertAgentInputRow) AgentInputRecord {
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

func agentInputRecordFromControlSQLC(row dbsqlc.InsertControlAgentInputRow) AgentInputRecord {
	return agentInputRecordFromNewFields(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.State,
		row.InputRank,
		row.ActorID,
		row.InputKind,
		nil,
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

func agentInputRecordFromInteractionResponseInsertSQLC(
	row dbsqlc.InsertInteractionResponseAgentInputRow,
) AgentInputRecord {
	return agentInputRecordFromNewFields(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.State,
		row.InputRank,
		row.ActorID,
		row.InputKind,
		nil,
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

func agentInputRecordFromSteeringAdmissionSQLC(row dbsqlc.ListSteeringAgentInputsForAdmissionRow) AgentInputRecord {
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

func agentInputRecordFromQueuedAdmissionSQLC(row dbsqlc.GetNextQueuedAgentInputForAdmissionRow) AgentInputRecord {
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
	id ID,
	projectID ID,
	agentID ID,
	state string,
	inputRank int64,
	actorID *ID,
	inputKind string,
	integrationTargetID *ID,
	idempotencyScope, inputIdempotencyKey string,
	queuedAt time.Time,
	admittedEventID *ID,
	admittedAt, canceledAt *time.Time,
	deliveryMode string,
	controlType string,
	targetInteractionID *ID,
	agentConfigID *ID,
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
		ActorID:             idFromSQLCPtr(actorID),
		InputKind:           inputKind,
		IntegrationTargetID: idFromSQLCPtr(integrationTargetID),
		IdempotencyScope:    idempotencyScope,
		InputIdempotencyKey: inputIdempotencyKey,
		QueuedAt:            queuedAt,
		AdmittedEventID:     idFromSQLCPtr(admittedEventID),
		AdmittedAt:          admittedAt,
		CanceledAt:          canceledAt,
		DeliveryMode:        AgentInputDeliveryMode(deliveryMode),
		ControlType:         controlType,
		TargetInteractionID: idFromSQLCPtr(targetInteractionID),
		AgentConfigID:       idFromSQLCPtr(agentConfigID),
		ResolvedAt:          resolvedAt,
		RejectedReason:      rejectedReason,
		Metadata:            metadata,
	}
}
