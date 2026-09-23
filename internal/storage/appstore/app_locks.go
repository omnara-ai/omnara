package appstore

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func LockAppsTx(
	ctx context.Context, tx pgx.Tx, projectID uuid.UUID, referenced []uuid.UUID, additional ...uuid.UUID,
) error {
	// Compiled references remain readable after revocation; only additional apps
	// supply live authority.
	return lockProjectAppsTx(ctx, tx, projectID, referenced, additional)
}

func lockProjectAppsTx(
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
		if err := q.LockProjectAppLifecycleShared(ctx, dbsqlc.LockProjectAppLifecycleSharedParams{AppID: id}); err != nil {
			return err
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	apps, err := q.ListProjectAppMetadataByIDs(ctx, dbsqlc.ListProjectAppMetadataByIDsParams{
		ProjectID: projectID, Ids: ordered,
	})
	if err != nil {
		return err
	}
	if len(apps) != len(ordered) {
		return storeerr.ErrNotFound
	}
	for _, app := range apps {
		if !ids[app.ID] {
			continue
		}
		if app.DeletedAt != nil {
			return storeerr.ErrNotFound
		}
		if app.State != string(ProjectAppStateActive) {
			return storeerr.ErrUnauthorized
		}
	}
	return nil
}
