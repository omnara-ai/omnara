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

var ErrProjectAppSetupChanged = storeerr.Tag(storeerr.ErrConflict,
	errors.New("app setup changed; refresh the app and start setup again"))

func (s *Store) ConfigureProjectApp(ctx context.Context, input ConfigureProjectAppInput) (ProjectAppRecord, error) {
	input, err := normalizeConfigureProjectAppInput(input)
	if err != nil {
		return ProjectAppRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectAppRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return ProjectAppRecord{}, err
	}
	if err := q.LockProjectAppLifecycleExclusive(
		ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: input.AppID},
	); err != nil {
		return ProjectAppRecord{}, err
	}
	current, err := getProjectApp(ctx, q, input.ProjectID, input.AppID)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	if current.Provider != input.Provider || (current.ProviderTenantID != "" &&
		(current.ProviderTenantID != input.ProviderTenantID || current.ProviderAccountRef != input.ProviderAccountRef)) {
		return ProjectAppRecord{}, storeerr.InvalidRequest(
			errors.New("app provider identity is immutable; create another app for a different bot or account"),
		)
	}
	if current.SetupRevision != input.ExpectedSetupRevision {
		return ProjectAppRecord{}, ErrProjectAppSetupChanged
	}
	if err := validateLauncherProviderScope(current.Settings.Launcher, input.Provider,
		input.ProviderTenantID, input.ProviderAccountRef); err != nil {
		return ProjectAppRecord{}, storeerr.InvalidRequest(err)
	}
	if input.Provider == IntegrationProviderSlack && input.OAuthFlowID == uuid.Nil {
		return ProjectAppRecord{}, storeerr.InvalidRequest(errors.New("slack credentials require OAuth setup"))
	}
	if err := validateAppInstaller(ctx, q, input.OrgID, input.ProjectID, input.InstalledByUserID); err != nil {
		return ProjectAppRecord{}, err
	}
	// Secret reference locks precede the app row. Setup's exclusive gate already
	// excludes disconnect/delete and concurrent credential changes.
	if err := validateProjectAppCredential(ctx, tx, input); err != nil {
		return ProjectAppRecord{}, err
	}
	row, err := q.ConfigureProjectApp(ctx, dbsqlc.ConfigureProjectAppParams{
		ProjectID:                input.ProjectID,
		ID:                       input.AppID,
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
		return ProjectAppRecord{}, ErrProjectAppSetupChanged
	}
	if storeutil.IsUniqueViolationOnConstraint(err, "project_apps_last_oauth_flow_id_idx") {
		return ProjectAppRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
	}
	if err != nil {
		return ProjectAppRecord{}, fmt.Errorf("configure app: %w", err)
	}
	record, err := projectAppRecord(row)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectAppRecord{}, err
	}
	return record, nil
}

func (s *Store) IntegrationOAuthFlowConsumed(ctx context.Context, flowID uuid.UUID) (bool, error) {
	if flowID == uuid.Nil {
		return false, errors.New("flow id is required")
	}
	return s.q.IntegrationOAuthFlowConsumed(ctx, dbsqlc.IntegrationOAuthFlowConsumedParams{FlowID: &flowID})
}

func IdempotencyScope(app ProjectAppRecord) string {
	return "integration:" + app.Provider + ":" + app.ID.String()
}

func validateProjectAppCredential(
	ctx context.Context,
	tx pgx.Tx,
	input ConfigureProjectAppInput,
) error {
	_, err := secretops.LockReference(ctx, tx, input.OrgID, input.CredentialSecretID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("validate app credential: %w", err)
	}
	credential, err := dbsqlc.New(tx).GetProjectAvailableSecret(ctx, dbsqlc.GetProjectAvailableSecretParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, SecretID: input.CredentialSecretID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize app credential: %w", err)
	}
	wantKind, err := ProjectAppCredentialKind(input.Provider)
	if err != nil {
		return storeerr.InvalidRequest(err)
	}
	if credential.Kind != string(wantKind) {
		return storeerr.InvalidRequest(fmt.Errorf("app requires a %s secret", wantKind))
	}
	if input.CredentialVersionID != uuid.Nil &&
		storeutil.IDFromPtr(credential.CurrentVersionID) != input.CredentialVersionID {
		return fmt.Errorf("app credential changed during validation: %w", storeerr.ErrConflict)
	}
	return nil
}

func validateAppInstaller(
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
		return fmt.Errorf("validate app installer: %w", err)
	}
	if identitystore.ProjectRolesAllow(roles, identitystore.ProjectActionManage) {
		return nil
	}
	return storeerr.ErrUnauthorized
}
