package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

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
