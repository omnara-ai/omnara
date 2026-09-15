package executionstore

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ChannelInputPrecondition protects a behavior's content choice when two
// callbacks describe the same message. A failed condition requires the behavior
// to read current state and render again, not retry the stale body unchanged.
type ChannelInputPrecondition struct {
	InputKey string
	Exists   bool
}

var ErrChannelInputPreconditionChanged = errors.New(
	"channel input precondition changed; read current state and render again")

const maxChannelInputKeyBytes = 512
const maxChannelLookupInputKeys = 20

func validateChannelInputKey(key string) error {
	if strings.TrimSpace(key) == "" || len(key) > maxChannelInputKeyBytes {
		return storeerr.InvalidRequest(errors.New("channel input key must contain 1 to 512 UTF-8 bytes"))
	}
	if err := dbsafe.Text(key); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}

func validateChannelInputKeys(key string, condition *ChannelInputPrecondition) error {
	if err := validateChannelInputKey(key); err != nil {
		return err
	}
	if condition != nil {
		return validateChannelInputKey(condition.InputKey)
	}
	return nil
}

func checkChannelInputPrecondition(
	ctx context.Context, tx pgx.Tx, projectID, agentID uuid.UUID, scope string, condition *ChannelInputPrecondition,
) error {
	if condition == nil {
		return nil
	}
	_, found, err := loadAgentInputByIdempotencyMaybeTx(ctx, tx, projectID, agentID, scope, condition.InputKey)
	if err != nil {
		return err
	}
	if found != condition.Exists {
		return ErrChannelInputPreconditionChanged
	}
	return nil
}

type LookupChannelWorkflowResult struct {
	Exists     bool
	AgentState AgentState
	InputKeys  []string
}

// LookupChannelWorkflow is a scoped observation, never permission to create an
// input. Admission repeats authorization and the chosen existence condition in
// its transaction. No media is downloaded and no provisional agent is persisted.
func (s *Store) LookupChannelWorkflow(
	ctx context.Context, identity ChannelWorkflowIdentity, inputKeys []string,
) (LookupChannelWorkflowResult, error) {
	if err := validateChannelLookupKeys(inputKeys); err != nil {
		return LookupChannelWorkflowResult{}, err
	}
	prepared, err := s.resolveChannelWorkflow(ctx, identity)
	if err != nil {
		return LookupChannelWorkflowResult{}, err
	}
	result := LookupChannelWorkflowResult{Exists: prepared.exists, InputKeys: []string{}}
	if !prepared.exists {
		return result, nil
	}
	agent, err := s.GetAgentInProject(ctx, identity.ProjectID, prepared.agentID)
	if err != nil {
		return LookupChannelWorkflowResult{}, err
	}
	result.AgentState = agent.State
	result.InputKeys, err = s.q.GetExistingChannelInputKeys(ctx, dbsqlc.GetExistingChannelInputKeysParams{
		ProjectID: identity.ProjectID, AgentID: prepared.agentID,
		IdempotencyScope: prepared.inputScope, InputKeys: inputKeys,
	})
	if err != nil {
		return LookupChannelWorkflowResult{}, err
	}
	return result, nil
}

func validateChannelLookupKeys(inputKeys []string) error {
	if len(inputKeys) > maxChannelLookupInputKeys {
		return storeerr.InvalidRequest(errors.New("too many channel input keys"))
	}
	seen := make(map[string]bool, len(inputKeys))
	for _, key := range inputKeys {
		if err := validateChannelInputKey(key); err != nil {
			return err
		}
		if seen[key] {
			return storeerr.InvalidRequest(errors.New("duplicate channel input key"))
		}
		seen[key] = true
	}
	return nil
}
