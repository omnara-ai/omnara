package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CreateExternalIntegrationInstall registers a customer-owned connection using
// its real installing account. It has no provider-account upsert identity and
// cannot contain a deployment app, credentials, or native agent ownership.
func (s *Store) CreateExternalIntegrationInstall(
	ctx context.Context,
	input CreateExternalIntegrationInstallInput,
) (IntegrationInstallRecord, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(errors.New("organization and project are required"))
	}
	var err error
	input.InstalledBy, err = normalizeIntegrationInstaller(input.OrgID, input.InstalledBy)
	if err != nil {
		return IntegrationInstallRecord{}, err
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if len(input.DisplayName) > 512 {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(errors.New("display name exceeds 512 bytes"))
	}
	if err := dbsafe.Text(input.DisplayName); err != nil {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(err)
	}
	input.Metadata, err = normalizedJSONObject(input.Metadata, "metadata")
	if err != nil {
		return IntegrationInstallRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationInstallRecord{}, fmt.Errorf("begin external connection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return IntegrationInstallRecord{}, err
	}
	q := s.q.WithTx(tx)
	if err := validateIntegrationInstaller(ctx, q, input.OrgID, input.ProjectID, input.InstalledBy); err != nil {
		return IntegrationInstallRecord{}, err
	}
	userID, keyID := identitystore.AccountPrincipalIDs(input.InstalledBy)
	row, err := q.InsertExternalIntegrationInstall(ctx, dbsqlc.InsertExternalIntegrationInstallParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, InstalledByUserID: userID, InstalledByOrgApiKeyID: keyID,
		DisplayName: input.DisplayName, Metadata: input.Metadata,
	})
	if err != nil {
		return IntegrationInstallRecord{}, integrationChannelWriteError("register external connection", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationInstallRecord{}, fmt.Errorf("commit external connection: %w", err)
	}
	record := integrationInstallRecordFromSQLC(row)
	record.Created = true
	return record, nil
}

// PublishExternalChannelDefinition is the project-management setup operation.
// Its caller authenticates project management; storage fences the actual live
// external connection and cannot publish a managed provider kind through it.
func (s *Store) PublishExternalChannelDefinition(
	ctx context.Context,
	input PublishChannelDefinitionInput,
) (ChannelDefinition, error) {
	input, err := normalizeChannelDefinition(input)
	if err != nil {
		return ChannelDefinition{}, storeerr.InvalidRequest(err)
	}
	if input.Kind != ChannelKindExternal || len(input.ConnectorCapabilities) != 0 {
		return ChannelDefinition{}, storeerr.InvalidRequest(errors.New("external setup requires an external definition"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ChannelDefinition{}, fmt.Errorf("begin external channel definition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	install, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return ChannelDefinition{}, err
	}
	if install.IntegrationKind != IntegrationKindExternal {
		return ChannelDefinition{}, storeerr.ErrNotFound
	}
	q := s.q.WithTx(tx)
	if _, err := q.LockIntegrationTargetCreateAuthority(ctx, dbsqlc.LockIntegrationTargetCreateAuthorityParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
	}); err != nil {
		return ChannelDefinition{}, integrationChannelReadError("lock external definition connection", err)
	}
	definition, err := upsertChannelDefinition(ctx, q, input)
	if err != nil {
		return ChannelDefinition{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ChannelDefinition{}, fmt.Errorf("commit external channel definition: %w", err)
	}
	return definition, nil
}
