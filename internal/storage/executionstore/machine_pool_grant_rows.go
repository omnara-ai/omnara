package executionstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
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
		row.MinMachineCpu,
		row.MinMachineMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.DeleteAfterIdleMinutes,
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
		row.MinMachineCpu,
		row.MinMachineMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.DeleteAfterIdleMinutes,
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
		row.MinMachineCpu,
		row.MinMachineMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.DeleteAfterIdleMinutes,
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
		row.MinMachineCpu,
		row.MinMachineMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.DeleteAfterIdleMinutes,
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
		row.MinMachineCpu,
		row.MinMachineMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.DeleteAfterIdleMinutes,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachinePoolGrantFromUpdate(row dbsqlc.UpdateProjectMachinePoolGrantRow) ProjectMachinePoolGrantRecord {
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
		row.MinMachineCpu,
		row.MinMachineMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.DeleteAfterIdleMinutes,
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
		row.MinMachineCpu,
		row.MinMachineMemoryMb,
		row.MaxMachineCpu,
		row.MaxMachineMemoryMb,
		row.DeleteAfterIdleMinutes,
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
	maxTotalMachines, maxTotalCPU, maxTotalMemoryMB *int32,
	minMachineCPU, minMachineMemoryMB *int32,
	maxMachineCPU, maxMachineMemoryMB, deleteAfterIdleMinutes *int32,
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
		DefaultMachineCPU:                    storeutil.IntPtr(defaultMachineCPU),
		DefaultMachineMemoryMB:               storeutil.IntPtr(defaultMachineMemoryMB),
		DefaultMachineEnvOverlay:             defaultMachineEnvOverlay,
		DefaultMachineSecretEnvOverlay:       defaultMachineSecretEnvOverlay,
		DefaultMachineProviderOptionsOverlay: defaultMachineProviderOptionsOverlay,
		DefaultCwd:                           defaultCwd,
		MaxTotalMachines:                     storeutil.IntPtr(maxTotalMachines),
		MaxTotalCPU:                          storeutil.IntPtr(maxTotalCPU),
		MaxTotalMemoryMB:                     storeutil.IntPtr(maxTotalMemoryMB),
		MinMachineCPU:                        storeutil.IntPtr(minMachineCPU),
		MinMachineMemoryMB:                   storeutil.IntPtr(minMachineMemoryMB),
		MaxMachineCPU:                        storeutil.IntPtr(maxMachineCPU),
		MaxMachineMemoryMB:                   storeutil.IntPtr(maxMachineMemoryMB),
		DeleteAfterIdleMinutes:               storeutil.IntPtr(deleteAfterIdleMinutes),
		IdempotencyKey:                       idempotencyKey,
		Metadata:                             json.RawMessage(metadata),
		CreatedAt:                            createdAt,
		UpdatedAt:                            updatedAt,
	}
}
