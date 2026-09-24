package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
	ctx context.Context,
	projectID, integrationID uuid.UUID,
	providerRefPrefix, displayName string,
) error {
	if projectID == uuid.Nil || integrationID == uuid.Nil || providerRefPrefix == "" || displayName == "" {
		return errors.New("project, integration, provider ref prefix, and display name are required")
	}
	_, err := s.q.UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
		ctx,
		dbsqlc.UpdateIntegrationTargetDisplayNamesByProviderRefPrefixParams{
			ProjectID:         projectID,
			IntegrationID:     integrationID,
			ProviderRefPrefix: providerRefPrefix,
			DisplayName:       displayName,
		},
	)
	if err != nil {
		return fmt.Errorf("update integration target display names: %w", err)
	}
	return nil
}

func (s *Store) GetIntegrationTarget(
	ctx context.Context,
	projectID, id uuid.UUID,
) (IntegrationTargetRecord, error) {
	return getIntegrationTarget(ctx, s.q, projectID, id)
}

func (s *Store) GetIntegrationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, id uuid.UUID,
) (IntegrationTargetRecord, error) {
	return getIntegrationTarget(ctx, dbsqlc.New(tx), projectID, id)
}

func getIntegrationTarget(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id uuid.UUID,
) (IntegrationTargetRecord, error) {
	row, err := q.GetIntegrationTarget(
		ctx,
		dbsqlc.GetIntegrationTargetParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrNotFound
		}
		return IntegrationTargetRecord{}, fmt.Errorf("get integration target: %w", err)
	}
	return integrationTargetRecordFromGetSQLC(row), nil
}

func integrationTargetRecordFromGetSQLC(
	row dbsqlc.GetIntegrationTargetRow,
) IntegrationTargetRecord {
	return integrationTargetRecord(dbsqlc.GetAgentConversationTargetRow{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, IntegrationID: row.IntegrationID,
		ProviderRef: row.ProviderRef, ProviderRefKind: row.ProviderRefKind,
		DisplayName: row.DisplayName, ProviderMetadata: row.ProviderMetadata,
		SelectionSlot: row.SelectionSlot,
		DeletedAt:     row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, row.OrgID)
}
