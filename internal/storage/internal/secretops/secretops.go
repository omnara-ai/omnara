package secretops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

type Facts struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	ManagementKind management.Kind
	OwnerKind      string
	Kind           secrets.Kind
}

func GetFacts(ctx context.Context, q *dbsqlc.Queries, orgID, secretID uuid.UUID) (Facts, error) {
	row, err := q.GetSecret(ctx, dbsqlc.GetSecretParams{OrgID: orgID, ID: secretID})
	if err != nil {
		return Facts{}, fmt.Errorf("get secret facts: %w", err)
	}
	return FactsFromGet(row), nil
}

func FactsFromGet(row dbsqlc.GetSecretRow) Facts {
	return Facts{
		ID:             row.ID,
		OrgID:          row.OrgID,
		ManagementKind: management.Kind(row.ManagementKind),
		OwnerKind:      row.OwnerKind,
		Kind:           secrets.Kind(row.Kind),
	}
}

func FactsFromProjectAvailable(row dbsqlc.GetProjectAvailableSecretRow) Facts {
	return Facts{
		ID:             row.ID,
		OrgID:          row.OrgID,
		ManagementKind: management.Kind(row.ManagementKind),
		OwnerKind:      row.OwnerKind,
		Kind:           secrets.Kind(row.Kind),
	}
}
