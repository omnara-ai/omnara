package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func projectMachineGrantFromSQLC(row dbsqlc.ProjectMachineGrant) ProjectMachineGrantRecord {
	return ProjectMachineGrantRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ProjectID:                 row.ProjectID,
		MachineID:                 row.MachineID,
		SourceKind:                ProjectMachineGrantSourceKind(row.SourceKind),
		ProjectMachinePoolGrantID: storeutil.IDFromPtr(row.ProjectMachinePoolGrantID),
		Description:               row.Description,
		Metadata:                  row.Metadata,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
	}
}

func machineDaemonTokenFromSQLC(row dbsqlc.MachineDaemonToken) MachineDaemonTokenRecord {
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
