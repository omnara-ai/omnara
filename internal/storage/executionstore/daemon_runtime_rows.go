package executionstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func daemonRuntimeFromHeartbeat(row dbsqlc.HeartbeatDaemonRuntimeRow) DaemonRuntimeRecord {
	return daemonRuntimeRecord(
		row.ID,
		row.OrgID,
		row.MachineID,
		row.DaemonTokenID,
		row.DaemonInstanceID,
		row.DaemonVersion,
		row.State,
		row.StateReasonCode,
		row.StateReasonMessage,
		row.Capacity,
		row.Metadata,
		row.CreatedAt,
		row.LastSeenAt,
		row.LeaseExpiresAt,
		row.EndedAt,
		row.UpdatedAt,
	)
}

func daemonRuntimeFromEnd(row dbsqlc.EndDaemonRuntimeRow) DaemonRuntimeRecord {
	return daemonRuntimeRecord(
		row.ID,
		row.OrgID,
		row.MachineID,
		row.DaemonTokenID,
		row.DaemonInstanceID,
		row.DaemonVersion,
		row.State,
		row.StateReasonCode,
		row.StateReasonMessage,
		row.Capacity,
		row.Metadata,
		row.CreatedAt,
		row.LastSeenAt,
		row.LeaseExpiresAt,
		row.EndedAt,
		row.UpdatedAt,
	)
}

func daemonRuntimeFromExpired(row dbsqlc.EndExpiredDaemonRuntimeRow) DaemonRuntimeRecord {
	return daemonRuntimeRecord(
		row.ID,
		row.OrgID,
		row.MachineID,
		row.DaemonTokenID,
		row.DaemonInstanceID,
		row.DaemonVersion,
		row.State,
		row.StateReasonCode,
		row.StateReasonMessage,
		row.Capacity,
		row.Metadata,
		row.CreatedAt,
		row.LastSeenAt,
		row.LeaseExpiresAt,
		row.EndedAt,
		row.UpdatedAt,
	)
}

func daemonRuntimeRecord(
	id, orgID, machineID, daemonTokenID, daemonInstanceID ID,
	daemonVersion, state, stateReasonCode, stateReasonMessage string,
	capacity, metadata []byte,
	createdAt time.Time,
	lastSeenAt, leaseExpiresAt time.Time,
	endedAt *time.Time,
	updatedAt time.Time,
) DaemonRuntimeRecord {
	return DaemonRuntimeRecord{
		ID:                 id,
		OrgID:              orgID,
		MachineID:          machineID,
		DaemonTokenID:      daemonTokenID,
		DaemonInstanceID:   daemonInstanceID,
		DaemonVersion:      daemonVersion,
		State:              DaemonRuntimeState(state),
		StateReasonCode:    stateReasonCode,
		StateReasonMessage: stateReasonMessage,
		Capacity:           json.RawMessage(capacity),
		Metadata:           json.RawMessage(metadata),
		CreatedAt:          createdAt,
		LastSeenAt:         lastSeenAt,
		LeaseExpiresAt:     leaseExpiresAt,
		EndedAt:            endedAt,
		UpdatedAt:          updatedAt,
	}
}
