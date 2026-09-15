package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/registryname"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) UpsertIntegrationInstall(
	ctx context.Context,
	input UpsertIntegrationInstallInput,
) (IntegrationInstallRecord, error) {
	input, err := normalizeUpsertIntegrationInstallInput(input)
	if err != nil {
		return IntegrationInstallRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationInstallRecord{}, fmt.Errorf("begin upsert integration install: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return IntegrationInstallRecord{}, err
	}
	if err := validateIntegrationInstaller(ctx, qtx, input.OrgID, input.ProjectID, input.InstalledBy); err != nil {
		return IntegrationInstallRecord{}, err
	}
	existing, found, err := lockIntegrationInstallIdentityTx(ctx, tx, input)
	if err != nil {
		return IntegrationInstallRecord{}, err
	}
	expectedKind, err := validateIntegrationInstallApp(ctx, qtx, input)
	if err != nil {
		return IntegrationInstallRecord{}, err
	}
	if err := validateIntegrationInstallCredential(ctx, tx, input, expectedKind); err != nil {
		return IntegrationInstallRecord{}, err
	}
	var record IntegrationInstallRecord
	if found {
		record, err = updateIntegrationInstallTx(ctx, qtx, existing.ID, input)
	} else {
		record, err = insertIntegrationInstallTx(ctx, qtx, input)
	}
	if err != nil {
		return IntegrationInstallRecord{}, err
	}
	if input.InitialRoute != nil {
		route := *input.InitialRoute
		route.ProjectID, route.IntegrationInstallID = record.ProjectID, record.ID
		if _, err := s.createIntegrationRouteTx(ctx, tx, route); err != nil {
			if errors.Is(err, storeerr.ErrIdempotencyConflict) {
				return IntegrationInstallRecord{}, storeerr.ErrConflict
			}
			return IntegrationInstallRecord{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationInstallRecord{}, fmt.Errorf("commit integration installation: %w", err)
	}
	return record, nil
}

func lockIntegrationInstallIdentityTx(
	ctx context.Context, tx pgx.Tx, input UpsertIntegrationInstallInput,
) (IntegrationInstallRecord, bool, error) {
	q := dbsqlc.New(tx)
	// Serialize first installation and reauthorization by real provider identity,
	// before any app locks. INSERT's app constraint must never make an upsert
	// wait for an existing installation while already holding its parent app.
	if err := q.LockIntegrationInstallIdentity(ctx, dbsqlc.LockIntegrationInstallIdentityParams{
		IntegrationAppID: input.IntegrationAppID, ProviderTenantID: storeutil.TextFromEmpty(input.ProviderTenantID),
		ProviderAccountRef: input.ProviderAccountRef,
	}); err != nil {
		return IntegrationInstallRecord{}, false, err
	}
	row, err := q.GetIntegrationInstallByAppProviderAccount(ctx, dbsqlc.GetIntegrationInstallByAppProviderAccountParams{
		IntegrationAppID: input.IntegrationAppID, ProviderTenantID: storeutil.TextFromEmpty(input.ProviderTenantID),
		ProviderAccountRef: input.ProviderAccountRef,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationInstallRecord{}, false, nil
	}
	if err != nil {
		return IntegrationInstallRecord{}, false, err
	}
	if row.OrgID != input.OrgID || row.ProjectID != input.ProjectID {
		return IntegrationInstallRecord{}, false, storeerr.ErrConflict
	}
	if err := q.LockIntegrationInstallLifecycleShared(ctx, dbsqlc.LockIntegrationInstallLifecycleSharedParams{
		InstallID: row.ID,
	}); err != nil {
		return IntegrationInstallRecord{}, false, err
	}
	row, err = q.LockIntegrationInstallByAppProviderAccount(ctx, dbsqlc.LockIntegrationInstallByAppProviderAccountParams{
		IntegrationAppID:   storeutil.IDFromNil(input.IntegrationAppID),
		ProviderTenantID:   storeutil.TextFromEmpty(input.ProviderTenantID),
		ProviderAccountRef: storeutil.TextFromEmpty(input.ProviderAccountRef),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationInstallRecord{}, false, storeerr.ErrConflict
	}
	if err != nil {
		return IntegrationInstallRecord{}, false, err
	}
	if row.OrgID != input.OrgID || row.ProjectID != input.ProjectID {
		return IntegrationInstallRecord{}, false, storeerr.ErrConflict
	}
	return integrationInstallRecordFromSQLC(row), true, nil
}

func insertIntegrationInstallTx(
	ctx context.Context, qtx *dbsqlc.Queries, input UpsertIntegrationInstallInput,
) (IntegrationInstallRecord, error) {
	installerUserID, installerKeyID := identitystore.AccountPrincipalIDs(input.InstalledBy)
	row, err := qtx.InsertIntegrationInstall(ctx, dbsqlc.InsertIntegrationInstallParams{
		OrgID:                  input.OrgID,
		ProjectID:              input.ProjectID,
		IntegrationAppID:       storeutil.IDFromNil(input.IntegrationAppID),
		InstalledByUserID:      installerUserID,
		InstalledByOrgApiKeyID: installerKeyID,
		Provider:               storeutil.TextFromEmpty(input.Provider),
		IntegrationKind:        string(input.IntegrationKind),
		ConnectionMode:         input.ConnectionMode,
		State:                  string(input.State),
		ProviderTenantID:       storeutil.TextFromEmpty(input.ProviderTenantID),
		ProviderAccountRef:     storeutil.TextFromEmpty(input.ProviderAccountRef),
		DisplayName:            input.DisplayName,
		CredentialSecretID:     storeutil.IDFromNil(input.CredentialSecretID),
		ProviderConfig:         input.ProviderConfig,
		ProviderIdentity:       input.ProviderIdentity,
		Metadata:               input.Metadata,
		LastOauthFlowID:        storeutil.IDFromNil(input.OAuthFlowID),
	})
	if err != nil {
		if storeutil.IsUniqueViolationOnConstraint(err, "integration_installs_last_oauth_flow_id_idx") {
			return IntegrationInstallRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
		if errors.Is(err, pgx.ErrNoRows) || storeutil.IsUniqueViolation(err) {
			return IntegrationInstallRecord{}, storeerr.ErrConflict
		}
		return IntegrationInstallRecord{}, integrationChannelWriteError("insert integration install", err)
	}
	record := integrationInstallRecordFromSQLC(row)
	record.Created = true
	return record, nil
}

func (s *Store) IntegrationOAuthFlowConsumed(ctx context.Context, flowID uuid.UUID) (bool, error) {
	if flowID == uuid.Nil {
		return false, errors.New("flow id is required")
	}
	consumed, err := s.q.IntegrationOAuthFlowConsumed(
		ctx,
		dbsqlc.IntegrationOAuthFlowConsumedParams{LastOauthFlowID: &flowID},
	)
	if err != nil {
		return false, fmt.Errorf("check integration oauth flow consumed: %w", err)
	}
	return consumed, nil
}

func (s *Store) GetIntegrationInstall(
	ctx context.Context,
	projectID, id uuid.UUID,
) (IntegrationInstallRecord, error) {
	return getIntegrationInstall(ctx, s.q, projectID, id)
}

func getIntegrationInstall(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id uuid.UUID,
) (IntegrationInstallRecord, error) {
	row, err := q.GetIntegrationInstall(
		ctx,
		dbsqlc.GetIntegrationInstallParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationInstallRecord{}, storeerr.ErrNotFound
		}
		return IntegrationInstallRecord{}, fmt.Errorf("get integration install: %w", err)
	}
	return integrationInstallRecordFromSQLC(row), nil
}

func (s *Store) GetIntegrationInstallByID(
	ctx context.Context,
	id uuid.UUID,
) (IntegrationInstallRecord, error) {
	return getIntegrationInstallByID(ctx, s.q, id)
}

func (s *Store) GetIntegrationInstallByIDTx(
	ctx context.Context,
	tx pgx.Tx,
	id uuid.UUID,
) (IntegrationInstallRecord, error) {
	return getIntegrationInstallByID(ctx, dbsqlc.New(tx), id)
}

func getIntegrationInstallByID(
	ctx context.Context,
	q *dbsqlc.Queries,
	id uuid.UUID,
) (IntegrationInstallRecord, error) {
	row, err := q.GetIntegrationInstallByID(ctx, dbsqlc.GetIntegrationInstallByIDParams{ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationInstallRecord{}, storeerr.ErrNotFound
		}
		return IntegrationInstallRecord{}, fmt.Errorf("get integration install by id: %w", err)
	}
	return integrationInstallRecordFromSQLC(row), nil
}

func (s *Store) GetSlackIntegrationInstallByIdentity(
	ctx context.Context,
	providerTenantID, providerAccountRef string,
) (IntegrationInstallRecord, error) {
	row, err := s.q.GetSlackIntegrationInstallByIdentity(
		ctx,
		dbsqlc.GetSlackIntegrationInstallByIdentityParams{
			ProviderTenantID:   providerTenantID,
			ProviderAccountRef: providerAccountRef,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationInstallRecord{}, storeerr.ErrNotFound
		}
		return IntegrationInstallRecord{}, fmt.Errorf("get integration install by provider account: %w", err)
	}
	return integrationInstallRecordFromSQLC(row), nil
}

type ListIntegrationInstallsForProjectInput struct {
	ProjectID uuid.UUID
	Filters   IntegrationInstallListFilters
	List      listing.Options
	Limit     int
}

type IntegrationInstallListFilters struct {
	AgentProfileID uuid.UUID
	OAuthFlowID    uuid.UUID
}

type ListIntegrationInstallsForProjectResult struct {
	Installs []IntegrationInstallRecord
	HasMore  bool
	Next     listing.Cursor
}

func (s *Store) ListIntegrationInstallsForProject(
	ctx context.Context,
	input ListIntegrationInstallsForProjectInput,
) (ListIntegrationInstallsForProjectResult, error) {
	if input.ProjectID == uuid.Nil {
		return ListIntegrationInstallsForProjectResult{}, errors.New("project id is required")
	}
	if input.Limit <= 0 {
		return ListIntegrationInstallsForProjectResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at") {
		return ListIntegrationInstallsForProjectResult{}, errors.New("unsupported integration install list sort")
	}
	rows, err := s.q.ListIntegrationInstallsForProject(ctx, dbsqlc.ListIntegrationInstallsForProjectParams{
		ProjectID: input.ProjectID, RowLimit: int32(input.Limit) + 1,
		NamePattern: input.List.NamePattern, SortField: input.List.SortField,
		SortDesc: input.List.SortDesc, CursorSet: input.List.After.Set,
		CursorKey: input.List.After.Key, CursorID: input.List.After.ID,
		AgentProfileID: storeutil.IDFromNil(input.Filters.AgentProfileID),
		OauthFlowID:    storeutil.IDFromNil(input.Filters.OAuthFlowID),
	})
	if err != nil {
		return ListIntegrationInstallsForProjectResult{}, fmt.Errorf("list integration installs: %w", err)
	}
	result := ListIntegrationInstallsForProjectResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	if result.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.Next = listing.Cursor{Set: true, Key: last.SortKey, ID: last.ID}
	}
	result.Installs = make([]IntegrationInstallRecord, 0, len(rows))
	for _, row := range rows {
		result.Installs = append(result.Installs, integrationInstallRecordFromListSQLC(row))
	}
	return result, nil
}

type DisableIntegrationInstallInput struct {
	ProjectID           uuid.UUID
	ID                  uuid.UUID
	ExpectedOAuthFlowID *uuid.UUID
}

func (s *Store) DisableIntegrationInstall(
	ctx context.Context,
	input DisableIntegrationInstallInput,
) (bool, error) {
	if input.ProjectID == uuid.Nil || input.ID == uuid.Nil || input.ExpectedOAuthFlowID == nil {
		return false, errors.New("project, integration install, and expected OAuth flow are required")
	}
	expectedOAuthFlowID := *input.ExpectedOAuthFlowID
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin disable integration install: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	current, err := qtx.LockIntegrationInstallForDisable(
		ctx,
		dbsqlc.LockIntegrationInstallForDisableParams{
			ProjectID: input.ProjectID,
			ID:        input.ID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, storeerr.ErrNotFound
		}
		return false, fmt.Errorf("lock integration install for disable: %w", err)
	}
	if IntegrationInstallState(current.State) != IntegrationInstallStateActive ||
		storeutil.IDFromPtr(current.LastOauthFlowID) != expectedOAuthFlowID {
		return false, nil
	}
	rows, err := qtx.DisableIntegrationInstall(
		ctx,
		dbsqlc.DisableIntegrationInstallParams{
			ProjectID:           input.ProjectID,
			ID:                  input.ID,
			ExpectedOauthFlowID: storeutil.IDFromNil(expectedOAuthFlowID),
		},
	)
	if err != nil {
		return false, fmt.Errorf("disable integration install: %w", err)
	}
	if rows != 1 {
		return false, storeerr.ErrStateTransitionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit disable integration install: %w", err)
	}
	return true, nil
}

func (s *Store) DeleteIntegrationInstall(ctx context.Context, projectID, id uuid.UUID) error {
	if projectID == uuid.Nil || id == uuid.Nil {
		return errors.New("project and integration install are required")
	}
	_, err := storeutil.RetryTransaction(ctx, "delete_integration_install", func() (struct{}, error) {
		return struct{}{}, s.deleteIntegrationInstallOnce(ctx, projectID, id)
	})
	return err
}

func (s *Store) deleteIntegrationInstallOnce(ctx context.Context, projectID, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete integration install: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	install, err := getIntegrationInstall(ctx, qtx, projectID, id)
	if err != nil {
		return err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, install.OrgID, projectID); err != nil {
		return err
	}
	// Freeze target admission before enumerating the agents that deletion will lock.
	if err := qtx.LockIntegrationInstallLifecycleExclusive(
		ctx,
		dbsqlc.LockIntegrationInstallLifecycleExclusiveParams{InstallID: id},
	); err != nil {
		return fmt.Errorf("lock integration install lifecycle for deletion: %w", err)
	}
	agentIDs, err := qtx.ListIntegrationInstallAgentIDsForLifecycle(
		ctx,
		dbsqlc.ListIntegrationInstallAgentIDsForLifecycleParams{
			ProjectID:            projectID,
			IntegrationInstallID: id,
		},
	)
	if err != nil {
		return fmt.Errorf("list integration install agents for lifecycle: %w", err)
	}
	agentRefs := make([]lifecyclelock.AgentRef, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		agentRefs = append(agentRefs, lifecyclelock.AgentRef{ProjectID: projectID, AgentID: agentID})
	}
	if err := lifecyclelock.Agents(ctx, tx, agentRefs); err != nil {
		return err
	}
	if _, err := qtx.LockIntegrationInstallForMutation(
		ctx,
		dbsqlc.LockIntegrationInstallForMutationParams{ProjectID: projectID, ID: id},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock integration install for deletion: %w", err)
	}
	if err := s.access.ClearInstallTargetsFromAgents(ctx, tx, projectID, id); err != nil {
		return err
	}
	params := dbsqlc.DeleteIntegrationInstallParams{
		ProjectID: projectID, ID: id,
	}
	rows, err := qtx.DeleteIntegrationInstall(ctx, params)
	if err != nil {
		return fmt.Errorf("delete integration install: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	if err := qtx.DeleteIntegrationInstallRuntimeUnits(
		ctx,
		dbsqlc.DeleteIntegrationInstallRuntimeUnitsParams{
			ProjectID: projectID, IntegrationInstallID: &id,
		},
	); err != nil {
		return fmt.Errorf("delete integration install runtime units: %w", err)
	}
	if err := qtx.DeleteIntegrationRoutes(ctx, dbsqlc.DeleteIntegrationRoutesParams{
		ProjectID: projectID, IntegrationInstallID: id,
	}); err != nil {
		return fmt.Errorf("delete integration install routes: %w", err)
	}
	if err := qtx.DeleteIntegrationTargets(ctx, dbsqlc.DeleteIntegrationTargetsParams{
		ProjectID: projectID, IntegrationInstallID: id,
	}); err != nil {
		return fmt.Errorf("delete integration install targets: %w", err)
	}
	if err := qtx.RevokeIntegrationInstallTargetBindings(
		ctx,
		dbsqlc.RevokeIntegrationInstallTargetBindingsParams{
			ProjectID: projectID, IntegrationInstallID: id,
		},
	); err != nil {
		return fmt.Errorf("revoke integration install bindings: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete integration install: %w", err)
	}
	return nil
}

func integrationIdempotencyScope(install IntegrationInstallRecord) string {
	return "integration:" + install.Provider + ":" + install.ID.String()
}

func normalizeUpsertIntegrationInstallInput(
	input UpsertIntegrationInstallInput,
) (UpsertIntegrationInstallInput, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return UpsertIntegrationInstallInput{}, errors.New("org and project are required")
	}
	installer, err := normalizeIntegrationInstaller(input.OrgID, input.InstalledBy)
	if err != nil {
		return UpsertIntegrationInstallInput{}, err
	}
	input.InstalledBy = installer
	if input.IntegrationAppID == uuid.Nil {
		return UpsertIntegrationInstallInput{}, storeerr.InvalidRequest(
			errors.New("managed installation requires a real app"))
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.IntegrationKind = IntegrationKind(strings.TrimSpace(string(input.IntegrationKind)))
	if input.IntegrationKind != IntegrationKindManaged {
		return UpsertIntegrationInstallInput{}, storeerr.InvalidRequest(
			errors.New("provider account upsert requires managed integration kind"))
	}
	input.ConnectionMode = strings.TrimSpace(input.ConnectionMode)
	input.ProviderTenantID = strings.TrimSpace(input.ProviderTenantID)
	input.ProviderAccountRef = strings.TrimSpace(input.ProviderAccountRef)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if !registryname.Valid(input.Provider) {
		return UpsertIntegrationInstallInput{}, errors.New(
			"provider must be a lowercase registry name",
		)
	}
	if input.Provider == IntegrationProviderSlack && input.ProviderTenantID == "" {
		return UpsertIntegrationInstallInput{}, storeerr.InvalidRequest(errors.New("slack installation requires a tenant"))
	}

	if input.State != IntegrationInstallStateActive && input.State != IntegrationInstallStateDisabled {
		return UpsertIntegrationInstallInput{}, fmt.Errorf("unsupported integration install state %q", input.State)
	}
	if input.IntegrationKind == "" || input.ConnectionMode == "" || input.ProviderAccountRef == "" {
		return UpsertIntegrationInstallInput{}, errors.New(
			"integration kind, connection mode, and provider account ref are required",
		)
	}
	if len(input.IntegrationKind) > 128 || len(input.ConnectionMode) > 128 ||
		len(input.ProviderTenantID) > 512 || len(input.ProviderAccountRef) > 512 ||
		len(input.DisplayName) > 512 {
		return UpsertIntegrationInstallInput{}, errors.New(
			"integration installation identifier exceeds its size limit",
		)
	}
	input.ProviderConfig, err = normalizedJSONObject(input.ProviderConfig, "provider_config")
	if err != nil {
		return UpsertIntegrationInstallInput{}, err
	}
	input.ProviderIdentity, err = normalizedJSONObject(input.ProviderIdentity, "provider_identity")
	if err != nil {
		return UpsertIntegrationInstallInput{}, err
	}
	input.Metadata, err = normalizedJSONObject(input.Metadata, "metadata")
	if err != nil {
		return UpsertIntegrationInstallInput{}, err
	}
	return input, nil
}

func validateIntegrationInstallApp(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input UpsertIntegrationInstallInput,
) (string, error) {

	app, err := qtx.LockIntegrationAppForInstallation(
		ctx,
		dbsqlc.LockIntegrationAppForInstallationParams{OrgID: input.OrgID, ID: input.IntegrationAppID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", storeerr.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load integration install app: %w", err)
	}
	if app.Provider != input.Provider ||
		(app.OwnerProjectID != nil && *app.OwnerProjectID != input.ProjectID) {
		return "", storeerr.ErrUnauthorized
	}
	if input.State == IntegrationInstallStateActive &&
		(app.State != string(IntegrationAppStateActive) || app.DeletedAt != nil) {
		return "", storeerr.ErrStateTransitionConflict
	}
	expectedKind := stringFromPtr(app.InstallationCredentialKind)
	if expectedKind == "" && input.CredentialSecretID != uuid.Nil {
		return "", errors.New("integration app does not accept installation credentials")
	}
	if expectedKind != "" && input.State == IntegrationInstallStateActive && input.CredentialSecretID == uuid.Nil {
		return "", errors.New("installation credential secret is required")
	}
	return expectedKind, nil
}

func validateIntegrationInstallCredential(
	ctx context.Context,
	tx pgx.Tx,
	input UpsertIntegrationInstallInput,
	expectedKind string,
) error {
	if input.CredentialSecretID == uuid.Nil {
		return nil
	}
	credential, err := secretops.LockReference(ctx, tx, input.OrgID, input.CredentialSecretID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("validate integration install credential: %w", err)
	}
	if credential.ManagementKind != management.Tenant ||
		credential.OwnerKind != secretstore.SecretOwnerProject ||
		credential.OwnerProjectID != input.ProjectID ||
		string(credential.Kind) != expectedKind {
		return storeerr.ErrNotFound
	}
	return nil
}

func ValidateProviderUserTenant(install IntegrationInstallRecord, providerTenantID string) error {
	if install.Provider == IntegrationProviderSlack &&
		(providerTenantID == "" || providerTenantID != install.ProviderTenantID) {
		return errors.New("slack actor tenant must match the integration install tenant")
	}
	return nil
}

func IdempotencyScope(install IntegrationInstallRecord) string {
	return integrationIdempotencyScope(install)
}

func updateIntegrationInstallTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	id uuid.UUID,
	input UpsertIntegrationInstallInput,
) (IntegrationInstallRecord, error) {
	installerUserID, installerKeyID := identitystore.AccountPrincipalIDs(input.InstalledBy)
	row, err := qtx.UpdateIntegrationInstall(ctx, dbsqlc.UpdateIntegrationInstallParams{
		ID:                     id,
		ProjectID:              input.ProjectID,
		InstalledByUserID:      installerUserID,
		InstalledByOrgApiKeyID: installerKeyID,
		ConnectionMode:         input.ConnectionMode,
		State:                  string(input.State),
		DisplayName:            input.DisplayName,
		CredentialSecretID:     storeutil.IDFromNil(input.CredentialSecretID),
		ProviderConfig:         input.ProviderConfig,
		ProviderIdentity:       input.ProviderIdentity,
		Metadata:               input.Metadata,
		LastOauthFlowID:        storeutil.IDFromNil(input.OAuthFlowID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && input.OAuthFlowID != uuid.Nil {
			return IntegrationInstallRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
		if storeutil.IsUniqueViolationOnConstraint(err, "integration_installs_last_oauth_flow_id_idx") {
			return IntegrationInstallRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
		if storeutil.IsUniqueViolation(err) {
			return IntegrationInstallRecord{}, storeerr.ErrConflict
		}
		return IntegrationInstallRecord{}, err
	}
	return integrationInstallRecordFromSQLC(row), nil
}

func integrationInstallRecordFromSQLC(row dbsqlc.IntegrationInstall) IntegrationInstallRecord {
	return IntegrationInstallRecord{
		ID:               row.ID,
		OrgID:            row.OrgID,
		ProjectID:        row.ProjectID,
		IntegrationAppID: storeutil.IDFromPtr(row.IntegrationAppID),
		InstalledBy: integrationInstallerPrincipal(
			row.OrgID, row.InstalledByUserID, row.InstalledByOrgApiKeyID,
		),
		Provider:              stringFromPtr(row.Provider),
		IntegrationKind:       IntegrationKind(row.IntegrationKind),
		ConnectionMode:        row.ConnectionMode,
		State:                 IntegrationInstallState(row.State),
		ProviderTenantID:      stringFromPtr(row.ProviderTenantID),
		ProviderAccountRef:    stringFromPtr(row.ProviderAccountRef),
		DisplayName:           row.DisplayName,
		CredentialSecretID:    storeutil.IDFromPtr(row.CredentialSecretID),
		ProviderConfig:        row.ProviderConfig,
		ProviderIdentity:      row.ProviderIdentity,
		Metadata:              row.Metadata,
		LastOAuthFlowID:       storeutil.IDFromPtr(row.LastOauthFlowID),
		ConfigurationRevision: row.ConfigurationRevision,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}

func integrationInstallRecordFromListSQLC(row dbsqlc.ListIntegrationInstallsForProjectRow) IntegrationInstallRecord {
	return IntegrationInstallRecord{
		ID:               row.ID,
		OrgID:            row.OrgID,
		ProjectID:        row.ProjectID,
		IntegrationAppID: storeutil.IDFromPtr(row.IntegrationAppID),
		InstalledBy: integrationInstallerPrincipal(
			row.OrgID, row.InstalledByUserID, row.InstalledByOrgApiKeyID,
		),
		Provider:              stringFromPtr(row.Provider),
		IntegrationKind:       IntegrationKind(row.IntegrationKind),
		ConnectionMode:        row.ConnectionMode,
		State:                 IntegrationInstallState(row.State),
		ProviderTenantID:      stringFromPtr(row.ProviderTenantID),
		ProviderAccountRef:    stringFromPtr(row.ProviderAccountRef),
		DisplayName:           row.DisplayName,
		CredentialSecretID:    storeutil.IDFromPtr(row.CredentialSecretID),
		ProviderConfig:        row.ProviderConfig,
		ProviderIdentity:      row.ProviderIdentity,
		Metadata:              row.Metadata,
		LastOAuthFlowID:       storeutil.IDFromPtr(row.LastOauthFlowID),
		ConfigurationRevision: row.ConfigurationRevision,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}
