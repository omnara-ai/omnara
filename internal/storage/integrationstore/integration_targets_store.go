package integrationstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CreateIntegrationTarget registers a project-owned address without creating an
// agent, binding, or current-channel selection. Its definition belongs to the
// same project and installation; address replay preserves that immutable scope.
func (s *Store) CreateIntegrationTarget(
	ctx context.Context,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	if isNilID(input.ChannelDefinitionID) {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(errors.New("channel definition is required"))
	}
	return s.createIntegrationTargetInTransaction(ctx, input)
}

// CreateIntegrationTargetTx composes project-owned registration with the caller's
// transaction. Agent authority must be checked separately when creating a binding.
func (s *Store) CreateIntegrationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	if tx == nil || isNilID(input.ChannelDefinitionID) {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("transaction and channel definition are required"))
	}
	return s.createIntegrationTarget(ctx, tx, input)
}

func (s *Store) createIntegrationTargetInTransaction(
	ctx context.Context,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("begin create integration target: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := s.createIntegrationTarget(
		ctx,
		tx,
		input,
	)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("commit create integration target: %w", err)
	}
	return record, nil
}

func (s *Store) createIntegrationTarget(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	input.ProviderRef = strings.TrimSpace(input.ProviderRef)
	input.ProviderRefKind = strings.TrimSpace(input.ProviderRefKind)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if isNilID(input.ProjectID) || isNilID(input.IntegrationInstallID) ||
		input.ProviderRef == "" || input.ProviderRefKind == "" {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(errors.New(
			"project, integration install, provider ref, and provider ref kind are required",
		))
	}
	if len(input.ProviderRef) > 2048 || len(input.ProviderRefKind) > 128 ||
		len(input.DisplayName) > 512 {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("integration target identifier exceeds its size limit"))
	}
	for _, field := range []struct{ name, value string }{
		{"provider_ref", input.ProviderRef},
		{"provider_ref_kind", input.ProviderRefKind},
		{"display_name", input.DisplayName},
	} {
		if err := dbsafe.Text(field.value); err != nil {
			return IntegrationTargetRecord{}, storeerr.InvalidRequest(fmt.Errorf("%s: %w", field.name, err))
		}
	}
	providerMetadataProvided := len(input.ProviderMetadata) != 0
	providerMetadata, err := normalizedJSONObject(input.ProviderMetadata, "provider_metadata")
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	input.ProviderMetadata = providerMetadata
	q := dbsqlc.New(tx)
	install, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if install.State != IntegrationInstallStateActive {
		return IntegrationTargetRecord{}, storeerr.ErrUnauthorized
	}
	if _, err := q.LockIntegrationTargetCreateAuthority(
		ctx,
		dbsqlc.LockIntegrationTargetCreateAuthorityParams{
			ProjectID:            input.ProjectID,
			IntegrationInstallID: input.IntegrationInstallID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrUnauthorized
		}
		return IntegrationTargetRecord{}, fmt.Errorf("lock integration target authority: %w", err)
	}
	if _, err := q.LockChannelDefinition(ctx, dbsqlc.LockChannelDefinitionParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID, ID: input.ChannelDefinitionID,
	}); err != nil {
		return IntegrationTargetRecord{}, integrationChannelReadError("lock channel definition", err)
	}
	if !isNilID(input.ParentChannelID) {
		if _, err := q.LockIntegrationChannelParent(ctx, dbsqlc.LockIntegrationChannelParentParams{
			ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
			ParentChannelID: input.ParentChannelID,
		}); err != nil {
			return IntegrationTargetRecord{}, integrationChannelReadError("lock channel parent", err)
		}
	}
	for range 5 {
		targetRef, refErr := s.targetRefGenerator(install.Provider)
		if refErr != nil {
			return IntegrationTargetRecord{}, refErr
		}
		row, insertErr := q.InsertIntegrationTarget(ctx, dbsqlc.InsertIntegrationTargetParams{
			ProjectID:            input.ProjectID,
			IntegrationInstallID: input.IntegrationInstallID,
			TargetRef:            targetRef,
			ProviderRef:          input.ProviderRef,
			ProviderRefKind:      input.ProviderRefKind,
			ParentChannelID:      sqlcIDFromNil(input.ParentChannelID),
			ChannelDefinitionID:  input.ChannelDefinitionID,
			DisplayName:          input.DisplayName,
			ProviderMetadata:     input.ProviderMetadata,
		})
		if insertErr == nil {
			record := integrationTargetRecordFromInsertSQLC(row, install.OrgID)
			record.Created = true
			return record, nil
		}
		if !errors.Is(insertErr, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, integrationChannelWriteError("insert integration target", insertErr)
		}
		existing, getErr := q.GetIntegrationTargetByProviderRef(
			ctx,
			dbsqlc.GetIntegrationTargetByProviderRefParams{
				ProjectID:            input.ProjectID,
				IntegrationInstallID: input.IntegrationInstallID,
				ProviderRef:          input.ProviderRef,
			},
		)
		if errors.Is(getErr, pgx.ErrNoRows) {
			continue
		}
		if getErr != nil {
			return IntegrationTargetRecord{}, fmt.Errorf("load existing integration target: %w", getErr)
		}
		record := integrationTargetRecordFromProviderRefSQLC(existing)
		if record.ProviderRefKind != input.ProviderRefKind || record.ParentChannelID != input.ParentChannelID ||
			record.ChannelDefinitionID != input.ChannelDefinitionID {
			return IntegrationTargetRecord{}, storeerr.ErrConflict
		}
		displayName := input.DisplayName
		if displayName == "" {
			displayName = record.DisplayName
		}
		if !providerMetadataProvided {
			input.ProviderMetadata = record.ProviderMetadata
		}
		if displayName == record.DisplayName &&
			storeutil.SameJSON(record.ProviderMetadata, input.ProviderMetadata) {
			return record, nil
		}
		updated, updateErr := q.UpdateResolvedIntegrationTarget(
			ctx,
			dbsqlc.UpdateResolvedIntegrationTargetParams{
				ProjectID: input.ProjectID, ID: record.ID,
				ProviderRefKind:  input.ProviderRefKind,
				DisplayName:      displayName,
				ProviderMetadata: input.ProviderMetadata,
			},
		)
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrConflict
		}
		if updateErr != nil {
			return IntegrationTargetRecord{}, integrationChannelWriteError(
				"update resolved integration target",
				updateErr,
			)
		}
		return integrationTargetRecordFromInsertSQLC(updated, install.OrgID), nil
	}
	return IntegrationTargetRecord{}, storeerr.ErrConflict
}

func newIntegrationTargetRef(provider string) (string, error) {
	var randomBytes [12]byte
	if _, err := io.ReadFull(rand.Reader, randomBytes[:]); err != nil {
		return "", fmt.Errorf("generate integration target ref: %w", err)
	}
	var ref strings.Builder
	ref.Grow(len(provider) + 1 + len(randomBytes))
	ref.WriteString(provider)
	ref.WriteByte('-')
	for _, value := range randomBytes {
		ref.WriteByte(integrationTargetRefAlphabet[int(value)%len(integrationTargetRefAlphabet)])
	}
	return ref.String(), nil
}

const integrationTargetRefAlphabet = "abcdefghijklmnpqrstvwxyz23456789"

func (s *Store) UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
	ctx context.Context,
	projectID, installID ID,
	providerRefPrefix, displayName string,
) error {
	if isNilID(projectID) || isNilID(installID) || providerRefPrefix == "" || displayName == "" {
		return errors.New("project, integration install, provider ref prefix, and display name are required")
	}
	if len(providerRefPrefix) > 2048 || len(displayName) > 512 {
		return errors.New("integration target update exceeds its size limit")
	}
	_, err := s.q.UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
		ctx,
		dbsqlc.UpdateIntegrationTargetDisplayNamesByProviderRefPrefixParams{
			ProjectID:            projectID,
			IntegrationInstallID: installID,
			ProviderRefPrefix:    providerRefPrefix,
			DisplayName:          displayName,
		},
	)
	if err != nil {
		return fmt.Errorf("update integration target display names: %w", err)
	}
	return nil
}

func (s *Store) GetIntegrationTarget(
	ctx context.Context,
	projectID, id ID,
) (IntegrationTargetRecord, error) {
	return getIntegrationTarget(ctx, s.q, projectID, id)
}

func (s *Store) GetIntegrationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, id ID,
) (IntegrationTargetRecord, error) {
	return getIntegrationTarget(ctx, dbsqlc.New(tx), projectID, id)
}

func getIntegrationTarget(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id ID,
) (IntegrationTargetRecord, error) {
	row, err := q.GetIntegrationTarget(
		ctx,
		dbsqlc.GetIntegrationTargetParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrNotFound
		}
		return IntegrationTargetRecord{}, fmt.Errorf("get integration target: %w", err)
	}
	return integrationTargetRecordFromGetSQLC(row), nil
}

func (s *Store) GetIntegrationTargetByProviderRef(
	ctx context.Context,
	projectID, integrationInstallID ID,
	providerRef string,
) (IntegrationTargetRecord, error) {
	return getIntegrationTargetByProviderRef(ctx, s.q, projectID, integrationInstallID, providerRef)
}

// GetIntegrationTargetByProviderRefTx resolves an existing address in the caller's
// transaction. Callers must separately check live agent access before exposing it.
func (s *Store) GetIntegrationTargetByProviderRefTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, integrationInstallID ID,
	providerRef string,
) (IntegrationTargetRecord, error) {
	if tx == nil {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(errors.New("transaction is required"))
	}
	return getIntegrationTargetByProviderRef(ctx, dbsqlc.New(tx), projectID, integrationInstallID, providerRef)
}

func getIntegrationTargetByProviderRef(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, integrationInstallID ID,
	providerRef string,
) (IntegrationTargetRecord, error) {
	row, err := q.GetIntegrationTargetByProviderRef(
		ctx,
		dbsqlc.GetIntegrationTargetByProviderRefParams{
			ProjectID:            projectID,
			IntegrationInstallID: integrationInstallID,
			ProviderRef:          providerRef,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrNotFound
		}
		return IntegrationTargetRecord{}, fmt.Errorf("get integration target by provider ref: %w", err)
	}
	return integrationTargetRecordFromProviderRefSQLC(row), nil
}

func (s *Store) ListIntegrationTargets(
	ctx context.Context,
	projectID, agentID ID,
) ([]IntegrationTargetSummary, error) {
	return listIntegrationTargets(ctx, s.q, projectID, agentID)
}

func (s *Store) ListIntegrationTargetsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
) ([]IntegrationTargetSummary, error) {
	return listIntegrationTargets(ctx, dbsqlc.New(tx), projectID, agentID)
}

func listIntegrationTargets(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
) ([]IntegrationTargetSummary, error) {
	rows, err := q.ListIntegrationTargets(
		ctx,
		dbsqlc.ListIntegrationTargetsParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list integration targets: %w", err)
	}
	out := make([]IntegrationTargetSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, integrationTargetSummaryFromSQLC(row))
	}
	return out, nil
}

func integrationTargetRecordFromInsertSQLC(
	row dbsqlc.IntegrationTarget,
	orgID ID,
) IntegrationTargetRecord {
	return integrationTargetRecordFromFields(
		row.ID, orgID, row.ProjectID, row.IntegrationInstallID,
		row.TargetRef, row.ProviderRef, row.ProviderRefKind, row.ParentChannelID, row.ChannelDefinitionID, row.DisplayName,
		row.ProviderMetadata, row.CreatedAt, row.UpdatedAt,
	)
}

func integrationTargetRecordFromGetSQLC(
	row dbsqlc.GetIntegrationTargetRow,
) IntegrationTargetRecord {
	return integrationTargetRecordFromFields(
		row.ID, row.OrgID, row.ProjectID, row.IntegrationInstallID,
		row.TargetRef, row.ProviderRef, row.ProviderRefKind, row.ParentChannelID, row.ChannelDefinitionID, row.DisplayName,
		row.ProviderMetadata, row.CreatedAt, row.UpdatedAt,
	)
}

func integrationTargetRecordFromProviderRefSQLC(
	row dbsqlc.GetIntegrationTargetByProviderRefRow,
) IntegrationTargetRecord {
	return integrationTargetRecordFromFields(
		row.ID, row.OrgID, row.ProjectID, row.IntegrationInstallID,
		row.TargetRef, row.ProviderRef, row.ProviderRefKind, row.ParentChannelID, row.ChannelDefinitionID, row.DisplayName,
		row.ProviderMetadata, row.CreatedAt, row.UpdatedAt,
	)
}

func integrationTargetRecordFromFields(
	id, orgID, projectID ID,
	integrationInstallID ID,
	targetRef, providerRef, providerRefKind string,
	parentChannelID *ID,
	channelDefinitionID ID,
	displayName string,
	providerMetadata json.RawMessage,
	createdAt, updatedAt time.Time,
) IntegrationTargetRecord {
	return IntegrationTargetRecord{
		ID:                   id,
		OrgID:                orgID,
		ProjectID:            projectID,
		IntegrationInstallID: integrationInstallID,
		TargetRef:            targetRef,
		ProviderRef:          providerRef,
		ProviderRefKind:      providerRefKind,
		ParentChannelID:      idFromSQLCPtr(parentChannelID),
		ChannelDefinitionID:  channelDefinitionID,
		DisplayName:          displayName,
		ProviderMetadata:     providerMetadata,
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}
}

func integrationTargetSummaryFromSQLC(row dbsqlc.ListIntegrationTargetsRow) IntegrationTargetSummary {
	return IntegrationTargetSummary{
		ID:                   row.ID,
		IntegrationInstallID: row.IntegrationInstallID,
		TargetRef:            row.TargetRef,
		Provider:             stringFromPtr(row.Provider),
		InstallState:         IntegrationInstallState(row.InstallState),
		ProviderRef:          row.ProviderRef,
		ProviderRefKind:      row.ProviderRefKind,
		DisplayName:          row.DisplayName,
		IsCurrent:            row.IsCurrent,
	}
}
