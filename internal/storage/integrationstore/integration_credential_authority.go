package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// LockIntegrationCredentialAccessTx requires the project and integration lifecycle
// gates. Call it before conversation or agent locks. Credential rotation preserves
// callback authority; revoking project access does not.
func LockIntegrationCredentialAccessTx(
	ctx context.Context, tx dbsqlc.DBTX, projectID, integrationID uuid.UUID,
) error {
	integration, err := getIntegration(ctx, dbsqlc.New(tx), projectID, integrationID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return storeerr.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if integration.State != IntegrationStateActive {
		return storeerr.ErrUnauthorized
	}
	_, err = lockAvailableIntegrationCredential(ctx, tx, integration.OrgID, projectID, integration.CredentialSecretID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return storeerr.ErrUnauthorized
	}
	return err
}

func lockAvailableIntegrationCredential(
	ctx context.Context, tx dbsqlc.DBTX, orgID, projectID, secretID uuid.UUID,
) (dbsqlc.GetProjectAvailableSecretRow, error) {
	if _, err := secretops.LockReference(ctx, tx, orgID, secretID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dbsqlc.GetProjectAvailableSecretRow{}, storeerr.ErrNotFound
		}
		return dbsqlc.GetProjectAvailableSecretRow{}, fmt.Errorf("lock integration credential: %w", err)
	}
	// Grant revocation takes the exclusive secret lock. Read availability in a
	// fresh statement after any wait, and retain the shared lock through commit.
	credential, err := dbsqlc.New(tx).GetProjectAvailableSecret(ctx, dbsqlc.GetProjectAvailableSecretParams{
		OrgID: orgID, ProjectID: projectID, SecretID: secretID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbsqlc.GetProjectAvailableSecretRow{}, storeerr.ErrNotFound
	}
	if err != nil {
		return dbsqlc.GetProjectAvailableSecretRow{}, fmt.Errorf("authorize integration credential: %w", err)
	}
	return credential, nil
}
