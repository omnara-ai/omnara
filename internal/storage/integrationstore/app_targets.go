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
	ProjectID, AgentID, ConnectionID uuid.UUID
	Address                          ConversationAddress
	DisplayName                      string
	Role                             TargetRoutingRole
	AppID                            uuid.UUID
	SelectionSlot                    string
}

// LockConversationTx must precede agent locks, after the project and all
// connection gates. Planning and confirmed follows use this same lock.
func LockConversationTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, connectionID uuid.UUID,
	address ConversationAddress,
) error {
	if projectID == uuid.Nil || connectionID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project and connection are required"))
	}
	if err := address.Validate(); err != nil {
		return err
	}
	return dbsqlc.New(tx).LockAppConversation(ctx, dbsqlc.LockAppConversationParams{
		ProjectID: projectID, ConnectionID: connectionID, Kind: address.Kind, Ref: address.Ref,
	})
}

// EnsureConversationTargetTx records attribution/selection, never authority or a
// subscription. Caller holds project, connection, conversation and agent gates;
// the agent may have been inserted earlier in this same launch transaction.
func (s *Store) EnsureConversationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	input EnsureConversationTargetInput,
) (IntegrationTargetRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ConnectionID == uuid.Nil {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("project, agent and connection are required"),
		)
	}
	if err := input.Address.Validate(); err != nil {
		return IntegrationTargetRecord{}, err
	}
	if input.Role != TargetAttribution && input.Role != TargetSelected && input.Role != TargetFollowed {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(errors.New("invalid target routing role"))
	}
	if (input.Role == TargetSelected && (input.AppID == uuid.Nil || input.SelectionSlot == "")) ||
		(input.Role != TargetSelected && (input.AppID != uuid.Nil || input.SelectionSlot != "")) {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("only a selected target requires an app and slot"),
		)
	}
	q := dbsqlc.New(tx)
	connection, err := getIntegrationConnection(ctx, q, input.ProjectID, input.ConnectionID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if connection.State != IntegrationConnectionStateActive {
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
	for range 5 {
		var existing dbsqlc.GetAgentConversationTargetRow
		if input.Role == TargetSelected {
			row, findErr := q.GetAppSelectionTarget(ctx, dbsqlc.GetAppSelectionTargetParams{
				ProjectID:    input.ProjectID,
				ConnectionID: input.ConnectionID,
				Kind:         input.Address.Kind,
				Ref:          input.Address.Ref,
				AppID:        &input.AppID,
				Slot:         &input.SelectionSlot,
			})
			existing, err = dbsqlc.GetAgentConversationTargetRow(row), findErr
		} else {
			existing, err = q.GetAgentConversationTarget(ctx, dbsqlc.GetAgentConversationTargetParams{
				ProjectID: input.ProjectID, AgentID: input.AgentID, ConnectionID: input.ConnectionID,
				Kind: input.Address.Kind, Ref: input.Address.Ref,
			})
		}
		if err == nil {
			if existing.AgentID != input.AgentID || existing.DeletedAt != nil {
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
			return appTargetRecord(existing, connection.OrgID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, err
		}
		if input.Role == TargetSelected {
			// Profile launches create a fresh agent for each selected slot.
			// Existing-agent launch slots are ordinary triggers and use an
			// attribution target, never a second selection on the same agent.
			_, err := q.GetAgentConversationTarget(ctx, dbsqlc.GetAgentConversationTargetParams{
				ProjectID: input.ProjectID, AgentID: input.AgentID, ConnectionID: input.ConnectionID,
				Kind: input.Address.Kind, Ref: input.Address.Ref,
			})
			if err == nil {
				return IntegrationTargetRecord{}, storeerr.ErrConflict
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return IntegrationTargetRecord{}, err
			}
		}
		ref, err := s.targetRefGenerator(connection.Provider)
		if err != nil {
			return IntegrationTargetRecord{}, err
		}
		row, err := q.InsertAppConversationTarget(ctx, dbsqlc.InsertAppConversationTargetParams{
			ProjectID:    input.ProjectID,
			AgentID:      input.AgentID,
			ConnectionID: input.ConnectionID,
			Kind:         input.Address.Kind,
			Ref:          input.Address.Ref,
			DisplayName:  strings.TrimSpace(input.DisplayName),
			TargetRef:    ref,
			RoutingRole:  string(input.Role),
			AppID:        storeutil.IDFromNil(input.AppID),
			Slot:         storeutil.TextFromEmpty(input.SelectionSlot),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // A concurrent selection or the short display reference collided.
		}
		if err != nil {
			return IntegrationTargetRecord{}, fmt.Errorf("create conversation target: %w", err)
		}
		result := appTargetRecord(dbsqlc.GetAgentConversationTargetRow(row), connection.OrgID)
		result.Created = true
		return result, nil
	}
	return IntegrationTargetRecord{}, storeerr.ErrConflict
}

func appTargetRecord(row dbsqlc.GetAgentConversationTargetRow, orgID uuid.UUID) IntegrationTargetRecord {
	record := integrationTargetRecordFromFields(
		row.ID,
		orgID,
		row.ProjectID,
		row.AgentID,
		row.IntegrationConnectionID,
		row.TargetRef,
		row.ProviderRef,
		row.ProviderRefKind,
		row.DisplayName,
		row.ProviderMetadata,
		row.CreatedAt,
		row.UpdatedAt,
	)
	record.RoutingRole, record.DeletedAt = TargetRoutingRole(row.RoutingRole), row.DeletedAt
	if row.AppID != nil {
		record.AppID = *row.AppID
	}
	if row.SelectionSlot != nil {
		record.SelectionSlot = *row.SelectionSlot
	}
	return record
}
