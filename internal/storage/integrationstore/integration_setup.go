package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrIntegrationSetupChanged = storeerr.Tag(storeerr.ErrConflict,
	errors.New("integration setup changed; refresh the integration and start setup again"))

var ErrIntegrationIdentityMismatch = storeerr.Tag(storeerr.ErrInvalidRequest,
	errors.New("integration provider identity is immutable; create another integration for a different bot or account"))

func (s *Store) ConfigureIntegration(
	ctx context.Context,
	input ConfigureIntegrationInput,
) (IntegrationRecord, error) {
	input, err := normalizeConfigureIntegrationInput(input)
	if err != nil {
		return IntegrationRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return IntegrationRecord{}, err
	}
	if err := q.LockIntegrationLifecycleExclusive(
		ctx,
		dbsqlc.LockIntegrationLifecycleExclusiveParams{IntegrationID: input.IntegrationID},
	); err != nil {
		return IntegrationRecord{}, err
	}
	current, err := getIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return IntegrationRecord{}, err
	}
	if current.Provider != input.Provider || (current.ProviderTenantID != "" &&
		(current.ProviderTenantID != input.ProviderTenantID || current.ProviderAccountRef != input.ProviderAccountRef)) {
		return IntegrationRecord{}, ErrIntegrationIdentityMismatch
	}
	if current.SetupRevision != input.ExpectedSetupRevision {
		return IntegrationRecord{}, ErrIntegrationSetupChanged
	}
	if input.Provider == integrationdefinition.ProviderSlack && input.OAuthFlowID == uuid.Nil {
		return IntegrationRecord{}, storeerr.InvalidRequest(errors.New("slack credentials require OAuth setup"))
	}
	if err := validateIntegrationInstaller(ctx, q, input.OrgID, input.ProjectID, input.InstalledByUserID); err != nil {
		return IntegrationRecord{}, err
	}
	// Secret reference locks precede the integration row. Setup's exclusive gate already
	// excludes disconnect/delete and concurrent credential changes.
	if err := validateIntegrationCredential(ctx, tx, input); err != nil {
		return IntegrationRecord{}, err
	}
	row, err := q.ConfigureIntegration(ctx, dbsqlc.ConfigureIntegrationParams{
		ProjectID:                input.ProjectID,
		ID:                       input.IntegrationID,
		InstalledByUserID:        &input.InstalledByUserID,
		ProviderTenantID:         &input.ProviderTenantID,
		ProviderAccountRef:       &input.ProviderAccountRef,
		ProviderAgentDisplayName: input.ProviderAgentDisplayName,
		CredentialSecretID:       input.CredentialSecretID,
		ProviderConfig:           input.ProviderConfig,
		ProviderIdentity:         input.ProviderIdentity,
		ProviderMetadata:         input.ProviderMetadata,
		OauthFlowID:              storeutil.IDFromNil(input.OAuthFlowID),
		ExpectedSetupRevision:    input.ExpectedSetupRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRecord{}, ErrIntegrationSetupChanged
	}
	if storeutil.IsUniqueViolationOnConstraint(err, "integrations_last_oauth_flow_id_idx") {
		return IntegrationRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
	}
	if err != nil {
		return IntegrationRecord{}, fmt.Errorf("configure integration: %w", err)
	}
	record, err := integrationRecord(row)
	if err != nil {
		return IntegrationRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationRecord{}, err
	}
	return record, nil
}

func (s *Store) IntegrationOAuthFlowConsumed(ctx context.Context, flowID uuid.UUID) (bool, error) {
	if flowID == uuid.Nil {
		return false, errors.New("flow id is required")
	}
	return s.q.IntegrationOAuthFlowConsumed(ctx, dbsqlc.IntegrationOAuthFlowConsumedParams{FlowID: &flowID})
}

func IdempotencyScope(integration IntegrationRecord) string {
	return "integration:" + string(integration.Provider) + ":" + integration.ID.String()
}

func validateIntegrationCredential(
	ctx context.Context,
	tx dbsqlc.DBTX,
	input ConfigureIntegrationInput,
) error {
	credential, err := lockAvailableIntegrationCredential(
		ctx, tx, input.OrgID, input.ProjectID, input.CredentialSecretID,
	)
	if err != nil {
		return err
	}
	wantKind, err := IntegrationCredentialKind(input.Provider)
	if err != nil {
		return storeerr.InvalidRequest(err)
	}
	if credential.Kind != string(wantKind) {
		return storeerr.InvalidRequest(fmt.Errorf("integration requires a %s secret", wantKind))
	}
	if input.CredentialVersionID != uuid.Nil &&
		storeutil.IDFromPtr(credential.CurrentVersionID) != input.CredentialVersionID {
		return fmt.Errorf("integration credential changed during validation: %w", storeerr.ErrConflict)
	}
	return nil
}

func validateIntegrationInstaller(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID, userID uuid.UUID,
) error {
	roles, err := qtx.ListProjectAuthorizationRolesForPrincipal(
		ctx,
		dbsqlc.ListProjectAuthorizationRolesForPrincipalParams{
			OrgID:     orgID,
			ProjectID: projectID,
			UserID:    &userID,
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
