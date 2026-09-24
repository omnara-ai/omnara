package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ConversationAddress struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

func (a ConversationAddress) Validate() error {
	if a.Kind == "" || len(a.Kind) > 128 || a.Ref == "" || len(a.Ref) > 2048 {
		return storeerr.InvalidRequest(
			errors.New("conversation requires a kind (at most 128 bytes) and address (at most 2048 bytes)"),
		)
	}
	if err := dbsafe.Text(a.Kind); err != nil {
		return storeerr.InvalidRequest(err)
	}
	if err := dbsafe.Text(a.Ref); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}

type EnsureConversationTargetInput struct {
	ProjectID, AgentID, IntegrationID uuid.UUID
	Address                           ConversationAddress
	DisplayName                       string
	SelectionSlot                     string
}

func LockConversationTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, integrationID uuid.UUID,
	address ConversationAddress,
) error {
	if projectID == uuid.Nil || integrationID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project and integration are required"))
	}
	if err := address.Validate(); err != nil {
		return err
	}
	return dbsqlc.New(tx).LockIntegrationConversation(ctx, dbsqlc.LockIntegrationConversationParams{
		ProjectID: projectID, IntegrationID: integrationID, Kind: address.Kind, Ref: address.Ref,
	})
}

func (s *Store) EnsureConversationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	input EnsureConversationTargetInput,
) (IntegrationTargetRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.IntegrationID == uuid.Nil {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("project, agent and integration are required"),
		)
	}
	if err := input.Address.Validate(); err != nil {
		return IntegrationTargetRecord{}, err
	}
	q := dbsqlc.New(tx)
	integration, err := getProjectIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if integration.State != ProjectIntegrationStateActive {
		return IntegrationTargetRecord{}, storeerr.ErrUnauthorized
	}
	agent, err := q.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if agent.State != "active" {
		return IntegrationTargetRecord{}, storeerr.ErrStateTransitionConflict
	}
	var existing dbsqlc.GetAgentConversationTargetRow
	if input.SelectionSlot != "" {
		row, findErr := q.GetIntegrationSelectionTarget(ctx, dbsqlc.GetIntegrationSelectionTargetParams{
			ProjectID:     input.ProjectID,
			IntegrationID: input.IntegrationID,
			Kind:          input.Address.Kind,
			Ref:           input.Address.Ref,
			Slot:          &input.SelectionSlot,
		})
		existing, err = dbsqlc.GetAgentConversationTargetRow(row), findErr
	} else {
		existing, err = q.GetAgentConversationTarget(ctx, dbsqlc.GetAgentConversationTargetParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: input.IntegrationID,
			Kind: input.Address.Kind, Ref: input.Address.Ref,
		})
	}
	if err == nil {
		if existing.AgentID != input.AgentID || existing.DeletedAt != nil {
			return IntegrationTargetRecord{}, storeerr.ErrConflict
		}
		return integrationTargetRecord(existing, integration.OrgID), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetRecord{}, err
	}
	if input.SelectionSlot != "" {
		_, err := q.GetAgentConversationTarget(ctx, dbsqlc.GetAgentConversationTargetParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: input.IntegrationID,
			Kind: input.Address.Kind, Ref: input.Address.Ref,
		})
		if err == nil {
			return IntegrationTargetRecord{}, storeerr.ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, err
		}
	}
	row, err := q.InsertIntegrationConversationTarget(ctx, dbsqlc.InsertIntegrationConversationTargetParams{
		ProjectID:     input.ProjectID,
		AgentID:       input.AgentID,
		IntegrationID: input.IntegrationID,
		Kind:          input.Address.Kind,
		Ref:           input.Address.Ref,
		DisplayName:   strings.TrimSpace(input.DisplayName),
		Slot:          storeutil.TextFromEmpty(input.SelectionSlot),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetRecord{}, storeerr.ErrConflict
	}
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("create conversation target: %w", err)
	}
	result := integrationTargetRecord(dbsqlc.GetAgentConversationTargetRow(row), integration.OrgID)
	result.Created = true
	return result, nil
}

func integrationTargetRecord(row dbsqlc.GetAgentConversationTargetRow, orgID uuid.UUID) IntegrationTargetRecord {
	record := IntegrationTargetRecord{
		ID: row.ID, OrgID: orgID, ProjectID: row.ProjectID, AgentID: row.AgentID, IntegrationID: row.IntegrationID,
		ProviderRef: row.ProviderRef, ProviderRefKind: row.ProviderRefKind,
		DisplayName: row.DisplayName, ProviderMetadata: row.ProviderMetadata,
		DeletedAt: row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.SelectionSlot != nil {
		record.SelectionSlot = *row.SelectionSlot
	}
	return record
}
