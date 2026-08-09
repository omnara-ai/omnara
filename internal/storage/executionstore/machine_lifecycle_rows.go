package executionstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func poolMachineCleanupCandidateFromSQLC(row dbsqlc.ListPoolMachinesForCleanupRow) PoolMachineCleanupCandidate {
	return PoolMachineCleanupCandidate{
		Machine: machineRecordFromSQLC(
			row.ID,
			row.OrgID,
			row.MachinePoolID,
			row.SourceKind,
			row.DisplayName,
			row.Description,
			row.Provider,
			row.LifecycleState,
			row.ProviderResourceID,
			row.ProviderProvisionAttemptedAt,
			row.ConnectionState,
			row.LastObservedAt,
			row.Cpu,
			row.MemoryMb,
			row.Cwd,
			row.Env,
			row.SecretEnv,
			row.ProviderOptions,
			row.IdempotencyKey,
			row.LifecycleReasonCode,
			row.LifecycleReasonMessage,
			row.NextReconcileAfter,
			row.ProvisionAttempts,
			row.DeleteAttempts,
			row.Metadata,
			row.DeletedAt,
			row.CreatedAt,
			row.UpdatedAt,
			row.LifecycleChangedAt,
			row.LifecycleVersion,
		),
		ReasonCode:    row.CleanupReasonCode,
		ReasonMessage: poolMachineCleanupReasonMessage(row.CleanupReasonCode),
	}
}

func poolMachineCleanupReasonMessage(code string) string {
	switch code {
	case "delete_failed_retry":
		return "retrying machine deletion after a delete failure"
	case "provisioning_stale_cleanup":
		return "cleaning up stale provisioning attempt"
	case "deleting_retry":
		return "retrying machine deletion"
	case "startup_or_daemon_bootstrap_failed":
		return "cleaning up machine because startup script or daemon bootstrap did not complete"
	default:
		return ""
	}
}

func poolMachineDeletionClaimFromSQLC(row dbsqlc.ClaimPoolMachineDeletionRow) PoolMachineDeletionClaim {
	return PoolMachineDeletionClaim{
		Machine: machineRecordFromSQLC(
			row.ID,
			row.OrgID,
			row.MachinePoolID,
			row.SourceKind,
			row.DisplayName,
			row.Description,
			row.Provider,
			row.LifecycleState,
			row.ProviderResourceID,
			row.ProviderProvisionAttemptedAt,
			row.ConnectionState,
			row.LastObservedAt,
			row.Cpu,
			row.MemoryMb,
			row.Cwd,
			row.Env,
			row.SecretEnv,
			row.ProviderOptions,
			row.IdempotencyKey,
			row.LifecycleReasonCode,
			row.LifecycleReasonMessage,
			row.NextReconcileAfter,
			row.ProvisionAttempts,
			row.DeleteAttempts,
			row.Metadata,
			row.DeletedAt,
			row.CreatedAt,
			row.UpdatedAt,
			row.LifecycleChangedAt,
			row.LifecycleVersion,
		),
		CanFinalizeMissingProviderResource: row.CanFinalizeMissingProviderResource,
	}
}

func providerRuntimeMismatchDeletionClaimFromSQLC(
	row dbsqlc.ClaimProviderRuntimeMismatchDeletionRow,
) PoolMachineDeletionClaim {
	return PoolMachineDeletionClaim{
		Machine: machineRecordFromSQLC(
			row.ID,
			row.OrgID,
			row.MachinePoolID,
			row.SourceKind,
			row.DisplayName,
			row.Description,
			row.Provider,
			row.LifecycleState,
			row.ProviderResourceID,
			row.ProviderProvisionAttemptedAt,
			row.ConnectionState,
			row.LastObservedAt,
			row.Cpu,
			row.MemoryMb,
			row.Cwd,
			row.Env,
			row.SecretEnv,
			row.ProviderOptions,
			row.IdempotencyKey,
			row.LifecycleReasonCode,
			row.LifecycleReasonMessage,
			row.NextReconcileAfter,
			row.ProvisionAttempts,
			row.DeleteAttempts,
			row.Metadata,
			row.DeletedAt,
			row.CreatedAt,
			row.UpdatedAt,
			row.LifecycleChangedAt,
			row.LifecycleVersion,
		),
		CanFinalizeMissingProviderResource: row.CanFinalizeMissingProviderResource,
	}
}

func machineRecordFromMarkPoolMachineDeletingSQLC(row dbsqlc.MarkPoolMachineDeletingRow) MachineRecord {
	return machineRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.MachinePoolID,
		row.SourceKind,
		row.DisplayName,
		row.Description,
		row.Provider,
		row.LifecycleState,
		row.ProviderResourceID,
		row.ProviderProvisionAttemptedAt,
		row.ConnectionState,
		row.LastObservedAt,
		row.Cpu,
		row.MemoryMb,
		row.Cwd,
		row.Env,
		row.SecretEnv,
		row.ProviderOptions,
		row.IdempotencyKey,
		row.LifecycleReasonCode,
		row.LifecycleReasonMessage,
		row.NextReconcileAfter,
		row.ProvisionAttempts,
		row.DeleteAttempts,
		row.Metadata,
		row.DeletedAt,
		row.CreatedAt,
		row.UpdatedAt,
		row.LifecycleChangedAt,
		row.LifecycleVersion,
	)
}

func machineRecordFromMarkPoolGrantMachinesDeletingSQLC(row dbsqlc.MarkPoolGrantMachinesDeletingRow) MachineRecord {
	return machineRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.MachinePoolID,
		row.SourceKind,
		row.DisplayName,
		row.Description,
		row.Provider,
		row.LifecycleState,
		row.ProviderResourceID,
		row.ProviderProvisionAttemptedAt,
		row.ConnectionState,
		row.LastObservedAt,
		row.Cpu,
		row.MemoryMb,
		row.Cwd,
		row.Env,
		row.SecretEnv,
		row.ProviderOptions,
		row.IdempotencyKey,
		row.LifecycleReasonCode,
		row.LifecycleReasonMessage,
		row.NextReconcileAfter,
		row.ProvisionAttempts,
		row.DeleteAttempts,
		row.Metadata,
		row.DeletedAt,
		row.CreatedAt,
		row.UpdatedAt,
		row.LifecycleChangedAt,
		row.LifecycleVersion,
	)
}

func machineRecordFromMarkMachinePoolMachinesDeletingSQLC(row dbsqlc.MarkMachinePoolMachinesDeletingRow) MachineRecord {
	return machineRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.MachinePoolID,
		row.SourceKind,
		row.DisplayName,
		row.Description,
		row.Provider,
		row.LifecycleState,
		row.ProviderResourceID,
		row.ProviderProvisionAttemptedAt,
		row.ConnectionState,
		row.LastObservedAt,
		row.Cpu,
		row.MemoryMb,
		row.Cwd,
		row.Env,
		row.SecretEnv,
		row.ProviderOptions,
		row.IdempotencyKey,
		row.LifecycleReasonCode,
		row.LifecycleReasonMessage,
		row.NextReconcileAfter,
		row.ProvisionAttempts,
		row.DeleteAttempts,
		row.Metadata,
		row.DeletedAt,
		row.CreatedAt,
		row.UpdatedAt,
		row.LifecycleChangedAt,
		row.LifecycleVersion,
	)
}

func machineRecordFromMarkRemovedAgentPoolSourceMachinesDeletingSQLC(
	row dbsqlc.MarkRemovedAgentPoolSourceMachinesDeletingRow,
) MachineRecord {
	return machineRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.MachinePoolID,
		row.SourceKind,
		row.DisplayName,
		row.Description,
		row.Provider,
		row.LifecycleState,
		row.ProviderResourceID,
		row.ProviderProvisionAttemptedAt,
		row.ConnectionState,
		row.LastObservedAt,
		row.Cpu,
		row.MemoryMb,
		row.Cwd,
		row.Env,
		row.SecretEnv,
		row.ProviderOptions,
		row.IdempotencyKey,
		row.LifecycleReasonCode,
		row.LifecycleReasonMessage,
		row.NextReconcileAfter,
		row.ProvisionAttempts,
		row.DeleteAttempts,
		row.Metadata,
		row.DeletedAt,
		row.CreatedAt,
		row.UpdatedAt,
		row.LifecycleChangedAt,
		row.LifecycleVersion,
	)
}

func machineRecordFromMarkArchivedAgentPoolMachinesDeletingSQLC(
	row dbsqlc.MarkArchivedAgentPoolMachinesDeletingRow,
) MachineRecord {
	return machineRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.MachinePoolID,
		row.SourceKind,
		row.DisplayName,
		row.Description,
		row.Provider,
		row.LifecycleState,
		row.ProviderResourceID,
		row.ProviderProvisionAttemptedAt,
		row.ConnectionState,
		row.LastObservedAt,
		row.Cpu,
		row.MemoryMb,
		row.Cwd,
		row.Env,
		row.SecretEnv,
		row.ProviderOptions,
		row.IdempotencyKey,
		row.LifecycleReasonCode,
		row.LifecycleReasonMessage,
		row.NextReconcileAfter,
		row.ProvisionAttempts,
		row.DeleteAttempts,
		row.Metadata,
		row.DeletedAt,
		row.CreatedAt,
		row.UpdatedAt,
		row.LifecycleChangedAt,
		row.LifecycleVersion,
	)
}

func machineRecordFromSQLC(
	id, orgID ID,
	machinePoolID *ID,
	sourceKind, displayName, description, provider, lifecycleState string,
	providerResourceID *string,
	providerProvisionAttemptedAt *time.Time,
	connectionState string,
	lastObservedAt *time.Time,
	cpu, memoryMB *int32,
	cwd string,
	env, secretEnv json.RawMessage,
	providerOptions *json.RawMessage,
	idempotencyKey, lifecycleReasonCode, lifecycleReasonMessage string,
	nextReconcileAfter *time.Time,
	provisionAttempts, deleteAttempts int32,
	metadata []byte,
	deletedAt *time.Time,
	createdAt, updatedAt, lifecycleChangedAt time.Time,
	lifecycleVersion int64,
) MachineRecord {
	return MachineRecord{
		ID:                           id,
		OrgID:                        orgID,
		MachinePoolID:                idFromSQLCPtr(machinePoolID),
		SourceKind:                   MachineSourceKind(sourceKind),
		DisplayName:                  displayName,
		Description:                  description,
		Provider:                     provider,
		LifecycleState:               MachineLifecycleState(lifecycleState),
		ProviderResourceID:           stringFromSQLCText(providerResourceID),
		ProviderProvisionAttemptedAt: providerProvisionAttemptedAt,
		ConnectionState:              MachineConnectionState(connectionState),
		LastObservedAt:               lastObservedAt,
		CPU:                          intPtrFromSQLC(cpu),
		MemoryMB:                     intPtrFromSQLC(memoryMB),
		Cwd:                          cwd,
		Env:                          env,
		SecretEnv:                    secretEnv,
		ProviderOptions:              rawMessageFromSQLCPtr(providerOptions),
		IdempotencyKey:               idempotencyKey,
		LifecycleReasonCode:          lifecycleReasonCode,
		LifecycleReasonMessage:       lifecycleReasonMessage,
		NextReconcileAfter:           nextReconcileAfter,
		ProvisionAttempts:            provisionAttempts,
		DeleteAttempts:               deleteAttempts,
		Metadata:                     json.RawMessage(metadata),
		DeletedAt:                    deletedAt,
		CreatedAt:                    createdAt,
		UpdatedAt:                    updatedAt,
		LifecycleChangedAt:           lifecycleChangedAt,
		LifecycleVersion:             lifecycleVersion,
	}
}

func machineSummaryRecordFromSQLC(
	id, orgID ID,
	sourceKind, displayName, description, provider, lifecycleState, connectionState string,
	lastObservedAt, deletedAt *time.Time,
	createdAt, updatedAt time.Time,
) MachineSummaryRecord {
	return MachineSummaryRecord{
		ID:              id,
		OrgID:           orgID,
		SourceKind:      MachineSourceKind(sourceKind),
		DisplayName:     displayName,
		Description:     description,
		Provider:        provider,
		LifecycleState:  MachineLifecycleState(lifecycleState),
		ConnectionState: MachineConnectionState(connectionState),
		LastObservedAt:  lastObservedAt,
		DeletedAt:       deletedAt,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}
}
