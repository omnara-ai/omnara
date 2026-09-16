package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// InstallationControlScope carries no credentials or agent authority. Disabled
// installations remain discoverable here so provider restoration can be applied.
type InstallationControlScope struct {
	ID, ProjectID         uuid.UUID
	ProviderTenantID      string
	ProviderAccountRef    string
	ProviderIdentity      json.RawMessage
	State                 IntegrationInstallState
	ConfigurationRevision int64
}

type ListConnectorInstallationControlScopesInput struct {
	IntegrationAppID uuid.UUID
	ProviderTenantID string
	AfterID          uuid.UUID
	ThroughID        *uuid.UUID // nil captures the current bound; a pointer to Nil is an empty fixed scan
	Limit            int32
	Capabilities     []channelconnector.Capability
}

type ListConnectorInstallationControlScopesResult struct {
	AppConfigurationRevision int64
	ThroughID                uuid.UUID
	Installations            []InstallationControlScope
	HasMore                  bool
}

// ListConnectorInstallationControlScopes uses immutable ID ordering and an
// explicit fixed upper bound. Neither progress nor bounds carry child authority.
func (s *Store) ListConnectorInstallationControlScopes(
	ctx context.Context, input ListConnectorInstallationControlScopesInput,
) (ListConnectorInstallationControlScopesResult, error) {
	var result ListConnectorInstallationControlScopesResult
	if input.IntegrationAppID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return result, storeerr.InvalidRequest(errors.New("app and limit between 1 and 100 are required"))
	}
	if err := validateProviderControlIdentity(input.ProviderTenantID); err != nil {
		return result, err
	}
	app, err := s.GetConnectorIntegrationApp(ctx, input.IntegrationAppID, input.Capabilities)
	if err != nil {
		return result, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, fmt.Errorf("begin installation control discovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if app.OwnerProjectID == uuid.Nil {
		err = lifecyclelock.EnterActiveOrganization(ctx, tx, app.OrgID)
	} else {
		err = lifecyclelock.EnterActiveProject(ctx, tx, app.OrgID, app.OwnerProjectID)
	}
	if err != nil {
		return result, err
	}
	app, err = lockProviderControlApp(ctx, tx, app)
	if err != nil {
		return result, err
	}
	q := s.q.WithTx(tx)
	if input.ThroughID == nil {
		result.ThroughID, err = connectorInstallationControlScopeEnd(ctx, q, app, input.ProviderTenantID)
		if err != nil {
			return result, err
		}
	} else {
		result.ThroughID = *input.ThroughID
	}
	if bytes.Compare(input.AfterID[:], result.ThroughID[:]) > 0 {
		return result, storeerr.InvalidRequest(errors.New("control progress exceeds the scan bound"))
	}
	rows, err := s.q.WithTx(tx).ListConnectorInstallationControlScopes(ctx,
		dbsqlc.ListConnectorInstallationControlScopesParams{
			OrgID: app.OrgID, IntegrationAppID: &app.ID, ProviderTenantID: input.ProviderTenantID,
			OwnerProjectID: storeutil.IDFromNil(app.OwnerProjectID), AfterID: input.AfterID, RowLimit: input.Limit + 1,
			ThroughID: result.ThroughID,
		})
	if err != nil {
		return result, fmt.Errorf("list installation control scopes: %w", err)
	}
	result.AppConfigurationRevision = app.ConfigurationRevision
	result.HasMore = len(rows) > int(input.Limit)
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	result.Installations = make([]InstallationControlScope, 0, len(rows))
	for _, row := range rows {
		result.Installations = append(result.Installations, InstallationControlScope{
			ID: row.ID, ProjectID: row.ProjectID, ProviderTenantID: row.ProviderTenantID,
			ProviderAccountRef: row.ProviderAccountRef, State: IntegrationInstallState(row.State),
			ConfigurationRevision: row.ConfigurationRevision,
			ProviderIdentity:      row.ProviderIdentity,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return ListConnectorInstallationControlScopesResult{}, fmt.Errorf("commit installation control discovery: %w", err)
	}
	return result, nil
}

type SetConnectorInstallationProviderStateInput struct {
	IntegrationAppID, IntegrationInstallID uuid.UUID
	ProviderTenantID                       string
	ProviderAccountRef                     string
	ExpectedAppConfigurationRevision       int64
	ExpectedConfigurationRevision          int64
	State                                  IntegrationInstallState
	Capabilities                           []channelconnector.Capability
}

type SetConnectorInstallationProviderStateResult struct {
	State                 IntegrationInstallState
	ConfigurationRevision int64
}

// SetConnectorInstallationProviderState applies a provider observation to the
// existing physical installation only. It never restores deletion, grants,
// routes, targets or terminal work. Observe provider state AFTER reading the
// expected revisions; conflicts require a fresh provider read, not a new fence
// attached to the old observation. No provider I/O belongs in this transaction.
func (s *Store) SetConnectorInstallationProviderState(
	ctx context.Context, input SetConnectorInstallationProviderStateInput,
) (SetConnectorInstallationProviderStateResult, error) {
	var result SetConnectorInstallationProviderStateResult
	if input.IntegrationAppID == uuid.Nil || input.IntegrationInstallID == uuid.Nil ||
		input.ExpectedConfigurationRevision < 1 || input.ExpectedAppConfigurationRevision < 1 ||
		(input.State != IntegrationInstallStateActive && input.State != IntegrationInstallStateDisabled) {
		return result, storeerr.InvalidRequest(errors.New(
			"installation scope, revisions and active/disabled state are required"))
	}
	for _, identity := range []string{input.ProviderTenantID, input.ProviderAccountRef} {
		if err := validateProviderControlIdentity(identity); err != nil {
			return result, err
		}
	}
	app, err := s.GetConnectorIntegrationApp(ctx, input.IntegrationAppID, input.Capabilities)
	if err != nil {
		return result, err
	}
	install, err := s.GetIntegrationInstallByID(ctx, input.IntegrationInstallID)
	if err != nil {
		return result, err
	}
	if install.IntegrationAppID != app.ID || install.OrgID != app.OrgID ||
		(app.OwnerProjectID != uuid.Nil && app.OwnerProjectID != install.ProjectID) {
		return result, storeerr.ErrNotFound
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, fmt.Errorf("begin installation provider state: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockIntegrationInstallLifecycleShared(ctx, tx, install.ProjectID, install.ID); err != nil {
		return result, err
	}
	q := s.q.WithTx(tx)
	locked, err := q.LockConnectorInstallationProviderState(ctx, dbsqlc.LockConnectorInstallationProviderStateParams{
		OrgID: app.OrgID, ProjectID: install.ProjectID, ID: install.ID, IntegrationAppID: &app.ID,
		ProviderTenantID: input.ProviderTenantID, ProviderAccountRef: input.ProviderAccountRef,
	})
	if err != nil {
		return result, integrationChannelReadError("lock installation provider state", err)
	}
	app, err = lockProviderControlApp(ctx, tx, app)
	if err != nil {
		return result, err
	}
	if locked.ConfigurationRevision != input.ExpectedConfigurationRevision ||
		app.ConfigurationRevision != input.ExpectedAppConfigurationRevision {
		return result, storeerr.ErrStateTransitionConflict
	}
	updated, err := q.SetConnectorInstallationProviderState(ctx, dbsqlc.SetConnectorInstallationProviderStateParams{
		ID: install.ID, ProjectID: install.ProjectID, IntegrationAppID: &app.ID,
		ExpectedConfigurationRevision: input.ExpectedConfigurationRevision, State: string(input.State),
	})
	if err != nil {
		return result, integrationChannelWriteError("set installation provider state", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit installation provider state: %w", err)
	}
	return SetConnectorInstallationProviderStateResult{
		State: IntegrationInstallState(updated.State), ConfigurationRevision: updated.ConfigurationRevision,
	}, nil
}

func lockProviderControlApp(
	ctx context.Context, tx pgx.Tx, authorized IntegrationAppRecord,
) (IntegrationAppRecord, error) {
	row, err := dbsqlc.New(tx).LockIntegrationAppForInstallation(ctx, dbsqlc.LockIntegrationAppForInstallationParams{
		OrgID: authorized.OrgID, ID: authorized.ID,
	})
	if err != nil {
		return IntegrationAppRecord{}, integrationChannelReadError("lock installation control app", err)
	}
	if row.DeletedAt != nil || IntegrationAppState(row.State) != IntegrationAppStateActive ||
		row.ConnectorKey != authorized.ConnectorKey || row.Provider != authorized.Provider ||
		storeutil.IDFromPtr(row.OwnerProjectID) != authorized.OwnerProjectID {
		return IntegrationAppRecord{}, storeerr.ErrNotFound
	}
	return integrationAppRecordFromSQLC(row), nil
}

func validateProviderControlIdentity(value string) error {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return storeerr.InvalidRequest(errors.New(
			"provider identity must contain 1 to 512 bytes without surrounding whitespace"))
	}
	if err := dbsafe.Text(value); err != nil {
		return storeerr.InvalidRequest(errors.New("provider identity is not valid database text"))
	}
	return nil
}
