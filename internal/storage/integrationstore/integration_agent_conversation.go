package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const agentConversationStateKind = "agent_conversation"

// AssignAgentIntegrationConversationTx requires the integration lifecycle gate and either the
// agent lock or the agent's uncommitted insert in this transaction.
func (s *Store) AssignAgentIntegrationConversationTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID, integrationID uuid.UUID, address ConversationAddress,
) error {
	if projectID == uuid.Nil || agentID == uuid.Nil || integrationID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project, agent and integration are required"))
	}
	if err := address.Validate(); err != nil {
		return err
	}
	data, err := encodeIntegrationState(address)
	if err != nil {
		return err
	}
	rows, err := dbsqlc.New(tx).InsertAgentIntegrationConversation(ctx, dbsqlc.InsertAgentIntegrationConversationParams{
		ProjectID:     projectID,
		AgentID:       agentID,
		IntegrationID: integrationID,
		Kind:          agentConversationStateKind,
		Data:          data,
	})
	if storeutil.IsUniqueViolationOnConstraint(err, "integration_states_project_id_integration_id_kind_key_key") {
		return storeerr.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("assign agent integration conversation: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	return nil
}

func (s *Store) GetAgentIntegrationConversation(
	ctx context.Context, projectID, agentID, integrationID uuid.UUID,
) (ConversationAddress, bool, error) {
	return getAgentIntegrationConversation(ctx, s.q, projectID, agentID, integrationID)
}

func getAgentIntegrationConversation(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, integrationID uuid.UUID,
) (ConversationAddress, bool, error) {
	state, err := q.GetIntegrationStateByKey(ctx, dbsqlc.GetIntegrationStateByKeyParams{
		ProjectID: projectID, IntegrationID: integrationID, Kind: agentConversationStateKind, Key: agentID.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversationAddress{}, false, nil
	}
	if err != nil {
		return ConversationAddress{}, false, err
	}
	var address ConversationAddress
	decoder := json.NewDecoder(bytes.NewReader(state.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&address); err != nil {
		return address, false, storeerr.InvalidRequest(fmt.Errorf("decode agent integration conversation: %w", err))
	}
	if err := address.Validate(); err != nil {
		return address, false, err
	}
	return address, true, nil
}

type AgentIntegrationConversationTarget struct {
	ID      uuid.UUID
	Address ConversationAddress
}

func GetAgentIntegrationConversationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, integrationID uuid.UUID,
) (AgentIntegrationConversationTarget, bool, error) {
	q := dbsqlc.New(tx)
	address, found, err := getAgentIntegrationConversation(ctx, q, projectID, agentID, integrationID)
	if errors.Is(err, storeerr.ErrInvalidRequest) {
		return AgentIntegrationConversationTarget{}, false, nil
	}
	if err != nil || !found {
		return AgentIntegrationConversationTarget{}, false, err
	}
	targetID, err := q.GetAssignedIntegrationConversationTarget(
		ctx,
		dbsqlc.GetAssignedIntegrationConversationTargetParams{
			ProjectID: projectID, AgentID: agentID, IntegrationID: integrationID, Kind: address.Kind, Ref: address.Ref,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIntegrationConversationTarget{}, false, nil
	}
	if err != nil {
		return AgentIntegrationConversationTarget{}, false, err
	}
	return AgentIntegrationConversationTarget{ID: targetID, Address: address}, true, nil
}
