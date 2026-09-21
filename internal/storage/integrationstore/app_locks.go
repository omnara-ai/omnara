package integrationstore

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// LockAppsTx precedes profile, machine-source and agent locks. Include the
// receipt's app in the same sorted set when admitting an inbox event. Compiled
// references remain valid metadata after disconnection/deletion; only additional
// apps authorize the current operation and must be active.
func LockAppsTx(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, refs []string, additional ...uuid.UUID) error {
	referenced := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		id, err := publicid.Decode(publicid.KindProjectApp, ref)
		if err != nil {
			return storeerr.InvalidRequest(err)
		}
		referenced = append(referenced, id)
	}
	return lockProjectAppsTx(ctx, tx, projectID, referenced, additional)
}

// Both compiled references and inbox admission use this sorted gate set. Only
// required apps authorize the operation; references may be disconnected/deleted.
func lockProjectAppsTx(
	ctx context.Context, tx pgx.Tx, projectID uuid.UUID, referenced, required []uuid.UUID,
) error {
	ids := make(map[uuid.UUID]bool, len(referenced)+len(required))
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
		return storeerr.ErrNotFound // Missing and foreign-project IDs grant no authority.
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
