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

type TargetRoutingRole string

const (
	TargetAttribution TargetRoutingRole = "attribution"
	TargetSelected    TargetRoutingRole = "selected"
	TargetFollowed    TargetRoutingRole = "followed"
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
	ProjectID, AgentID, AppID uuid.UUID
	Address                   ConversationAddress
	DisplayName               string
	Role                      TargetRoutingRole
	SelectionSlot             string
	IsToolContext             bool
}

// LockConversationTx must precede agent locks, after the project and all
// app gates. Planning and confirmed follows use this same lock.
func LockConversationTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, appID uuid.UUID,
	address ConversationAddress,
) error {
	if projectID == uuid.Nil || appID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project and app are required"))
	}
	if err := address.Validate(); err != nil {
		return err
	}
	return dbsqlc.New(tx).LockAppConversation(ctx, dbsqlc.LockAppConversationParams{
		ProjectID: projectID, AppID: appID, Kind: address.Kind, Ref: address.Ref,
	})
}

// EnsureConversationTargetTx records attribution/selection and may bind the
// initial target as an immutable tool context, never a credential grant or
// subscription. Caller holds project, app, conversation and agent gates;
// the agent may have been inserted earlier in this same launch transaction.
func (s *Store) EnsureConversationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	input EnsureConversationTargetInput,
) (IntegrationTargetRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.AppID == uuid.Nil {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("project, agent and app are required"),
		)
	}
	if err := input.Address.Validate(); err != nil {
		return IntegrationTargetRecord{}, err
	}
	if input.Role != TargetAttribution && input.Role != TargetSelected && input.Role != TargetFollowed {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(errors.New("invalid target routing role"))
	}
	if (input.Role == TargetSelected) != (input.SelectionSlot != "") {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("only a selected target requires a slot"),
		)
	}
	q := dbsqlc.New(tx)
	app, err := getProjectApp(ctx, q, input.ProjectID, input.AppID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if app.State != ProjectAppStateActive {
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
	if input.Role == TargetSelected {
		row, findErr := q.GetAppSelectionTarget(ctx, dbsqlc.GetAppSelectionTargetParams{
			ProjectID: input.ProjectID,
			AppID:     input.AppID,
			Kind:      input.Address.Kind,
			Ref:       input.Address.Ref,
			Slot:      &input.SelectionSlot,
		})
		existing, err = dbsqlc.GetAgentConversationTargetRow(row), findErr
	} else {
		existing, err = q.GetAgentConversationTarget(ctx, dbsqlc.GetAgentConversationTargetParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, AppID: input.AppID,
			Kind: input.Address.Kind, Ref: input.Address.Ref,
		})
	}
	if err == nil {
		if existing.AgentID != input.AgentID || existing.DeletedAt != nil ||
			(input.IsToolContext && !existing.IsToolContext) {
			return IntegrationTargetRecord{}, storeerr.ErrConflict
		}
		if input.Role == TargetFollowed && existing.RoutingRole == string(TargetAttribution) {
			if err := q.MarkConversationTargetFollowed(
				ctx,
				dbsqlc.MarkConversationTargetFollowedParams{
					ProjectID: input.ProjectID,
					AgentID:   input.AgentID,
					ID:        existing.ID,
				},
			); err != nil {
				return IntegrationTargetRecord{}, err
			}
			existing.RoutingRole = string(TargetFollowed)
		}
		return appTargetRecord(existing, app.OrgID), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetRecord{}, err
	}
	if input.Role == TargetSelected {
		// Profile launches create a fresh agent for each selected slot.
		// Existing-agent launch slots are ordinary triggers and use an
		// attribution target, never a second selection on the same agent.
		_, err := q.GetAgentConversationTarget(ctx, dbsqlc.GetAgentConversationTargetParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, AppID: input.AppID,
			Kind: input.Address.Kind, Ref: input.Address.Ref,
		})
		if err == nil {
			return IntegrationTargetRecord{}, storeerr.ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, err
		}
	}
	row, err := q.InsertAppConversationTarget(ctx, dbsqlc.InsertAppConversationTargetParams{
		ProjectID:     input.ProjectID,
		AgentID:       input.AgentID,
		AppID:         input.AppID,
		Kind:          input.Address.Kind,
		Ref:           input.Address.Ref,
		DisplayName:   strings.TrimSpace(input.DisplayName),
		IsToolContext: input.IsToolContext,
		RoutingRole:   string(input.Role),
		Slot:          storeutil.TextFromEmpty(input.SelectionSlot),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetRecord{}, storeerr.ErrConflict
	}
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("create conversation target: %w", err)
	}
	result := appTargetRecord(dbsqlc.GetAgentConversationTargetRow(row), app.OrgID)
	result.Created = true
	return result, nil
}

func appTargetRecord(row dbsqlc.GetAgentConversationTargetRow, orgID uuid.UUID) IntegrationTargetRecord {
	record := IntegrationTargetRecord{
		ID: row.ID, OrgID: orgID, ProjectID: row.ProjectID, AgentID: row.AgentID, AppID: row.AppID,
		ProviderRef: row.ProviderRef, ProviderRefKind: row.ProviderRefKind,
		DisplayName: row.DisplayName, ProviderMetadata: row.ProviderMetadata,
		RoutingRole: TargetRoutingRole(row.RoutingRole), IsToolContext: row.IsToolContext,
		DeletedAt: row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.SelectionSlot != nil {
		record.SelectionSlot = *row.SelectionSlot
	}
	return record
}
