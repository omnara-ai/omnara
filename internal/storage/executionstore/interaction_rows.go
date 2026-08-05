package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func agentInteractionRecordFromSQLC(row dbsqlc.AgentInteractionReadProjection) AgentInteractionRecord {
	return AgentInteractionRecord{
		ID:                 row.ID,
		ProjectID:          row.ProjectID,
		AgentID:            row.AgentID,
		TurnID:             row.TurnID,
		ModelCallContextID: row.ModelCallContextID,
		ToolCallID:         row.ToolCallID,
		ProviderCallID:     row.ProviderCallID,
		InteractionKind:    AgentInteractionKind(row.InteractionKind),
		State:              AgentInteractionState(row.State),
		Request:            row.Request,
		Resolution:         row.Resolution,
		ResolvedByInputID:  idFromSQLCPtr(row.ResolvedByInputID),
		CreatedAt:          row.CreatedAt,
		ResolvedAt:         nullableTimeToZero(row.ResolvedAt),
	}
}
