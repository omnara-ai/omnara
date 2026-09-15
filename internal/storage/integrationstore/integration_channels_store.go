package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/registryname"
	secretspkg "github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
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
	if input.OwnerProjectID != uuid.Nil {
		err = lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.OwnerProjectID)
	} else {
		err = lifecyclelock.EnterActiveOrganization(ctx, tx, input.OrgID)
	}
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	row, err := qtx.InsertIntegrationApp(ctx, dbsqlc.InsertIntegrationAppParams{
		OrgID:                      input.OrgID,
		OwnerProjectID:             storeutil.IDFromNil(input.OwnerProjectID),
		Provider:                   input.Provider,
		ProviderAppRef:             input.ProviderAppRef,
		DisplayName:                input.DisplayName,
		ConnectorKey:               input.ConnectorKey,
		CredentialSecretID:         storeutil.IDFromNil(input.CredentialSecretID),
		InstallationCredentialKind: storeutil.TextFromEmpty(input.InstallationCredentialKind),
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

// GetOrCreateIntegrationApp registers physical provider identity independently
// of installations. Existing configuration and disabled state are preserved.
func (s *Store) GetOrCreateIntegrationApp(
	ctx context.Context, input CreateIntegrationAppInput,
) (IntegrationAppRecord, error) {
	input, err := normalizeCreateIntegrationAppInput(input)
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	app, err := s.CreateIntegrationApp(ctx, input)
	if !errors.Is(err, storeerr.ErrConflict) {
		return app, err
	}
	row, err := s.q.GetIntegrationAppByProviderRef(ctx, dbsqlc.GetIntegrationAppByProviderRefParams{
		OrgID: input.OrgID, OwnerProjectID: storeutil.IDFromNil(input.OwnerProjectID),
		Provider: input.Provider, ProviderAppRef: input.ProviderAppRef,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationAppRecord{}, storeerr.ErrConflict
	}
	if err != nil {
		return IntegrationAppRecord{}, integrationChannelReadError("get existing integration app", err)
	}
	if row.ConnectorKey != input.ConnectorKey ||
		stringFromPtr(row.InstallationCredentialKind) != input.InstallationCredentialKind ||
		(input.CredentialSecretID != uuid.Nil && storeutil.IDFromPtr(row.CredentialSecretID) != input.CredentialSecretID) {
		return IntegrationAppRecord{}, storeerr.ErrConflict
	}
	return integrationAppRecordFromSQLC(row), nil
}

func (s *Store) GetIntegrationApp(ctx context.Context, orgID, id uuid.UUID) (IntegrationAppRecord, error) {
	if orgID == uuid.Nil || id == uuid.Nil {
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
	id uuid.UUID,
	capabilities []channelconnector.Capability,
) (IntegrationAppRecord, error) {
	if id == uuid.Nil {
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
	integrationAppID uuid.UUID,
	providerTenantID, providerAccountRef string,
) (IntegrationInstallRecord, error) {
	providerTenantID = strings.TrimSpace(providerTenantID)
	providerAccountRef = strings.TrimSpace(providerAccountRef)
	if integrationAppID == uuid.Nil || providerAccountRef == "" {
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
		IntegrationAppID:   storeutil.IDFromNil(integrationAppID),
		ProviderTenantID:   storeutil.TextFromEmpty(providerTenantID),
		ProviderAccountRef: storeutil.TextFromEmpty(providerAccountRef),
	})
	if err != nil {
		return IntegrationInstallRecord{}, integrationChannelReadError(
			"get connector integration install", err,
		)
	}
	return integrationInstallRecordFromConnectorFields(
		row.ID, row.OrgID, row.ProjectID, row.IntegrationAppID,
		row.InstalledByUserID, row.InstalledByOrgApiKeyID,
		row.Provider, row.IntegrationKind, row.ConnectionMode, row.State,
		row.ProviderTenantID, row.ProviderAccountRef, row.DisplayName,
		row.CredentialSecretID, row.ProviderConfig, row.ProviderIdentity,
		row.Metadata, row.LastOauthFlowID, row.CreatedAt, row.UpdatedAt,
		row.ConfigurationRevision,
	), nil
}

func (s *Store) GetConnectorIntegrationInstallByID(
	ctx context.Context,
	integrationAppID, id uuid.UUID,
) (IntegrationInstallRecord, error) {
	if integrationAppID == uuid.Nil || id == uuid.Nil {
		return IntegrationInstallRecord{}, errors.New("integration app and installation are required")
	}
	row, err := s.q.GetConnectorIntegrationInstallByID(
		ctx,
		dbsqlc.GetConnectorIntegrationInstallByIDParams{IntegrationAppID: storeutil.IDFromNil(integrationAppID), ID: id},
	)
	if err != nil {
		return IntegrationInstallRecord{}, integrationChannelReadError(
			"get connector integration install by id", err,
		)
	}
	return integrationInstallRecordFromConnectorFields(
		row.ID, row.OrgID, row.ProjectID, row.IntegrationAppID,
		row.InstalledByUserID, row.InstalledByOrgApiKeyID,
		row.Provider, row.IntegrationKind, row.ConnectionMode, row.State,
		row.ProviderTenantID, row.ProviderAccountRef, row.DisplayName,
		row.CredentialSecretID, row.ProviderConfig, row.ProviderIdentity,
		row.Metadata, row.LastOauthFlowID, row.CreatedAt, row.UpdatedAt,
		row.ConfigurationRevision,
	), nil
}

func integrationInstallRecordFromConnectorFields(
	id, orgID, projectID uuid.UUID,
	appID *uuid.UUID,
	installedByUserID, installedByOrgAPIKeyID *uuid.UUID,
	provider *string,
	integrationKind, connectionMode, state string,
	providerTenantID, providerAccountRef *string,
	displayName string,
	credentialSecretID *uuid.UUID,
	providerConfig, providerIdentity, metadata []byte,
	lastOAuthFlowID *uuid.UUID,
	createdAt, updatedAt time.Time,
	configurationRevision int64,
) IntegrationInstallRecord {
	return IntegrationInstallRecord{
		ID:                    id,
		OrgID:                 orgID,
		ProjectID:             projectID,
		IntegrationAppID:      storeutil.IDFromPtr(appID),
		InstalledBy:           integrationInstallerPrincipal(orgID, installedByUserID, installedByOrgAPIKeyID),
		Provider:              stringFromPtr(provider),
		IntegrationKind:       IntegrationKind(integrationKind),
		ConnectionMode:        connectionMode,
		State:                 IntegrationInstallState(state),
		ProviderTenantID:      stringFromPtr(providerTenantID),
		ProviderAccountRef:    stringFromPtr(providerAccountRef),
		DisplayName:           displayName,
		CredentialSecretID:    storeutil.IDFromPtr(credentialSecretID),
		ProviderConfig:        providerConfig,
		ProviderIdentity:      providerIdentity,
		Metadata:              metadata,
		LastOAuthFlowID:       storeutil.IDFromPtr(lastOAuthFlowID),
		ConfigurationRevision: configurationRevision,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
	}
}

func (s *Store) CreateIntegrationRoute(
	ctx context.Context,
	input CreateIntegrationRouteInput,
) (IntegrationRouteRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationRouteRecord{}, fmt.Errorf("begin create integration route: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	route, err := s.createIntegrationRouteTx(ctx, tx, input)
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationRouteRecord{}, fmt.Errorf("commit create integration route: %w", err)
	}
	return route, nil
}

func (s *Store) createIntegrationRouteTx(
	ctx context.Context, tx pgx.Tx, input CreateIntegrationRouteInput,
) (IntegrationRouteRecord, error) {
	input, err := normalizeCreateIntegrationRouteInput(input)
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
	qtx := s.q.WithTx(tx)
	install, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
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
	if input.AgentProfileID != uuid.Nil {
		if err := s.access.ValidateInstallBinding(ctx, tx, InstallBinding{
			OrgID: install.OrgID, ProjectID: input.ProjectID, AgentProfileID: input.AgentProfileID,
		}); err != nil {
			return IntegrationRouteRecord{}, err
		}
	}
	row, err := qtx.InsertIntegrationRoute(ctx, dbsqlc.InsertIntegrationRouteParams{
		AgentProfileID:       storeutil.IDFromNil(input.AgentProfileID),
		ProjectID:            input.ProjectID,
		IntegrationInstallID: input.IntegrationInstallID,
		DeploymentKey:        input.DeploymentKey,
		BehaviorKey:          input.BehaviorKey,
		Configuration:        input.Configuration,
		State:                string(input.State),
		MaxActiveRoutes:      MaxActiveIntegrationRoutesPerInstall,
	})
	if err == nil {
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
	return integrationRouteRecordFromSQLC(existing), nil
}

func integrationRouteDefinitionMatches(
	record dbsqlc.IntegrationRoute,
	input CreateIntegrationRouteInput,
) bool {
	return record.ProjectID == input.ProjectID &&
		record.IntegrationInstallID == input.IntegrationInstallID &&
		record.DeploymentKey == input.DeploymentKey && record.BehaviorKey == input.BehaviorKey &&
		storeutil.IDFromPtr(record.AgentProfileID) == input.AgentProfileID &&
		jsoncanonical.Equal(record.Configuration, input.Configuration)
}

func (s *Store) ListActiveIntegrationRoutes(
	ctx context.Context,
	projectID, integrationInstallID uuid.UUID,
) ([]IntegrationRouteRecord, error) {
	if projectID == uuid.Nil || integrationInstallID == uuid.Nil {
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
	projectID, integrationInstallID, id uuid.UUID,
) error {
	if projectID == uuid.Nil || integrationInstallID == uuid.Nil || id == uuid.Nil {
		return errors.New("project, integration install, and integration route are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete integration route: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := lockIntegrationInstallLifecycleShared(ctx, tx, projectID, integrationInstallID); err != nil {
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
			IntegrationRouteID: storeutil.IDFromNil(id),
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
		tx,
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
	return createIntegrationTargetBinding(ctx, tx, input)
}

func createIntegrationTargetBinding(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetBindingInput,
) (IntegrationTargetBindingRecord, error) {
	var err error
	input, err = normalizeCreateIntegrationTargetBindingInput(input)
	if err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	if _, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID); err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	q := dbsqlc.New(tx)
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
	if input.IntegrationRouteID != uuid.Nil {
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
		IntegrationRouteID:   storeutil.IDFromNil(input.IntegrationRouteID),
		ReceiveAllowed:       input.ReceiveAllowed,
		ReadAllowed:          input.ReadAllowed,
		SendAllowed:          input.SendAllowed,
		Source:               input.Source,
		Metadata:             input.Metadata,
	}
	if grants := input.ReplyChannelGrants; grants != nil {
		params.ReplyReceiveAllowed = &grants.ReceiveAllowed
		params.ReplyReadAllowed = &grants.ReadAllowed
		params.ReplySendAllowed = &grants.SendAllowed
	}
	existing, err := q.GetActiveIntegrationTargetBindingByIdentity(
		ctx,
		dbsqlc.GetActiveIntegrationTargetBindingByIdentityParams{
			ProjectID:           input.ProjectID,
			AgentID:             input.AgentID,
			IntegrationTargetID: input.IntegrationTargetID,
			IntegrationRouteID:  storeutil.IDFromNil(input.IntegrationRouteID),
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
				IntegrationRouteID:  storeutil.IDFromNil(input.IntegrationRouteID),
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
		record.ReadAllowed == input.ReadAllowed &&
		record.SendAllowed == input.SendAllowed &&
		sameChannelGrants(record.ReplyChannelGrants, input.ReplyChannelGrants) &&
		record.Source == input.Source && jsoncanonical.Equal(record.Metadata, input.Metadata)
}

func (s *Store) GetIntegrationTargetBinding(
	ctx context.Context,
	projectID, id uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	if projectID == uuid.Nil || id == uuid.Nil {
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

func (s *Store) RevokeIntegrationTargetBinding(ctx context.Context, projectID, id uuid.UUID) error {
	if projectID == uuid.Nil || id == uuid.Nil {
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
	projectID, agentID, integrationTargetID uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || integrationTargetID == uuid.Nil {
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
	projectID, agentID, integrationTargetID uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	return getActiveReceiveBindingForTarget(ctx, s.q, projectID, agentID, integrationTargetID)
}

func getActiveReceiveBindingForTarget(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, integrationTargetID uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || integrationTargetID == uuid.Nil {
		return IntegrationTargetBindingRecord{}, errors.New("project, agent, and integration target are required")
	}
	row, err := q.GetActiveReceiveBindingForTarget(
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

func (s *Store) GetActiveReceiveBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, integrationInstallID, integrationTargetID, id uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	return lockActiveInputBinding(ctx, tx, projectID, agentID, integrationInstallID, integrationTargetID, id, false)
}

// GetActiveInteractionBindingTx authorizes responses to sent prompts independently
// of a channel's subscription to ordinary incoming messages.
func (s *Store) GetActiveInteractionBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, integrationInstallID, integrationTargetID, id uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	return lockActiveInputBinding(ctx, tx, projectID, agentID, integrationInstallID, integrationTargetID, id, true)
}

func lockActiveInputBinding(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, integrationInstallID, integrationTargetID, id uuid.UUID,
	forInteractionResponse bool,
) (IntegrationTargetBindingRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || integrationInstallID == uuid.Nil ||
		integrationTargetID == uuid.Nil || id == uuid.Nil {
		return IntegrationTargetBindingRecord{}, errors.New(
			"project, agent, integration install, integration target, and binding are required",
		)
	}
	row, err := dbsqlc.New(tx).LockActiveIntegrationTargetBinding(ctx, dbsqlc.LockActiveIntegrationTargetBindingParams{
		ProjectID: projectID, AgentID: agentID, IntegrationInstallID: integrationInstallID,
		IntegrationTargetID: integrationTargetID, ID: id, ForInteractionResponse: forInteractionResponse,
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError(
			"lock active input binding", err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func (s *Store) ListAgentChannelTargets(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	input ListAgentChannelTargetsInput,
) (AgentChannelTargetPage, error) {
	return listAgentChannelTargets(ctx, s.q, projectID, agentID, input)
}

func (s *Store) ListAgentChannelTargetsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
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
	projectID, agentID uuid.UUID,
	input ListAgentChannelTargetsInput,
) (AgentChannelTargetPage, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return AgentChannelTargetPage{}, errors.New("project and agent are required")
	}
	if input.Limit <= 0 || input.Limit > MaxAgentChannelTargetsPageSize {
		return AgentChannelTargetPage{}, fmt.Errorf(
			"channel page limit must be between 1 and %d",
			MaxAgentChannelTargetsPageSize,
		)
	}
	params := dbsqlc.ListAgentChannelTargetsParams{
		ParentChannelID: storeutil.IDFromNil(input.ParentChannelID),
		ProjectID:       projectID,
		AgentID:         agentID,
		RowLimit:        int32(input.Limit + 1),
	}
	if input.After != nil {
		if input.After.CreatedAt.IsZero() || input.After.ID == uuid.Nil {
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
			ParentChannelID:      storeutil.IDFromPtr(row.ParentChannelID),
			ID:                   row.ID,
			IntegrationInstallID: row.IntegrationInstallID,
			TargetRef:            row.TargetRef,
			ProviderRef:          row.ProviderRef,
			ProviderRefKind:      row.ProviderRefKind,
			DisplayName:          row.DisplayName,
			Provider:             stringFromPtr(row.Provider),
			IntegrationKind:      IntegrationKind(row.IntegrationKind),
			InstallState:         IntegrationInstallState(row.InstallState),
			ConnectorKey:         stringFromPtr(row.ConnectorKey),
			AppState:             IntegrationAppState(stringFromPtr(row.AppState)),
			ReceiveAllowed:       row.ReceiveAllowed,
			ReadAllowed:          row.ReadAllowed,
			SendAllowed:          row.SendAllowed,
			CreatedAt:            row.CreatedAt,
		})
	}
	page.Targets = out
	return page, nil
}

func (s *Store) GetAgentChannelToolEligibility(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) (AgentChannelToolEligibility, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return AgentChannelToolEligibility{}, errors.New("project and agent are required")
	}
	row, err := s.q.GetAgentChannelToolEligibility(
		ctx,
		dbsqlc.GetAgentChannelToolEligibilityParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return AgentChannelToolEligibility{}, fmt.Errorf("get agent channel tool eligibility: %w", err)
	}
	return AgentChannelToolEligibility{List: row.ListAllowed, Read: row.ReadAllowed, Send: row.SendAllowed}, nil
}

func normalizeCreateIntegrationAppInput(input CreateIntegrationAppInput) (CreateIntegrationAppInput, error) {
	if input.OrgID == uuid.Nil {
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
	if input.ProjectID == uuid.Nil || input.IntegrationInstallID == uuid.Nil {
		return CreateIntegrationRouteInput{}, errors.New("project and integration install are required")
	}
	input.DeploymentKey = strings.TrimSpace(input.DeploymentKey)
	input.BehaviorKey = strings.TrimSpace(input.BehaviorKey)
	if input.DeploymentKey == "" || input.BehaviorKey == "" {
		return CreateIntegrationRouteInput{}, errors.New(
			"deployment key and behavior key are required",
		)
	}
	if len(input.DeploymentKey) > 512 || !registryname.Valid(input.BehaviorKey) {
		return CreateIntegrationRouteInput{}, errors.New(
			"integration route behavior exceeds its contract",
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
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil ||
		input.IntegrationInstallID == uuid.Nil || input.IntegrationTargetID == uuid.Nil {
		return CreateIntegrationTargetBindingInput{}, storeerr.InvalidRequest(errors.New(
			"project, agent, integration install, and integration target are required",
		))
	}
	if !input.ReceiveAllowed && !input.ReadAllowed && !input.SendAllowed {
		return CreateIntegrationTargetBindingInput{}, storeerr.InvalidRequest(
			errors.New("at least one binding permission is required"))
	}
	if grants := input.ReplyChannelGrants; grants != nil {
		if !input.SendAllowed || (!grants.ReceiveAllowed && !grants.ReadAllowed && !grants.SendAllowed) {
			return CreateIntegrationTargetBindingInput{}, storeerr.InvalidRequest(
				errors.New("reply channel grants require binding send permission and at least one child permission"))
		}
		grantsCopy := *grants
		input.ReplyChannelGrants = &grantsCopy
	}
	input.Source = strings.TrimSpace(input.Source)
	if input.Source == "" {
		return CreateIntegrationTargetBindingInput{}, storeerr.InvalidRequest(errors.New("binding source is required"))
	}
	if len(input.Source) > 128 {
		return CreateIntegrationTargetBindingInput{}, storeerr.InvalidRequest(
			errors.New("binding source exceeds its size limit"))
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
		OwnerProjectID:             storeutil.IDFromPtr(row.OwnerProjectID),
		Provider:                   row.Provider,
		ProviderAppRef:             row.ProviderAppRef,
		DisplayName:                row.DisplayName,
		ConnectorKey:               row.ConnectorKey,
		CredentialSecretID:         storeutil.IDFromPtr(row.CredentialSecretID),
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
		AgentProfileID:       storeutil.IDFromPtr(row.AgentProfileID),
		ID:                   row.ID,
		ProjectID:            row.ProjectID,
		IntegrationInstallID: row.IntegrationInstallID,
		DeploymentKey:        row.DeploymentKey,
		BehaviorKey:          row.BehaviorKey,
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
		IntegrationRouteID:   storeutil.IDFromPtr(row.IntegrationRouteID),
		ReceiveAllowed:       row.ReceiveAllowed,
		ReadAllowed:          row.ReadAllowed,
		SendAllowed:          row.SendAllowed,
		Source:               row.Source,
		Metadata:             row.Metadata,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
		ReplyChannelGrants: channelGrantsFromSQLC(
			row.ReplyReceiveAllowed, row.ReplyReadAllowed, row.ReplySendAllowed),
	}
}

func (s *Store) GetActiveReadBindingForTarget(
	ctx context.Context,
	projectID, agentID, integrationTargetID uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || integrationTargetID == uuid.Nil {
		return IntegrationTargetBindingRecord{}, errors.New("project, agent, and integration target are required")
	}
	row, err := s.q.GetActiveReadBindingForTarget(ctx, dbsqlc.GetActiveReadBindingForTargetParams{
		ProjectID: projectID, AgentID: agentID, IntegrationTargetID: integrationTargetID,
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError(
			"get active read binding", err,
		)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}
