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
	projectID, appID uuid.UUID,
	providerRefPrefix, displayName string,
) error {
	if projectID == uuid.Nil || appID == uuid.Nil || providerRefPrefix == "" || displayName == "" {
		return errors.New("project, app, provider ref prefix, and display name are required")
	}
	_, err := s.q.UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
		ctx,
		dbsqlc.UpdateIntegrationTargetDisplayNamesByProviderRefPrefixParams{
			ProjectID:         projectID,
			AppID:             appID,
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
	return appTargetRecord(dbsqlc.GetAgentConversationTargetRow{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, AppID: row.AppID,
		ProviderRef: row.ProviderRef, ProviderRefKind: row.ProviderRefKind,
		DisplayName: row.DisplayName, ProviderMetadata: row.ProviderMetadata,
		SelectionSlot: row.SelectionSlot, IsToolContext: row.IsToolContext,
		DeletedAt: row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, row.OrgID)
}

// GetAgentAppToolContext includes retired targets: retirement cannot remove the
// immutable destination restriction. Callers must still authorize the app/agent.
func (s *Store) GetAgentAppToolContext(
	ctx context.Context,
	projectID, agentID, appID uuid.UUID,
) (IntegrationTargetRecord, bool, error) {
	row, err := s.q.GetAgentAppToolContext(ctx, dbsqlc.GetAgentAppToolContextParams{
		ProjectID: projectID, AgentID: agentID, AppID: appID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetRecord{}, false, nil
	}
	if err != nil {
		return IntegrationTargetRecord{}, false, fmt.Errorf("get agent app tool context: %w", err)
	}
	return integrationTargetRecordFromGetSQLC(dbsqlc.GetIntegrationTargetRow(row)), true, nil
}
