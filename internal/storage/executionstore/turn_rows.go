package executionstore

import "github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"

func agentTurnRecordFromInsertSQLC(row dbsqlc.InsertAgentTurnRow) AgentTurnRecord {
	return agentTurnRecordFromFields(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.TurnSequence,
		row.LatestEventID,
		row.LatestSemanticEventID,
	)
}

func agentTurnRecordFromCurrentContinuableSQLC(row dbsqlc.CurrentContinuableAgentTurnRow) AgentTurnRecord {
	return agentTurnRecordFromFields(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.TurnSequence,
		row.LatestEventID,
		row.LatestSemanticEventID,
	)
}

func agentTurnRecordFromFields(
	id, projectID, agentID ID,
	sequence int64,
	latestEventID, latestSemanticEventID ID,
) AgentTurnRecord {
	return AgentTurnRecord{
		ID:                    id,
		ProjectID:             projectID,
		AgentID:               agentID,
		TurnSequence:          sequence,
		LatestEventID:         latestEventID,
		LatestSemanticEventID: latestSemanticEventID,
	}
}

func agentRuntimeLockRecordFromSQLC(row dbsqlc.AgentRuntimeLock) AgentRuntimeLockRecord {
	return AgentRuntimeLockRecord{
		ID:                row.ID,
		AgentID:           row.AgentID,
		WorkerProcessID:   row.WorkerProcessID,
		StartedAt:         row.StartedAt,
		RenewedAt:         row.RenewedAt,
		LeaseExpiresAt:    row.LeaseExpiresAt,
		CancelRequestedAt: row.CancelRequestedAt,
	}
}
