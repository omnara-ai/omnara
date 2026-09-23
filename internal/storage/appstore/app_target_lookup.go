package appstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) UpdateAppTargetDisplayNamesByProviderRefPrefix(
	ctx context.Context,
	projectID, appID uuid.UUID,
	providerRefPrefix, displayName string,
) error {
	if projectID == uuid.Nil || appID == uuid.Nil || providerRefPrefix == "" || displayName == "" {
		return errors.New("project, app, provider ref prefix, and display name are required")
	}
	_, err := s.q.UpdateAppTargetDisplayNamesByProviderRefPrefix(
		ctx,
		dbsqlc.UpdateAppTargetDisplayNamesByProviderRefPrefixParams{
			ProjectID:         projectID,
			AppID:             appID,
			ProviderRefPrefix: providerRefPrefix,
			DisplayName:       displayName,
		},
	)
	if err != nil {
		return fmt.Errorf("update app target display names: %w", err)
	}
	return nil
}

func (s *Store) GetAppTarget(
	ctx context.Context,
	projectID, id uuid.UUID,
) (AppTargetRecord, error) {
	return getAppTarget(ctx, s.q, projectID, id)
}

func (s *Store) GetAppTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, id uuid.UUID,
) (AppTargetRecord, error) {
	return getAppTarget(ctx, dbsqlc.New(tx), projectID, id)
}

func getAppTarget(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id uuid.UUID,
) (AppTargetRecord, error) {
	row, err := q.GetAppTarget(
		ctx,
		dbsqlc.GetAppTargetParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AppTargetRecord{}, storeerr.ErrNotFound
		}
		return AppTargetRecord{}, fmt.Errorf("get app target: %w", err)
	}
	return appTargetRecordFromGetSQLC(row), nil
}

func appTargetRecordFromGetSQLC(
	row dbsqlc.GetAppTargetRow,
) AppTargetRecord {
	return appTargetRecord(dbsqlc.GetAgentConversationTargetRow{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, AppID: row.AppID,
		ProviderRef: row.ProviderRef, ProviderRefKind: row.ProviderRefKind,
		DisplayName: row.DisplayName, ProviderMetadata: row.ProviderMetadata,
		SelectionSlot: row.SelectionSlot,
		DeletedAt:     row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, row.OrgID)
}
