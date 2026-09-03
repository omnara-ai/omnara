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
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CreateIntegrationTarget supports the native integration compatibility path,
// which still projects its creator onto integration_targets.agent_id.
func (s *Store) CreateIntegrationTarget(
	ctx context.Context,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	return s.createIntegrationTargetInTransaction(ctx, input, false)
}

// GetOrCreateIntegrationTargetForBinding resolves a project-owned external address
// without assigning agent ownership. AgentID is the attaching authority context;
// the caller must create an explicit binding before using the target for input or output.
func (s *Store) GetOrCreateIntegrationTargetForBinding(
	ctx context.Context,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	return s.createIntegrationTargetInTransaction(ctx, input, true)
}

func (s *Store) GetOrCreateIntegrationTargetForBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	if tx == nil {
		return IntegrationTargetRecord{}, errors.New("transaction is required")
	}
	return s.createIntegrationTarget(ctx, dbsqlc.New(tx), input, true)
}

func (s *Store) createIntegrationTargetInTransaction(
	ctx context.Context,
	input CreateIntegrationTargetInput,
	bindingManaged bool,
) (IntegrationTargetRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("begin create integration target: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := s.createIntegrationTarget(
		ctx,
		dbsqlc.New(tx),
		input,
		bindingManaged,
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
	q *dbsqlc.Queries,
	input CreateIntegrationTargetInput,
	bindingManaged bool,
) (IntegrationTargetRecord, error) {
	input.ProviderRef = strings.TrimSpace(input.ProviderRef)
	input.ProviderRefKind = strings.TrimSpace(input.ProviderRefKind)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.IntegrationInstallID) ||
		input.ProviderRef == "" || input.ProviderRefKind == "" {
		return IntegrationTargetRecord{}, errors.New(
			"project, agent, integration install, provider ref, and provider ref kind are required",
		)
	}
	if len(input.ProviderRef) > 2048 || len(input.ProviderRefKind) > 128 ||
		len(input.DisplayName) > 512 {
		return IntegrationTargetRecord{}, errors.New("integration target identifier exceeds its size limit")
	}
	providerMetadataProvided := len(input.ProviderMetadata) != 0
	providerMetadata, err := normalizedJSONObject(input.ProviderMetadata, "provider_metadata")
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	input.ProviderMetadata = providerMetadata
	install, err := getIntegrationInstall(ctx, q, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if install.State != IntegrationInstallStateActive {
		return IntegrationTargetRecord{}, storeerr.ErrUnauthorized
	}
	// This method is the native-integration compatibility path, where the
	// target's agent_id projection is still required by the existing Slack
	// implementation. Connector installations are project-owned and must use
	// GetOrCreateIntegrationTargetForBinding so authorization lives only in
	// integration_target_bindings.
	if !bindingManaged && isNilID(install.AgentID) && isNilID(install.AgentProfileID) {
		return IntegrationTargetRecord{}, errors.New(
			"connector integration targets require binding-aware resolution",
		)
	}
	if !bindingManaged && !isNilID(install.AgentID) && install.AgentID != input.AgentID {
		return IntegrationTargetRecord{}, storeerr.ErrConflict
	}
	if _, err := q.LockIntegrationTargetCreateAuthority(
		ctx,
		dbsqlc.LockIntegrationTargetCreateAuthorityParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID,
			IntegrationInstallID: input.IntegrationInstallID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrUnauthorized
		}
		return IntegrationTargetRecord{}, fmt.Errorf("lock integration target authority: %w", err)
	}
	var row dbsqlc.IntegrationTarget
	creatorAgentID := input.AgentID
	if bindingManaged {
		creatorAgentID = NilID
	}
	for range 5 {
		targetRef, refErr := newIntegrationTargetRef(install.Provider)
		if refErr != nil {
			return IntegrationTargetRecord{}, refErr
		}
		row, err = q.InsertIntegrationTarget(ctx, dbsqlc.InsertIntegrationTargetParams{
			ProjectID:            input.ProjectID,
			AgentID:              sqlcIDFromNil(creatorAgentID),
			IntegrationInstallID: input.IntegrationInstallID,
			TargetRef:            targetRef,
			ProviderRef:          input.ProviderRef,
			ProviderRefKind:      input.ProviderRefKind,
			DisplayName:          input.DisplayName,
			ProviderMetadata:     input.ProviderMetadata,
		})
		if !storeutil.IsUniqueViolationOnConstraint(err, "integration_targets_agent_target_ref_idx") {
			break
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := q.GetIntegrationTargetByProviderRef(
			ctx,
			dbsqlc.GetIntegrationTargetByProviderRefParams{
				ProjectID:            input.ProjectID,
				IntegrationInstallID: input.IntegrationInstallID,
				ProviderRef:          input.ProviderRef,
			},
		)
		if errors.Is(getErr, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrNotFound
		}
		if getErr != nil {
			return IntegrationTargetRecord{}, fmt.Errorf("load existing integration target: %w", getErr)
		}
		record := integrationTargetRecordFromProviderRefSQLC(existing)
		if (bindingManaged && !isNilID(record.AgentID)) ||
			(!bindingManaged && record.AgentID != input.AgentID) ||
			record.ProviderRefKind != input.ProviderRefKind {
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
	if err != nil {
		return IntegrationTargetRecord{}, integrationChannelWriteError(
			"insert integration target",
			err,
		)
	}
	record := integrationTargetRecordFromInsertSQLC(row, install.OrgID)
	record.Created = true
	return record, nil
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
	row, err := s.q.GetIntegrationTargetByProviderRef(
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
		dbsqlc.ListIntegrationTargetsParams{ProjectID: projectID, AgentID: &agentID},
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
		row.ID, orgID, row.ProjectID, row.AgentID, row.IntegrationInstallID,
		row.TargetRef, row.ProviderRef, row.ProviderRefKind, row.DisplayName,
		row.ProviderMetadata, row.CreatedAt, row.UpdatedAt,
	)
}

func integrationTargetRecordFromGetSQLC(
	row dbsqlc.GetIntegrationTargetRow,
) IntegrationTargetRecord {
	return integrationTargetRecordFromFields(
		row.ID, row.OrgID, row.ProjectID, row.AgentID, row.IntegrationInstallID,
		row.TargetRef, row.ProviderRef, row.ProviderRefKind, row.DisplayName,
		row.ProviderMetadata, row.CreatedAt, row.UpdatedAt,
	)
}

func integrationTargetRecordFromProviderRefSQLC(
	row dbsqlc.GetIntegrationTargetByProviderRefRow,
) IntegrationTargetRecord {
	return integrationTargetRecordFromFields(
		row.ID, row.OrgID, row.ProjectID, row.AgentID, row.IntegrationInstallID,
		row.TargetRef, row.ProviderRef, row.ProviderRefKind, row.DisplayName,
		row.ProviderMetadata, row.CreatedAt, row.UpdatedAt,
	)
}

func integrationTargetRecordFromFields(
	id, orgID, projectID ID,
	agentID *ID,
	integrationInstallID ID,
	targetRef, providerRef, providerRefKind, displayName string,
	providerMetadata json.RawMessage,
	createdAt, updatedAt time.Time,
) IntegrationTargetRecord {
	return IntegrationTargetRecord{
		ID:                   id,
		OrgID:                orgID,
		ProjectID:            projectID,
		AgentID:              idFromSQLCPtr(agentID),
		IntegrationInstallID: integrationInstallID,
		TargetRef:            targetRef,
		ProviderRef:          providerRef,
		ProviderRefKind:      providerRefKind,
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
		Provider:             row.Provider,
		InstallState:         IntegrationInstallState(row.InstallState),
		ProviderRef:          row.ProviderRef,
		ProviderRefKind:      row.ProviderRefKind,
		DisplayName:          row.DisplayName,
		IsCurrent:            row.IsCurrent,
	}
}
