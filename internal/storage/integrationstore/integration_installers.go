package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Attribution retains the account subject, not the browser session or token
// that authenticated a user. A deployment connector is never an account.
func normalizeIntegrationInstaller(
	orgID ID,
	principal identitystore.PrincipalRecord,
) (identitystore.PrincipalRecord, error) {
	if !identitystore.IsAccountPrincipal(principal) ||
		(!isNilID(principal.OrgID) && principal.OrgID != orgID) ||
		principal.ChannelConnectorID != "" || len(principal.ChannelConnectorCapabilities) != 0 ||
		!isNilID(principal.MachineDaemonTokenID) {
		return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	if principal.Type == identitystore.PrincipalTypeUser {
		if !isNilID(principal.OrgAPIKeyID) {
			return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
		}
		return identitystore.NewUserPrincipal(principal.ID), nil
	}
	if (!isNilID(principal.OrgAPIKeyID) && principal.OrgAPIKeyID != principal.ID) ||
		!isNilID(principal.PersonalAccessTokenID) || !isNilID(principal.BrowserSessionID) ||
		!isNilID(principal.OAuthAccessTokenID) {
		return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	return identitystore.NewOrgAPIKeyPrincipal(orgID, principal.ID), nil
}

// The exclusive principal constraint guarantees exactly one reference.
func integrationInstallerPrincipal(orgID ID, userID, orgAPIKeyID *ID) identitystore.PrincipalRecord {
	if userID != nil {
		return identitystore.NewUserPrincipal(*userID)
	}
	return identitystore.NewOrgAPIKeyPrincipal(orgID, *orgAPIKeyID)
}

func validateIntegrationInstaller(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
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
