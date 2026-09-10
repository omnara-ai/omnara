package secretops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

type Facts struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	ManagementKind management.Kind
	OwnerKind      string
	OwnerProjectID uuid.UUID
	OwnerUserID    uuid.UUID
	Kind           secrets.Kind
}

func LockReference(
	ctx context.Context,
	tx pgx.Tx,
	orgID, secretID uuid.UUID,
) (Facts, error) {
	q := dbsqlc.New(tx)
	if _, err := q.LockSecretForReference(
		ctx,
		dbsqlc.LockSecretForReferenceParams{OrgID: orgID, ID: secretID},
	); err != nil {
		return Facts{}, fmt.Errorf("lock secret for reference: %w", err)
	}
	return GetFacts(ctx, q, orgID, secretID)
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
		OwnerProjectID: idFromPtr(row.OwnerProjectID),
		OwnerUserID:    idFromPtr(row.OwnerUserID),
		Kind:           secrets.Kind(row.Kind),
	}
}

func FactsFromProjectAvailable(row dbsqlc.GetProjectAvailableSecretRow) Facts {
	return Facts{
		ID:             row.ID,
		OrgID:          row.OrgID,
		ManagementKind: management.Kind(row.ManagementKind),
		OwnerKind:      row.OwnerKind,
		OwnerProjectID: idFromPtr(row.OwnerProjectID),
		OwnerUserID:    idFromPtr(row.OwnerUserID),
		Kind:           secrets.Kind(row.Kind),
	}
}

func idFromPtr(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
