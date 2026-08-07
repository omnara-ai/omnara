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
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateIntegrationTarget(
	ctx context.Context,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.IntegrationInstallID) ||
		input.ProviderRef == "" || input.ProviderRefKind == "" {
		return IntegrationTargetRecord{}, errors.New(
			"project, agent, integration install, provider ref, and provider ref kind are required",
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("begin create integration target: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	install, err := getIntegrationInstall(ctx, qtx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, install.OrgID, input.ProjectID); err != nil {
		return IntegrationTargetRecord{}, err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
	}}); err != nil {
		return IntegrationTargetRecord{}, err
	}
	agent, err := qtx.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{
		ProjectID: input.ProjectID,
		ID:        input.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("revalidate integration target agent: %w", err)
	}
	if agent.State != "active" {
		return IntegrationTargetRecord{}, storeerr.ErrStateTransitionConflict
	}
	if _, err := qtx.LockIntegrationInstallForMutation(
		ctx,
		dbsqlc.LockIntegrationInstallForMutationParams{
			ProjectID: input.ProjectID,
			ID:        input.IntegrationInstallID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrNotFound
		}
		return IntegrationTargetRecord{}, fmt.Errorf("lock integration install for target: %w", err)
	}
	install, err = getIntegrationInstall(ctx, qtx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if install.State != IntegrationInstallStateActive {
		return IntegrationTargetRecord{}, storeerr.ErrUnauthorized
	}
	if !isNilID(install.AgentID) && install.AgentID != input.AgentID {
		return IntegrationTargetRecord{}, storeerr.ErrConflict
	}
	var row dbsqlc.IntegrationTarget
	for range 5 {
		targetRef, refErr := newIntegrationTargetRef(install.Provider)
		if refErr != nil {
			return IntegrationTargetRecord{}, refErr
		}
		row, err = qtx.InsertIntegrationTarget(ctx, dbsqlc.InsertIntegrationTargetParams{
			ProjectID:            input.ProjectID,
			AgentID:              input.AgentID,
			IntegrationInstallID: input.IntegrationInstallID,
			TargetRef:            targetRef,
			ProviderRef:          input.ProviderRef,
			ProviderRefKind:      input.ProviderRefKind,
			DisplayName:          strings.TrimSpace(input.DisplayName),
		})
		if !storeutil.IsUniqueViolationOnConstraint(err, "integration_targets_agent_target_ref_idx") {
			break
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := qtx.GetIntegrationTargetByProviderRef(
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
		if record.AgentID != input.AgentID || record.ProviderRefKind != input.ProviderRefKind {
			return IntegrationTargetRecord{}, storeerr.ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return IntegrationTargetRecord{}, fmt.Errorf("commit existing integration target: %w", err)
		}
		return record, nil
	}
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("insert integration target: %w", err)
	}
	record := integrationTargetRecordFromInsertSQLC(row, install.OrgID)
	record.Created = true
	if err := tx.Commit(ctx); err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("commit create integration target: %w", err)
	}
	return record, nil
}

func newIntegrationTargetRef(provider string) (string, error) {
	var buf [4]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return "", fmt.Errorf("generate integration target ref: %w", err)
	}
	return fmt.Sprintf("%s-%c%c%c%c", provider,
		integrationTargetRefAlphabet[int(buf[0])%len(integrationTargetRefAlphabet)],
		integrationTargetRefAlphabet[int(buf[1])%len(integrationTargetRefAlphabet)],
		integrationTargetRefAlphabet[int(buf[2])%len(integrationTargetRefAlphabet)],
		integrationTargetRefAlphabet[int(buf[3])%len(integrationTargetRefAlphabet)]), nil
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
	id, orgID, projectID, agentID, integrationInstallID ID,
	targetRef, providerRef, providerRefKind, displayName string,
	providerMetadata json.RawMessage,
	createdAt, updatedAt time.Time,
) IntegrationTargetRecord {
	return IntegrationTargetRecord{
		ID:                   id,
		OrgID:                orgID,
		ProjectID:            projectID,
		AgentID:              agentID,
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
