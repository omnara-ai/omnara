package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
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
		DefaultMachineCPU:             storeutil.IntPtr(row.DefaultMachineCpu),
		DefaultMachineMemoryMB:        storeutil.IntPtr(row.DefaultMachineMemoryMb),
		DefaultMachineEnv:             row.DefaultMachineEnv,
		DefaultMachineSecretEnv:       row.DefaultMachineSecretEnv,
		DefaultMachineProviderOptions: row.DefaultMachineProviderOptions,
		DefaultCwd:                    row.DefaultCwd,
		ProviderConfig:                row.ProviderConfig,
		ProviderAuthSecretID:          idFromSQLCPtr(row.ProviderAuthSecretID),
		ProviderAuthEnvVar:            row.ProviderAuthEnvVar,
		RuntimeProtectionEnabled:      row.RuntimeProtectionEnabled,
		MaxTotalMachines:              row.MaxTotalMachines,
		MaxTotalCPU:                   storeutil.IntPtr(row.MaxTotalCpu),
		MaxTotalMemoryMB:              storeutil.IntPtr(row.MaxTotalMemoryMb),
		MinMachineCPU:                 storeutil.IntPtr(row.MinMachineCpu),
		MinMachineMemoryMB:            storeutil.IntPtr(row.MinMachineMemoryMb),
		MaxMachineCPU:                 storeutil.IntPtr(row.MaxMachineCpu),
		MaxMachineMemoryMB:            storeutil.IntPtr(row.MaxMachineMemoryMb),
		DeleteAfterIdleMinutes:        storeutil.IntPtr(row.DeleteAfterIdleMinutes),
		Metadata:                      row.Metadata,
		DeletedAt:                     row.DeletedAt,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	}
}

func machinePoolRecordFromListSQLC(row dbsqlc.ListMachinePoolsRow) MachinePoolListRecord {
	return MachinePoolListRecord{
		MachinePoolRecord: MachinePoolRecord{
			ID:                            row.ID,
			OrgID:                         row.OrgID,
			Name:                          row.Name,
			ManagementKind:                management.Kind(row.ManagementKind),
			Description:                   row.Description,
			Provider:                      row.Provider,
			DefaultMachineCPU:             storeutil.IntPtr(row.DefaultMachineCpu),
			DefaultMachineMemoryMB:        storeutil.IntPtr(row.DefaultMachineMemoryMb),
			DefaultMachineEnv:             row.DefaultMachineEnv,
			DefaultMachineSecretEnv:       row.DefaultMachineSecretEnv,
			DefaultMachineProviderOptions: row.DefaultMachineProviderOptions,
			DefaultCwd:                    row.DefaultCwd,
			ProviderConfig:                row.ProviderConfig,
			ProviderAuthSecretID:          idFromSQLCPtr(row.ProviderAuthSecretID),
			ProviderAuthEnvVar:            row.ProviderAuthEnvVar,
			RuntimeProtectionEnabled:      row.RuntimeProtectionEnabled,
			MaxTotalMachines:              row.MaxTotalMachines,
			MaxTotalCPU:                   storeutil.IntPtr(row.MaxTotalCpu),
			MaxTotalMemoryMB:              storeutil.IntPtr(row.MaxTotalMemoryMb),
			MinMachineCPU:                 storeutil.IntPtr(row.MinMachineCpu),
			MinMachineMemoryMB:            storeutil.IntPtr(row.MinMachineMemoryMb),
			MaxMachineCPU:                 storeutil.IntPtr(row.MaxMachineCpu),
			MaxMachineMemoryMB:            storeutil.IntPtr(row.MaxMachineMemoryMb),
			DeleteAfterIdleMinutes:        storeutil.IntPtr(row.DeleteAfterIdleMinutes),
			Metadata:                      row.Metadata,
			DeletedAt:                     row.DeletedAt,
			CreatedAt:                     row.CreatedAt,
			UpdatedAt:                     row.UpdatedAt,
		},
		Usage: MachinePoolUsageRecord{
			Machines: row.ActiveMachines,
			CPU:      row.ActiveCpu,
			MemoryMB: row.ActiveMemoryMb,
		},
	}
}
