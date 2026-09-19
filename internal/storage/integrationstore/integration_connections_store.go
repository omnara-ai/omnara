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
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateIntegrationConnection(
	ctx context.Context,
	input SaveIntegrationConnectionInput,
) (IntegrationConnectionRecord, error) {
	return s.saveIntegrationConnection(ctx, uuid.Nil, input, true)
}

func (s *Store) UpdateIntegrationConnection(
	ctx context.Context,
	id uuid.UUID,
	input SaveIntegrationConnectionInput,
) (IntegrationConnectionRecord, error) {
	if id == uuid.Nil {
		return IntegrationConnectionRecord{}, storeerr.InvalidRequest(errors.New("connection id is required"))
	}
	return s.saveIntegrationConnection(ctx, id, input, false)
}

// UpdateIntegrationConnectionWithVerifiedIdentity saves freshly verified GitHub
// or Discord observations. Provider I/O happens before this transaction; the
// connection revision and credential version fence stale verification results.
// It retains explicit-update deletion protection and all ordinary save gates.
func (s *Store) UpdateIntegrationConnectionWithVerifiedIdentity(
	ctx context.Context,
	id uuid.UUID,
	input SaveIntegrationConnectionInput,
) (IntegrationConnectionRecord, error) {
	if input.SourceVerifiedIdentityRevision.IsZero() ||
		input.CredentialVersionID == uuid.Nil || len(input.ProviderIdentity) == 0 ||
		(input.Provider != IntegrationProviderGitHub && input.Provider != IntegrationProviderDiscord) {
		return IntegrationConnectionRecord{}, storeerr.InvalidRequest(
			errors.New(
				"verified provider identity requires a hosted account, connection revision and credential version",
			),
		)
	}
	input.verifiedProviderIdentity = true
	return s.UpdateIntegrationConnection(ctx, id, input)
}

func (s *Store) saveIntegrationConnection(
	ctx context.Context,
	id uuid.UUID,
	input SaveIntegrationConnectionInput,
	createOnly bool,
) (IntegrationConnectionRecord, error) {
	var err error
	input, err = normalizeSaveIntegrationConnectionInput(input)
	if err != nil {
		return IntegrationConnectionRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationConnectionRecord{}, fmt.Errorf("begin save integration connection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	input, _, err = s.lockIntegrationConnectionSaveTx(ctx, tx, id, input, createOnly)
	if err != nil {
		return IntegrationConnectionRecord{}, err
	}
	record, err := s.writeIntegrationConnectionTx(ctx, tx, input)
	if err != nil {
		return IntegrationConnectionRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationConnectionRecord{}, fmt.Errorf("commit integration connection: %w", err)
	}
	return record, nil
}

func (s *Store) lockIntegrationConnectionSaveTx(
	ctx context.Context,
	tx pgx.Tx,
	id uuid.UUID,
	input SaveIntegrationConnectionInput,
	createOnly bool,
) (SaveIntegrationConnectionInput, uuid.UUID, error) {
	qtx := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return input, uuid.Nil, err
	}
	if id != uuid.Nil {
		current, err := getIntegrationConnection(ctx, qtx, input.ProjectID, id)
		if err != nil {
			return input, uuid.Nil, err
		}
		if current.Provider != input.Provider || current.ProviderTenantID != input.ProviderTenantID ||
			current.ProviderAccountRef != input.ProviderAccountRef {
			return input, uuid.Nil, storeerr.InvalidRequest(
				errors.New("connection provider account identity is immutable"),
			)
		}
	}
	if err := qtx.LockIntegrationConnectionAccount(ctx, dbsqlc.LockIntegrationConnectionAccountParams{
		Provider:         input.Provider,
		ProviderTenantID: input.ProviderTenantID, ProviderAccountRef: input.ProviderAccountRef,
	}); err != nil {
		return input, uuid.Nil, fmt.Errorf("lock provider account: %w", err)
	}
	current, findErr := qtx.GetIntegrationConnectionByProviderAccount(
		ctx, dbsqlc.GetIntegrationConnectionByProviderAccountParams{
			Provider:         input.Provider,
			ProviderTenantID: input.ProviderTenantID, ProviderAccountRef: input.ProviderAccountRef,
		},
	)
	if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
		return input, uuid.Nil, findErr
	}
	if findErr == nil {
		if createOnly {
			return input, uuid.Nil, storeerr.ErrConflict
		}
		if current.OrgID != input.OrgID || current.ProjectID != input.ProjectID {
			return input, uuid.Nil, storeerr.ErrConflict
		}
		if id != uuid.Nil && current.ID != id {
			return input, uuid.Nil, storeerr.ErrNotFound
		}
		// Rotation and state changes serialize against admitted work, before any
		// secret locks. A reconnect must not bypass revocation.
		if err := qtx.LockIntegrationConnectionLifecycleExclusive(
			ctx,
			dbsqlc.LockIntegrationConnectionLifecycleExclusiveParams{ConnectionID: current.ID},
		); err != nil {
			return input, uuid.Nil, err
		}
	}
	// A deletion can commit while the exclusive gate is waiting. Account
	// creation is still serialized, so resolve its live identity again before
	// returning the setup identity or deciding to update an existing row.
	current, findErr = qtx.GetIntegrationConnectionByProviderAccount(
		ctx, dbsqlc.GetIntegrationConnectionByProviderAccountParams{
			Provider:         input.Provider,
			ProviderTenantID: input.ProviderTenantID, ProviderAccountRef: input.ProviderAccountRef,
		},
	)
	if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
		return input, uuid.Nil, findErr
	}

	// Provider refreshes may omit a display name they could not observe. Keep
	// the latest observation under the gate. Explicit account PUT replaces it,
	// including an intentionally empty string.
	if id == uuid.Nil && findErr == nil && input.ProviderAgentDisplayName == "" {
		input.ProviderAgentDisplayName = current.ProviderAgentDisplayName
	}

	if id != uuid.Nil {
		// Deletion also takes the connection gate. Re-read after waiting so an
		// update cannot recreate a connection that was deleted concurrently.
		current, err := getIntegrationConnection(ctx, qtx, input.ProjectID, id)
		if err != nil {
			return input, uuid.Nil, err
		}
		// Only OAuth setup may rebind Slack credentials. Check the latest binding
		// under the lifecycle gate so a stale PUT cannot undo a reconnect.
		if current.Provider == IntegrationProviderSlack && input.CredentialSecretID != current.CredentialSecretID {
			return input, uuid.Nil, storeerr.InvalidRequest(errors.New("change Slack credentials through OAuth setup"))
		}
		if !input.verifiedProviderIdentity {
			// Ordinary account management preserves observations, including an
			// OAuth refresh that completed while this save waited.
			input.ProviderIdentity, input.ProviderMetadata = current.ProviderIdentity, current.ProviderMetadata
		} else if !current.UpdatedAt.Equal(input.SourceVerifiedIdentityRevision) {
			return input, uuid.Nil, storeerr.ErrConflict
		}
	}
	if err := validateConnectionInstaller(
		ctx,
		qtx,
		input.OrgID,
		input.ProjectID,
		input.InstalledByUserID,
	); err != nil {
		return input, uuid.Nil, err
	}
	return input, current.ID, nil
}

// writeIntegrationConnectionTx requires lockIntegrationConnectionSaveTx first.
// Atomic app setup also locks destinations before entering this secret/row phase.
func (s *Store) writeIntegrationConnectionTx(
	ctx context.Context,
	tx pgx.Tx,
	input SaveIntegrationConnectionInput,
) (IntegrationConnectionRecord, error) {
	qtx := dbsqlc.New(tx)
	if err := validateIntegrationConnectionCredential(ctx, tx, input); err != nil {
		return IntegrationConnectionRecord{}, err
	}

	row, err := qtx.InsertIntegrationConnection(ctx, dbsqlc.InsertIntegrationConnectionParams{
		OrgID:                    input.OrgID,
		ProjectID:                input.ProjectID,
		InstalledByUserID:        input.InstalledByUserID,
		Provider:                 input.Provider,
		State:                    string(input.State),
		ProviderTenantID:         input.ProviderTenantID,
		ProviderAccountRef:       input.ProviderAccountRef,
		ProviderAgentDisplayName: input.ProviderAgentDisplayName,
		CredentialSecretID:       storeutil.IDFromNil(input.CredentialSecretID),
		ProviderConfig:           input.ProviderConfig,
		ProviderIdentity:         input.ProviderIdentity,
		ProviderMetadata:         input.ProviderMetadata,
		LastOauthFlowID:          storeutil.IDFromNil(input.OAuthFlowID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, findErr := qtx.LockIntegrationConnectionByProviderAccount(
			ctx,
			dbsqlc.LockIntegrationConnectionByProviderAccountParams{
				Provider:           input.Provider,
				ProviderTenantID:   input.ProviderTenantID,
				ProviderAccountRef: input.ProviderAccountRef,
			},
		)
		if findErr != nil {
			if errors.Is(findErr, pgx.ErrNoRows) {
				// INSERT can conflict on global provider-account uniqueness or
				// OAuth replay. With no live account row, an OAuth flow may
				// already be consumed by a deleted connection.
				if input.OAuthFlowID != uuid.Nil {
					return IntegrationConnectionRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
				}
				return IntegrationConnectionRecord{}, storeerr.ErrConflict
			}
			return IntegrationConnectionRecord{}, findErr
		}
		existingRecord := integrationConnectionRecordFromSQLC(existing)
		if existingRecord.OrgID != input.OrgID || existingRecord.ProjectID != input.ProjectID {
			return IntegrationConnectionRecord{}, storeerr.ErrConflict
		}
		record, updateErr := updateIntegrationConnectionTx(ctx, qtx, existing.ID, input)
		if updateErr != nil {
			return IntegrationConnectionRecord{}, fmt.Errorf("update integration connection: %w", updateErr)
		}
		return record, nil
	}
	if err != nil {
		if storeutil.IsUniqueViolationOnConstraint(err, "integration_connections_last_oauth_flow_id_idx") {
			return IntegrationConnectionRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
		if storeutil.IsUniqueViolation(err) {
			return IntegrationConnectionRecord{}, storeerr.ErrConflict
		}
		return IntegrationConnectionRecord{}, fmt.Errorf("insert integration connection: %w", err)
	}
	record := integrationConnectionRecordFromSQLC(row)
	record.Created = true
	if err := resourceguard.Lock(ctx, qtx, "integration_connections", input.ProjectID.String()); err != nil {
		return IntegrationConnectionRecord{}, err
	}
	limits, err := resourceguard.ResolveLimits(ctx, qtx, input.OrgID)
	if err != nil {
		return IntegrationConnectionRecord{}, err
	}
	count, err := qtx.CountActiveIntegrationConnectionsForProject(
		ctx,
		dbsqlc.CountActiveIntegrationConnectionsForProjectParams{ProjectID: input.ProjectID},
	)
	if err != nil {
		return IntegrationConnectionRecord{}, err
	}
	if count > limits.MaxActiveIntegrationConnectionsPerProject {
		return IntegrationConnectionRecord{}, fmt.Errorf(
			"project connection limit of %d reached: %w",
			limits.MaxActiveIntegrationConnectionsPerProject,
			storeerr.ErrConflict,
		)
	}
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

func (s *Store) GetIntegrationConnection(
	ctx context.Context,
	projectID, id uuid.UUID,
) (IntegrationConnectionRecord, error) {
	return getIntegrationConnection(ctx, s.q, projectID, id)
}

func getIntegrationConnection(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id uuid.UUID,
) (IntegrationConnectionRecord, error) {
	row, err := q.GetIntegrationConnection(
		ctx,
		dbsqlc.GetIntegrationConnectionParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationConnectionRecord{}, storeerr.ErrNotFound
		}
		return IntegrationConnectionRecord{}, fmt.Errorf("get integration connection: %w", err)
	}
	return integrationConnectionRecordFromSQLC(row), nil
}

func (s *Store) GetIntegrationConnectionByID(
	ctx context.Context,
	id uuid.UUID,
) (IntegrationConnectionRecord, error) {
	return getIntegrationConnectionByID(ctx, s.q, id)
}

func (s *Store) GetIntegrationConnectionByIDTx(
	ctx context.Context,
	tx pgx.Tx,
	id uuid.UUID,
) (IntegrationConnectionRecord, error) {
	return getIntegrationConnectionByID(ctx, dbsqlc.New(tx), id)
}

func getIntegrationConnectionByID(
	ctx context.Context,
	q *dbsqlc.Queries,
	id uuid.UUID,
) (IntegrationConnectionRecord, error) {
	row, err := q.GetIntegrationConnectionByID(ctx, dbsqlc.GetIntegrationConnectionByIDParams{ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationConnectionRecord{}, storeerr.ErrNotFound
		}
		return IntegrationConnectionRecord{}, fmt.Errorf("get integration connection by id: %w", err)
	}
	return integrationConnectionRecordFromSQLC(row), nil
}

// GetIntegrationConnectionByProviderAccount resolves provider callback identity globally.
func (s *Store) GetIntegrationConnectionByProviderAccount(
	ctx context.Context,
	provider, providerTenantID, providerAccountRef string,
) (IntegrationConnectionRecord, error) {
	row, err := s.q.GetIntegrationConnectionByProviderAccount(
		ctx,
		dbsqlc.GetIntegrationConnectionByProviderAccountParams{
			Provider:           provider,
			ProviderTenantID:   providerTenantID,
			ProviderAccountRef: providerAccountRef,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationConnectionRecord{}, storeerr.ErrNotFound
		}
		return IntegrationConnectionRecord{}, fmt.Errorf("get integration connection by provider account: %w", err)
	}
	return integrationConnectionRecordFromSQLC(row), nil
}

type ListIntegrationConnectionsForProjectInput struct {
	ProjectID uuid.UUID
	Filters   IntegrationConnectionListFilters
	List      listing.Options
	Limit     int
}

type IntegrationConnectionListFilters struct {
	OAuthFlowID uuid.UUID
}

type ListIntegrationConnectionsForProjectResult struct {
	Connections []IntegrationConnectionRecord
	HasMore     bool
	Next        listing.Cursor
}

func (s *Store) ListIntegrationConnectionsForProject(
	ctx context.Context,
	input ListIntegrationConnectionsForProjectInput,
) (ListIntegrationConnectionsForProjectResult, error) {
	if input.ProjectID == uuid.Nil {
		return ListIntegrationConnectionsForProjectResult{}, errors.New("project id is required")
	}
	if input.Limit <= 0 {
		return ListIntegrationConnectionsForProjectResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at") {
		return ListIntegrationConnectionsForProjectResult{}, errors.New("unsupported integration connection list sort")
	}
	rows, err := s.q.ListIntegrationConnectionsForProject(ctx, dbsqlc.ListIntegrationConnectionsForProjectParams{
		ProjectID: input.ProjectID, RowLimit: int32(input.Limit) + 1,
		NamePattern: input.List.NamePattern, SortField: input.List.SortField,
		SortDesc: input.List.SortDesc, CursorSet: input.List.After.Set,
		CursorKey: input.List.After.Key, CursorID: input.List.After.ID,
		OauthFlowID: storeutil.IDFromNil(input.Filters.OAuthFlowID),
	})
	if err != nil {
		return ListIntegrationConnectionsForProjectResult{}, fmt.Errorf("list integration connections: %w", err)
	}
	result := ListIntegrationConnectionsForProjectResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	if result.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.Next = listing.Cursor{Set: true, Key: last.SortKey, ID: last.ID}
	}
	result.Connections = make([]IntegrationConnectionRecord, 0, len(rows))
	for _, row := range rows {
		result.Connections = append(result.Connections, integrationConnectionRecordFromListSQLC(row))
	}
	return result, nil
}

type DisableIntegrationConnectionInput struct {
	ProjectID           uuid.UUID
	ID                  uuid.UUID
	ExpectedOAuthFlowID *uuid.UUID
}

func (s *Store) DisableIntegrationConnection(
	ctx context.Context,
	input DisableIntegrationConnectionInput,
) (bool, error) {
	if input.ProjectID == uuid.Nil || input.ID == uuid.Nil || input.ExpectedOAuthFlowID == nil {
		return false, errors.New("project, integration connection, and expected OAuth flow are required")
	}
	expectedOAuthFlowID := *input.ExpectedOAuthFlowID
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin disable integration connection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	connection, err := getIntegrationConnection(ctx, qtx, input.ProjectID, input.ID)
	if err != nil {
		return false, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, connection.OrgID, input.ProjectID); err != nil {
		return false, err
	}
	if err := qtx.LockIntegrationConnectionLifecycleExclusive(
		ctx,
		dbsqlc.LockIntegrationConnectionLifecycleExclusiveParams{ConnectionID: input.ID},
	); err != nil {
		return false, fmt.Errorf("lock integration connection lifecycle for disable: %w", err)
	}
	current, err := qtx.LockIntegrationConnectionForDisable(
		ctx,
		dbsqlc.LockIntegrationConnectionForDisableParams{
			ProjectID: input.ProjectID,
			ID:        input.ID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, storeerr.ErrNotFound
		}
		return false, fmt.Errorf("lock integration connection for disable: %w", err)
	}
	if IntegrationConnectionState(current.State) != IntegrationConnectionStateActive ||
		storeutil.IDFromPtr(current.LastOauthFlowID) != expectedOAuthFlowID {
		return false, nil
	}
	rows, err := qtx.DisableIntegrationConnection(
		ctx,
		dbsqlc.DisableIntegrationConnectionParams{
			ProjectID:           input.ProjectID,
			ID:                  input.ID,
			ExpectedOauthFlowID: storeutil.IDFromNil(expectedOAuthFlowID),
		},
	)
	if err != nil {
		return false, fmt.Errorf("disable integration connection: %w", err)
	}
	if rows != 1 {
		return false, storeerr.ErrStateTransitionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit disable integration connection: %w", err)
	}
	return true, nil
}

func (s *Store) DeleteIntegrationConnection(ctx context.Context, projectID, id uuid.UUID) error {
	if projectID == uuid.Nil || id == uuid.Nil {
		return errors.New("project and integration connection are required")
	}
	_, err := storeutil.RetryTransaction(ctx, "delete_integration_connection", func() (struct{}, error) {
		return struct{}{}, s.deleteIntegrationConnectionOnce(ctx, projectID, id)
	})
	return err
}

func (s *Store) deleteIntegrationConnectionOnce(ctx context.Context, projectID, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete integration connection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	install, err := getIntegrationConnection(ctx, q, projectID, id)
	if err != nil {
		return err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, install.OrgID, projectID); err != nil {
		return err
	}
	// Freeze target admission before enumerating the agents that deletion will lock.
	if err := q.LockIntegrationConnectionLifecycleExclusive(
		ctx,
		dbsqlc.LockIntegrationConnectionLifecycleExclusiveParams{ConnectionID: id},
	); err != nil {
		return fmt.Errorf("lock integration connection lifecycle for deletion: %w", err)
	}
	agentIDs, err := q.ListIntegrationConnectionAgentIDsForLifecycle(
		ctx,
		dbsqlc.ListIntegrationConnectionAgentIDsForLifecycleParams{
			ProjectID:               projectID,
			IntegrationConnectionID: id,
		},
	)
	if err != nil {
		return fmt.Errorf("list integration connection agents for lifecycle: %w", err)
	}
	agentRefs := make([]lifecyclelock.AgentRef, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		agentRefs = append(agentRefs, lifecyclelock.AgentRef{ProjectID: projectID, AgentID: agentID})
	}
	if err := lifecyclelock.Agents(ctx, tx, agentRefs); err != nil {
		return err
	}
	if _, err := q.LockIntegrationConnectionForMutation(
		ctx,
		dbsqlc.LockIntegrationConnectionForMutationParams{ProjectID: projectID, ID: id},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock integration connection for deletion: %w", err)
	}
	if err := s.access.ClearConnectionTargetsFromAgents(ctx, tx, projectID, id); err != nil {
		return err
	}
	if err := q.DeleteIntegrationTargets(ctx, dbsqlc.DeleteIntegrationTargetsParams{
		ProjectID: projectID, IntegrationConnectionID: id,
	}); err != nil {
		return fmt.Errorf("delete integration targets: %w", err)
	}
	rows, err := q.DeleteIntegrationConnection(ctx, dbsqlc.DeleteIntegrationConnectionParams{
		ProjectID: projectID, ID: id,
	})
	if err != nil {
		return fmt.Errorf("delete integration connection: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete integration connection: %w", err)
	}
	return nil
}

func integrationIdempotencyScope(install IntegrationConnectionRecord) string {
	return "integration:" + install.Provider + ":" + install.ID.String()
}

func validateIntegrationConnectionCredential(
	ctx context.Context,
	tx pgx.Tx,
	input SaveIntegrationConnectionInput,
) error {
	_, err := secretops.LockReference(ctx, tx, input.OrgID, input.CredentialSecretID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("validate integration connection credential: %w", err)
	}
	credential, err := dbsqlc.New(tx).GetProjectAvailableSecret(ctx, dbsqlc.GetProjectAvailableSecretParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, SecretID: input.CredentialSecretID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize connection credential: %w", err)
	}
	wantKind, err := IntegrationConnectionCredentialKind(input.Provider)
	if err != nil {
		return storeerr.InvalidRequest(err)
	}
	if credential.Kind != string(wantKind) {
		return storeerr.InvalidRequest(fmt.Errorf("connection requires a %s secret", wantKind))
	}
	if input.CredentialVersionID != uuid.Nil &&
		storeutil.IDFromPtr(credential.CurrentVersionID) != input.CredentialVersionID {
		return fmt.Errorf("connection credential changed during validation: %w", storeerr.ErrConflict)
	}
	return nil
}

func validateConnectionInstaller(
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
		return fmt.Errorf("validate connection installer: %w", err)
	}
	if identitystore.ProjectRolesAllow(roles, identitystore.ProjectActionManage) {
		return nil
	}
	return storeerr.ErrUnauthorized
}

func IdempotencyScope(install IntegrationConnectionRecord) string {
	return integrationIdempotencyScope(install)
}

func updateIntegrationConnectionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	id uuid.UUID,
	input SaveIntegrationConnectionInput,
) (IntegrationConnectionRecord, error) {
	row, err := qtx.UpdateIntegrationConnection(ctx, dbsqlc.UpdateIntegrationConnectionParams{
		ID:                       id,
		ProjectID:                input.ProjectID,
		InstalledByUserID:        input.InstalledByUserID,
		State:                    string(input.State),
		ProviderAgentDisplayName: input.ProviderAgentDisplayName,
		CredentialSecretID:       storeutil.IDFromNil(input.CredentialSecretID),
		ProviderConfig:           input.ProviderConfig,
		ProviderIdentity:         input.ProviderIdentity,
		ProviderMetadata:         input.ProviderMetadata,
		LastOauthFlowID:          storeutil.IDFromNil(input.OAuthFlowID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && input.OAuthFlowID != uuid.Nil {
			return IntegrationConnectionRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
		if storeutil.IsUniqueViolationOnConstraint(err, "integration_connections_last_oauth_flow_id_idx") {
			return IntegrationConnectionRecord{}, storeerr.ErrIntegrationOAuthFlowConsumed
		}
		if storeutil.IsUniqueViolation(err) {
			return IntegrationConnectionRecord{}, storeerr.ErrConflict
		}
		return IntegrationConnectionRecord{}, err
	}
	return integrationConnectionRecordFromSQLC(row), nil
}

func integrationConnectionRecordFromSQLC(row dbsqlc.IntegrationConnection) IntegrationConnectionRecord {
	return IntegrationConnectionRecord{
		ID:                       row.ID,
		OrgID:                    row.OrgID,
		ProjectID:                row.ProjectID,
		InstalledByUserID:        row.InstalledByUserID,
		Provider:                 row.Provider,
		State:                    IntegrationConnectionState(row.State),
		ProviderTenantID:         row.ProviderTenantID,
		ProviderAccountRef:       row.ProviderAccountRef,
		ProviderAgentDisplayName: row.ProviderAgentDisplayName,
		CredentialSecretID:       storeutil.IDFromPtr(row.CredentialSecretID),
		ProviderConfig:           row.ProviderConfig,
		ProviderIdentity:         row.ProviderIdentity,
		ProviderMetadata:         row.ProviderMetadata,
		LastOAuthFlowID:          storeutil.IDFromPtr(row.LastOauthFlowID),
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
}

func integrationConnectionRecordFromListSQLC(
	row dbsqlc.ListIntegrationConnectionsForProjectRow,
) IntegrationConnectionRecord {
	return IntegrationConnectionRecord{
		ID:                       row.ID,
		OrgID:                    row.OrgID,
		ProjectID:                row.ProjectID,
		InstalledByUserID:        row.InstalledByUserID,
		Provider:                 row.Provider,
		State:                    IntegrationConnectionState(row.State),
		ProviderTenantID:         row.ProviderTenantID,
		ProviderAccountRef:       row.ProviderAccountRef,
		ProviderAgentDisplayName: row.ProviderAgentDisplayName,
		CredentialSecretID:       storeutil.IDFromPtr(row.CredentialSecretID),
		ProviderConfig:           row.ProviderConfig,
		ProviderIdentity:         row.ProviderIdentity,
		ProviderMetadata:         row.ProviderMetadata,
		LastOAuthFlowID:          storeutil.IDFromPtr(row.LastOauthFlowID),
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
}
