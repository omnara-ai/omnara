package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Attribution retains the account subject, not the browser session or token
// that authenticated a user. A deployment connector is never an account.
func normalizeIntegrationInstaller(
	orgID uuid.UUID,
	principal identitystore.PrincipalRecord,
) (identitystore.PrincipalRecord, error) {
	if !identitystore.IsAccountPrincipal(principal) ||
		(principal.OrgID != uuid.Nil && principal.OrgID != orgID) ||
		principal.ChannelConnectorID != "" || len(principal.ChannelConnectorCapabilities) != 0 ||
		principal.MachineDaemonTokenID != uuid.Nil {
		return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	if principal.Type == identitystore.PrincipalTypeUser {
		if principal.OrgAPIKeyID != uuid.Nil {
			return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
		}
		return identitystore.NewUserPrincipal(principal.ID), nil
	}
	if (principal.OrgAPIKeyID != uuid.Nil && principal.OrgAPIKeyID != principal.ID) ||
		principal.PersonalAccessTokenID != uuid.Nil || principal.BrowserSessionID != uuid.Nil ||
		principal.OAuthAccessTokenID != uuid.Nil {
		return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	return identitystore.NewOrgAPIKeyPrincipal(orgID, principal.ID), nil
}

// The exclusive principal constraint guarantees exactly one reference.
func integrationInstallerPrincipal(orgID uuid.UUID, userID, orgAPIKeyID *uuid.UUID) identitystore.PrincipalRecord {
	if userID != nil {
		return identitystore.NewUserPrincipal(*userID)
	}
	return identitystore.NewOrgAPIKeyPrincipal(orgID, *orgAPIKeyID)
}

func validateIntegrationInstaller(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID uuid.UUID,
	principal identitystore.PrincipalRecord,
) error {
	userID, orgAPIKeyID := identitystore.AccountPrincipalIDs(principal)
	if orgAPIKeyID != nil {
		// Key revocation removes its memberships in the same transaction. Lock
		// the key before checking roles, and before profile/agent locks, so a
		// revoked key cannot install using authorization read before revocation.
		if _, err := qtx.LockOrgAPIKeyForUpdate(ctx, dbsqlc.LockOrgAPIKeyForUpdateParams{
			OrgID: orgID, ID: *orgAPIKeyID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return storeerr.ErrUnauthorized
			}
			return fmt.Errorf("lock integration installer: %w", err)
		}
	}
	roles, err := qtx.ListProjectAuthorizationRolesForPrincipal(
		ctx,
		dbsqlc.ListProjectAuthorizationRolesForPrincipalParams{
			OrgID:       orgID,
			ProjectID:   projectID,
			UserID:      userID,
			OrgApiKeyID: orgAPIKeyID,
		},
	)
	if err != nil {
		return fmt.Errorf("validate integration installer: %w", err)
	}
	if identitystore.ProjectRolesAllow(roles, identitystore.ProjectActionManage) {
		return nil
	}
	return storeerr.ErrUnauthorized
}
