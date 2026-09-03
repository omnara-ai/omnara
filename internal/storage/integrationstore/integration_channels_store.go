package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/registryname"
	secretspkg "github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateIntegrationApp(
	ctx context.Context,
	input CreateIntegrationAppInput,
) (IntegrationAppRecord, error) {
	var err error
	input, err = normalizeCreateIntegrationAppInput(input)
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationAppRecord{}, fmt.Errorf("begin create integration app: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if !isNilID(input.OwnerProjectID) {
		if err := lockProjectLifecycleShared(ctx, qtx, input.OwnerProjectID); err != nil {
			return IntegrationAppRecord{}, err
		}
	}
	if _, err := qtx.LockOrganizationLifecycleShared(
		ctx,
		dbsqlc.LockOrganizationLifecycleSharedParams{OrgID: input.OrgID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationAppRecord{}, storeerr.ErrNotFound
		}
		return IntegrationAppRecord{}, fmt.Errorf("lock integration app organization owner: %w", err)
	}
	if !isNilID(input.OwnerProjectID) {
		if _, err := qtx.LockIntegrationAppProjectOwner(
			ctx,
			dbsqlc.LockIntegrationAppProjectOwnerParams{
				OrgID: input.OrgID, OwnerProjectID: input.OwnerProjectID,
			},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return IntegrationAppRecord{}, storeerr.ErrNotFound
			}
			return IntegrationAppRecord{}, fmt.Errorf("lock integration app project owner: %w", err)
		}
	}
	row, err := qtx.InsertIntegrationApp(ctx, dbsqlc.InsertIntegrationAppParams{
		OrgID:                      input.OrgID,
		OwnerProjectID:             sqlcIDFromNil(input.OwnerProjectID),
		Provider:                   input.Provider,
		ProviderAppRef:             input.ProviderAppRef,
		DisplayName:                input.DisplayName,
		ConnectorKey:               input.ConnectorKey,
		CredentialSecretID:         sqlcIDFromNil(input.CredentialSecretID),
		InstallationCredentialKind: sqlcTextFromEmpty(input.InstallationCredentialKind),
		ProviderConfig:             input.ProviderConfig,
		ProviderMetadata:           input.ProviderMetadata,
		State:                      string(input.State),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationAppRecord{}, storeerr.ErrNotFound
		}
		if storeutil.IsUniqueViolation(err) {
			return IntegrationAppRecord{}, storeerr.ErrConflict
		}
		return IntegrationAppRecord{}, integrationChannelWriteError("create integration app", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationAppRecord{}, fmt.Errorf("commit create integration app: %w", err)
	}
	return integrationAppRecordFromSQLC(row), nil
}

func (s *Store) GetIntegrationApp(ctx context.Context, orgID, id ID) (IntegrationAppRecord, error) {
	if isNilID(orgID) || isNilID(id) {
		return IntegrationAppRecord{}, errors.New("org and integration app are required")
	}
	row, err := s.q.GetIntegrationApp(ctx, dbsqlc.GetIntegrationAppParams{OrgID: orgID, ID: id})
	if err != nil {
		return IntegrationAppRecord{}, integrationChannelReadError("get integration app", err)
	}
	return integrationAppRecordFromSQLC(row), nil
}

func (s *Store) GetConnectorIntegrationApp(
	ctx context.Context,
	id ID,
	capabilities []channelconnector.Capability,
) (IntegrationAppRecord, error) {
	if isNilID(id) {
		return IntegrationAppRecord{}, errors.New("integration app is required")
	}
	connectorKeys, providers, err := normalizedCapabilityColumns(capabilities)
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	row, err := s.q.GetConnectorIntegrationApp(ctx, dbsqlc.GetConnectorIntegrationAppParams{
		ID: id, ConnectorKeys: connectorKeys, Providers: providers,
	})
	if err != nil {
		return IntegrationAppRecord{}, integrationChannelReadError("get connector integration app", err)
	}
	return integrationAppRecordFromSQLC(row), nil
}

func (s *Store) GetConnectorIntegrationInstall(
	ctx context.Context,
	integrationAppID ID,
	providerTenantID, providerAccountRef string,
) (IntegrationInstallRecord, error) {
	providerTenantID = strings.TrimSpace(providerTenantID)
	providerAccountRef = strings.TrimSpace(providerAccountRef)
	if isNilID(integrationAppID) || providerAccountRef == "" {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(errors.New(
			"integration app and provider account ref are required",
		))
	}
	if len(providerTenantID) > 512 || len(providerAccountRef) > 512 {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(errors.New(
			"provider tenant or account ref exceeds its size limit",
		))
	}
	if err := dbsafe.Text(providerTenantID); err != nil {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(
			fmt.Errorf("provider tenant id: %w", err),
		)
	}
	if err := dbsafe.Text(providerAccountRef); err != nil {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(
			fmt.Errorf("provider account ref: %w", err),
		)
	}
	row, err := s.q.GetConnectorIntegrationInstall(ctx, dbsqlc.GetConnectorIntegrationInstallParams{
		IntegrationAppID:   integrationAppID,
		ProviderTenantID:   providerTenantID,
		ProviderAccountRef: providerAccountRef,
	})
	if err != nil {
		return IntegrationInstallRecord{}, integrationChannelReadError(
			"get connector integration install", err,
		)
	}
	return integrationInstallRecordFromConnectorFields(
		row.ID, row.OrgID, row.ProjectID, row.IntegrationAppID,
		row.AgentProfileID, row.AgentID, row.InstalledByUserID,
		row.Provider, row.IntegrationKind, row.ConnectionMode, row.State,
		row.ProviderTenantID, row.ProviderAccountRef, row.ProviderAgentDisplayName,
		row.CredentialSecretID, row.ProviderConfig, row.ProviderIdentity,
		row.ProviderMetadata, row.LastOauthFlowID, row.CreatedAt, row.UpdatedAt,
		row.ConfigurationRevision,
	), nil
}

func (s *Store) GetConnectorIntegrationInstallByID(
	ctx context.Context,
	integrationAppID, id ID,
) (IntegrationInstallRecord, error) {
	if isNilID(integrationAppID) || isNilID(id) {
		return IntegrationInstallRecord{}, errors.New("integration app and installation are required")
	}
	row, err := s.q.GetConnectorIntegrationInstallByID(
		ctx,
		dbsqlc.GetConnectorIntegrationInstallByIDParams{IntegrationAppID: integrationAppID, ID: id},
	)
	if err != nil {
		return IntegrationInstallRecord{}, integrationChannelReadError(
			"get connector integration install by id", err,
		)
	}
	return integrationInstallRecordFromConnectorFields(
		row.ID, row.OrgID, row.ProjectID, row.IntegrationAppID,
		row.AgentProfileID, row.AgentID, row.InstalledByUserID,
		row.Provider, row.IntegrationKind, row.ConnectionMode, row.State,
		row.ProviderTenantID, row.ProviderAccountRef, row.ProviderAgentDisplayName,
		row.CredentialSecretID, row.ProviderConfig, row.ProviderIdentity,
		row.ProviderMetadata, row.LastOauthFlowID, row.CreatedAt, row.UpdatedAt,
		row.ConfigurationRevision,
	), nil
}

func integrationInstallRecordFromConnectorFields(
	id, orgID, projectID, appID ID,
	agentProfileID, agentID *ID,
	installedByUserID ID,
	provider, integrationKind, connectionMode, state,
	providerTenantID, providerAccountRef, providerAgentDisplayName string,
	credentialSecretID *ID,
	providerConfig, providerIdentity, providerMetadata []byte,
	lastOAuthFlowID *ID,
	createdAt, updatedAt time.Time,
	configurationRevision int64,
) IntegrationInstallRecord {
	return IntegrationInstallRecord{
		ID:                       id,
		OrgID:                    orgID,
		ProjectID:                projectID,
		IntegrationAppID:         appID,
		AgentProfileID:           idFromSQLCPtr(agentProfileID),
		AgentID:                  idFromSQLCPtr(agentID),
		InstalledByUserID:        installedByUserID,
		Provider:                 provider,
		IntegrationKind:          integrationKind,
		ConnectionMode:           connectionMode,
		State:                    IntegrationInstallState(state),
		ProviderTenantID:         providerTenantID,
		ProviderAccountRef:       providerAccountRef,
		ProviderAgentDisplayName: providerAgentDisplayName,
		CredentialSecretID:       idFromSQLCPtr(credentialSecretID),
		ProviderConfig:           providerConfig,
		ProviderIdentity:         providerIdentity,
		ProviderMetadata:         providerMetadata,
		LastOAuthFlowID:          idFromSQLCPtr(lastOAuthFlowID),
		ConfigurationRevision:    configurationRevision,
		CreatedAt:                createdAt,
		UpdatedAt:                updatedAt,
	}
}

func (s *Store) CreateIntegrationRoute(
	ctx context.Context,
	input CreateIntegrationRouteInput,
) (IntegrationRouteRecord, error) {
	var err error
	input, err = normalizeCreateIntegrationRouteInput(input)
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationRouteRecord{}, fmt.Errorf("begin create integration route: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockIntegrationInstallForRouteMutation(
		ctx,
		dbsqlc.LockIntegrationInstallForRouteMutationParams{
			ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
		},
	); err != nil {
		return IntegrationRouteRecord{}, integrationChannelReadError(
			"lock integration installation for route create",
			err,
		)
	}
	row, err := qtx.InsertIntegrationRoute(ctx, dbsqlc.InsertIntegrationRouteParams{
		ProjectID:            input.ProjectID,
		IntegrationInstallID: input.IntegrationInstallID,
		DeploymentKey:        input.DeploymentKey,
		HandlerKey:           input.HandlerKey,
		HandlerVersion:       int32(input.HandlerVersion),
		Configuration:        input.Configuration,
		State:                string(input.State),
		MaxActiveRoutes:      MaxActiveIntegrationRoutesPerInstall,
	})
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return IntegrationRouteRecord{}, fmt.Errorf("commit create integration route: %w", err)
		}
		return integrationRouteRecordFromSQLC(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if storeutil.IsUniqueViolation(err) {
			return IntegrationRouteRecord{}, storeerr.ErrConflict
		}
		return IntegrationRouteRecord{}, integrationChannelWriteError("create integration route", err)
	}
	existing, err := qtx.GetIntegrationRouteByDeploymentKey(
		ctx,
		dbsqlc.GetIntegrationRouteByDeploymentKeyParams{
			ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
			DeploymentKey: input.DeploymentKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRouteRecord{}, storeerr.InvalidRequest(fmt.Errorf(
			"integration installation reached the %d active-route limit",
			MaxActiveIntegrationRoutesPerInstall,
		))
	}
	if err != nil {
		return IntegrationRouteRecord{}, integrationChannelReadError(
			"load integration route create replay",
			err,
		)
	}
	if existing.DeletedAt != nil || !integrationRouteDefinitionMatches(existing, input) {
		return IntegrationRouteRecord{}, storeerr.ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationRouteRecord{}, fmt.Errorf("commit integration route create replay: %w", err)
	}
	return integrationRouteRecordFromSQLC(existing), nil
}

func integrationRouteDefinitionMatches(
	record dbsqlc.IntegrationRoute,
	input CreateIntegrationRouteInput,
) bool {
	return record.ProjectID == input.ProjectID &&
		record.IntegrationInstallID == input.IntegrationInstallID &&
		record.DeploymentKey == input.DeploymentKey && record.HandlerKey == input.HandlerKey &&
		int(record.HandlerVersion) == input.HandlerVersion &&
		storeutil.SameJSON(record.Configuration, input.Configuration)
}

func (s *Store) ListActiveIntegrationRoutes(
	ctx context.Context,
	projectID, integrationInstallID ID,
) ([]IntegrationRouteRecord, error) {
	if isNilID(projectID) || isNilID(integrationInstallID) {
		return nil, errors.New("project and integration install are required")
	}
	rows, err := s.q.ListActiveIntegrationRoutes(ctx, dbsqlc.ListActiveIntegrationRoutesParams{
		ProjectID: projectID, IntegrationInstallID: integrationInstallID,
		RowLimit: MaxActiveIntegrationRoutesPerInstall + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("list active integration routes: %w", err)
	}
	if len(rows) > MaxActiveIntegrationRoutesPerInstall {
		return nil, storeerr.InvalidRequest(fmt.Errorf(
			"integration installation exceeds the %d active-route limit",
			MaxActiveIntegrationRoutesPerInstall,
		))
	}
	out := make([]IntegrationRouteRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, integrationRouteRecordFromSQLC(row))
	}
	return out, nil
}

func (s *Store) DeleteIntegrationRoute(
	ctx context.Context,
	projectID, integrationInstallID, id ID,
) error {
	if isNilID(projectID) || isNilID(integrationInstallID) || isNilID(id) {
		return errors.New("project, integration install, and integration route are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete integration route: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := lockProjectLifecycleShared(ctx, qtx, projectID); err != nil {
		return err
	}
	if _, err := qtx.LockIntegrationInstallForRouteMutation(
		ctx,
		dbsqlc.LockIntegrationInstallForRouteMutationParams{
			ProjectID: projectID, IntegrationInstallID: integrationInstallID,
		},
	); err != nil {
		return integrationChannelReadError("lock integration installation for route delete", err)
	}
	rows, err := qtx.DeleteIntegrationRoute(ctx, dbsqlc.DeleteIntegrationRouteParams{
		ProjectID: projectID, IntegrationInstallID: integrationInstallID, ID: id,
	})
	if err != nil {
		return fmt.Errorf("delete integration route: %w", err)
	}
	if rows == 0 {
		if _, err := qtx.GetIntegrationRoute(ctx, dbsqlc.GetIntegrationRouteParams{
			ProjectID: projectID, IntegrationInstallID: integrationInstallID, ID: id,
		}); err != nil {
			return integrationChannelReadError("load integration route delete replay", err)
		}
	}
	if err := qtx.RevokeIntegrationTargetBindingsForRoute(
		ctx,
		dbsqlc.RevokeIntegrationTargetBindingsForRouteParams{
			ProjectID: projectID, IntegrationInstallID: integrationInstallID,
			IntegrationRouteID: sqlcIDFromNil(id),
		},
	); err != nil {
		return fmt.Errorf("revoke integration route bindings: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete integration route: %w", err)
	}
	return nil
}

func (s *Store) CreateIntegrationTargetBinding(
	ctx context.Context,
	input CreateIntegrationTargetBindingInput,
) (IntegrationTargetBindingRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationTargetBindingRecord{}, fmt.Errorf("begin create integration target binding: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := createIntegrationTargetBinding(
		ctx,
		s.q.WithTx(tx),
		input,
	)
	if err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationTargetBindingRecord{}, fmt.Errorf("commit create integration target binding: %w", err)
	}
	return record, nil
}

func (s *Store) CreateIntegrationTargetBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetBindingInput,
) (IntegrationTargetBindingRecord, error) {
	if tx == nil {
		return IntegrationTargetBindingRecord{}, errors.New("transaction is required")
	}
	return createIntegrationTargetBinding(ctx, dbsqlc.New(tx), input)
}

func createIntegrationTargetBinding(
	ctx context.Context,
	q *dbsqlc.Queries,
	input CreateIntegrationTargetBindingInput,
) (IntegrationTargetBindingRecord, error) {
	var err error
	input, err = normalizeCreateIntegrationTargetBindingInput(input)
	if err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: input.ProjectID,
		ID:        input.AgentID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetBindingRecord{}, storeerr.ErrNotFound
		}
		return IntegrationTargetBindingRecord{}, fmt.Errorf("lock agent for integration target binding: %w", err)
	}
	if _, err := q.LockIntegrationTargetForBinding(
		ctx,
		dbsqlc.LockIntegrationTargetForBindingParams{
			ProjectID:            input.ProjectID,
			IntegrationInstallID: input.IntegrationInstallID,
			IntegrationTargetID:  input.IntegrationTargetID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetBindingRecord{}, storeerr.ErrNotFound
		}
		return IntegrationTargetBindingRecord{}, fmt.Errorf(
			"lock integration target for binding: %w",
			err,
		)
	}
	if !isNilID(input.IntegrationRouteID) {
		if _, err := q.LockActiveIntegrationRouteForBinding(
			ctx,
			dbsqlc.LockActiveIntegrationRouteForBindingParams{
				ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
				ID: input.IntegrationRouteID,
			},
		); err != nil {
			return IntegrationTargetBindingRecord{}, integrationChannelReadError(
				"lock integration route for target binding",
				err,
			)
		}
	}
	params := dbsqlc.InsertIntegrationTargetBindingParams{
		ProjectID:            input.ProjectID,
		AgentID:              input.AgentID,
		IntegrationInstallID: input.IntegrationInstallID,
		IntegrationTargetID:  input.IntegrationTargetID,
		IntegrationRouteID:   sqlcIDFromNil(input.IntegrationRouteID),
		ReceiveAllowed:       input.ReceiveAllowed,
		SendAllowed:          input.SendAllowed,
		Source:               input.Source,
		Metadata:             input.Metadata,
	}
	existing, err := q.GetActiveIntegrationTargetBindingByIdentity(
		ctx,
		dbsqlc.GetActiveIntegrationTargetBindingByIdentityParams{
			ProjectID:           input.ProjectID,
			AgentID:             input.AgentID,
			IntegrationTargetID: input.IntegrationTargetID,
			IntegrationRouteID:  sqlcIDFromNil(input.IntegrationRouteID),
			Source:              input.Source,
		},
	)
	existingFound := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetBindingRecord{}, fmt.Errorf("load active integration target binding: %w", err)
	}
	if existingFound {
		existingRecord := integrationTargetBindingRecordFromSQLC(existing)
		if integrationTargetBindingDefinitionMatches(existingRecord, input) {
			return existingRecord, nil
		}
	}
	if input.ReceiveAllowed && (!existingFound || !existing.ReceiveAllowed) {
		activeReceiveBindings, countErr := q.CountActiveReceiveBindingsForTargetRoute(
			ctx,
			dbsqlc.CountActiveReceiveBindingsForTargetRouteParams{
				ProjectID:           input.ProjectID,
				IntegrationTargetID: input.IntegrationTargetID,
				IntegrationRouteID:  sqlcIDFromNil(input.IntegrationRouteID),
			},
		)
		if countErr != nil {
			return IntegrationTargetBindingRecord{}, fmt.Errorf(
				"count active receive bindings for integration target route: %w",
				countErr,
			)
		}
		if activeReceiveBindings >= MaxActiveReceiveBindingsPerTargetRoute {
			return IntegrationTargetBindingRecord{}, storeerr.InvalidRequest(fmt.Errorf(
				"integration route target exceeds the %d active-binding limit",
				MaxActiveReceiveBindingsPerTargetRoute,
			))
		}
	}
	if existingFound {
		revoked, revokeErr := q.RevokeIntegrationTargetBinding(
			ctx,
			dbsqlc.RevokeIntegrationTargetBindingParams{ProjectID: input.ProjectID, ID: existing.ID},
		)
		if revokeErr != nil {
			return IntegrationTargetBindingRecord{}, fmt.Errorf(
				"revoke integration target binding: %w",
				revokeErr,
			)
		}
		if revoked != 1 {
			return IntegrationTargetBindingRecord{}, storeerr.ErrStateTransitionConflict
		}
	}
	row, err := q.InsertIntegrationTargetBinding(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) || storeutil.IsUniqueViolation(err) {
		return IntegrationTargetBindingRecord{}, storeerr.ErrConflict
	}
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelWriteError(
			"create integration target binding",
			err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func integrationTargetBindingDefinitionMatches(
	record IntegrationTargetBindingRecord,
	input CreateIntegrationTargetBindingInput,
) bool {
	return record.ProjectID == input.ProjectID &&
		record.AgentID == input.AgentID &&
		record.IntegrationInstallID == input.IntegrationInstallID &&
		record.IntegrationTargetID == input.IntegrationTargetID &&
		record.IntegrationRouteID == input.IntegrationRouteID &&
		record.ReceiveAllowed == input.ReceiveAllowed &&
		record.SendAllowed == input.SendAllowed &&
		record.Source == input.Source &&
		storeutil.SameJSON(record.Metadata, input.Metadata)
}

func (s *Store) GetIntegrationTargetBinding(
	ctx context.Context,
	projectID, id ID,
) (IntegrationTargetBindingRecord, error) {
	if isNilID(projectID) || isNilID(id) {
		return IntegrationTargetBindingRecord{}, errors.New("project and integration binding are required")
	}
	row, err := s.q.GetIntegrationTargetBinding(ctx, dbsqlc.GetIntegrationTargetBindingParams{
		ProjectID: projectID, ID: id,
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError(
			"get integration target binding", err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func (s *Store) RevokeIntegrationTargetBinding(ctx context.Context, projectID, id ID) error {
	if isNilID(projectID) || isNilID(id) {
		return errors.New("project and integration binding are required")
	}
	rows, err := s.q.RevokeIntegrationTargetBinding(
		ctx,
		dbsqlc.RevokeIntegrationTargetBindingParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		return fmt.Errorf("revoke integration target binding: %w", err)
	}
	if rows == 1 {
		return nil
	}
	found, err := s.q.IntegrationTargetBindingExists(
		ctx,
		dbsqlc.IntegrationTargetBindingExistsParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		return fmt.Errorf("load integration target binding revoke replay: %w", err)
	}
	if !found {
		return storeerr.ErrNotFound
	}
	return nil
}

func (s *Store) GetActiveSendBindingForTarget(
	ctx context.Context,
	projectID, agentID, integrationTargetID ID,
) (IntegrationTargetBindingRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(integrationTargetID) {
		return IntegrationTargetBindingRecord{}, errors.New("project, agent, and integration target are required")
	}
	row, err := s.q.GetActiveSendBindingForTarget(ctx, dbsqlc.GetActiveSendBindingForTargetParams{
		ProjectID: projectID, AgentID: agentID, IntegrationTargetID: integrationTargetID,
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError(
			"get active send binding", err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func (s *Store) GetActiveReceiveBindingForTarget(
	ctx context.Context,
	projectID, agentID, integrationTargetID ID,
) (IntegrationTargetBindingRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(integrationTargetID) {
		return IntegrationTargetBindingRecord{}, errors.New("project, agent, and integration target are required")
	}
	row, err := s.q.GetActiveReceiveBindingForTarget(
		ctx,
		dbsqlc.GetActiveReceiveBindingForTargetParams{
			ProjectID: projectID, AgentID: agentID, IntegrationTargetID: integrationTargetID,
		},
	)
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError(
			"get active receive binding", err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func (s *Store) GetActiveSendBinding(
	ctx context.Context,
	projectID, agentID, id ID,
) (IntegrationTargetBindingRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return IntegrationTargetBindingRecord{}, errors.New("project, agent, and binding are required")
	}
	row, err := s.q.GetActiveSendBinding(ctx, dbsqlc.GetActiveSendBindingParams{
		ProjectID: projectID, AgentID: agentID, ID: id,
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError(
			"get active send binding", err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func (s *Store) GetActiveReceiveBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, integrationInstallID, integrationTargetID, id ID,
) (IntegrationTargetBindingRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(integrationInstallID) ||
		isNilID(integrationTargetID) || isNilID(id) {
		return IntegrationTargetBindingRecord{}, errors.New(
			"project, agent, integration install, integration target, and binding are required",
		)
	}
	row, err := dbsqlc.New(tx).GetActiveReceiveBinding(ctx, dbsqlc.GetActiveReceiveBindingParams{
		ProjectID: projectID, AgentID: agentID, IntegrationInstallID: integrationInstallID,
		IntegrationTargetID: integrationTargetID, ID: id,
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError(
			"get active receive binding", err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func (s *Store) ListActiveReceiveBindingsForTargetRoute(
	ctx context.Context,
	projectID, integrationInstallID, integrationRouteID, integrationTargetID ID,
) ([]IntegrationTargetBindingRecord, error) {
	if isNilID(projectID) || isNilID(integrationInstallID) || isNilID(integrationRouteID) ||
		isNilID(integrationTargetID) {
		return nil, errors.New(
			"project, installation, route, and target are required",
		)
	}
	rows, err := s.q.ListActiveReceiveBindingsForTargetRoute(
		ctx,
		dbsqlc.ListActiveReceiveBindingsForTargetRouteParams{
			ProjectID: projectID, IntegrationInstallID: integrationInstallID,
			IntegrationRouteID:  sqlcIDFromNil(integrationRouteID),
			IntegrationTargetID: integrationTargetID,
			RowLimit:            MaxActiveReceiveBindingsPerTargetRoute + 1,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list active receive bindings for target route: %w", err)
	}
	if len(rows) > MaxActiveReceiveBindingsPerTargetRoute {
		return nil, storeerr.InvalidRequest(fmt.Errorf(
			"integration route target exceeds the %d active-binding limit",
			MaxActiveReceiveBindingsPerTargetRoute,
		))
	}
	out := make([]IntegrationTargetBindingRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, integrationTargetBindingRecordFromSQLC(row))
	}
	return out, nil
}

func (s *Store) ListAgentChannelTargets(
	ctx context.Context,
	projectID, agentID ID,
	input ListAgentChannelTargetsInput,
) (AgentChannelTargetPage, error) {
	return listAgentChannelTargets(ctx, s.q, projectID, agentID, input)
}

func (s *Store) ListAgentChannelTargetsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
	input ListAgentChannelTargetsInput,
) (AgentChannelTargetPage, error) {
	if tx == nil {
		return AgentChannelTargetPage{}, errors.New("transaction is required")
	}
	return listAgentChannelTargets(ctx, dbsqlc.New(tx), projectID, agentID, input)
}

func listAgentChannelTargets(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
	input ListAgentChannelTargetsInput,
) (AgentChannelTargetPage, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return AgentChannelTargetPage{}, errors.New("project and agent are required")
	}
	if input.Limit <= 0 || input.Limit > MaxAgentChannelTargetsPageSize {
		return AgentChannelTargetPage{}, fmt.Errorf(
			"channel page limit must be between 1 and %d",
			MaxAgentChannelTargetsPageSize,
		)
	}
	params := dbsqlc.ListAgentChannelTargetsParams{
		ProjectID: projectID,
		AgentID:   agentID,
		RowLimit:  int32(input.Limit + 1),
	}
	if input.After != nil {
		if input.After.CreatedAt.IsZero() || isNilID(input.After.ID) {
			return AgentChannelTargetPage{}, errors.New("channel page cursor is invalid")
		}
		params.CursorSet = true
		params.CursorCreatedAt = input.After.CreatedAt
		params.CursorID = input.After.ID
	}
	rows, err := q.ListAgentChannelTargets(
		ctx,
		params,
	)
	if err != nil {
		return AgentChannelTargetPage{}, fmt.Errorf("list agent channel targets: %w", err)
	}
	page := AgentChannelTargetPage{}
	if len(rows) > input.Limit {
		rows = rows[:input.Limit]
		last := rows[len(rows)-1]
		page.Next = &AgentChannelTargetCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	out := make([]AgentChannelTarget, 0, len(rows))
	for _, row := range rows {
		out = append(out, AgentChannelTarget{
			ID:                   row.ID,
			IntegrationInstallID: row.IntegrationInstallID,
			TargetRef:            row.TargetRef,
			ProviderRef:          row.ProviderRef,
			ProviderRefKind:      row.ProviderRefKind,
			DisplayName:          row.DisplayName,
			Provider:             row.Provider,
			InstallState:         IntegrationInstallState(row.InstallState),
			ConnectorKey:         row.ConnectorKey,
			AppState:             IntegrationAppState(row.AppState),
			ReceiveAllowed:       row.ReceiveAllowed,
			SendAllowed:          row.SendAllowed,
			CreatedAt:            row.CreatedAt,
		})
	}
	page.Targets = out
	return page, nil
}

func (s *Store) GetAgentChannelToolEligibility(
	ctx context.Context,
	projectID, agentID ID,
) (AgentChannelToolEligibility, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return AgentChannelToolEligibility{}, errors.New("project and agent are required")
	}
	row, err := s.q.GetAgentChannelToolEligibility(
		ctx,
		dbsqlc.GetAgentChannelToolEligibilityParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return AgentChannelToolEligibility{}, fmt.Errorf("get agent channel tool eligibility: %w", err)
	}
	return AgentChannelToolEligibility{List: row.ListAllowed, Send: row.SendAllowed}, nil
}

func (s *Store) ListModelCallIntegrationOriginTargets(
	ctx context.Context,
	projectID, agentID, turnID, modelCallContextID ID,
) ([]ID, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(turnID) || isNilID(modelCallContextID) {
		return nil, errors.New("project, agent, turn, and model call context are required")
	}
	rows, err := s.q.ListModelCallIntegrationOriginTargets(
		ctx,
		dbsqlc.ListModelCallIntegrationOriginTargetsParams{
			ProjectID: projectID, AgentID: agentID, TurnID: turnID,
			ModelCallContextID: modelCallContextID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list model call integration origin targets: %w", err)
	}
	out := make([]ID, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (s *Store) ListInputIntegrationOriginTargets(
	ctx context.Context,
	projectID, agentID ID,
	inputIDs []ID,
) ([]ID, error) {
	if isNilID(projectID) || isNilID(agentID) || len(inputIDs) == 0 {
		return nil, errors.New("project, agent, and input ids are required")
	}
	for _, inputID := range inputIDs {
		if isNilID(inputID) {
			return nil, errors.New("input ids cannot contain a nil id")
		}
	}
	rows, err := s.q.ListInputIntegrationOriginTargets(
		ctx,
		dbsqlc.ListInputIntegrationOriginTargetsParams{
			ProjectID: projectID, AgentID: agentID, InputIds: inputIDs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list input integration origin targets: %w", err)
	}
	out := make([]ID, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (s *Store) GetLatestModelCallIntegrationOrigin(
	ctx context.Context,
	projectID, agentID, turnID, modelCallContextID ID,
) (IntegrationInputOrigin, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(turnID) || isNilID(modelCallContextID) {
		return IntegrationInputOrigin{}, false, errors.New(
			"project, agent, turn, and model call context are required",
		)
	}
	row, err := s.q.GetLatestModelCallIntegrationOrigin(
		ctx,
		dbsqlc.GetLatestModelCallIntegrationOriginParams{
			ProjectID: projectID, AgentID: agentID, TurnID: turnID,
			ModelCallContextID: modelCallContextID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationInputOrigin{}, false, nil
	}
	if err != nil {
		return IntegrationInputOrigin{}, false, fmt.Errorf(
			"get latest model call integration origin: %w",
			err,
		)
	}
	if row.IntegrationTargetID == nil {
		return IntegrationInputOrigin{}, false, nil
	}
	return IntegrationInputOrigin{
		TargetID:  *row.IntegrationTargetID,
		BindingID: idFromSQLCPtr(row.IntegrationTargetBindingID),
	}, true, nil
}

func (s *Store) GetLatestInputIntegrationOrigin(
	ctx context.Context,
	projectID, agentID ID,
	inputIDs []ID,
) (IntegrationInputOrigin, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || len(inputIDs) == 0 {
		return IntegrationInputOrigin{}, false, errors.New("project, agent, and input ids are required")
	}
	for _, inputID := range inputIDs {
		if isNilID(inputID) {
			return IntegrationInputOrigin{}, false, errors.New("input ids cannot contain a nil id")
		}
	}
	row, err := s.q.GetLatestInputIntegrationOrigin(
		ctx,
		dbsqlc.GetLatestInputIntegrationOriginParams{
			ProjectID: projectID, AgentID: agentID, InputIds: inputIDs,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationInputOrigin{}, false, nil
	}
	if err != nil {
		return IntegrationInputOrigin{}, false, fmt.Errorf("get latest input integration origin: %w", err)
	}
	if row.IntegrationTargetID == nil {
		return IntegrationInputOrigin{}, false, nil
	}
	return IntegrationInputOrigin{
		TargetID:  *row.IntegrationTargetID,
		BindingID: idFromSQLCPtr(row.IntegrationTargetBindingID),
	}, true, nil
}

func normalizeCreateIntegrationAppInput(input CreateIntegrationAppInput) (CreateIntegrationAppInput, error) {
	if isNilID(input.OrgID) {
		return CreateIntegrationAppInput{}, errors.New("org is required")
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.ProviderAppRef = strings.TrimSpace(input.ProviderAppRef)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.ConnectorKey = strings.TrimSpace(input.ConnectorKey)
	input.InstallationCredentialKind = strings.TrimSpace(input.InstallationCredentialKind)
	if !registryname.Valid(input.Provider) ||
		input.ProviderAppRef == "" || !registryname.Valid(input.ConnectorKey) {
		return CreateIntegrationAppInput{}, errors.New(
			"registry-name provider and connector key, and provider app ref are required",
		)
	}
	if len(input.ProviderAppRef) > 512 || len(input.DisplayName) > 512 {
		return CreateIntegrationAppInput{}, errors.New(
			"integration app identifier exceeds its size limit",
		)
	}
	if input.State != IntegrationAppStateActive && input.State != IntegrationAppStateDisabled {
		return CreateIntegrationAppInput{}, fmt.Errorf("unsupported integration app state %q", input.State)
	}
	if input.InstallationCredentialKind != "" &&
		!validIntegrationCredentialKind(input.InstallationCredentialKind) {
		return CreateIntegrationAppInput{}, fmt.Errorf(
			"unsupported installation credential kind %q",
			input.InstallationCredentialKind,
		)
	}
	var err error
	input.ProviderConfig, err = normalizedJSONObject(input.ProviderConfig, "provider_config")
	if err != nil {
		return CreateIntegrationAppInput{}, err
	}
	input.ProviderMetadata, err = normalizedJSONObject(input.ProviderMetadata, "provider_metadata")
	if err != nil {
		return CreateIntegrationAppInput{}, err
	}
	return input, nil
}

func validIntegrationCredentialKind(kind string) bool {
	switch secretspkg.Kind(kind) {
	case secretspkg.KindGeneric,
		secretspkg.KindOAuthTokenSet,
		secretspkg.KindSlackAppCredentials,
		secretspkg.KindAWSCredentials,
		secretspkg.KindIntegrationCredentials:
		return true
	default:
		return false
	}
}

func normalizeCreateIntegrationRouteInput(
	input CreateIntegrationRouteInput,
) (CreateIntegrationRouteInput, error) {
	if isNilID(input.ProjectID) || isNilID(input.IntegrationInstallID) {
		return CreateIntegrationRouteInput{}, errors.New("project and integration install are required")
	}
	input.DeploymentKey = strings.TrimSpace(input.DeploymentKey)
	input.HandlerKey = strings.TrimSpace(input.HandlerKey)
	if input.DeploymentKey == "" || input.HandlerKey == "" || input.HandlerVersion <= 0 {
		return CreateIntegrationRouteInput{}, errors.New(
			"deployment key, handler key, and positive handler version are required",
		)
	}
	if len(input.DeploymentKey) > 512 || !registryname.Valid(input.HandlerKey) {
		return CreateIntegrationRouteInput{}, errors.New(
			"integration route handler exceeds its contract",
		)
	}
	if input.State != IntegrationRouteStateActive && input.State != IntegrationRouteStateDisabled {
		return CreateIntegrationRouteInput{}, fmt.Errorf("unsupported integration route state %q", input.State)
	}
	configuration, err := normalizedJSONObject(input.Configuration, "configuration")
	if err != nil {
		return CreateIntegrationRouteInput{}, err
	}
	input.Configuration = configuration
	return input, nil
}

func normalizeCreateIntegrationTargetBindingInput(
	input CreateIntegrationTargetBindingInput,
) (CreateIntegrationTargetBindingInput, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) ||
		isNilID(input.IntegrationInstallID) || isNilID(input.IntegrationTargetID) {
		return CreateIntegrationTargetBindingInput{}, errors.New(
			"project, agent, integration install, and integration target are required",
		)
	}
	if !input.ReceiveAllowed && !input.SendAllowed {
		return CreateIntegrationTargetBindingInput{}, errors.New("at least one binding permission is required")
	}
	if input.ReceiveAllowed && isNilID(input.IntegrationRouteID) {
		return CreateIntegrationTargetBindingInput{}, errors.New("receive bindings require an integration route")
	}
	input.Source = strings.TrimSpace(input.Source)
	if input.Source == "" {
		return CreateIntegrationTargetBindingInput{}, errors.New("binding source is required")
	}
	if len(input.Source) > 128 {
		return CreateIntegrationTargetBindingInput{}, errors.New("binding source exceeds its size limit")
	}
	metadata, err := normalizedJSONObject(input.Metadata, "metadata")
	if err != nil {
		return CreateIntegrationTargetBindingInput{}, err
	}
	input.Metadata = metadata
	return input, nil
}

func normalizedCapabilityColumns(
	capabilities []channelconnector.Capability,
) ([]string, []string, error) {
	capabilities, err := channelconnector.NormalizeCapabilities(capabilities)
	if err != nil {
		return nil, nil, err
	}
	connectorKeys := make([]string, 0, len(capabilities))
	providers := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		connectorKeys = append(connectorKeys, capability.ConnectorKey)
		providers = append(providers, capability.Provider)
	}
	return connectorKeys, providers, nil
}

func normalizedClaimCapability(
	capability channelconnector.Capability,
) (channelconnector.Capability, error) {
	capabilities, err := channelconnector.NormalizeCapabilities([]channelconnector.Capability{
		capability,
	})
	if err != nil {
		return channelconnector.Capability{}, err
	}
	return capabilities[0], nil
}

func integrationChannelReadError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func integrationAppRecordFromSQLC(row dbsqlc.IntegrationApp) IntegrationAppRecord {
	return IntegrationAppRecord{
		ID:                         row.ID,
		OrgID:                      row.OrgID,
		OwnerProjectID:             idFromSQLCPtr(row.OwnerProjectID),
		Provider:                   row.Provider,
		ProviderAppRef:             row.ProviderAppRef,
		DisplayName:                row.DisplayName,
		ConnectorKey:               row.ConnectorKey,
		CredentialSecretID:         idFromSQLCPtr(row.CredentialSecretID),
		InstallationCredentialKind: stringFromPtr(row.InstallationCredentialKind),
		ProviderConfig:             row.ProviderConfig,
		ProviderMetadata:           row.ProviderMetadata,
		ConfigurationRevision:      row.ConfigurationRevision,
		State:                      IntegrationAppState(row.State),
		CreatedAt:                  row.CreatedAt,
		UpdatedAt:                  row.UpdatedAt,
	}
}

func integrationRouteRecordFromSQLC(row dbsqlc.IntegrationRoute) IntegrationRouteRecord {
	return IntegrationRouteRecord{
		ID:                   row.ID,
		ProjectID:            row.ProjectID,
		IntegrationInstallID: row.IntegrationInstallID,
		DeploymentKey:        row.DeploymentKey,
		HandlerKey:           row.HandlerKey,
		HandlerVersion:       int(row.HandlerVersion),
		Configuration:        row.Configuration,
		State:                IntegrationRouteState(row.State),
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
}

func integrationTargetBindingRecordFromSQLC(
	row dbsqlc.IntegrationTargetBinding,
) IntegrationTargetBindingRecord {
	return IntegrationTargetBindingRecord{
		ID:                   row.ID,
		ProjectID:            row.ProjectID,
		AgentID:              row.AgentID,
		IntegrationInstallID: row.IntegrationInstallID,
		IntegrationTargetID:  row.IntegrationTargetID,
		IntegrationRouteID:   idFromSQLCPtr(row.IntegrationRouteID),
		ReceiveAllowed:       row.ReceiveAllowed,
		SendAllowed:          row.SendAllowed,
		Source:               row.Source,
		Metadata:             row.Metadata,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
}
