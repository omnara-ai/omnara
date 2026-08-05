package executionstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func projectMachinePoolGrantFromUpsert(row dbsqlc.UpsertProjectMachinePoolGrantRow) ProjectMachinePoolGrantRecord {
	return projectMachinePoolGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachinePoolID,
		row.Description,
		row.DefaultMachineCpu,
		row.DefaultMachineMemoryMb,
		row.DefaultMachineEnvOverlay,
		row.DefaultMachineSecretEnvOverlay,
		row.DefaultMachineProviderOptionsOverlay,
		row.DefaultCwd,
		row.MaxTotalMachines,
		row.MaxTotalCpu,
		row.MaxTotalMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachinePoolGrantFromIdempotency(
	row dbsqlc.GetProjectMachinePoolGrantByIdempotencyRow,
) ProjectMachinePoolGrantRecord {
	return projectMachinePoolGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachinePoolID,
		row.Description,
		row.DefaultMachineCpu,
		row.DefaultMachineMemoryMb,
		row.DefaultMachineEnvOverlay,
		row.DefaultMachineSecretEnvOverlay,
		row.DefaultMachineProviderOptionsOverlay,
		row.DefaultCwd,
		row.MaxTotalMachines,
		row.MaxTotalCpu,
		row.MaxTotalMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachinePoolGrantFromGet(row dbsqlc.GetProjectMachinePoolGrantRow) ProjectMachinePoolGrantRecord {
	return projectMachinePoolGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachinePoolID,
		row.Description,
		row.DefaultMachineCpu,
		row.DefaultMachineMemoryMb,
		row.DefaultMachineEnvOverlay,
		row.DefaultMachineSecretEnvOverlay,
		row.DefaultMachineProviderOptionsOverlay,
		row.DefaultCwd,
		row.MaxTotalMachines,
		row.MaxTotalCpu,
		row.MaxTotalMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachinePoolGrantFromActiveMachinePool(
	row dbsqlc.GetActiveProjectMachinePoolGrantForMachinePoolRow,
) ProjectMachinePoolGrantRecord {
	return projectMachinePoolGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachinePoolID,
		row.Description,
		row.DefaultMachineCpu,
		row.DefaultMachineMemoryMb,
		row.DefaultMachineEnvOverlay,
		row.DefaultMachineSecretEnvOverlay,
		row.DefaultMachineProviderOptionsOverlay,
		row.DefaultCwd,
		row.MaxTotalMachines,
		row.MaxTotalCpu,
		row.MaxTotalMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachinePoolGrantFromList(row dbsqlc.ListProjectMachinePoolGrantsRow) ProjectMachinePoolGrantRecord {
	return projectMachinePoolGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachinePoolID,
		row.Description,
		row.DefaultMachineCpu,
		row.DefaultMachineMemoryMb,
		row.DefaultMachineEnvOverlay,
		row.DefaultMachineSecretEnvOverlay,
		row.DefaultMachineProviderOptionsOverlay,
		row.DefaultCwd,
		row.MaxTotalMachines,
		row.MaxTotalCpu,
		row.MaxTotalMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachinePoolGrantFromDelete(row dbsqlc.DeleteProjectMachinePoolGrantRow) ProjectMachinePoolGrantRecord {
	return projectMachinePoolGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachinePoolID,
		row.Description,
		row.DefaultMachineCpu,
		row.DefaultMachineMemoryMb,
		row.DefaultMachineEnvOverlay,
		row.DefaultMachineSecretEnvOverlay,
		row.DefaultMachineProviderOptionsOverlay,
		row.DefaultCwd,
		row.MaxTotalMachines,
		row.MaxTotalCpu,
		row.MaxTotalMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachinePoolGrantRecord(
	id, orgID, projectID, machinePoolID ID,
	description string,
	defaultMachineCPU, defaultMachineMemoryMB *int32,
	defaultMachineEnvOverlay, defaultMachineSecretEnvOverlay, defaultMachineProviderOptionsOverlay json.RawMessage,
	defaultCwd string,
	maxTotalMachines, maxTotalCPU, maxTotalMemoryMB, maxMachineCPU, maxMachineMemoryMB *int32,
	idempotencyKey string,
	metadata []byte,
	createdAt, updatedAt time.Time,
) ProjectMachinePoolGrantRecord {
	return ProjectMachinePoolGrantRecord{
		ID:                                   id,
		OrgID:                                orgID,
		ProjectID:                            projectID,
		MachinePoolID:                        machinePoolID,
		Description:                          description,
		DefaultMachineCPU:                    intPtrFromSQLC(defaultMachineCPU),
		DefaultMachineMemoryMB:               intPtrFromSQLC(defaultMachineMemoryMB),
		DefaultMachineEnvOverlay:             defaultMachineEnvOverlay,
		DefaultMachineSecretEnvOverlay:       defaultMachineSecretEnvOverlay,
		DefaultMachineProviderOptionsOverlay: defaultMachineProviderOptionsOverlay,
		DefaultCwd:                           defaultCwd,
		MaxTotalMachines:                     intPtrFromSQLC(maxTotalMachines),
		MaxTotalCPU:                          intPtrFromSQLC(maxTotalCPU),
		MaxTotalMemoryMB:                     intPtrFromSQLC(maxTotalMemoryMB),
		MaxMachineCPU:                        intPtrFromSQLC(maxMachineCPU),
		MaxMachineMemoryMB:                   intPtrFromSQLC(maxMachineMemoryMB),
		IdempotencyKey:                       idempotencyKey,
		Metadata:                             json.RawMessage(metadata),
		CreatedAt:                            createdAt,
		UpdatedAt:                            updatedAt,
	}
}
