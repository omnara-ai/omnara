package executionstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func toolCallRecordFromGetSQLC(row dbsqlc.GetToolCallRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, row.CompletedAt,
	)
}

func toolCallRecordFromProviderSQLC(row dbsqlc.GetToolCallByProviderCallRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, row.CompletedAt,
	)
}

func toolCallRecordFromRunnableSQLC(
	row dbsqlc.NextRunnableToolCallForModelOutputRow,
) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, row.CompletedAt,
	)
}

func toolCallRecordFromAgentListSQLC(row dbsqlc.ListToolCallsForAgentRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, row.CompletedAt,
	)
}

func toolCallRecordFromWatermarkSQLC(
	row dbsqlc.ListCompletedToolCallsAtWatermarkRow,
) ToolCallRecord {
	record := toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, timePtr(row.CompletedAt),
	)
	record.ToolCallResultID = row.ToolCallResultID
	record.ToolResultEventID = row.ToolResultEventID
	record.SourceEventSequence = row.SourceEventSequence
	record.ToolResultEventSequence = row.ToolResultEventSequence
	return record
}

func toolCallRecordFromSQLC(
	id uuid.UUID,
	projectID uuid.UUID,
	agentID uuid.UUID,
	turnID uuid.UUID,
	sourceEventID uuid.UUID,
	modelCallContextID uuid.UUID,
	providerCallID string,
	name string,
	input json.RawMessage,
	toolType string,
	state string,
	outcome string,
	runtimeLockID *uuid.UUID,
	resultContentParts []byte,
	createdAt time.Time,
	completedAt *time.Time,
) ToolCallRecord {
	return ToolCallRecord{
		ID:                 id,
		ProjectID:          projectID,
		AgentID:            agentID,
		TurnID:             turnID,
		SourceEventID:      sourceEventID,
		ModelCallContextID: modelCallContextID,
		ProviderCallID:     providerCallID,
		Name:               name,
		Input:              input,
		Type:               toolType,
		CreatedAt:          createdAt,
		State:              ToolCallState(state),
		Outcome:            ToolResultOutcome(outcome),
		RuntimeLockID:      storeutil.IDFromPtr(runtimeLockID),
		ResultContentParts: json.RawMessage(resultContentParts),
		CompletedAt:        completedAt,
	}
}
