package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrProjectIntegrationSetupChanged = storeerr.Tag(storeerr.ErrConflict,
	errors.New("integration setup changed; refresh the integration and start setup again"))

func (s *Store) ConfigureProjectIntegration(
	ctx context.Context,
	input ConfigureProjectIntegrationInput,
) (ProjectIntegrationRecord, error) {
	input, err := normalizeConfigureProjectIntegrationInput(input)
	if err != nil {
		return ProjectIntegrationRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	if err := q.LockProjectIntegrationLifecycleExclusive(
		ctx,
		dbsqlc.LockProjectIntegrationLifecycleExclusiveParams{IntegrationID: input.IntegrationID},
	); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	current, err := getProjectIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	if current.Provider != input.Provider || (current.ProviderTenantID != "" &&
		(current.ProviderTenantID != input.ProviderTenantID || current.ProviderAccountRef != input.ProviderAccountRef)) {
		return ProjectIntegrationRecord{}, storeerr.InvalidRequest(
			errors.New("integration provider identity is immutable; create another integration for a different bot or account"),
		)
	}
	if current.SetupRevision != input.ExpectedSetupRevision {
		return ProjectIntegrationRecord{}, ErrProjectIntegrationSetupChanged
	}
	if err := validateLauncherProviderScope(current.Settings.Launcher, input.Provider,
		input.ProviderTenantID, input.ProviderAccountRef); err != nil {
		return ProjectIntegrationRecord{}, storeerr.InvalidRequest(err)
	}
	if input.Provider == IntegrationProviderSlack && input.OAuthFlowID == uuid.Nil {
		return ProjectIntegrationRecord{}, storeerr.InvalidRequest(errors.New("slack credentials require OAuth setup"))
	}
	if err := validateIntegrationInstaller(ctx, q, input.OrgID, input.ProjectID, input.InstalledByUserID); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	// Secret reference locks precede the integration row. Setup's exclusive gate already
	// excludes disconnect/delete and concurrent credential changes.
	if err := validateProjectIntegrationCredential(ctx, tx, input); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	row, err := q.ConfigureProjectIntegration(ctx, dbsqlc.ConfigureProjectIntegrationParams{
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
		return ProjectIntegrationRecord{}, ErrProjectIntegrationSetupChanged
	}
	if storeutil.IsUniqueViolationOnConstraint(err, "project_integrations_last_oauth_flow_id_idx") {
		return ProjectIntegrationRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
	}
	if err != nil {
		return ProjectIntegrationRecord{}, fmt.Errorf("configure integration: %w", err)
	}
	record, err := projectIntegrationRecord(row)
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	return record, nil
}

func (s *Store) IntegrationOAuthFlowConsumed(ctx context.Context, flowID uuid.UUID) (bool, error) {
	if flowID == uuid.Nil {
		return false, errors.New("flow id is required")
	}
	return s.q.IntegrationOAuthFlowConsumed(ctx, dbsqlc.IntegrationOAuthFlowConsumedParams{FlowID: &flowID})
}

func IdempotencyScope(integration ProjectIntegrationRecord) string {
	return "integration:" + integration.Provider + ":" + integration.ID.String()
}

func validateProjectIntegrationCredential(
	ctx context.Context,
	tx pgx.Tx,
	input ConfigureProjectIntegrationInput,
) error {
	_, err := secretops.LockReference(ctx, tx, input.OrgID, input.CredentialSecretID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("validate integration credential: %w", err)
	}
	credential, err := dbsqlc.New(tx).GetProjectAvailableSecret(ctx, dbsqlc.GetProjectAvailableSecretParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, SecretID: input.CredentialSecretID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize integration credential: %w", err)
	}
	wantKind, err := ProjectIntegrationCredentialKind(input.Provider)
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
