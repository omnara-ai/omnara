package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func launchIntegrationIDsTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input LaunchAgentInput,
) ([]uuid.UUID, error) {
	var config AgentConfigRecord
	if input.DerivedConfig != nil {
		derived := withDefaultAgentConfigCompilation(*input.DerivedConfig)
		config = AgentConfigRecord{
			CompiledDefinition:      derived.CompiledDefinition,
			EffectiveDefinitionHash: derived.EffectiveDefinitionHash,
		}
	} else {
		var err error
		config, err = loadAgentConfigTx(ctx, q, input.ProjectID, input.AgentConfigID)
		if err != nil {
			return nil, err
		}
	}
	contract, err := launchableRuntimeContract(config)
	if err != nil {
		return nil, err
	}
	refs := contract.ReferencedIntegrationIDs()
	for _, subscription := range input.Subscriptions {
		if subscription.IntegrationID == uuid.Nil {
			return nil, storeerr.InvalidRequest(errors.New("subscription integration id is required"))
		}
		refs = append(refs, subscription.IntegrationID)
	}
	return refs, nil
}

func configChangeReplayExistsTx(ctx context.Context, q *dbsqlc.Queries, input ChangeAgentConfigInput) (bool, error) {
	if input.IdempotencyKey == "" {
		return false, nil
	}
	_, err := q.GetAgentInputByIdempotency(ctx, dbsqlc.GetAgentInputByIdempotencyParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID,
		IdempotencyScope: "agent_config_change", InputIdempotencyKey: input.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load idempotent config change: %w", err)
	}
	return true, nil
}

// Previous-only integrations need no gate: activation leaves subscriptions unchanged
// and reconciles interaction selection under the agent lock.
func lockConfigChangeIntegrationsTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	input ChangeAgentConfigInput,
) error {
	if replay, err := configChangeReplayExistsTx(ctx, q, input); err != nil || replay {
		return err
	}
	_, next, err := validateLiveAgentConfigChangeTx(
		ctx,
		q,
		input.ProjectID,
		uuid.Nil,
		input.CreateAgentConfigInput,
	)
	if err != nil {
		return err
	}
	if err := integrationstore.LockIntegrationsTx(ctx, tx, input.ProjectID, next.ReferencedIntegrationIDs()); err != nil {
		// An activation may have committed while we waited; replay must survive revocation.
		if replay, replayErr := configChangeReplayExistsTx(ctx, q, input); replayErr == nil && replay {
			return nil
		}
		return err
	}
	return nil
}
