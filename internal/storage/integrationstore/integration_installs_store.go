package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
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
	var err error
	input, err = normalizeUpsertIntegrationInstallInput(input)
	if err != nil {
		return IntegrationInstallRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationInstallRecord{}, fmt.Errorf("begin upsert integration install: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return IntegrationInstallRecord{}, err
	}
	if err := s.access.ValidateInstallBinding(
		ctx,
		tx,
		InstallBinding{
			OrgID:          input.OrgID,
			ProjectID:      input.ProjectID,
			AgentProfileID: input.AgentProfileID,
			AgentID:        input.AgentID,
		},
	); err != nil {
		return IntegrationInstallRecord{}, err
	}
	if err := validateIntegrationInstaller(
		ctx,
		qtx,
		input.OrgID,
		input.ProjectID,
		input.InstalledByUserID,
	); err != nil {
		return IntegrationInstallRecord{}, err
	}
	if err := validateIntegrationInstallCredential(ctx, qtx, input); err != nil {
		return IntegrationInstallRecord{}, err
	}

	row, err := qtx.InsertIntegrationInstall(ctx, dbsqlc.InsertIntegrationInstallParams{
		OrgID:                    input.OrgID,
		ProjectID:                input.ProjectID,
		AgentProfileID:           sqlcIDFromNil(input.AgentProfileID),
		AgentID:                  sqlcIDFromNil(input.AgentID),
		InstalledByUserID:        input.InstalledByUserID,
		Provider:                 input.Provider,
		IntegrationKind:          input.IntegrationKind,
		ConnectionMode:           input.ConnectionMode,
		State:                    string(input.State),
		ProviderTenantID:         input.ProviderTenantID,
		ProviderAccountRef:       input.ProviderAccountRef,
		ProviderAgentDisplayName: input.ProviderAgentDisplayName,
		CredentialSecretID:       sqlcIDFromNil(input.CredentialSecretID),
		ProviderConfig:           input.ProviderConfig,
		ProviderIdentity:         input.ProviderIdentity,
		ProviderMetadata:         input.ProviderMetadata,
		LastOauthFlowID:          sqlcIDFromNil(input.OAuthFlowID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, findErr := qtx.LockIntegrationInstallByProviderAccount(
			ctx,
			dbsqlc.LockIntegrationInstallByProviderAccountParams{
				Provider:           input.Provider,
				ProviderTenantID:   sqlcTextFromEmpty(input.ProviderTenantID),
				ProviderAccountRef: input.ProviderAccountRef,
			},
		)
		if findErr != nil {
			if errors.Is(findErr, pgx.ErrNoRows) {
				return IntegrationInstallRecord{}, storeerr.ErrConflict
			}
			return IntegrationInstallRecord{}, findErr
		}
		existingRecord := integrationInstallRecordFromSQLC(existing)
		if existingRecord.OrgID != input.OrgID || existingRecord.ProjectID != input.ProjectID ||
			existingRecord.AgentProfileID != input.AgentProfileID || existingRecord.AgentID != input.AgentID ||
			existingRecord.IntegrationKind != input.IntegrationKind {
			return IntegrationInstallRecord{}, storeerr.ErrConflict
		}
		record, updateErr := updateIntegrationInstallTx(ctx, qtx, existing.ID, input)
		if updateErr != nil {
			return IntegrationInstallRecord{}, fmt.Errorf("update integration install: %w", updateErr)
		}
		if err := tx.Commit(ctx); err != nil {
			return IntegrationInstallRecord{}, fmt.Errorf("commit update integration install: %w", err)
		}
		return record, nil
	}
	if err != nil {
		if storeutil.IsUniqueViolationOnConstraint(err, "integration_installs_last_oauth_flow_id_idx") {
			return IntegrationInstallRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
		if storeutil.IsUniqueViolation(err) {
			return IntegrationInstallRecord{}, storeerr.ErrConflict
		}
		return IntegrationInstallRecord{}, fmt.Errorf("insert integration install: %w", err)
	}
	record := integrationInstallRecordFromSQLC(row)
	record.Created = true
	if err := tx.Commit(ctx); err != nil {
		return IntegrationInstallRecord{}, fmt.Errorf("commit insert integration install: %w", err)
	}
	return record, nil
}

func (s *Store) IntegrationOAuthFlowConsumed(ctx context.Context, flowID ID) (bool, error) {
	if isNilID(flowID) {
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
	projectID, id ID,
) (IntegrationInstallRecord, error) {
	return getIntegrationInstall(ctx, s.q, projectID, id)
}

func getIntegrationInstall(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id ID,
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
	id ID,
) (IntegrationInstallRecord, error) {
	return getIntegrationInstallByID(ctx, s.q, id)
}

func (s *Store) GetIntegrationInstallByIDTx(
	ctx context.Context,
	tx pgx.Tx,
	id ID,
) (IntegrationInstallRecord, error) {
	return getIntegrationInstallByID(ctx, dbsqlc.New(tx), id)
}

func getIntegrationInstallByID(
	ctx context.Context,
	q *dbsqlc.Queries,
	id ID,
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

func (s *Store) GetIntegrationInstallByProviderAccount(
	ctx context.Context,
	provider, providerTenantID, providerAccountRef string,
) (IntegrationInstallRecord, error) {
	row, err := s.q.GetIntegrationInstallByProviderAccount(
		ctx,
		dbsqlc.GetIntegrationInstallByProviderAccountParams{
			Provider:           provider,
			ProviderTenantID:   sqlcTextFromEmpty(providerTenantID),
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
	ProjectID ID
	Filters   IntegrationInstallListFilters
	List      listing.Options
	Limit     int
}

type IntegrationInstallListFilters struct {
	AgentProfileID ID
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
	if isNilID(input.ProjectID) {
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
		AgentProfileID: sqlcIDFromNil(input.Filters.AgentProfileID),
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
	ProjectID           ID
	ID                  ID
	ExpectedOAuthFlowID *ID
}

func (s *Store) DisableIntegrationInstall(
	ctx context.Context,
	input DisableIntegrationInstallInput,
) (bool, error) {
	if isNilID(input.ProjectID) || isNilID(input.ID) || input.ExpectedOAuthFlowID == nil {
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
		idFromSQLCPtr(current.LastOauthFlowID) != expectedOAuthFlowID {
		return false, nil
	}
	rows, err := qtx.DisableIntegrationInstall(
		ctx,
		dbsqlc.DisableIntegrationInstallParams{
			ProjectID:           input.ProjectID,
			ID:                  input.ID,
			ExpectedOauthFlowID: sqlcIDFromNil(expectedOAuthFlowID),
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

func (s *Store) DeleteIntegrationInstall(ctx context.Context, projectID, id ID) error {
	if isNilID(projectID) || isNilID(id) {
		return errors.New("project and integration install are required")
	}
	_, err := storeutil.RetryTransaction(ctx, func() (struct{}, error) {
		return struct{}{}, s.deleteIntegrationInstallOnce(ctx, projectID, id)
	})
	return err
}

func (s *Store) deleteIntegrationInstallOnce(ctx context.Context, projectID, id ID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete integration install: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	install, err := getIntegrationInstall(ctx, q, projectID, id)
	if err != nil {
		return err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, install.OrgID, projectID); err != nil {
		return err
	}
	agentIDs, err := q.ListIntegrationInstallAgentIDsForLifecycle(
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
	if _, err := q.LockIntegrationInstallForMutation(
		ctx,
		dbsqlc.LockIntegrationInstallForMutationParams{ProjectID: projectID, ID: id},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock integration install for deletion: %w", err)
	}
	if _, err := getIntegrationInstall(ctx, q, projectID, id); err != nil {
		return err
	}
	if err := s.access.ClearInstallTargetsFromAgents(ctx, tx, projectID, id); err != nil {
		return err
	}
	if err := q.DeleteIntegrationTargets(ctx, dbsqlc.DeleteIntegrationTargetsParams{
		ProjectID: projectID, IntegrationInstallID: id,
	}); err != nil {
		return fmt.Errorf("delete integration targets: %w", err)
	}
	rows, err := q.DeleteIntegrationInstall(ctx, dbsqlc.DeleteIntegrationInstallParams{
		ProjectID: projectID, ID: id,
	})
	if err != nil {
		return fmt.Errorf("delete integration install: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
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
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.InstalledByUserID) {
		return UpsertIntegrationInstallInput{}, errors.New("org, project, and installed-by user are required")
	}
	if isNilID(input.AgentProfileID) == isNilID(input.AgentID) {
		return UpsertIntegrationInstallInput{}, errors.New("exactly one of agent profile and agent is required")
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.IntegrationKind = strings.TrimSpace(input.IntegrationKind)
	input.ConnectionMode = strings.TrimSpace(input.ConnectionMode)
	input.ProviderTenantID = strings.TrimSpace(input.ProviderTenantID)
	input.ProviderAccountRef = strings.TrimSpace(input.ProviderAccountRef)
	input.ProviderAgentDisplayName = strings.TrimSpace(input.ProviderAgentDisplayName)
	if input.Provider != IntegrationProviderSlack {
		return UpsertIntegrationInstallInput{}, fmt.Errorf("unsupported integration provider %q", input.Provider)
	}
	if input.ProviderTenantID == "" {
		return UpsertIntegrationInstallInput{}, errors.New("provider tenant id is required for slack integrations")
	}
	if isNilID(input.CredentialSecretID) {
		return UpsertIntegrationInstallInput{}, errors.New("credential secret is required for slack integrations")
	}
	if input.State != IntegrationInstallStateActive && input.State != IntegrationInstallStateDisabled {
		return UpsertIntegrationInstallInput{}, fmt.Errorf("unsupported integration install state %q", input.State)
	}
	if input.IntegrationKind == "" || input.ConnectionMode == "" || input.ProviderAccountRef == "" {
		return UpsertIntegrationInstallInput{}, errors.New(
			"integration kind, connection mode, and provider account ref are required",
		)
	}
	var err error
	input.ProviderConfig, err = normalizedJSONObject(input.ProviderConfig, "provider_config")
	if err != nil {
		return UpsertIntegrationInstallInput{}, err
	}
	input.ProviderIdentity, err = normalizedJSONObject(input.ProviderIdentity, "provider_identity")
	if err != nil {
		return UpsertIntegrationInstallInput{}, err
	}
	input.ProviderMetadata, err = normalizedJSONObject(input.ProviderMetadata, "provider_metadata")
	if err != nil {
		return UpsertIntegrationInstallInput{}, err
	}
	return input, nil
}

func validateIntegrationInstallCredential(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input UpsertIntegrationInstallInput,
) error {
	if isNilID(input.CredentialSecretID) {
		return nil
	}
	row, err := qtx.GetSecret(
		ctx,
		dbsqlc.GetSecretParams{OrgID: input.OrgID, ID: input.CredentialSecretID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("validate integration install credential: %w", err)
	}
	if management.Kind(row.ManagementKind) != management.Tenant ||
		row.OwnerKind != secretstore.SecretOwnerProject || row.OwnerProjectID == nil ||
		*row.OwnerProjectID != input.ProjectID ||
		row.Kind != string(secretstore.SecretKindSlackAppCredentials) {
		return storeerr.ErrNotFound
	}
	return nil
}

func validateIntegrationInstaller(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID, userID ID,
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
	id ID,
	input UpsertIntegrationInstallInput,
) (IntegrationInstallRecord, error) {
	row, err := qtx.UpdateIntegrationInstall(ctx, dbsqlc.UpdateIntegrationInstallParams{
		ID:                       id,
		ProjectID:                input.ProjectID,
		InstalledByUserID:        input.InstalledByUserID,
		ConnectionMode:           input.ConnectionMode,
		State:                    string(input.State),
		ProviderAgentDisplayName: input.ProviderAgentDisplayName,
		CredentialSecretID:       sqlcIDFromNil(input.CredentialSecretID),
		ProviderConfig:           input.ProviderConfig,
		ProviderIdentity:         input.ProviderIdentity,
		ProviderMetadata:         input.ProviderMetadata,
		LastOauthFlowID:          sqlcIDFromNil(input.OAuthFlowID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && !isNilID(input.OAuthFlowID) {
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
		ID:                       row.ID,
		OrgID:                    row.OrgID,
		ProjectID:                row.ProjectID,
		AgentProfileID:           idFromSQLCPtr(row.AgentProfileID),
		AgentID:                  idFromSQLCPtr(row.AgentID),
		InstalledByUserID:        row.InstalledByUserID,
		Provider:                 row.Provider,
		IntegrationKind:          row.IntegrationKind,
		ConnectionMode:           row.ConnectionMode,
		State:                    IntegrationInstallState(row.State),
		ProviderTenantID:         row.ProviderTenantID,
		ProviderAccountRef:       row.ProviderAccountRef,
		ProviderAgentDisplayName: row.ProviderAgentDisplayName,
		CredentialSecretID:       idFromSQLCPtr(row.CredentialSecretID),
		ProviderConfig:           row.ProviderConfig,
		ProviderIdentity:         row.ProviderIdentity,
		ProviderMetadata:         row.ProviderMetadata,
		LastOAuthFlowID:          idFromSQLCPtr(row.LastOauthFlowID),
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
}

func integrationInstallRecordFromListSQLC(row dbsqlc.ListIntegrationInstallsForProjectRow) IntegrationInstallRecord {
	return IntegrationInstallRecord{
		ID:                       row.ID,
		OrgID:                    row.OrgID,
		ProjectID:                row.ProjectID,
		AgentProfileID:           idFromSQLCPtr(row.AgentProfileID),
		AgentID:                  idFromSQLCPtr(row.AgentID),
		InstalledByUserID:        row.InstalledByUserID,
		Provider:                 row.Provider,
		IntegrationKind:          row.IntegrationKind,
		ConnectionMode:           row.ConnectionMode,
		State:                    IntegrationInstallState(row.State),
		ProviderTenantID:         row.ProviderTenantID,
		ProviderAccountRef:       row.ProviderAccountRef,
		ProviderAgentDisplayName: row.ProviderAgentDisplayName,
		CredentialSecretID:       idFromSQLCPtr(row.CredentialSecretID),
		ProviderConfig:           row.ProviderConfig,
		ProviderIdentity:         row.ProviderIdentity,
		ProviderMetadata:         row.ProviderMetadata,
		LastOAuthFlowID:          idFromSQLCPtr(row.LastOauthFlowID),
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
}
