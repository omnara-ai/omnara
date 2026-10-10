package executionstore

import (
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func agentRecordFromIdempotencySQLC(row dbsqlc.GetAgentByIdempotencyKeyRow) AgentRecord {
	record := agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.InteractionTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
		row.ParentAgentID,
		row.SubagentKey,
	)
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
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
		row.InteractionTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
		row.ParentAgentID,
		row.SubagentKey,
	)
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
}

func agentRecordFromListForProjectsSQLC(row dbsqlc.ListAgentsForProjectsRow) AgentRecord {
	record := agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.InteractionTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
		row.ParentAgentID,
		row.SubagentKey,
	)
	record.IntegrationTarget = IntegrationTargetDisplay{
		Provider: integrationdefinition.ProviderForKind(
			integrationdefinition.Kind(row.IntegrationTargetIntegrationKind),
		),
		ProviderTenantID: row.IntegrationTargetProviderTenantID,
		ScopeRef:         row.IntegrationTargetScopeRef,
		ScopeKind:        row.IntegrationTargetScopeKind,
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
		row.InteractionTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
		row.ParentAgentID,
		row.SubagentKey,
	)
	record.IntegrationTarget = IntegrationTargetDisplay{
		Provider: integrationdefinition.ProviderForKind(
			integrationdefinition.Kind(row.IntegrationTargetIntegrationKind),
		),
		ProviderTenantID: row.IntegrationTargetProviderTenantID,
		ScopeRef:         row.IntegrationTargetScopeRef,
		ScopeKind:        row.IntegrationTargetScopeKind,
		DisplayName:      row.IntegrationTargetDisplayName,
	}
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
}

func agentRecordFromListForProjectsByCreatedAtDescSQLC(
	row dbsqlc.ListAgentsForProjectsByCreatedAtDescRow,
) AgentRecord {
	record := agentRecordFromSQLC(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.State,
		row.Name,
		row.AgentProfileID,
		row.CurrentConfigID,
		row.InteractionTargetID,
		row.IdempotencyKey,
		row.NextEventSequence,
		row.CreatedAt,
		row.UpdatedAt,
		row.ArchivedAt,
		row.ParentAgentID,
		row.SubagentKey,
	)
	record.IntegrationTarget = IntegrationTargetDisplay{
		Provider: integrationdefinition.ProviderForKind(
			integrationdefinition.Kind(row.IntegrationTargetIntegrationKind),
		),
		ProviderTenantID: row.IntegrationTargetProviderTenantID,
		ScopeRef:         row.IntegrationTargetScopeRef,
		ScopeKind:        row.IntegrationTargetScopeKind,
		DisplayName:      row.IntegrationTargetDisplayName,
	}
	record.Model = AgentModelDisplay{
		ProviderConfig: row.ModelProviderConfigName,
		Name:           row.ModelName,
	}
	return record
}

func agentRecordFromSQLC(
	id uuid.UUID,
	orgID uuid.UUID,
	projectID uuid.UUID,
	state string,
	name string,
	agentProfileID *uuid.UUID,
	currentConfigID uuid.UUID,
	interactionTargetID *uuid.UUID,
	idempotencyKey string,
	nextEventSequence int64,
	createdAt time.Time,
	updatedAt time.Time,
	archivedAt *time.Time,
	parentAgentID *uuid.UUID,
	subagentKey string,
) AgentRecord {
	return AgentRecord{
		ID:                  id,
		OrgID:               orgID,
		ProjectID:           projectID,
		AgentProfileID:      storeutil.IDFromPtr(agentProfileID),
		State:               AgentState(state),
		Name:                name,
		CurrentConfigID:     currentConfigID,
		InteractionTargetID: storeutil.IDFromPtr(interactionTargetID),
		IdempotencyKey:      idempotencyKey,
		NextEventSequence:   nextEventSequence,
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
		ArchivedAt:          archivedAt,
		ParentAgentID:       storeutil.IDFromPtr(parentAgentID),
		SubagentKey:         subagentKey,
	}
}
