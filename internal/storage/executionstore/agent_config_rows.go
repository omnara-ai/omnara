package executionstore

import "github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"

func agentConfigRecordFromSQLC(row dbsqlc.AgentConfig) AgentConfigRecord {
	return AgentConfigRecord{
		ID:                      row.ID,
		OrgID:                   row.OrgID,
		ProjectID:               row.ProjectID,
		Definition:              row.Definition,
		Source:                  row.Source,
		SourceFormat:            row.SourceFormat,
		SourceHash:              row.SourceHash,
		ConfiguredModelID:       row.ConfiguredModelID,
		CompiledDefinition:      row.CompiledDefinition,
		CompilerVersion:         row.CompilerVersion,
		EffectiveDefinitionHash: row.EffectiveDefinitionHash,
		CreatedAt:               row.CreatedAt,
	}
}

func agentConfigRecordFromUpsertSQLC(row dbsqlc.UpsertAgentConfigByHashRow) AgentConfigRecord {
	return AgentConfigRecord{
		ID:                      row.ID,
		OrgID:                   row.OrgID,
		ProjectID:               row.ProjectID,
		Definition:              row.Definition,
		Source:                  row.Source,
		SourceFormat:            row.SourceFormat,
		SourceHash:              row.SourceHash,
		ConfiguredModelID:       row.ConfiguredModelID,
		CompiledDefinition:      row.CompiledDefinition,
		CompilerVersion:         row.CompilerVersion,
		EffectiveDefinitionHash: row.EffectiveDefinitionHash,
		CreatedAt:               row.CreatedAt,
	}
}

func agentConfigSnapshotFromSQLC(
	row dbsqlc.CaptureAgentConfigForModelContextRow,
) AgentConfigSnapshotRecord {
	config := AgentConfigRecord{
		ID:                      row.ID,
		OrgID:                   row.OrgID,
		ProjectID:               row.ProjectID,
		Definition:              row.Definition,
		Source:                  row.Source,
		SourceFormat:            row.SourceFormat,
		SourceHash:              row.SourceHash,
		ConfiguredModelID:       row.ConfiguredModelID,
		CompiledDefinition:      row.CompiledDefinition,
		CompilerVersion:         row.CompilerVersion,
		EffectiveDefinitionHash: row.EffectiveDefinitionHash,
		CreatedAt:               row.CreatedAt,
	}
	return AgentConfigSnapshotRecord{
		AgentConfig:        config,
		InputEventSequence: row.InputEventSequence,
	}
}

func agentConfigSnapshotAtWatermarkFromSQLC(
	row dbsqlc.CaptureAgentConfigForEventWatermarkRow,
) AgentConfigSnapshotRecord {
	config := AgentConfigRecord{
		ID:                      row.ID,
		OrgID:                   row.OrgID,
		ProjectID:               row.ProjectID,
		Definition:              row.Definition,
		Source:                  row.Source,
		SourceFormat:            row.SourceFormat,
		SourceHash:              row.SourceHash,
		ConfiguredModelID:       row.ConfiguredModelID,
		CompiledDefinition:      row.CompiledDefinition,
		CompilerVersion:         row.CompilerVersion,
		EffectiveDefinitionHash: row.EffectiveDefinitionHash,
		CreatedAt:               row.CreatedAt,
	}
	return AgentConfigSnapshotRecord{
		AgentConfig:        config,
		InputEventSequence: row.InputEventSequence,
	}
}

func agentProfileRecordFromInsertSQLC(row dbsqlc.InsertAgentProfileRow, orgID ID) AgentProfileRecord {
	return AgentProfileRecord{
		ID:                row.ID,
		OrgID:             orgID,
		ProjectID:         row.ProjectID,
		Name:              row.Name,
		CurrentConfigID:   row.CurrentConfigID,
		CurrentGeneration: int(row.CurrentGeneration),
		IdempotencyKey:    row.IdempotencyKey,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func agentProfileRecordFromGetSQLC(row dbsqlc.GetAgentProfileRow) AgentProfileRecord {
	return AgentProfileRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		ProjectID:         row.ProjectID,
		Name:              row.Name,
		CurrentConfigID:   row.CurrentConfigID,
		CurrentGeneration: int(row.CurrentGeneration),
		IdempotencyKey:    row.IdempotencyKey,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func agentProfileRecordFromListForProjectSQLC(row dbsqlc.ListAgentProfilesForProjectRow) AgentProfileRecord {
	record := AgentProfileRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		ProjectID:         row.ProjectID,
		Name:              row.Name,
		CurrentConfigID:   row.CurrentConfigID,
		CurrentGeneration: int(row.CurrentGeneration),
		IdempotencyKey:    row.IdempotencyKey,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
	record.CurrentConfig = AgentConfigRecord{
		ID:                      row.ConfigID,
		OrgID:                   row.ConfigOrgID,
		ProjectID:               row.ConfigProjectID,
		ConfiguredModelID:       row.ConfigConfiguredModelID,
		Source:                  row.ConfigSource,
		SourceFormat:            row.ConfigSourceFormat,
		SourceHash:              row.ConfigSourceHash,
		CompiledDefinition:      row.ConfigCompiledDefinition,
		CompilerVersion:         row.ConfigCompilerVersion,
		EffectiveDefinitionHash: row.ConfigEffectiveDefinitionHash,
		CreatedAt:               row.ConfigCreatedAt,
	}
	return record
}

func agentProfileRecordFromGetByIdempotencySQLC(
	row dbsqlc.GetAgentProfileByIdempotencyKeyRow,
) AgentProfileRecord {
	return AgentProfileRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		ProjectID:         row.ProjectID,
		Name:              row.Name,
		CurrentConfigID:   row.CurrentConfigID,
		CurrentGeneration: int(row.CurrentGeneration),
		IdempotencyKey:    row.IdempotencyKey,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func agentProfileRecordFromRetargetSQLC(
	row dbsqlc.RetargetAgentProfileRow,
	orgID ID,
) AgentProfileRecord {
	return AgentProfileRecord{
		ID:                row.ID,
		OrgID:             orgID,
		ProjectID:         row.ProjectID,
		Name:              row.Name,
		CurrentConfigID:   row.CurrentConfigID,
		CurrentGeneration: int(row.CurrentGeneration),
		IdempotencyKey:    row.IdempotencyKey,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}
