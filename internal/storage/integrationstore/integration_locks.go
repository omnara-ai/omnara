package integrationstore

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func LockIntegrationsTx(
	ctx context.Context, tx pgx.Tx, projectID uuid.UUID, referenced []uuid.UUID, additional ...uuid.UUID,
) error {
	// Compiled references remain readable after revocation; only additional integrations
	// supply live authority.
	return lockProjectIntegrationsTx(ctx, tx, projectID, referenced, additional)
}

func lockProjectIntegrationsTx(
	ctx context.Context, tx pgx.Tx, projectID uuid.UUID, referenced, required []uuid.UUID,
) error {
	ids := make(map[uuid.UUID]bool)
	for _, id := range referenced {
		if id != uuid.Nil {
			ids[id] = false
		}
	}
	for _, id := range required {
		if id != uuid.Nil {
			ids[id] = true
		}
	}
	ordered := slices.Collect(maps.Keys(ids))
	slices.SortFunc(ordered, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	q := dbsqlc.New(tx)
	for _, id := range ordered {
		if err := q.LockProjectIntegrationLifecycleShared(ctx, dbsqlc.LockProjectIntegrationLifecycleSharedParams{
			IntegrationID: id,
		}); err != nil {
			return err
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	integrations, err := q.ListProjectIntegrationMetadataByIDs(ctx, dbsqlc.ListProjectIntegrationMetadataByIDsParams{
		ProjectID: projectID, Ids: ordered,
	})
	if err != nil {
		return err
	}
	if len(integrations) != len(ordered) {
		return storeerr.ErrNotFound
	}
	for _, integration := range integrations {
		if !ids[integration.ID] {
			continue
		}
		if integration.DeletedAt != nil {
			return storeerr.ErrNotFound
		}
		if integration.State != string(ProjectIntegrationStateActive) {
			return storeerr.ErrUnauthorized
		}
	}
	return nil
}
