package appstore

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

// AssignAgentAppConversationTx requires the app lifecycle gate and either the
// agent lock or the agent's uncommitted insert in this transaction.
func (s *Store) AssignAgentAppConversationTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID, appID uuid.UUID, address ConversationAddress,
) error {
	if projectID == uuid.Nil || agentID == uuid.Nil || appID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project, agent and app are required"))
	}
	if err := address.Validate(); err != nil {
		return err
	}
	data, err := encodeAppState(address)
	if err != nil {
		return err
	}
	rows, err := dbsqlc.New(tx).InsertAgentAppConversation(ctx, dbsqlc.InsertAgentAppConversationParams{
		ProjectID: projectID, AgentID: agentID, AppID: appID, Kind: agentConversationStateKind, Data: data,
	})
	if storeutil.IsUniqueViolationOnConstraint(err, "app_states_project_id_app_id_kind_key_key") {
		return storeerr.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("assign agent app conversation: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	return nil
}

func (s *Store) GetAgentAppConversation(
	ctx context.Context, projectID, agentID, appID uuid.UUID,
) (ConversationAddress, bool, error) {
	state, err := s.q.GetAppStateByKey(ctx, dbsqlc.GetAppStateByKeyParams{
		ProjectID: projectID, AppID: appID, Kind: agentConversationStateKind, Key: agentID.String(),
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
		return address, false, fmt.Errorf("decode agent app conversation: %w", err)
	}
	if err := address.Validate(); err != nil {
		return address, false, err
	}
	return address, true, nil
}
