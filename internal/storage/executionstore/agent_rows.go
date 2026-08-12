package executionstore

import (
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func agentRecordFromInsertSQLC(row dbsqlc.InsertAgentRow) AgentRecord {
	return agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
	)
}

func agentRecordFromIdempotencySQLC(row dbsqlc.GetAgentByIdempotencyKeyRow) AgentRecord {
	return agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
	)
}

func agentRecordFromGetSQLC(row dbsqlc.GetAgentRow) AgentRecord {
	return agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
	)
}

func agentRecordFromProjectSQLC(row dbsqlc.GetAgentInProjectRow) AgentRecord {
	record := agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
	)
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
}

func agentRecordFromListForProjectSQLC(row dbsqlc.ListAgentsForProjectRow) AgentRecord {
	record := agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
	)
	record.IntegrationTarget = IntegrationTargetDisplay{
		Provider:         row.IntegrationTargetProvider,
		ProviderTenantID: row.IntegrationTargetProviderTenantID,
		ProviderRef:      row.IntegrationTargetProviderRef,
		ProviderRefKind:  row.IntegrationTargetProviderRefKind,
		DisplayName:      row.IntegrationTargetDisplayName,
	}
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
}

func agentRecordFromListRecentForProjectsSQLC(row dbsqlc.ListRecentAgentsForProjectsRow) AgentRecord {
	record := agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
	)
	record.IntegrationTarget = IntegrationTargetDisplay{
		Provider:         row.IntegrationTargetProvider,
		ProviderTenantID: row.IntegrationTargetProviderTenantID,
		ProviderRef:      row.IntegrationTargetProviderRef,
		ProviderRefKind:  row.IntegrationTargetProviderRefKind,
		DisplayName:      row.IntegrationTargetDisplayName,
	}
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
}

func agentRecordFromListForProjectByCreatedAtDescSQLC(
	row dbsqlc.ListAgentsForProjectByCreatedAtDescRow,
) AgentRecord {
	record := agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.IntegrationTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
	)
	record.IntegrationTarget = IntegrationTargetDisplay{
		Provider:         row.IntegrationTargetProvider,
		ProviderTenantID: row.IntegrationTargetProviderTenantID,
		ProviderRef:      row.IntegrationTargetProviderRef,
		ProviderRefKind:  row.IntegrationTargetProviderRefKind,
		DisplayName:      row.IntegrationTargetDisplayName,
	}
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
}

func agentRecordFromSQLC(
	id ID,
	orgID ID,
	projectID ID,
	state string,
	name string,
	agentProfileID *ID,
	currentConfigID ID,
	integrationTargetID *ID,
	idempotencyKey string,
	nextEventSequence int64,
	createdAt time.Time,
	updatedAt time.Time,
	archivedAt *time.Time,
) AgentRecord {
	return AgentRecord{
		ID:                  id,
		OrgID:               orgID,
		ProjectID:           projectID,
		AgentProfileID:      idFromSQLCPtr(agentProfileID),
		State:               AgentState(state),
		Name:                name,
		CurrentConfigID:     currentConfigID,
		IntegrationTargetID: idFromSQLCPtr(integrationTargetID),
		IdempotencyKey:      idempotencyKey,
		NextEventSequence:   nextEventSequence,
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
		ArchivedAt:          archivedAt,
	}
}
