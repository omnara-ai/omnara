package resourceguard

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
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

func OwnerScope(orgID uuid.UUID, ownerKind string, projectID, userID uuid.UUID) string {
	return fmt.Sprintf("%s:%s:%s:%s", orgID, ownerKind, projectID, userID)
}
