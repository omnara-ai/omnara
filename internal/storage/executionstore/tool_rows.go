package executionstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func toolCallRecordFromInsertSQLC(row dbsqlc.InsertToolCallRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, row.CompletedAt,
	)
}

func toolCallRecordFromCompleteSQLC(row dbsqlc.CompleteToolCallRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromRuntimeCompleteSQLC(row dbsqlc.CompleteRuntimeToolCallRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromMachineUnreachableCompleteSQLC(
	row dbsqlc.CompleteMachineUnreachableToolCallRow,
) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromFailRuntimeSQLC(row dbsqlc.FailRuntimeToolCallsRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromCustomCompleteSQLC(row dbsqlc.CompleteCustomToolCallRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

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

func toolCallRecordFromContextSQLC(row dbsqlc.ListToolCallsForModelContextRow) ToolCallRecord {
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

func toolCallRecordFromReadySQLC(row dbsqlc.MarkToolCallReadyRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, row.CompletedAt,
	)
}

func toolCallRecordFromProcessCompleteSQLC(
	row dbsqlc.CompleteToolCallFromProcessRow,
) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromQuestionInteractionCompleteSQLC(
	row dbsqlc.CompleteToolCallFromQuestionInteractionRow,
) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromStartedProcessCompleteSQLC(
	row dbsqlc.CompleteToolCallFromStartedProcessRow,
) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromProcessActionCompleteSQLC(
	row dbsqlc.CompleteToolCallFromProcessActionRow,
) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromCancelSQLC(row dbsqlc.CancelNonTerminalToolCallsForAgentRow) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromPermissionInteractionCompleteSQLC(
	row dbsqlc.CompleteToolCallFromPermissionInteractionRow,
) ToolCallRecord {
	return toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
}

func toolCallRecordFromSQLC(
	id ID,
	projectID ID,
	agentID ID,
	turnID ID,
	sourceEventID ID,
	modelCallContextID ID,
	providerCallID string,
	name string,
	input json.RawMessage,
	toolType string,
	state string,
	outcome string,
	runtimeLockID *ID,
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
		RuntimeLockID:      idFromSQLCPtr(runtimeLockID),
		ResultContentParts: json.RawMessage(resultContentParts),
		CompletedAt:        completedAt,
	}
}
