package executionstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func projectMachineGrantFromUpsert(
	row dbsqlc.UpsertProjectMachineGrantRow,
) ProjectMachineGrantRecord {
	return projectMachineGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachineID,
		row.SourceKind,
		row.ProjectMachinePoolGrantID,
		row.Description,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachineGrantFromActiveMachine(
	row dbsqlc.ListActiveProjectMachineGrantsForMachineRow,
) ProjectMachineGrantRecord {
	return projectMachineGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachineID,
		row.SourceKind,
		row.ProjectMachinePoolGrantID,
		row.Description,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachineGrantFromGet(row dbsqlc.GetProjectMachineGrantRow) ProjectMachineGrantRecord {
	return projectMachineGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachineID,
		row.SourceKind,
		row.ProjectMachinePoolGrantID,
		row.Description,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachineGrantFromIdempotency(
	row dbsqlc.GetProjectMachineGrantByIdempotencyRow,
) ProjectMachineGrantRecord {
	return projectMachineGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachineID,
		row.SourceKind,
		row.ProjectMachinePoolGrantID,
		row.Description,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachineGrantFromDelete(
	row dbsqlc.DeleteProjectMachineGrantRow,
) ProjectMachineGrantRecord {
	return projectMachineGrantRecord(
		row.ID,
		row.OrgID,
		row.ProjectID,
		row.MachineID,
		row.SourceKind,
		row.ProjectMachinePoolGrantID,
		row.Description,
		row.IdempotencyKey,
		row.Metadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func projectMachineGrantRecord(
	id, orgID, projectID, machineID ID,
	sourceKind string,
	projectMachinePoolGrantID *ID,
	description string,
	idempotencyKey string,
	metadata []byte,
	createdAt, updatedAt time.Time,
) ProjectMachineGrantRecord {
	return ProjectMachineGrantRecord{
		ID:                        id,
		OrgID:                     orgID,
		ProjectID:                 projectID,
		MachineID:                 machineID,
		SourceKind:                ProjectMachineGrantSourceKind(sourceKind),
		ProjectMachinePoolGrantID: idFromSQLCPtr(projectMachinePoolGrantID),
		Description:               description,
		IdempotencyKey:            idempotencyKey,
		Metadata:                  json.RawMessage(metadata),
		CreatedAt:                 createdAt,
		UpdatedAt:                 updatedAt,
	}
}

func machineDaemonTokenFromCreate(row dbsqlc.MachineDaemonToken) MachineDaemonTokenRecord {
	return MachineDaemonTokenRecord{
		ID:           row.ID,
		OrgID:        row.OrgID,
		MachineID:    row.MachineID,
		Name:         row.Name,
		TokenHash:    row.TokenHash,
		Metadata:     row.Metadata,
		CreatedAt:    row.CreatedAt,
		LastUsedAt:   row.LastUsedAt,
		RevokedAt:    row.RevokedAt,
		RevokeReason: row.RevokeReason,
	}
}

func machineDaemonTokenFromList(row dbsqlc.MachineDaemonToken) MachineDaemonTokenRecord {
	return MachineDaemonTokenRecord{
		ID:           row.ID,
		OrgID:        row.OrgID,
		MachineID:    row.MachineID,
		Name:         row.Name,
		TokenHash:    row.TokenHash,
		Metadata:     row.Metadata,
		CreatedAt:    row.CreatedAt,
		LastUsedAt:   row.LastUsedAt,
		RevokedAt:    row.RevokedAt,
		RevokeReason: row.RevokeReason,
	}
}

func machineDaemonTokenFromRevoke(row dbsqlc.MachineDaemonToken) MachineDaemonTokenRecord {
	return MachineDaemonTokenRecord{
		ID:           row.ID,
		OrgID:        row.OrgID,
		MachineID:    row.MachineID,
		Name:         row.Name,
		TokenHash:    row.TokenHash,
		Metadata:     row.Metadata,
		CreatedAt:    row.CreatedAt,
		LastUsedAt:   row.LastUsedAt,
		RevokedAt:    row.RevokedAt,
		RevokeReason: row.RevokeReason,
	}
}
