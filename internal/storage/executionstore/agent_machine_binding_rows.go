package executionstore

import "github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"

func agentMachineBindingRecordFromSQLC(row dbsqlc.AgentMachineBinding) AgentMachineBindingRecord {
	return AgentMachineBindingRecord{
		ID:               row.ID,
		OrgID:            row.OrgID,
		ProjectID:        row.ProjectID,
		AgentID:          row.AgentID,
		CreateToolCallID: idFromSQLCPtr(row.CreateToolCallID),
		DeleteToolCallID: idFromSQLCPtr(row.DeleteToolCallID),
		MachineID:        row.MachineID,
		MachineRef:       row.MachineRef,
		BindingKind:      AgentMachineBindingKind(row.BindingKind),
		State:            AgentMachineBindingState(row.State),
		Description:      row.Description,
		Cwd:              row.Cwd,
		EnvOverlay:       row.EnvOverlay,
		SecretEnvOverlay: row.SecretEnvOverlay,
		Metadata:         row.Metadata,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func executableAgentMachineBindingRecordFromSQLC(
	row dbsqlc.ListExecutableAgentMachineBindingsRow,
) AgentMachineBindingRecord {
	return AgentMachineBindingRecord{
		ID:               row.ID,
		OrgID:            row.OrgID,
		ProjectID:        row.ProjectID,
		AgentID:          row.AgentID,
		CreateToolCallID: idFromSQLCPtr(row.CreateToolCallID),
		DeleteToolCallID: idFromSQLCPtr(row.DeleteToolCallID),
		MachineID:        row.MachineID,
		MachineRef:       row.MachineRef,
		BindingKind:      AgentMachineBindingKind(row.BindingKind),
		State:            AgentMachineBindingState(row.State),
		Description:      row.Description,
		Cwd:              row.EffectiveCwd,
		EnvOverlay:       row.EnvOverlay,
		SecretEnvOverlay: row.SecretEnvOverlay,
		Metadata:         row.Metadata,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}
