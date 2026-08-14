package resourceguard

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func Lock(ctx context.Context, q *dbsqlc.Queries, resourceKind, scope string) error {
	if err := q.LockResourceCreation(ctx, dbsqlc.LockResourceCreationParams{
		ResourceKind: resourceKind,
		Scope:        scope,
	}); err != nil {
		return fmt.Errorf("lock %s resource creation: %w", resourceKind, err)
	}
	return nil
}

func ResolveLimits(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID uuid.UUID,
) (dbsqlc.EffectiveResourceLimit, error) {
	limits, err := q.GetEffectiveResourceLimits(ctx, dbsqlc.GetEffectiveResourceLimitsParams{
		OrgID: orgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbsqlc.EffectiveResourceLimit{}, fmt.Errorf(
			"resolve resource limits: %w",
			storeerr.ErrNotFound,
		)
	}
	if err != nil {
		return dbsqlc.EffectiveResourceLimit{}, fmt.Errorf("resolve resource limits: %w", err)
	}
	return limits, nil
}

func OwnerScope(orgID uuid.UUID, ownerKind string, projectID, userID uuid.UUID) string {
	return fmt.Sprintf("%s:%s:%s:%s", orgID, ownerKind, projectID, userID)
}
