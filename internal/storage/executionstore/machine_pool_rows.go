package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

func machinePoolRecordFromSQLC(row dbsqlc.MachinePool) MachinePoolRecord {
	return MachinePoolRecord{
		ID:                            row.ID,
		OrgID:                         row.OrgID,
		Name:                          row.Name,
		ManagementKind:                management.Kind(row.ManagementKind),
		Description:                   row.Description,
		Provider:                      row.Provider,
		DefaultMachineCPU:             intPtrFromSQLC(row.DefaultMachineCpu),
		DefaultMachineMemoryMB:        intPtrFromSQLC(row.DefaultMachineMemoryMb),
		DefaultMachineEnv:             row.DefaultMachineEnv,
		DefaultMachineSecretEnv:       row.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: row.DefaultMachineProviderOptions,
		DefaultCwd:                    row.DefaultCwd,
		ProviderConfig:                row.ProviderConfig,
		ProviderAuthSecretID:          idFromSQLCPtr(row.ProviderAuthSecretID),
		ProviderAuthEnvVar:            row.ProviderAuthEnvVar,
		RuntimeProtectionEnabled:      row.RuntimeProtectionEnabled,
		MaxTotalMachines:              row.MaxTotalMachines,
		MaxTotalCPU:                   intPtrFromSQLC(row.MaxTotalCpu),
		MaxTotalMemoryMB:              intPtrFromSQLC(row.MaxTotalMemoryMb),
		MinMachineCPU:                 intPtrFromSQLC(row.MinMachineCpu),
		MinMachineMemoryMB:            intPtrFromSQLC(row.MinMachineMemoryMb),
		MaxMachineCPU:                 intPtrFromSQLC(row.MaxMachineCpu),
		MaxMachineMemoryMB:            intPtrFromSQLC(row.MaxMachineMemoryMb),
		Metadata:                      row.Metadata,
		DeletedAt:                     row.DeletedAt,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	}
}

func machinePoolRecordFromListSQLC(row dbsqlc.ListMachinePoolsRow) MachinePoolRecord {
	return MachinePoolRecord{
		ID:                            row.ID,
		OrgID:                         row.OrgID,
		Name:                          row.Name,
		ManagementKind:                management.Kind(row.ManagementKind),
		Description:                   row.Description,
		Provider:                      row.Provider,
		DefaultMachineCPU:             intPtrFromSQLC(row.DefaultMachineCpu),
		DefaultMachineMemoryMB:        intPtrFromSQLC(row.DefaultMachineMemoryMb),
		DefaultMachineEnv:             row.DefaultMachineEnv,
		DefaultMachineSecretEnv:       row.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: row.DefaultMachineProviderOptions,
		DefaultCwd:                    row.DefaultCwd,
		ProviderConfig:                row.ProviderConfig,
		ProviderAuthSecretID:          idFromSQLCPtr(row.ProviderAuthSecretID),
		ProviderAuthEnvVar:            row.ProviderAuthEnvVar,
		RuntimeProtectionEnabled:      row.RuntimeProtectionEnabled,
		MaxTotalMachines:              row.MaxTotalMachines,
		MaxTotalCPU:                   intPtrFromSQLC(row.MaxTotalCpu),
		MaxTotalMemoryMB:              intPtrFromSQLC(row.MaxTotalMemoryMb),
		MinMachineCPU:                 intPtrFromSQLC(row.MinMachineCpu),
		MinMachineMemoryMB:            intPtrFromSQLC(row.MinMachineMemoryMb),
		MaxMachineCPU:                 intPtrFromSQLC(row.MaxMachineCpu),
		MaxMachineMemoryMB:            intPtrFromSQLC(row.MaxMachineMemoryMb),
		Metadata:                      row.Metadata,
		DeletedAt:                     row.DeletedAt,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	}
}
